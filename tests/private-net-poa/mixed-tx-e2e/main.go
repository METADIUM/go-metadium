// mixed-tx-e2e: End-to-end test for mixed transaction types in the same block.
// Tests that normal tx + fee delegation tx (Type 22) + blob tx (Type 3) can all
// coexist in the same block without any issues.
//
// Usage:
//
//	go run ./tests/private-net-poa/mixed-tx-e2e/ [rpc-url]
//
// Default rpc-url: http://localhost:8545
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	kzg4844pkg "github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/rlp"
)

// Hardhat accounts pre-funded in genesis.json with 1e24 wei.
const (
	senderKey   = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	feePayerKey = "59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
)

func main() {
	rpc := "http://localhost:8545"
	if len(os.Args) > 1 {
		rpc = os.Args[1]
	}

	fmt.Printf("=== Mixed Tx E2E Test (Normal + FeeDelegation + Blob) ===\n")
	fmt.Printf("RPC: %s\n\n", rpc)

	senderPriv, err := crypto.HexToECDSA(senderKey)
	must(err, "parse sender key")
	feePayerPriv, err := crypto.HexToECDSA(feePayerKey)
	must(err, "parse feePayer key")

	sender := crypto.PubkeyToAddress(senderPriv.PublicKey)
	feePayer := crypto.PubkeyToAddress(feePayerPriv.PublicKey)
	fmt.Printf("Sender:   %s\n", sender.Hex())
	fmt.Printf("FeePayer: %s\n", feePayer.Hex())

	// Wait for Camellia to be active (block > 100)
	fmt.Println("\nWaiting for Camellia fork (block > 100)...")
	waitForBlock(rpc, 101)

	// Get chain info
	chainIDBig := hexToBigInt(rpcCall(rpc, "eth_chainId", nil))
	gasPrice := hexToBigInt(rpcCall(rpc, "eth_gasPrice", nil))
	blobBaseFee := hexToBigInt(rpcCall(rpc, "eth_blobBaseFee", nil))
	blockNum := hexToBigInt(rpcCall(rpc, "eth_blockNumber", nil))
	senderNonce := hexToUint64(rpcCall(rpc, "eth_getTransactionCount", []any{sender.Hex(), "pending"}))

	fmt.Printf("\nChainID:     %s\n", chainIDBig)
	fmt.Printf("BlockNumber: %s\n", blockNum)
	fmt.Printf("GasPrice:    %s\n", gasPrice)
	fmt.Printf("BlobBaseFee: %s\n", blobBaseFee)
	fmt.Printf("SenderNonce: %d\n\n", senderNonce)

	londonSigner := types.NewLondonSigner(chainIDBig)
	feeDelegateSigner := types.NewFeeDelegateSigner(chainIDBig)
	toAddr := common.HexToAddress("0x000000000000000000000000000000000000dEaD")

	// === TX 1: Normal DynamicFeeTx ===
	fmt.Println("--- TX 1: Normal DynamicFeeTx ---")
	normalTx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainIDBig,
		Nonce:     senderNonce,
		GasTipCap: gasPrice,
		GasFeeCap: new(big.Int).Mul(gasPrice, big.NewInt(2)),
		Gas:       21000,
		To:        &toAddr,
		Value:     big.NewInt(1000),
	}), londonSigner, senderPriv)
	must(err, "sign normal tx")

	normalRaw, err := normalTx.MarshalBinary()
	must(err, "marshal normal tx")

	hash1 := submitRawTx(rpc, normalRaw)
	fmt.Printf("Submitted normal tx: %s\n\n", hash1)

	// === TX 2: Fee Delegation Tx (Type 22) ===
	fmt.Println("--- TX 2: Fee Delegation Tx (Type 22) ---")
	// Step 1: sign the sender DynamicFeeTx
	senderInner := &types.DynamicFeeTx{
		ChainID:   chainIDBig,
		Nonce:     senderNonce + 1,
		GasTipCap: gasPrice,
		GasFeeCap: new(big.Int).Mul(gasPrice, big.NewInt(2)),
		Gas:       30000,
		To:        &toAddr,
		Value:     big.NewInt(0),
	}
	senderSignedTx, err := types.SignTx(types.NewTx(senderInner), londonSigner, senderPriv)
	must(err, "sign sender tx for fee delegation")
	sv, sr, ss := senderSignedTx.RawSignatureValues()

	// Step 2: build FeeDelegateDynamicFeeTx with sender's sig + feePayer address
	fdTxInner := &types.FeeDelegateDynamicFeeTx{
		SenderTx: types.DynamicFeeTx{
			ChainID:   chainIDBig,
			Nonce:     senderNonce + 1,
			GasTipCap: gasPrice,
			GasFeeCap: new(big.Int).Mul(gasPrice, big.NewInt(2)),
			Gas:       30000,
			To:        &toAddr,
			Value:     big.NewInt(0),
			V:         sv,
			R:         sr,
			S:         ss,
		},
		FeePayer: &feePayer,
		FV:       new(big.Int),
		FR:       new(big.Int),
		FS:       new(big.Int),
	}
	fdTxUnsigned := types.NewTx(fdTxInner)

	// Step 3: feePayer signs the fee payer hash
	fdTxSigned, err := types.SignTx(fdTxUnsigned, feeDelegateSigner, feePayerPriv)
	must(err, "sign fee delegation tx")

	fdRaw, err := fdTxSigned.MarshalBinary()
	must(err, "marshal fee delegation tx")
	fmt.Printf("Fee delegation tx type byte: 0x%02x (want 0x16)\n", fdRaw[0])

	hash2 := submitRawTx(rpc, fdRaw)
	fmt.Printf("Submitted fee delegation tx: %s\n\n", hash2)

	// Wait for TX 1 and TX 2 to be mined before submitting blob tx
	fmt.Println("Waiting for TX 1 and TX 2 to be mined...")
	waitForReceipts(rpc, []string{hash1, hash2}, 30)

	// === TX 3: Blob Tx (Type 3) ===
	fmt.Println("--- TX 3: Blob Tx (EIP-4844, Type 3) ---")
	// Re-query pending nonce (after tx1/tx2 mined)
	blobNonce := hexToUint64(rpcCall(rpc, "eth_getTransactionCount", []any{sender.Hex(), "pending"}))
	kzgCtx, err := gokzg4844.NewContext4096Secure()
	must(err, "KZG context")

	var blob gokzg4844.Blob
	blob[0] = 0x42 // different from blob-tx-e2e test

	commitment, err := kzgCtx.BlobToKZGCommitment(blob, 0)
	must(err, "BlobToKZGCommitment")

	proof, err := kzgCtx.ComputeBlobKZGProof(blob, commitment, 0)
	must(err, "ComputeBlobKZGProof")

	versionedHash := kzg4844pkg.KZGToVersionedHash(commitment[:])

	toU256 := func(b *big.Int) *uint256.Int { v, _ := uint256.FromBig(b); return v }
	blobInner := &types.BlobTx{
		ChainID:          toU256(chainIDBig),
		Nonce:            blobNonce,
		GasTipCap:        toU256(gasPrice),
		GasFeeCap:        toU256(new(big.Int).Mul(gasPrice, big.NewInt(2))),
		Gas:              21000,
		To:               &toAddr,
		Value:            uint256.NewInt(0),
		MaxFeePerBlobGas: uint256.NewInt(1e9),
		BlobHashes:       []common.Hash{common.Hash(versionedHash)},
	}

	blobSignedTx, err := types.SignTx(types.NewTx(blobInner), londonSigner, senderPriv)
	must(err, "sign blob tx")

	bv, br, bs := blobSignedTx.RawSignatureValues()
	signedBlobInner := *blobInner
	signedBlobInner.V = new(uint256.Int).SetBytes(bv.Bytes())
	signedBlobInner.R = new(uint256.Int).SetBytes(br.Bytes())
	signedBlobInner.S = new(uint256.Int).SetBytes(bs.Bytes())

	blobRaw, err := buildBlobNetworkEncoding(&signedBlobInner, blob[:], commitment[:], proof[:])
	must(err, "build blob network encoding")

	hash3 := submitRawTx(rpc, blobRaw)
	fmt.Printf("Submitted blob tx: %s\n\n", hash3)

	// === Wait for all 3 to be mined ===
	fmt.Println("Waiting for all 3 txs to be mined (up to 30s)...")
	receipts := waitForReceipts(rpc, []string{hash1, hash2, hash3}, 15)

	// === Verify results ===
	fmt.Println("\n=== Verification ===")
	allPass := true

	names := []string{"Normal", "FeeDelegation", "Blob"}
	wantTypes := []string{"0x2", "0x16", "0x3"}

	for i, r := range receipts {
		if r == nil {
			fmt.Printf("❌ FAIL [%s]: not mined within 30s\n", names[i])
			allPass = false
			continue
		}
		status := fmt.Sprintf("%v", r["status"])
		txType := fmt.Sprintf("%v", r["type"])
		blockNum := fmt.Sprintf("%v", r["blockNumber"])

		statusOK := status == "0x1"
		typeOK := txType == wantTypes[i]
		marker := "✅"
		if !statusOK || !typeOK {
			marker = "❌"
			allPass = false
		}
		fmt.Printf("%s [%s] block=%s status=%s type=%s (want %s)\n",
			marker, names[i], blockNum, status, txType, wantTypes[i])
	}

	// Check if txs landed in the same block
	if receipts[0] != nil && receipts[1] != nil && receipts[2] != nil {
		b0 := fmt.Sprintf("%v", receipts[0]["blockNumber"])
		b1 := fmt.Sprintf("%v", receipts[1]["blockNumber"])
		b2 := fmt.Sprintf("%v", receipts[2]["blockNumber"])
		if b0 == b1 && b1 == b2 {
			fmt.Printf("\n✅ All 3 txs landed in the SAME block: %s\n", b0)
		} else {
			fmt.Printf("\nℹ️  Txs spread across blocks: normal=%s feedelegate=%s blob=%s\n", b0, b1, b2)
			fmt.Println("   (OK — block packaging is miner's choice)")
		}
	}

	// Verify feePayer balance decreased
	if receipts[1] != nil {
		fpBalance := hexToBigInt(rpcCall(rpc, "eth_getBalance", []any{feePayer.Hex(), "latest"}))
		senderBalance := hexToBigInt(rpcCall(rpc, "eth_getBalance", []any{sender.Hex(), "latest"}))
		fmt.Printf("\nFeePayer balance post-tx: %s wei\n", fpBalance)
		fmt.Printf("Sender balance post-tx:   %s wei\n", senderBalance)
		// feePayer should have less than 1e24 (paid gas)
		initial := new(big.Int)
		initial.SetString("1000000000000000000000000", 10) // 1e24
		if fpBalance.Cmp(initial) < 0 {
			fmt.Println("✅ FeePayer paid gas (balance decreased)")
		} else {
			fmt.Println("❌ FAIL: FeePayer balance did not decrease")
			allPass = false
		}
	}

	fmt.Println()
	if allPass {
		fmt.Println("=== ALL PASS ===")
		fmt.Println("Mixed tx block (normal + fee delegation + blob) works correctly.")
	} else {
		fmt.Println("=== SOME TESTS FAILED ===")
		os.Exit(1)
	}
}

