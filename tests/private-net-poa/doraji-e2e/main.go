// doraji-e2e: checks each Doraji EIP on both sides of the fork block against
// a running private network (tests/private-net-poa, set up with DORAJI_BLOCK).
//
// Before the fork every feature must be absent; after it every feature must
// work. Read-only checks use eth_call at the block before the fork and at the
// head; the EIP-7702 check sends transactions.
//
// Usage:
//
//	go run ./tests/private-net-poa/doraji-e2e/ [rpc-url] [doraji-block]
//
// Defaults: http://localhost:8545, 200.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/ethclient/gethclient"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// Hardhat account 0, funded in the private network's genesis.
const senderKeyHex = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"

var (
	pass, fail int
	ctx        = context.Background()
)

func ok(format string, a ...any)  { pass++; fmt.Printf("  PASS  "+format+"\n", a...) }
func bad(format string, a ...any) { fail++; fmt.Printf("  FAIL  "+format+"\n", a...) }

func main() {
	url, forkBlock := "http://localhost:8545", uint64(200)
	if len(os.Args) > 1 {
		url = os.Args[1]
	}
	if len(os.Args) > 2 {
		n, err := strconv.ParseUint(os.Args[2], 10, 64)
		if err != nil {
			die("doraji block: %v", err)
		}
		forkBlock = n
	}
	rc, err := rpc.Dial(url)
	if err != nil {
		die("dial: %v", err)
	}
	ec, gc := ethclient.NewClient(rc), gethclient.New(rc)

	fmt.Printf("=== Doraji fork e2e — %s, dorajiBlock %d ===\n", url, forkBlock)
	head := waitForBlock(ec, forkBlock+3)
	pre := new(big.Int).SetUint64(forkBlock - 1)
	fmt.Printf("head %d; pre-fork state = block %d\n\n", head, pre)

	checkCLZ(gc, pre)
	checkP256(ec, pre)
	checkBLS(ec, pre)
	checkModExpLimit(ec, pre)
	checkHistoryStorage(ec, gc, rc, forkBlock)
	checkSetCode(ec, rc)

	fmt.Printf("\n=== %d passed, %d failed ===\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "doraji-e2e: "+format+"\n", a...)
	os.Exit(2)
}

func waitForBlock(ec *ethclient.Client, n uint64) uint64 {
	for i := 0; ; i++ {
		head, err := ec.BlockNumber(ctx)
		if err == nil && head >= n {
			return head
		}
		if i%15 == 0 {
			fmt.Printf("waiting for block %d (head %d)\n", n, head)
		}
		time.Sleep(2 * time.Second)
	}
}

// call runs an eth_call against to with data at block (nil = latest).
func call(ec *ethclient.Client, to common.Address, data []byte, block *big.Int) ([]byte, error) {
	return ec.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data, Gas: 1_000_000}, block)
}

// EIP-7939: CLZ (0x1e). Runs PUSH1 1, CLZ, MSTORE, RETURN from a code
// override, so no deployment is needed.
func checkCLZ(gc *gethclient.Client, pre *big.Int) {
	fmt.Println("EIP-7939 CLZ")
	probe := common.HexToAddress("0x000000000000000000000000000000000000c12a")
	code := common.FromHex("0x60011e60005260206000f3")
	ov := map[common.Address]gethclient.OverrideAccount{probe: {Code: code}}
	msg := ethereum.CallMsg{To: &probe, Gas: 100_000}

	if _, err := gc.CallContract(ctx, msg, pre, &ov); err != nil && strings.Contains(err.Error(), "invalid opcode") {
		ok("before the fork 0x1e is an invalid opcode")
	} else {
		bad("before the fork: want invalid opcode, got err %v", err)
	}
	out, err := gc.CallContract(ctx, msg, nil, &ov)
	if err == nil && new(big.Int).SetBytes(out).Uint64() == 255 {
		ok("after the fork CLZ(1) = 255")
	} else {
		bad("after the fork: CLZ(1) = %x, err %v", out, err)
	}
}

