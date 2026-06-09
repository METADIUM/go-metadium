// camellia-contract-e2e: deploys a real solc-0.8.28 (cancun target) contract
// and exercises functions whose compiled bytecode contains PUSH0, MCOPY, and
// transient storage (TSTORE/TLOAD). This verifies the developer path — actual
// compiler output, not hand-written opcodes — works end-to-end on a live
// Camellia network.
//
// Usage:
//
//	E2E_SENDER_KEY=<hex> go run ./tests/private-net-poa/camellia-contract-e2e/ [rpc-url]
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

	_ "embed"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

//go:embed Camellia.bin
var contractBinHex string

//go:embed Camellia.abi
var contractABI string

const defaultSenderKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

func main() {
	rpc := "http://localhost:8545"
	if len(os.Args) > 1 {
		rpc = os.Args[1]
	}
	fmt.Printf("=== Camellia real-contract E2E (solc 0.8.28, cancun) ===\nRPC: %s\n\n", rpc)

	keyHex := defaultSenderKey
	if k := os.Getenv("E2E_SENDER_KEY"); k != "" {
		keyHex = strings.TrimPrefix(k, "0x")
	}
	key, err := crypto.HexToECDSA(keyHex)
	must(err, "parse key")
	sender := crypto.PubkeyToAddress(key.PublicKey)
	fmt.Printf("Sender: %s\n", sender.Hex())

	parsed, err := abi.JSON(strings.NewReader(contractABI))
	must(err, "parse ABI")

	chainID := hexToBigInt(rpcCall(rpc, "eth_chainId", nil))
	gasPrice := hexToBigInt(rpcCall(rpc, "eth_gasPrice", nil))
	nonce := hexToUint64(rpcCall(rpc, "eth_getTransactionCount", []any{sender.Hex(), "pending"}))
	signer := types.NewLondonSigner(chainID)
	feeCap := new(big.Int).Mul(gasPrice, big.NewInt(2))

	sendTx := func(to *common.Address, data []byte, gas uint64) map[string]any {
		tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
			ChainID: chainID, Nonce: nonce, GasTipCap: gasPrice, GasFeeCap: feeCap,
			Gas: gas, To: to, Data: data,
		}), signer, key)
		must(err, "sign tx")
		raw, _ := tx.MarshalBinary()
		h := submitRawTx(rpc, raw)
		nonce++
		r := waitForReceipt(rpc, h, 15)
		if r == nil {
			fail(fmt.Sprintf("tx %s not mined", h))
		}
		if fmt.Sprintf("%v", r["status"]) != "0x1" {
			fail(fmt.Sprintf("tx %s reverted: status=%v", h, r["status"]))
		}
		return r
	}
	callView := func(to common.Address, data []byte) []byte {
		res := rpcCall(rpc, "eth_call", []any{map[string]any{"to": to.Hex(), "data": "0x" + common.Bytes2Hex(data)}, "latest"})
		return common.FromHex(strings.Trim(res, `"`))
	}

	// 1. Deploy
	fmt.Println("\n--- deploy (PUSH0/MCOPY/transient in runtime) ---")
	r := sendTx(nil, common.FromHex(strings.TrimSpace(contractBinHex)), 1_000_000)
	addr := common.HexToAddress(fmt.Sprintf("%v", r["contractAddress"]))
	fmt.Printf("✅ deployed at %s (block %v)\n", addr.Hex(), r["blockNumber"])

	code := strings.Trim(rpcCall(rpc, "eth_getCode", []any{addr.Hex(), "latest"}), `"`)
	cb := common.FromHex(code)
	has := func(op byte) bool {
		for i := 0; i < len(cb); i++ {
			b := cb[i]
			if b == op {
				return true
			}
			if b >= 0x60 && b <= 0x7f { // PUSH1..PUSH32: skip immediate
				i += int(b-0x60) + 1
			}
		}
		return false
	}
	fmt.Printf("   runtime opcodes: PUSH0=%v MCOPY=%v TLOAD=%v TSTORE=%v\n", has(0x5f), has(0x5e), has(0x5c), has(0x5d))
	if !has(0x5f) || !has(0x5e) || !has(0x5c) || !has(0x5d) {
		fail("expected compiler-emitted PUSH0/MCOPY/TLOAD/TSTORE in deployed runtime")
	}

	// 2. set(0x42) → x() == 0x42  (PUSH0 path on a normal storage write)
	fmt.Println("\n--- set(0x42) / x() ---")
	d, _ := parsed.Pack("set", big.NewInt(0x42))
	sendTx(&addr, d, 100_000)
	d, _ = parsed.Pack("x")
	got := new(big.Int).SetBytes(callView(addr, d))
	checkEq("x after set", got, big.NewInt(0x42))

	// 3. inc() → x() == 0x43
	fmt.Println("\n--- inc() / x() ---")
	d, _ = parsed.Pack("inc")
	sendTx(&addr, d, 100_000)
	d, _ = parsed.Pack("x")
	got = new(big.Int).SetBytes(callView(addr, d))
	checkEq("x after inc", got, big.NewInt(0x43))

	// 4. echo(bytes) round-trip (MCOPY-based memory copy)
	fmt.Println("\n--- echo(bytes) [MCOPY] ---")
	payload := common.FromHex("0xdeadbeefcafe0011223344556677889900aabbccddeeff")
	d, _ = parsed.Pack("echo", payload)
	out, err := parsed.Unpack("echo", callView(addr, d))
	must(err, "unpack echo")
	echoed := out[0].([]byte)
	if !bytes.Equal(echoed, payload) {
		fail(fmt.Sprintf("echo mismatch: got %x want %x", echoed, payload))
	}
	fmt.Printf("✅ echo round-trip ok (%d bytes via MCOPY)\n", len(echoed))

	// 5. txn(0x99) → transient write+read within same call returns 0x99
	fmt.Println("\n--- txn(0x99) [transient same-call] ---")
	d, _ = parsed.Pack("txn", big.NewInt(0x99))
	out, err = parsed.Unpack("txn", callView(addr, d))
	must(err, "unpack txn")
	checkEq("txn same-call transient", out[0].(*big.Int), big.NewInt(0x99))

	fmt.Println("\n=== ALL PASS ===")
	fmt.Println("Real solc-0.8.28 (cancun) contract deployed and exercised: PUSH0, MCOPY, transient storage all work on live network.")
}

func checkEq(label string, got, want *big.Int) {
	if got.Cmp(want) != 0 {
		fail(fmt.Sprintf("%s: got %s want %s", label, got, want))
	}
	fmt.Printf("✅ %s = 0x%x\n", label, want)
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
