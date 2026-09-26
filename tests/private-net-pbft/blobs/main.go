// Command blobs sends N blobs of random data (default 2: a full block on
// Metadium, whose MaxBlobGasPerBlock is 2 blobs, 2 × 128 KiB) in
// back-to-back transactions of at most 2 blobs each, the pool's limit per
// transaction, and waits for them to be committed, for sidecar.sh (§11.3
// M-10). It prints the blocks they landed in.
//
//	go run ./tests/private-net-pbft/blobs RPC_URL [N]
//
// The sender is the network's first test account (setup.sh).
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/rlp"
)

const senderKey = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

func main() {
	if len(os.Args) < 2 {
		fail(fmt.Errorf("usage: blobs RPC_URL [N]"))
	}
	url, n := os.Args[1], 2
	if len(os.Args) > 2 {
		var err error
		if n, err = strconv.Atoi(os.Args[2]); err != nil || n < 1 || n > 6 {
			fail(fmt.Errorf("N must be 1..6"))
		}
	}
	key, _ := crypto.HexToECDSA(senderKey)
	from := crypto.PubkeyToAddress(key.PublicKey)
	chainID := hexBig(call(url, "eth_chainId"))
	gasPrice := hexBig(call(url, "eth_gasPrice"))
	nonce := hexBig(call(url, "eth_getTransactionCount", from.Hex(), "pending")).Uint64()

	ctx, err := gokzg4844.NewContext4096Secure()
	if err != nil {
		fail(err)
	}
	var hashes []string
	size := 0
	for left := n; left > 0; left -= 2 {
		k := min(left, 2)
		raw := blobTx(ctx, key, chainID, gasPrice, nonce, k)
		nonce++
		size += len(raw)
		h := strings.Trim(call(url, "eth_sendRawTransaction", "0x"+hex.EncodeToString(raw)), `"`)
		if !strings.HasPrefix(h, "0x") || len(h) != 66 {
			fail(fmt.Errorf("send: %s", h))
		}
		hashes = append(hashes, h)
	}
	blocks := map[uint64]bool{}
	for _, h := range hashes {
		blocks[receipt(url, h)] = true
	}
	var list []string
	for b := range blocks {
		list = append(list, strconv.FormatUint(b, 10))
	}
	fmt.Printf("%d blobs in %d txs (%d bytes on the wire) in block %s\n", n, len(hashes), size, strings.Join(list, ","))
}

// blobTx is a signed blob transaction with k random blobs, network-encoded.
func blobTx(ctx *gokzg4844.Context, key *ecdsa.PrivateKey, chainID, gasPrice *big.Int, nonce uint64, k int) []byte {
	var blobs, commitments, proofs [][]byte
	var hashes []common.Hash
	for i := 0; i < k; i++ {
		var blob gokzg4844.Blob
		// Random field elements: each 32-byte element's top byte stays 0,
		// below the BLS12-381 modulus.
		for j := 0; j < len(blob); j += 32 {
			rand.Read(blob[j+1 : j+32])
		}
		c, err := ctx.BlobToKZGCommitment(blob, 0)
		if err != nil {
			fail(err)
		}
		p, err := ctx.ComputeBlobKZGProof(blob, c, 0)
		if err != nil {
			fail(err)
		}
		blobs, commitments, proofs = append(blobs, blob[:]), append(commitments, c[:]), append(proofs, p[:])
		hashes = append(hashes, common.Hash(kzg4844.KZGToVersionedHash(c[:])))
	}
	u := func(b *big.Int) *uint256.Int { v, _ := uint256.FromBig(b); return v }
	to := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	inner := &types.BlobTx{
		ChainID: u(chainID), Nonce: nonce, GasTipCap: u(gasPrice), GasFeeCap: u(new(big.Int).Mul(gasPrice, big.NewInt(2))),
		Gas: 21000, To: &to, Value: uint256.NewInt(0), MaxFeePerBlobGas: uint256.NewInt(1e9), BlobHashes: hashes,
	}
	signed, err := types.SignTx(types.NewTx(inner), types.NewLondonSigner(chainID), key)
	if err != nil {
		fail(err)
	}
	v, r, s := signed.RawSignatureValues()
	wire := *inner
	wire.V, wire.R, wire.S = u(v), u(r), u(s)
	enc, err := rlp.EncodeToBytes(struct {
		Tx                         *types.BlobTx
		Blobs, Commitments, Proofs [][]byte
	}{&wire, blobs, commitments, proofs})
	if err != nil {
		fail(err)
	}
	return append([]byte{types.BlobTxType}, enc...)
}

// receipt waits for hash to be committed and returns its block.
func receipt(url, hash string) uint64 {
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		var rc struct {
			BlockNumber string `json:"blockNumber"`
			Status      string `json:"status"`
		}
		if res := call(url, "eth_getTransactionReceipt", hash); res != "null" && json.Unmarshal([]byte(res), &rc) == nil && rc.BlockNumber != "" {
			if rc.Status != "0x1" {
				fail(fmt.Errorf("tx %s failed in block %s", hash, rc.BlockNumber))
			}
			return hexBig(rc.BlockNumber).Uint64()
		}
	}
	fail(fmt.Errorf("tx %s not committed in 60 s", hash))
	return 0
}

func call(url, method string, params ...any) string {
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		fail(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Result json.RawMessage           `json:"result"`
		Error  *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		fail(fmt.Errorf("%s: %s", method, b))
	}
	if out.Error != nil {
		fail(fmt.Errorf("%s: %s", method, out.Error.Message))
	}
	return string(out.Result)
}

func hexBig(s string) *big.Int {
	n, _ := new(big.Int).SetString(strings.TrimPrefix(strings.Trim(s, `"`), "0x"), 16)
	if n == nil {
		return new(big.Int)
	}
	return n
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "blobs:", err)
	os.Exit(1)
}