// EIP-7951: P256VERIFY at 0x100, with a signature made here.
func checkP256(ec *ethclient.Client, pre *big.Int) {
	fmt.Println("EIP-7951 P256VERIFY")
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	hash := sha256.Sum256([]byte("doraji"))
	r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		die("p256 sign: %v", err)
	}
	input := append(hash[:], common.LeftPadBytes(r.Bytes(), 32)...)
	input = append(input, common.LeftPadBytes(s.Bytes(), 32)...)
	input = append(input, common.LeftPadBytes(key.X.Bytes(), 32)...)
	input = append(input, common.LeftPadBytes(key.Y.Bytes(), 32)...)
	p256 := common.BytesToAddress([]byte{0x01, 0x00})

	if out, err := call(ec, p256, input, pre); err == nil && len(out) == 0 {
		ok("before the fork 0x100 is an empty account")
	} else {
		bad("before the fork: 0x100 returned %x, err %v", out, err)
	}
	if out, err := call(ec, p256, input, nil); err == nil && bytes.Equal(out, common.LeftPadBytes([]byte{1}, 32)) {
		ok("after the fork a valid signature verifies")
	} else {
		bad("after the fork: valid signature returned %x, err %v", out, err)
	}
	input[0] ^= 0xff
	if out, err := call(ec, p256, input, nil); err == nil && len(out) == 0 {
		ok("after the fork a wrong hash returns empty")
	} else {
		bad("after the fork: wrong hash returned %x, err %v", out, err)
	}
}

// EIP-2537: G1ADD at 0x0b; adding the point at infinity to itself returns 128
// zero bytes. Before the fork 0x0b is an empty account and returns nothing.
func checkBLS(ec *ethclient.Client, pre *big.Int) {
	fmt.Println("EIP-2537 BLS12-381")
	g1add := common.BytesToAddress([]byte{0x0b})
	input := make([]byte, 256)
	if out, err := call(ec, g1add, input, pre); err == nil && len(out) == 0 {
		ok("before the fork 0x0b is an empty account")
	} else {
		bad("before the fork: 0x0b returned %d bytes, err %v", len(out), err)
	}
	if out, err := call(ec, g1add, input, nil); err == nil && len(out) == 128 && bytes.Equal(out, make([]byte, 128)) {
		ok("after the fork G1ADD(inf, inf) = inf")
	} else {
		bad("after the fork: G1ADD returned %d bytes, err %v", len(out), err)
	}
}

// EIP-7823: ModExp refuses a base longer than 1024 bytes after the fork.
func checkModExpLimit(ec *ethclient.Client, pre *big.Int) {
	fmt.Println("EIP-7823 ModExp input limit")
	modexp := common.BytesToAddress([]byte{0x05})
	input := modexpInput(1025)
	if out, err := call(ec, modexp, input, pre); err == nil && len(out) == 1 {
		ok("before the fork a 1025-byte base is accepted")
	} else {
		bad("before the fork: 1025-byte base returned %x, err %v", out, err)
	}
	if _, err := call(ec, modexp, input, nil); err != nil {
		ok("after the fork a 1025-byte base is refused (%v)", err)
	} else {
		bad("after the fork: a 1025-byte base was accepted")
	}
	if out, err := call(ec, modexp, modexpInput(1024), nil); err == nil && len(out) == 1 {
		ok("after the fork a 1024-byte base is accepted")
	} else {
		bad("after the fork: 1024-byte base returned %x, err %v", out, err)
	}
}

// modexpInput is base^1 mod 7 with a base of baseLen bytes.
func modexpInput(baseLen int) []byte {
	in := common.LeftPadBytes(big.NewInt(int64(baseLen)).Bytes(), 32)
	in = append(in, common.LeftPadBytes([]byte{1}, 32)...)
	in = append(in, common.LeftPadBytes([]byte{1}, 32)...)
	base := make([]byte, baseLen)
	base[baseLen-1] = 9
	in = append(in, base...)
	return append(in, 1, 7)
}

