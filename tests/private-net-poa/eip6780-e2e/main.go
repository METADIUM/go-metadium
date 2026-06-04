// eip6780-e2e: End-to-end test for EIP-6780 SELFDESTRUCT semantics on a live
// (or private) Camellia-enabled network.
//
//  1. Deploy a contract whose runtime code is CALLER; SELFDESTRUCT (0x33ff).
//  2. Call SELFDESTRUCT from a separate transaction.
//  3. Verify via eth_getCode that the code is preserved (EIP-6780: only
//     contracts created in the same tx are actually destroyed).
//
// Usage:
//
//	E2E_SENDER_KEY=<hex> go run ./tests/private-net-poa/eip6780-e2e/ [rpc-url]
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

// Hardhat account 0 — pre-funded in genesis.json with 1e24 wei.
// Override with E2E_SENDER_KEY to run against a live network.
const defaultSenderKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

// init (12 bytes): PUSH1 2, PUSH1 0x0c, PUSH1 0, CODECOPY, PUSH1 2, PUSH1 0, RETURN
// runtime (2 bytes): CALLER (0x33), SELFDESTRUCT (0xff)
var sdDeployCode = common.FromHex("0x6002600c60003960026000f333ff")

func main() {
	rpc := "http://localhost:8545"
	if len(os.Args) > 1 {
		rpc = os.Args[1]
	}

	fmt.Printf("=== EIP-6780 SELFDESTRUCT E2E Test ===\n")
	fmt.Printf("RPC: %s\n\n", rpc)

	keyHex := defaultSenderKey
	if k := os.Getenv("E2E_SENDER_KEY"); k != "" {
		keyHex = strings.TrimPrefix(k, "0x")
	}
	key, err := crypto.HexToECDSA(keyHex)
	must(err, "parse key")
	sender := crypto.PubkeyToAddress(key.PublicKey)
	fmt.Printf("Sender: %s\n", sender.Hex())

	chainIDBig := hexToBigInt(rpcCall(rpc, "eth_chainId", nil))
	gasPrice := hexToBigInt(rpcCall(rpc, "eth_gasPrice", nil))
	nonce := hexToUint64(rpcCall(rpc, "eth_getTransactionCount", []any{sender.Hex(), "pending"}))
	fmt.Printf("ChainID: %s  GasPrice: %s  Nonce: %d\n\n", chainIDBig, gasPrice, nonce)

	signer := types.NewLondonSigner(chainIDBig)
	feeCap := new(big.Int).Mul(gasPrice, big.NewInt(2))

	// === TX 1: deploy the self-destructing contract ===
	fmt.Println("--- TX 1: deploy contract (runtime = CALLER; SELFDESTRUCT) ---")
	deployTx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainIDBig,
		Nonce:     nonce,
		GasTipCap: gasPrice,
		GasFeeCap: feeCap,
		Gas:       100000,
		To:        nil,
		Data:      sdDeployCode,
	}), signer, key)
	must(err, "sign deploy tx")

	deployRaw, err := deployTx.MarshalBinary()
	must(err, "marshal deploy tx")
	hash1 := submitRawTx(rpc, deployRaw)
	fmt.Printf("Submitted deploy tx: %s\n", hash1)

	r1 := waitForReceipt(rpc, hash1, 15)
	if r1 == nil {
		fail("deploy tx not mined within 30s")
	}
	if fmt.Sprintf("%v", r1["status"]) != "0x1" {
		fail(fmt.Sprintf("deploy tx failed: status=%v", r1["status"]))
	}
	contractAddr := fmt.Sprintf("%v", r1["contractAddress"])
	fmt.Printf("✅ deployed at %s (block %v)\n\n", contractAddr, r1["blockNumber"])

	code := strings.Trim(rpcCall(rpc, "eth_getCode", []any{contractAddr, "latest"}), `"`)
	if code != "0x33ff" {
		fail(fmt.Sprintf("unexpected runtime code: %s (want 0x33ff)", code))
	}
	fmt.Printf("Runtime code: %s ✓\n\n", code)

	// === TX 2: trigger SELFDESTRUCT from a separate tx ===
	fmt.Println("--- TX 2: call contract → SELFDESTRUCT executes ---")
	to := common.HexToAddress(contractAddr)
	callTx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainIDBig,
		Nonce:     nonce + 1,
		GasTipCap: gasPrice,
		GasFeeCap: feeCap,
		Gas:       100000,
		To:        &to,
	}), signer, key)
	must(err, "sign call tx")

	callRaw, err := callTx.MarshalBinary()
	must(err, "marshal call tx")
	hash2 := submitRawTx(rpc, callRaw)
	fmt.Printf("Submitted selfdestruct tx: %s\n", hash2)

	r2 := waitForReceipt(rpc, hash2, 15)
	if r2 == nil {
		fail("selfdestruct tx not mined within 30s")
	}
	if fmt.Sprintf("%v", r2["status"]) != "0x1" {
		fail(fmt.Sprintf("selfdestruct tx failed: status=%v", r2["status"]))
	}
	fmt.Printf("✅ selfdestruct tx mined (block %v)\n\n", r2["blockNumber"])

	// === Verify: code must be preserved (EIP-6780) ===
	codeAfter := strings.Trim(rpcCall(rpc, "eth_getCode", []any{contractAddr, "latest"}), `"`)
	fmt.Printf("Code after SELFDESTRUCT: %s\n", codeAfter)
	if codeAfter == "0x33ff" {
		fmt.Println("\n=== ALL PASS ===")
		fmt.Println("EIP-6780: code preserved after SELFDESTRUCT in a separate tx.")
	} else {
		fail(fmt.Sprintf("EIP-6780 NOT applied — code after selfdestruct: %s", codeAfter))
	}
}

func fail(msg string) {
	fmt.Printf("❌ FAIL: %s\n", msg)
	os.Exit(1)
}

func submitRawTx(rpc string, raw []byte) string {
	txHashResult := rpcCall(rpc, "eth_sendRawTransaction", []any{"0x" + common.Bytes2Hex(raw)})
	txHash := strings.Trim(txHashResult, `"`)
	if strings.HasPrefix(txHash, "0x") && len(txHash) == 66 {
		return txHash
	}
	fmt.Fprintf(os.Stderr, "FATAL: eth_sendRawTransaction returned: %s\n", txHashResult)
	os.Exit(1)
	return ""
}

func waitForReceipt(rpc, hash string, maxAttempts int) map[string]any {
	for attempt := 0; attempt < maxAttempts; attempt++ {
		time.Sleep(2 * time.Second)
		raw := rpcCall(rpc, "eth_getTransactionReceipt", []any{hash})
		if raw == "null" || raw == "" {
			continue
		}
		var r map[string]any
		if err := json.Unmarshal([]byte(raw), &r); err == nil {
			return r
		}
	}
	return nil
}

// RPC helpers

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      int    `json:"id"`
}

func rpcCallRaw(url, method string, params []any) string {
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(rpcReq{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Sprintf(`{"error":"%v"}`, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func rpcCall(url, method string, params []any) string {
	raw := rpcCallRaw(url, method, params)
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return raw
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
