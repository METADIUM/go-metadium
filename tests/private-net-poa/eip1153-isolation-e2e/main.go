// eip1153-isolation-e2e: verifies EIP-1153 transient storage semantics on a
// live Camellia network — specifically that transient storage is isolated
// across transactions (a security-relevant property), while remaining
// readable within a single transaction.
//
// The deployed contract runtime does, on every call:
//
//	slot0 = TLOAD(0)        // transient value seen at the start of the tx
//	TSTORE(0, 0x42)         // set transient[0]
//	slot1 = TLOAD(0)        // == 0x42 within the same tx
//
// Calling it twice in separate txs must yield:
//   - slot0 == 0 on BOTH calls  (the 0x42 from call #1 must NOT leak into #2)
//   - slot1 == 0x42 on both     (same-tx write/read works)
//
// Usage:
//
//	E2E_SENDER_KEY=<hex> go run ./tests/private-net-poa/eip1153-isolation-e2e/ [rpc-url]
//
// Default rpc-url: http://localhost:8545
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

const defaultSenderKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

// runtime: 6000 5c 6000 55  6042 6000 5d  6000 5c 6001 55  00
//
//	PUSH1 0; TLOAD; PUSH1 0; SSTORE          slot0 = TLOAD(0)
//	PUSH1 0x42; PUSH1 0; TSTORE              transient[0] = 0x42
//	PUSH1 0; TLOAD; PUSH1 1; SSTORE          slot1 = TLOAD(0)
//	STOP
//
// deploy: PUSH1 0x12; PUSH1 0x0c; PUSH1 0; CODECOPY; PUSH1 0x12; PUSH1 0; RETURN
const deployCode = "0x6012600c60003960126000f360005c600055604260005d60005c60015500"
const runtimeCode = "0x60005c600055604260005d60005c60015500"

func main() {
	rpc := "http://localhost:8545"
	if len(os.Args) > 1 {
		rpc = os.Args[1]
	}
	fmt.Printf("=== EIP-1153 transient storage isolation E2E ===\nRPC: %s\n\n", rpc)

	keyHex := defaultSenderKey
	if k := os.Getenv("E2E_SENDER_KEY"); k != "" {
		keyHex = strings.TrimPrefix(k, "0x")
	}
	key, err := crypto.HexToECDSA(keyHex)
	must(err, "parse key")
	sender := crypto.PubkeyToAddress(key.PublicKey)
	fmt.Printf("Sender: %s\n", sender.Hex())

	chainID := hexToBigInt(rpcCall(rpc, "eth_chainId", nil))
	gasPrice := hexToBigInt(rpcCall(rpc, "eth_gasPrice", nil))
	nonce := hexToUint64(rpcCall(rpc, "eth_getTransactionCount", []any{sender.Hex(), "pending"}))
	signer := types.NewLondonSigner(chainID)
	feeCap := new(big.Int).Mul(gasPrice, big.NewInt(2))

	sendTx := func(to *common.Address, data []byte) map[string]any {
		tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
			ChainID: chainID, Nonce: nonce, GasTipCap: gasPrice, GasFeeCap: feeCap,
			Gas: 200_000, To: to, Data: data,
		}), signer, key)
		must(err, "sign")
		raw, _ := tx.MarshalBinary()
		h := submitRawTx(rpc, raw)
		nonce++
		r := waitForReceipt(rpc, h, 15)
		if r == nil || fmt.Sprintf("%v", r["status"]) != "0x1" {
			fail(fmt.Sprintf("tx %s failed: %v", h, r))
		}
		return r
	}
	slot := func(addr common.Address, s int64) *big.Int {
		res := rpcCall(rpc, "eth_getStorageAt", []any{addr.Hex(), fmt.Sprintf("0x%x", s), "latest"})
		return hexToBigInt(strings.Trim(res, `"`))
	}

	// Deploy
	fmt.Println("--- deploy transient-probe contract ---")
	r := sendTx(nil, common.FromHex(deployCode))
	addr := common.HexToAddress(fmt.Sprintf("%v", r["contractAddress"]))
	code := strings.Trim(rpcCall(rpc, "eth_getCode", []any{addr.Hex(), "latest"}), `"`)
	if !strings.EqualFold(code, runtimeCode) {
		fail(fmt.Sprintf("runtime mismatch: got %s want %s", code, runtimeCode))
	}
	fmt.Printf("✅ deployed at %s\n\n", addr.Hex())

	// Call #1
	fmt.Println("--- call #1 ---")
	sendTx(&addr, nil)
	s0a, s1a := slot(addr, 0), slot(addr, 1)
	fmt.Printf("   slot0 (tx-start TLOAD) = 0x%x  | slot1 (same-tx TLOAD) = 0x%x\n", s0a, s1a)

	// Call #2 — in a separate tx; transient[0] from call #1 must NOT be visible
	fmt.Println("--- call #2 (separate tx) ---")
	sendTx(&addr, nil)
	s0b, s1b := slot(addr, 0), slot(addr, 1)
	fmt.Printf("   slot0 (tx-start TLOAD) = 0x%x  | slot1 (same-tx TLOAD) = 0x%x\n\n", s0b, s1b)

	ok := true
	if s1a.Cmp(big.NewInt(0x42)) != 0 || s1b.Cmp(big.NewInt(0x42)) != 0 {
		fmt.Println("❌ same-tx TSTORE→TLOAD did not return 0x42")
		ok = false
	} else {
		fmt.Println("✅ same-tx: TSTORE(0x42) → TLOAD == 0x42 on both calls")
	}
	if s0a.Sign() != 0 || s0b.Sign() != 0 {
		fmt.Printf("❌ ISOLATION VIOLATION: tx-start TLOAD non-zero (call1=0x%x call2=0x%x) — transient leaked across txs\n", s0a, s0b)
		ok = false
	} else {
		fmt.Println("✅ cross-tx isolation: tx-start TLOAD == 0 on both calls (no leak from prior tx)")
	}

	if ok {
		fmt.Println("\n=== ALL PASS ===")
		fmt.Println("EIP-1153 transient storage is isolated across transactions and works within a transaction.")
	} else {
		fmt.Println("\n=== FAILED ===")
		os.Exit(1)
	}
}