// EIP-2935: the history contract is written into the state at the fork
// block, and from then on holds each parent block hash. The hash it holds is
// compared with the node's own block hash and with BLOCKHASH; a header
// decoded by ethclient is not used, since Header.Hash() recomputed on the
// client side does not cover Metadium's extra header fields.
func checkHistoryStorage(ec *ethclient.Client, gc *gethclient.Client, rc *rpc.Client, forkBlock uint64) {
	fmt.Println("EIP-2935 history storage")
	addr := params.HistoryStorageAddress
	before := new(big.Int).SetUint64(forkBlock - 1)
	if code, err := ec.CodeAt(ctx, addr, before); err == nil && len(code) == 0 {
		ok("before the fork the contract has no code")
	} else {
		bad("before the fork: code %x, err %v", code, err)
	}
	if code, err := ec.CodeAt(ctx, addr, new(big.Int).SetUint64(forkBlock)); err == nil && bytes.Equal(code, params.HistoryStorageCode) {
		ok("at the fork block the contract code is installed")
	} else {
		bad("at the fork block: code %d bytes, err %v", len(code), err)
	}
	// The contract serves the last HistoryServeWindow blocks and reverts for
	// anything older; BLOCKHASH answers for the last 256. Both windows are
	// taken from the head at the time of the call, so what is expected of a
	// block depends on how far the chain has moved on: right after the fork
	// the fork's parent is in both windows, hours later in neither. The
	// checks below say which case they are in rather than assume the first.
	head, _ := ec.BlockNumber(ctx)
	probe := common.HexToAddress("0x000000000000000000000000000000000000c12b")
	for _, n := range []uint64{forkBlock - 1, head - 1} {
		var block struct {
			Hash common.Hash `json:"hash"`
		}
		if err := rc.CallContext(ctx, &block, "eth_getBlockByNumber", hexutil.EncodeUint64(n), false); err != nil {
			bad("block %d: %v", n, err)
			continue
		}
		num := common.LeftPadBytes(new(big.Int).SetUint64(n).Bytes(), 32)
		out, err := call(ec, addr, num, nil)
		if head-n > params.HistoryServeWindow {
			if err != nil {
				ok("block %d is %d blocks behind the head, outside the window: the contract reverts", n, head-n)
			} else {
				bad("block %d, outside the window: contract returned %x, want a revert", n, out)
			}
			continue
		}
		if err != nil || common.BytesToHash(out) != block.Hash {
			bad("block %d: contract %x, node %x, err %v", n, out, block.Hash, err)
			continue
		}
		if head-n > 256 {
			ok("the contract returns the hash of block %d, beyond BLOCKHASH's reach", n)
			continue
		}
		// PUSH32 n, BLOCKHASH, MSTORE, RETURN 32
		code := append(append([]byte{0x7f}, num...), common.FromHex("0x4060005260206000f3")...)
		ov := map[common.Address]gethclient.OverrideAccount{probe: {Code: code}}
		if opcode, err := gc.CallContract(ctx, ethereum.CallMsg{To: &probe, Gas: 100_000}, nil, &ov); err == nil && common.BytesToHash(opcode) == block.Hash {
			ok("the contract returns the hash of block %d, as BLOCKHASH does", n)
		} else {
			bad("block %d: BLOCKHASH %x, node %x, err %v", n, opcode, block.Hash, err)
		}
	}
	// The block before the fork's parent was never recorded: inside the
	// window the contract returns the empty slot for it, outside it reverts
	// like for any other old block.
	unrecorded := forkBlock - 2
	out, err := call(ec, addr, common.LeftPadBytes(new(big.Int).SetUint64(unrecorded).Bytes(), 32), nil)
	switch {
	case head-unrecorded > params.HistoryServeWindow:
		if err != nil {
			ok("block %d, never recorded and outside the window: the contract reverts", unrecorded)
		} else {
			bad("block %d, outside the window: contract returned %x, want a revert", unrecorded, out)
		}
	case err == nil && common.BytesToHash(out) == (common.Hash{}):
		ok("block %d, before the fork's parent, was never recorded", unrecorded)
	default:
		bad("block %d: contract returned %x, err %v", unrecorded, out, err)
	}
}

// returns42 is the runtime code of the delegation target.
var returns42 = common.FromHex("0x602a60005260206000f3")