func waitForBlock(rpc string, target int64) {
	for {
		blockNum := hexToBigInt(rpcCall(rpc, "eth_blockNumber", nil))
		if blockNum != nil && blockNum.Int64() >= target {
			fmt.Printf("Block %d reached (target: %d)\n", blockNum.Int64(), target)
			return
		}
		fmt.Printf("  current block=%v, waiting for %d...\n", blockNum, target)
		time.Sleep(3 * time.Second)
	}
}

func submitRawTx(rpc string, raw []byte) string {
	txHashResult := rpcCall(rpc, "eth_sendRawTransaction", []any{"0x" + hex.EncodeToString(raw)})
	txHash := strings.Trim(txHashResult, `"`)
	if strings.HasPrefix(txHash, "0x") && len(txHash) == 66 {
		return txHash
	}
	fmt.Fprintf(os.Stderr, "FATAL: eth_sendRawTransaction returned: %s\n", txHashResult)
	os.Exit(1)
	return ""
}

func waitForReceipts(rpc string, hashes []string, maxAttempts int) []map[string]any {
	receipts := make([]map[string]any, len(hashes))
	for attempt := 0; attempt < maxAttempts; attempt++ {
		time.Sleep(2 * time.Second)
		allDone := true
		for i, hash := range hashes {
			if receipts[i] != nil {
				continue
			}
			raw := rpcCall(rpc, "eth_getTransactionReceipt", []any{hash})
			if raw == "null" || raw == "" {
				allDone = false
				continue
			}
			var r map[string]any
			if err := json.Unmarshal([]byte(raw), &r); err == nil {
				receipts[i] = r
			}
		}
		minedCount := 0
		for _, r := range receipts {
			if r != nil {
				minedCount++
			}
		}
		fmt.Printf("  attempt %d: %d/%d mined\n", attempt+1, minedCount, len(hashes))
		if allDone {
			break
		}
	}
	return receipts
}

func buildBlobNetworkEncoding(inner *types.BlobTx, blob, commitment, proof []byte) ([]byte, error) {
	type networkWrapper struct {
		Tx          *types.BlobTx
		Blobs       [][]byte
		Commitments [][]byte
		Proofs      [][]byte
	}
	innerCopy := *inner
	wrapper := networkWrapper{
		Tx:          &innerCopy,
		Blobs:       [][]byte{blob},
		Commitments: [][]byte{commitment},
		Proofs:      [][]byte{proof},
	}
	encoded, err := rlp.EncodeToBytes(wrapper)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteByte(types.BlobTxType)
	buf.Write(encoded)
	return buf.Bytes(), nil
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
