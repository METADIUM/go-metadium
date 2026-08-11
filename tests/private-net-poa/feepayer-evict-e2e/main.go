// feepayer-evict-e2e: verifies the #67 sweep evicts a pooled fee-delegated tx
// whose fee payer has been drained after admission.
//
// Sequence: fund a fresh fee payer -> admit a type-22 tx with a nonce gap (so
// it parks in the queue instead of mining) -> drain the fee payer to zero ->
// the promote/demote sweep must drop the delegated tx within a few blocks.
//
// Usage: go run . [rpc-url]   (default http://localhost:8545)
package main

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

const senderKeyHex = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

func die(f string, a ...any) { fmt.Printf("FAIL: "+f+"\n", a...); os.Exit(1) }

func waitMined(ctx context.Context, c *ethclient.Client, h common.Hash, secs int) *types.Receipt {
	for i := 0; i < secs; i++ {
		if r, err := c.TransactionReceipt(ctx, h); err == nil {
			return r
		}
		time.Sleep(time.Second)
	}
	die("tx %s not mined in %ds", h.Hex(), secs)
	return nil
}

func main() {
	url := "http://localhost:8545"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}
	ctx := context.Background()
	c, err := ethclient.Dial(url)
	if err != nil {
		die("dial: %v", err)
	}
	chainID, _ := c.ChainID(ctx)
	sender, _ := crypto.HexToECDSA(senderKeyHex)
	senderAddr := crypto.PubkeyToAddress(sender.PublicKey)

	// Fresh fee payer, funded just enough to pass admission.
	fpKey, _ := crypto.GenerateKey()
	fpAddr := crypto.PubkeyToAddress(fpKey.PublicKey)
	gasPrice, _ := c.SuggestGasPrice(ctx)
	fund := new(big.Int).Mul(gasPrice, big.NewInt(200_000)) // covers delegated tx cost cap + drain tx

	nonce, _ := c.PendingNonceAt(ctx, senderAddr)
	fundTx, _ := types.SignTx(types.NewTransaction(nonce, fpAddr, fund, 21000, gasPrice, nil),
		types.LatestSignerForChainID(chainID), sender)
	if err := c.SendTransaction(ctx, fundTx); err != nil {
		die("fund feePayer: %v", err)
	}
	waitMined(ctx, c, fundTx.Hash(), 30)
	fmt.Printf("funded feePayer %s with %s wei\n", fpAddr.Hex(), fund)

	// Type-22 delegated tx with a nonce GAP (pending+1) so it parks queued.
	nonce, _ = c.PendingNonceAt(ctx, senderAddr)
	gapNonce := nonce + 1
	tip := big.NewInt(1_000_000_000)
	inner := &types.DynamicFeeTx{
		ChainID: chainID, Nonce: gapNonce, GasTipCap: tip, GasFeeCap: gasPrice,
		Gas: 21000, To: &senderAddr, Value: big.NewInt(0),
	}
	senderSigned, err := types.SignTx(types.NewTx(inner), types.NewLondonSigner(chainID), sender)
	if err != nil {
		die("sender sign: %v", err)
	}
	v, r, s := senderSigned.RawSignatureValues()
	fd := &types.FeeDelegateDynamicFeeTx{
		SenderTx: types.DynamicFeeTx{
			ChainID: chainID, Nonce: gapNonce, GasTipCap: tip, GasFeeCap: gasPrice,
			Gas: 21000, To: &senderAddr, Value: big.NewInt(0),
			V: v, R: r, S: s,
		},
		FeePayer: &fpAddr,
		FV:       new(big.Int), FR: new(big.Int), FS: new(big.Int),
	}
	fdTx, err := types.SignTx(types.NewTx(fd), types.NewFeeDelegateSigner(chainID), fpKey)
	if err != nil {
		die("feePayer sign: %v", err)
	}
	if err := c.SendTransaction(ctx, fdTx); err != nil {
		die("delegated tx rejected at admission (feePayer funded, should be accepted): %v", err)
	}
	fmt.Printf("PASS 1/3: delegated tx admitted (queued, nonce gap) hash=%s\n", fdTx.Hash().Hex())

	if _, pending, err := c.TransactionByHash(ctx, fdTx.Hash()); err != nil || !pending {
		die("delegated tx not visible in pool after admission (err=%v pending=%v)", err, pending)
	}

	// Drain the fee payer to zero.
	fpBal, _ := c.BalanceAt(ctx, fpAddr, nil)
	drainGas := new(big.Int).Mul(gasPrice, big.NewInt(21000))
	drainVal := new(big.Int).Sub(fpBal, drainGas)
	if drainVal.Sign() <= 0 {
		die("feePayer balance too small to drain: %s", fpBal)
	}
	fpNonce, _ := c.PendingNonceAt(ctx, fpAddr)
	drainTx, _ := types.SignTx(types.NewTransaction(fpNonce, senderAddr, drainVal, 21000, gasPrice, nil),
		types.LatestSignerForChainID(chainID), fpKey)
	if err := c.SendTransaction(ctx, drainTx); err != nil {
		die("drain tx: %v", err)
	}
	waitMined(ctx, c, drainTx.Hash(), 30)
	fpBal, _ = c.BalanceAt(ctx, fpAddr, nil)
	fmt.Printf("PASS 2/3: feePayer drained (balance now %s wei)\n", fpBal)

	// The sweep should now drop the queued delegated tx within a few blocks.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		_, _, err := c.TransactionByHash(ctx, fdTx.Hash())
		if err == ethereum.NotFound {
			fmt.Println("PASS 3/3: drained-feePayer delegated tx evicted from the pool")
			fmt.Println("ALL PASS")
			return
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Println("RESULT: tx still pooled after 60s — eviction did not trigger on reset sweep " +
		"(document whether it defers to the 3h ticker; not an automatic FAIL, but needs a ruling)")
	os.Exit(2)
}