// EIP-7702: delegate a fresh account to a contract, call it, and check the
// RPC view of the transaction; an empty authorization list is refused.
func checkSetCode(ec *ethclient.Client, rc *rpc.Client) {
	fmt.Println("EIP-7702 set-code transaction")
	chainID, err := ec.ChainID(ctx)
	if err != nil {
		die("chain id: %v", err)
	}
	signer := types.NewDorajiSigner(chainID)
	key, _ := crypto.HexToECDSA(senderKeyHex)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	tip, _ := ec.SuggestGasTipCap(ctx)
	head, _ := ec.HeaderByNumber(ctx, nil)
	feeCap := new(big.Int).Add(new(big.Int).Mul(head.BaseFee, big.NewInt(2)), tip)

	// Deploy the target: initcode that returns the runtime code.
	initcode := append(common.FromHex("0x600a600c600039600a6000f3"), returns42...)
	nonce, _ := ec.PendingNonceAt(ctx, sender)
	deploy := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{
		ChainID: chainID, Nonce: nonce, GasTipCap: tip, GasFeeCap: feeCap, Gas: 100_000, Data: initcode,
	})
	rcpt := send(ec, deploy)
	if rcpt == nil || rcpt.Status != 1 {
		bad("target deployment failed")
		return
	}
	target := rcpt.ContractAddress
	if code, _ := ec.CodeAt(ctx, target, nil); !bytes.Equal(code, returns42) {
		bad("target code %x", code)
		return
	}

	authKey, _ := crypto.GenerateKey()
	authority := crypto.PubkeyToAddress(authKey.PublicKey)
	auth, err := types.SignSetCode(authKey, types.SetCodeAuthorization{
		ChainID: *uint256.MustFromBig(chainID), Address: target, Nonce: 0,
	})
	if err != nil {
		die("sign authorization: %v", err)
	}
	cid := uint256.MustFromBig(chainID)
	tx := types.MustSignNewTx(key, signer, &types.SetCodeTx{
		ChainID: cid, Nonce: nonce + 1, GasTipCap: uint256.MustFromBig(tip), GasFeeCap: uint256.MustFromBig(feeCap),
		Gas: 100_000, To: authority, Value: uint256.NewInt(0), AuthList: []types.SetCodeAuthorization{auth},
	})
	rcpt = send(ec, tx)
	if rcpt != nil && rcpt.Status == 1 && rcpt.Type == types.SetCodeTxType {
		ok("set-code transaction mined in block %d", rcpt.BlockNumber)
	} else {
		bad("set-code transaction: receipt %+v", rcpt)
		return
	}
	if code, _ := ec.CodeAt(ctx, authority, nil); bytes.Equal(code, types.AddressToDelegation(target)) {
		ok("the authority carries the delegation designator")
	} else {
		bad("authority code %x", code)
	}
	if n, _ := ec.NonceAt(ctx, authority, nil); n == 1 {
		ok("the authority's nonce is 1")
	} else {
		bad("authority nonce %d", n)
	}
	if out, err := call(ec, authority, nil, nil); err == nil && new(big.Int).SetBytes(out).Uint64() == 42 {
		ok("a call to the authority runs the target's code")
	} else {
		bad("call to the authority returned %x, err %v", out, err)
	}
	var raw map[string]any
	if err := rc.CallContext(ctx, &raw, "eth_getTransactionByHash", tx.Hash()); err == nil && raw["type"] == "0x4" {
		if list, _ := raw["authorizationList"].([]any); len(list) == 1 {
			ok("eth_getTransactionByHash shows type 0x4 with the authorization list")
		} else {
			bad("eth_getTransactionByHash: authorizationList %v", raw["authorizationList"])
		}
	} else {
		bad("eth_getTransactionByHash: type %v, err %v", raw["type"], err)
	}

	empty := types.MustSignNewTx(key, signer, &types.SetCodeTx{
		ChainID: cid, Nonce: nonce + 2, GasTipCap: uint256.MustFromBig(tip), GasFeeCap: uint256.MustFromBig(feeCap),
		Gas: 100_000, To: authority, Value: uint256.NewInt(0),
	})
	if err := ec.SendTransaction(ctx, empty); err != nil {
		ok("an empty authorization list is refused (%v)", err)
	} else {
		bad("a set-code transaction with no authorizations was accepted")
	}
}

func send(ec *ethclient.Client, tx *types.Transaction) *types.Receipt {
	if err := ec.SendTransaction(ctx, tx); err != nil {
		bad("send %x: %v", tx.Hash(), err)
		return nil
	}
	for i := 0; i < 60; i++ {
		if r, err := ec.TransactionReceipt(ctx, tx.Hash()); err == nil {
			return r
		}
		time.Sleep(time.Second)
	}
	bad("no receipt for %x", tx.Hash())
	return nil
}