func fail(msg string) { fmt.Printf("❌ FAIL: %s\n", msg); os.Exit(1) }

func submitRawTx(rpc string, raw []byte) string {
	res := rpcCall(rpc, "eth_sendRawTransaction", []any{"0x" + common.Bytes2Hex(raw)})
	h := strings.Trim(res, `"`)
	if strings.HasPrefix(h, "0x") && len(h) == 66 {
		return h
	}
	fmt.Fprintf(os.Stderr, "FATAL: sendRawTransaction returned: %s\n", res)
	os.Exit(1)
	return ""
}

func waitForReceipt(rpc, hash string, n int) map[string]any {
	for i := 0; i < n; i++ {
		time.Sleep(2 * time.Second)
		raw := rpcCall(rpc, "eth_getTransactionReceipt", []any{hash})
		if raw == "null" || raw == "" {
			continue
		}
		var r map[string]any
		if json.Unmarshal([]byte(raw), &r) == nil {
			return r
		}
	}
	return nil
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      int    `json:"id"`
}

func rpcCall(url, method string, params []any) string {
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(rpcReq{"2.0", method, params, 1})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Sprintf(`"error: %v"`, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &r) != nil {
		return string(b)
	}
	if r.Error != nil {
		return fmt.Sprintf(`"error: %s"`, r.Error.Message)
	}
	return string(r.Result)
}

func hexToBigInt(s string) *big.Int {
	s = strings.Trim(strings.TrimPrefix(strings.Trim(s, `"`), "0x"), `"`)
	n := new(big.Int)
	if s == "" {
		return n
	}
	n.SetString(s, 16)
	return n
}
func hexToUint64(s string) uint64 { return hexToBigInt(s).Uint64() }
func must(err error, ctx string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL [%s]: %v\n", ctx, err)
		os.Exit(1)
	}
}
