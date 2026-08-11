// bigtx-e2e: verifies the restored 256KB txpool ceiling end-to-end on a live
// PoA network mirroring production block gas limit (105M).
//
//  1. NEGATIVE: a creation tx whose RLP size exceeds params.MaxTransactionSize
//     (262,144) must be rejected by the pool with "oversized data".
//  2. POSITIVE: a creation tx depositing a max-size contract
//     (params.MaxCodeSize = 253,952 runtime bytes, ~254KB tx) must be
//     accepted, propagated to the other nodes' pools, mined with status 1,
//     and the deployed code must read back at exactly 253,952 bytes.
//
// Usage: go run . [rpc1] [rpc2] [rpc3]   (defaults: localhost:8545/8546/8547)
package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

const hardhatKey0 = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

func die(format string, a ...any) {
	fmt.Printf("FAIL: "+format+"\n", a...)
	os.Exit(1)
}

// initcodeFor returns initcode that deposits `runtimeLen` bytes of runtime
// code: 13-byte CODECOPY prologue followed by the payload.
func initcodeFor(runtimeLen int) []byte {
	if runtimeLen > 0xFFFFFF {
		panic("runtimeLen too large for PUSH3")
	}
	pro := []byte{
		0x62, byte(runtimeLen >> 16), byte(runtimeLen >> 8), byte(runtimeLen), // PUSH3 len
		0x80,       // DUP1
		0x60, 0x0d, // PUSH1 13 (payload offset)
		0x60, 0x00, // PUSH1 0  (mem dest)
		0x39,       // CODECOPY
		0x60, 0x00, // PUSH1 0
		0xf3, // RETURN
	}
	return append(pro, make([]byte, runtimeLen)...)
}

func signedCreation(key *ecdsa.PrivateKey, chainID *big.Int, nonce uint64, gasPrice *big.Int, gas uint64, initcode []byte) *types.Transaction {
	tx := types.NewContractCreation(nonce, big.NewInt(0), gas, gasPrice, initcode)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
	if err != nil {
		die("sign: %v", err)
	}
	return signed
}

func main() {
	urls := []string{"http://localhost:8545", "http://localhost:8546", "http://localhost:8547"}
	for i, a := range os.Args[1:] {
		if i < 3 {
			urls[i] = a
		}
	}
	ctx := context.Background()

	key, err := crypto.HexToECDSA(hardhatKey0)
	if err != nil {
		die("key: %v", err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)

	var clients []*ethclient.Client
	for _, u := range urls {
		c, err := ethclient.Dial(u)
		if err != nil {
			die("dial %s: %v", u, err)
		}
		clients = append(clients, c)
	}
	c1 := clients[0]

	chainID, err := c1.ChainID(ctx)
	if err != nil {
		die("chainID: %v", err)
	}
	head, err := c1.HeaderByNumber(ctx, nil)
	if err != nil {
		die("head: %v", err)
	}
	bal, _ := c1.BalanceAt(ctx, from, nil)
	gasPrice, err := c1.SuggestGasPrice(ctx)
	if err != nil {
		die("gasPrice: %v", err)
	}
	fmt.Printf("chainID=%v head=%v gasLimit=%d camellia@100 sender=%s balance=%s gasPrice=%s\n",
		chainID, head.Number, head.GasLimit, from.Hex(), bal.String(), gasPrice.String())
	fmt.Printf("params: MaxCodeSize=%d MaxTransactionSize=%d\n", params.MaxCodeSize, params.MaxTransactionSize)

	nonce, err := c1.PendingNonceAt(ctx, from)
	if err != nil {
		die("nonce: %v", err)
	}

	// ---- 1. NEGATIVE: tx RLP > 262,144 must be rejected ----
	over := signedCreation(key, chainID, nonce, gasPrice, 60_000_000, initcodeFor(params.MaxTransactionSize))
	oRaw, _ := rlp.EncodeToBytes(over)
	err = c1.SendTransaction(ctx, over)
	if err == nil {
		die("oversized tx (%d bytes RLP) was ACCEPTED — ceiling not enforced", len(oRaw))
	}
	if !strings.Contains(err.Error(), "oversized") {
		die("oversized tx rejected with unexpected error: %v", err)
	}
	fmt.Printf("PASS 1/4: oversized tx (RLP %d > %d) rejected: %v\n", len(oRaw), params.MaxTransactionSize, err)

	// ---- 2. POSITIVE: max-size contract deployment ----
	initcode := initcodeFor(params.MaxCodeSize)
	big1 := signedCreation(key, chainID, nonce, gasPrice, 60_000_000, initcode)
	bRaw, _ := rlp.EncodeToBytes(big1)
	if len(bRaw) <= 131072 {
		die("test tx unexpectedly small (%d bytes) — not exercising the 128KB regression", len(bRaw))
	}
	if len(bRaw) > params.MaxTransactionSize {
		die("test tx too big (%d bytes) — constructor overhead math wrong", len(bRaw))
	}
	if err := c1.SendTransaction(ctx, big1); err != nil {
		die("max-size deployment tx (RLP %d bytes) rejected by pool: %v", len(bRaw), err)
	}
	fmt.Printf("PASS 2/4: %d-byte tx (>128KB legacy limit, <=256KB) accepted by node1 pool, hash=%s\n", len(bRaw), big1.Hash().Hex())

	// ---- 3. PROPAGATION: other nodes must see the tx ----
	seen := map[int]bool{}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && (!seen[1] || !seen[2]) {
		for i := 1; i <= 2; i++ {
			if seen[i] {
				continue
			}
			if _, pending, err := clients[i].TransactionByHash(ctx, big1.Hash()); err == nil {
				seen[i] = true
				fmt.Printf("PASS 3/4: tx visible on node%d (pending=%v)\n", i+1, pending)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !seen[1] || !seen[2] {
		die("tx did not propagate to all nodes within 60s (node2=%v node3=%v)", seen[1], seen[2])
	}

	// ---- 4. INCLUSION + code readback ----
	var rcpt *types.Receipt
	deadline = time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		rcpt, err = c1.TransactionReceipt(ctx, big1.Hash())
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if rcpt == nil {
		die("tx not mined within 120s")
	}
	if rcpt.Status != 1 {
		die("tx mined but reverted (status=0), gasUsed=%d", rcpt.GasUsed)
	}
	blk, err := c1.BlockByNumber(ctx, rcpt.BlockNumber)
	if err != nil {
		die("block: %v", err)
	}
	code, err := c1.CodeAt(ctx, rcpt.ContractAddress, nil)
	if err != nil {
		die("codeAt: %v", err)
	}
	if len(code) != params.MaxCodeSize {
		die("deployed code %d bytes, want %d", len(code), params.MaxCodeSize)
	}
	fmt.Printf("PASS 4/4: mined in block %v (status=1) gasUsed=%d blockGasLimit=%d — deployed code %d bytes at %s\n",
		rcpt.BlockNumber, rcpt.GasUsed, blk.GasLimit(), len(code), rcpt.ContractAddress.Hex())
	fmt.Printf("ALL PASS — 256KB pool ceiling verified end-to-end (accept, propagate, include, %d-byte code deposit under %d gas limit)\n",
		len(code), blk.GasLimit())
}
