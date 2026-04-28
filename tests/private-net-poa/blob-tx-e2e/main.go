// blob-tx-e2e: End-to-end test for EIP-4844 blob transaction submission
// against the private-net-poa 3-node network.
//
// Usage:
//
//	go run ./tests/private-net-poa/blob-tx-e2e/ [rpc-url]
//
// Default rpc-url: http://192.168.0.151:8545
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

// Hardhat account 0 — pre-funded in genesis.json with 1e24 wei.
const senderKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

func main() {
	rpc := "http://192.168.0.151:8545"
	if len(os.Args) > 1 {
		rpc = os.Args[1]
	}

	fmt.Printf("=== EIP-4844 Blob Tx E2E Test ===\n")
	fmt.Printf("RPC: %s\n\n", rpc)

	key, err := crypto.HexToECDSA(senderKey)
	must(err, "parse key")
	sender := crypto.PubkeyToAddress(key.PublicKey)
	fmt.Printf("Sender: %s\n", sender.Hex())

	// 1. Get chain info
	chainIDBig := hexToBigInt(rpcCall(rpc, "eth_chainId", nil))
	gasPrice := hexToBigInt(rpcCall(rpc, "eth_gasPrice", nil))
	blobBaseFee := hexToBigInt(rpcCall(rpc, "eth_blobBaseFee", nil))
	blockNum := hexToBigInt(rpcCall(rpc, "eth_blockNumber", nil))
	nonce := hexToUint64(rpcCall(rpc, "eth_getTransactionCount", []any{sender.Hex(), "pending"}))

	fmt.Printf("ChainID:     %s\n", chainIDBig)
	fmt.Printf("BlockNumber: %s\n", blockNum)
	fmt.Printf("BlobBaseFee: %s wei\n", blobBaseFee)
	fmt.Printf("Nonce:       %d\n\n", nonce)

	// 2. Build valid blob + KZG commitment + proof
	fmt.Println("Building blob with KZG commitment and proof...")
	kzgCtx, err := gokzg4844.NewContext4096Secure()
	must(err, "KZG context")

	var blob gokzg4844.Blob
	blob[0] = 0x01

	commitment, err := kzgCtx.BlobToKZGCommitment(blob, 0)
	must(err, "BlobToKZGCommitment")

	proof, err := kzgCtx.ComputeBlobKZGProof(blob, commitment, 0)
	must(err, "ComputeBlobKZGProof")

	versionedHash := kzg4844pkg.KZGToVersionedHash(commitment[:])
	fmt.Printf("Versioned hash: %s\n", common.Hash(versionedHash).Hex())

	// 3. Build and sign BlobTx.
	// After signing, we extract V/R/S to build the network encoding separately.
	toU256 := func(b *big.Int) *uint256.Int { v, _ := uint256.FromBig(b); return v }
	toAddr := common.HexToAddress("0x000000000000000000000000000000000000dEaD")

	inner := &types.BlobTx{
		ChainID:          toU256(chainIDBig),
		Nonce:            nonce,
		GasTipCap:        toU256(gasPrice),
		GasFeeCap:        toU256(new(big.Int).Mul(gasPrice, big.NewInt(2))),
		Gas:              21000,
		To:               &toAddr,
		Value:            uint256.NewInt(0),
		MaxFeePerBlobGas: uint256.NewInt(1e9), // well above blobBaseFee=1
		BlobHashes:       []common.Hash{common.Hash(versionedHash)},
	}

	signer := types.NewLondonSigner(chainIDBig)
	signedTx, err := types.SignTx(types.NewTx(inner), signer, key)
	must(err, "SignTx")

	fmt.Printf("TxHash: %s\n", signedTx.Hash().Hex())
	fmt.Printf("Type:   %d\n\n", signedTx.Type())

	// 4. Extract V, R, S from signed tx and build the signed inner BlobTx for network encoding.
	v, r, s := signedTx.RawSignatureValues()
	signedInner := *inner
	signedInner.V = new(uint256.Int).SetBytes(v.Bytes())
	signedInner.R = new(uint256.Int).SetBytes(r.Bytes())
	signedInner.S = new(uint256.Int).SetBytes(s.Bytes())

	// 5. Build network encoding: 0x03 || rlp([[tx_fields], blobs, commitments, proofs])
	rawNetwork, err := buildNetworkEncoding(&signedInner, blob[:], commitment[:], proof[:])
	must(err, "network encoding")
	fmt.Printf("Network-encoded tx: %d bytes\n", len(rawNetwork))

	// 6. Submit via eth_sendRawTransaction
	fmt.Println("\nSubmitting blob tx...")
	txHashResult := rpcCall(rpc, "eth_sendRawTransaction", []any{"0x" + hex.EncodeToString(rawNetwork)})
	txHash := strings.Trim(txHashResult, `"`)

	if strings.HasPrefix(txHash, "0x") && len(txHash) == 66 {
		fmt.Printf("✅ PASS: eth_sendRawTransaction accepted\n   hash=%s\n\n", txHash)
	} else {
		fmt.Printf("❌ FAIL: eth_sendRawTransaction returned: %s\n", txHashResult)
		os.Exit(1)
	}

	// 7. eth_getBlobSidecar — tx is in pool, sidecar should be available
	fmt.Println("Checking eth_getBlobSidecar (in-pool)...")
	sidecarResp := rpcCallRaw(rpc, "eth_getBlobSidecar", []any{txHash})
	if strings.Contains(sidecarResp, `"blobs"`) {
		fmt.Println("✅ PASS: eth_getBlobSidecar returned sidecar (tx in pool)")
	} else if strings.Contains(sidecarResp, `"result":null`) {
		fmt.Println("⚠️  SKIP: eth_getBlobSidecar null (tx mined already)")
	} else {
		fmt.Printf("❌ FAIL: eth_getBlobSidecar unexpected: %s\n", sidecarResp)
		os.Exit(1)
	}

	// 8. Wait for receipt (up to 30s, 2s block time)
	fmt.Println("\nWaiting for tx to be mined (up to 30s)...")
	var receiptRaw string
	for i := 0; i < 15; i++ {
		time.Sleep(2 * time.Second)
		receiptRaw = rpcCall(rpc, "eth_getTransactionReceipt", []any{txHash})
		if receiptRaw != "null" && receiptRaw != "" {
			break
		}
		fmt.Printf("  block=%s waiting...\n", hexToBigInt(rpcCall(rpc, "eth_blockNumber", nil)))
	}

	if receiptRaw == "null" || receiptRaw == "" {
		fmt.Println("❌ FAIL: tx not mined within 30s")
		os.Exit(1)
	}

	// 9. Parse receipt
	var receipt map[string]any
	must(json.Unmarshal([]byte(receiptRaw), &receipt), "parse receipt")

	status := fmt.Sprintf("%v", receipt["status"])
	txType := fmt.Sprintf("%v", receipt["type"])
	blockMined := fmt.Sprintf("%v", receipt["blockNumber"])

	fmt.Printf("✅ PASS: Blob tx mined!\n")
	fmt.Printf("   status:      %s (want 0x1)\n", status)
	fmt.Printf("   type:        %s (want 0x3)\n", txType)
	fmt.Printf("   blockNumber: %s\n", blockMined)

	if status != "0x1" {
		fmt.Printf("❌ FAIL: status=%s\n", status)
		os.Exit(1)
	}
	if txType != "0x3" {
		fmt.Printf("❌ FAIL: type=%s, expected 0x3\n", txType)
		os.Exit(1)
	}

	// 10. eth_getBlobSidecar after mining — sidecar is not persisted post-mine
	fmt.Println("\nChecking eth_getBlobSidecar post-mining...")
	sidecarPost := rpcCallRaw(rpc, "eth_getBlobSidecar", []any{txHash})
	if strings.Contains(sidecarPost, `"result":null`) {
		fmt.Println("✅ PASS: sidecar correctly pruned after mining")
	} else {
		fmt.Printf("ℹ️  INFO: post-mine sidecar response: %s\n", sidecarPost)
	}

	fmt.Printf("\n=== ALL PASS ===\n")
	fmt.Printf("EIP-4844 blob tx created, submitted, and mined successfully on private-net-poa\n")
}

// buildNetworkEncoding builds: 0x03 || rlp([[tx_fields], blobs, commitments, proofs])
func buildNetworkEncoding(inner *types.BlobTx, blob, commitment, proof []byte) ([]byte, error) {
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
