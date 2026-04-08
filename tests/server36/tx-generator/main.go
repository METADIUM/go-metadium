// tx-generator: Generates mixed transactions (normal + fee delegation + blob)
// for long-term stability testing. Sends one batch of each type and exits.
//
// Usage:
//
//	tx-generator [options]
//	  -rpc       RPC endpoint (default: http://localhost:8545)
//	  -batch     Number of normal txs per batch (default: 5)
//	  -json      Output JSON summary only
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"math/rand"
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

// Hardhat test accounts
var senderKeys = []string{
	"ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80",
	"59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d",
	"5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a",
}

var senderEndpoints = []string{
	"http://localhost:8545",
	"http://localhost:8546",
	"http://localhost:8547",
}

var toAddr = common.HexToAddress("0x90F79bf6EB2c4f870365E785982E1f101E93b906")

type result struct {
	NormalSent int `json:"normal_sent"`
	NormalFail int `json:"normal_fail"`
	FDSent     int `json:"fd_sent"`
	FDFail     int `json:"fd_fail"`
	BlobSent   int `json:"blob_sent"`
	BlobFail   int `json:"blob_fail"`
}

func main() {
	rpcBase := flag.String("rpc", "http://localhost:8545", "base RPC endpoint")
	batch := flag.Int("batch", 5, "normal txs per batch")
	jsonOut := flag.Bool("json", false, "JSON output only")
	flag.Parse()

	_ = rpcBase
	res := result{}

	// Parse keys
	type account struct {
		key      string
		endpoint string
		addr     common.Address
	}
	accounts := make([]account, len(senderKeys))
	for i, k := range senderKeys {
		priv, err := crypto.HexToECDSA(k)
		if err != nil {
			fatal("parse key %d: %v", i, err)
		}
		accounts[i] = account{
			key:      k,
			endpoint: senderEndpoints[i],
			addr:     crypto.PubkeyToAddress(priv.PublicKey),
		}
	}

	chainIDBig := hexToBigInt(rpcCall(*rpcBase, "eth_chainId", nil))
	gasPrice := hexToBigInt(rpcCall(*rpcBase, "eth_gasPrice", nil))

	londonSigner := types.NewLondonSigner(chainIDBig)
	feeDelegateSigner := types.NewFeeDelegateSigner(chainIDBig)

	// === 1. Normal DynamicFeeTx batch ===
	// Use only accounts[0] and accounts[1] for normal txs; accounts[2] reserved for blob tx
	normalAccounts := accounts[:2]
	for i := 0; i < *batch; i++ {
		acct := normalAccounts[i%len(normalAccounts)]
		priv, _ := crypto.HexToECDSA(acct.key)
		nonce := getNonce(acct.endpoint, acct.addr)

		tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
			ChainID:   chainIDBig,
			Nonce:     nonce,
			GasTipCap: gasPrice,
			GasFeeCap: new(big.Int).Mul(gasPrice, big.NewInt(2)),
			Gas:       21000,
			To:        &toAddr,
			Value:     big.NewInt(int64(rand.Intn(100) + 1)),
		}), londonSigner, priv)
		if err != nil {
			res.NormalFail++
			continue
		}
		raw, _ := tx.MarshalBinary()
		if submitRawTx(acct.endpoint, raw) != "" {
			res.NormalSent++
		} else {
			res.NormalFail++
		}
		time.Sleep(100 * time.Millisecond)
	}

	// === 2. Fee Delegation Tx ===
	{
		senderPriv, _ := crypto.HexToECDSA(senderKeys[0])
		feePayerPriv, _ := crypto.HexToECDSA(senderKeys[1])
		feePayer := accounts[1].addr
		nonce := getNonce(senderEndpoints[0], accounts[0].addr)

		senderInner := &types.DynamicFeeTx{
			ChainID:   chainIDBig,
			Nonce:     nonce,
			GasTipCap: gasPrice,
			GasFeeCap: new(big.Int).Mul(gasPrice, big.NewInt(2)),
			Gas:       30000,
			To:        &toAddr,
			Value:     big.NewInt(0),
		}
		senderSignedTx, err := types.SignTx(types.NewTx(senderInner), londonSigner, senderPriv)
		if err != nil {
			res.FDFail++
		} else {
			sv, sr, ss := senderSignedTx.RawSignatureValues()
			fdTxInner := &types.FeeDelegateDynamicFeeTx{
				SenderTx: types.DynamicFeeTx{
					ChainID:   chainIDBig,
					Nonce:     nonce,
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
			fdTxSigned, err := types.SignTx(types.NewTx(fdTxInner), feeDelegateSigner, feePayerPriv)
			if err != nil {
				res.FDFail++
			} else {
				fdRaw, _ := fdTxSigned.MarshalBinary()
				if submitRawTx(senderEndpoints[0], fdRaw) != "" {
					res.FDSent++
				} else {
					res.FDFail++
				}
			}
		}
	}

	// === 3. Blob Tx ===
	{
		senderPriv, _ := crypto.HexToECDSA(senderKeys[2])
		nonce := getNonce(senderEndpoints[2], accounts[2].addr)

		kzgCtx, err := gokzg4844.NewContext4096Secure()
		if err != nil {
			res.BlobFail++
		} else {
			var blob gokzg4844.Blob
			blob[0] = byte(rand.Intn(64)) // must be < 128 for valid BLS12-381 field element
			blob[1] = byte(rand.Intn(256))

			commitment, err := kzgCtx.BlobToKZGCommitment(blob, 0)
			if err != nil {
				res.BlobFail++
			} else {
				proof, err := kzgCtx.ComputeBlobKZGProof(blob, commitment, 0)
				if err != nil {
					res.BlobFail++
				} else {
					versionedHash := kzg4844pkg.KZGToVersionedHash(commitment[:])
					toU256 := func(b *big.Int) *uint256.Int { v, _ := uint256.FromBig(b); return v }

					blobInner := &types.BlobTx{
						ChainID:          toU256(chainIDBig),
						Nonce:            nonce,
						GasTipCap:        toU256(gasPrice),
						GasFeeCap:        toU256(new(big.Int).Mul(gasPrice, big.NewInt(2))),
						Gas:              21000,
						To:               &toAddr,
						Value:            uint256.NewInt(0),
						MaxFeePerBlobGas: uint256.NewInt(1e9),
						BlobHashes:       []common.Hash{common.Hash(versionedHash)},
					}

					blobSignedTx, err := types.SignTx(types.NewTx(blobInner), londonSigner, senderPriv)
					if err != nil {
						res.BlobFail++
					} else {
						bv, br, bs := blobSignedTx.RawSignatureValues()
						signedBlobInner := *blobInner
						signedBlobInner.V = new(uint256.Int).SetBytes(bv.Bytes())
						signedBlobInner.R = new(uint256.Int).SetBytes(br.Bytes())
						signedBlobInner.S = new(uint256.Int).SetBytes(bs.Bytes())

						blobRaw, err := buildBlobNetworkEncoding(&signedBlobInner, blob[:], commitment[:], proof[:])
						if err != nil {
							res.BlobFail++
						} else {
							if submitRawTx(senderEndpoints[2], blobRaw) != "" {
								res.BlobSent++
							} else {
								res.BlobFail++
							}
						}
					}
				}
			}
		}
	}

	if *jsonOut {
		j, _ := json.Marshal(res)
		fmt.Println(string(j))
	} else {
		fmt.Printf("Normal: sent=%d fail=%d | FeeDelegate: sent=%d fail=%d | Blob: sent=%d fail=%d\n",
			res.NormalSent, res.NormalFail, res.FDSent, res.FDFail, res.BlobSent, res.BlobFail)
	}

	total := res.NormalFail + res.FDFail + res.BlobFail
	if total > 0 {
		os.Exit(1)
	}
}

func getNonce(endpoint string, addr common.Address) uint64 {
	return hexToUint64(rpcCall(endpoint, "eth_getTransactionCount", []any{addr.Hex(), "pending"}))
}

func submitRawTx(endpoint string, raw []byte) string {
	result := rpcCall(endpoint, "eth_sendRawTransaction", []any{"0x" + hex.EncodeToString(raw)})
	hash := strings.Trim(result, `"`)
	if strings.HasPrefix(hash, "0x") && len(hash) == 66 {
		return hash
	}
	fmt.Fprintf(os.Stderr, "submitRawTx: endpoint=%s result=%s\n", endpoint, result)
	return ""
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

func rpcCall(url, method string, params []any) string {
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(rpcReq{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)

	var r struct {
		Result json.RawMessage                        `json:"result"`
		Error  *struct{ Message string `json:"message"` } `json:"error"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return string(b)
	}
	if r.Error != nil {
		fmt.Fprintf(os.Stderr, "rpc error [%s]: %s\n", method, r.Error.Message)
		return ""
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

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}
