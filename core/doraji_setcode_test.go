// Copyright 2026 The go-metadium Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"bytes"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// returns42 is the runtime code of the delegation target: PUSH1 42, MSTORE,
// RETURN 32 bytes. It returns the word 42 whatever the input.
var returns42 = common.Hex2Bytes("602a60005260206000f3")

// setCodeEnv is a chain at block 1 with a funded sender, a funded authority
// at nonce 0, and a contract that returns 42.
type setCodeEnv struct {
	t         *testing.T
	cfg       *params.ChainConfig
	chain     *BlockChain
	header    *types.Header
	state     *state.StateDB
	senderKey *ecdsa.PrivateKey
	authKey   *ecdsa.PrivateKey
	sender    common.Address
	authority common.Address
	target    common.Address
}

func newSetCodeEnv(t *testing.T, cfg *params.ChainConfig) *setCodeEnv {
	t.Helper()
	e := &setCodeEnv{t: t, cfg: cfg, target: common.HexToAddress("0x00000000000000000000000000000000000000cc")}
	e.senderKey, _ = crypto.GenerateKey()
	e.authKey, _ = crypto.GenerateKey()
	e.sender = crypto.PubkeyToAddress(e.senderKey.PublicKey)
	e.authority = crypto.PubkeyToAddress(e.authKey.PublicKey)

	db := rawdb.NewMemoryDatabase()
	gspec := &Genesis{Config: cfg, Alloc: GenesisAlloc{
		e.sender:    {Balance: big.NewInt(1e18)},
		e.authority: {Balance: big.NewInt(1e18)},
		e.target:    {Balance: big.NewInt(0), Code: returns42},
	}}
	genesis := gspec.MustCommit(db, nil)
	e.chain, _ = NewBlockChain(db, nil, gspec, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
	t.Cleanup(e.chain.Stop)
	blocks, _ := GenerateChain(cfg, genesis, ethash.NewFaker(), db, 1, nil)
	e.header = blocks[0].Header()
	var err error
	e.state, err = state.New(genesis.Root(), state.NewDatabase(db), nil)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	return e
}

// signer is the Doraji signer regardless of cfg: the tests that run before
// the fork need a signed set-code transaction to be refused by execution.
func (e *setCodeEnv) signer() types.Signer { return types.NewDorajiSigner(e.cfg.ChainID) }

func (e *setCodeEnv) authorization(key *ecdsa.PrivateKey, chainID uint64, to common.Address, nonce uint64) types.SetCodeAuthorization {
	e.t.Helper()
	auth, err := types.SignSetCode(key, types.SetCodeAuthorization{ChainID: *uint256.NewInt(chainID), Address: to, Nonce: nonce})
	if err != nil {
		e.t.Fatal(err)
	}
	return auth
}

// apply executes tx against the environment's state and returns the result.
func (e *setCodeEnv) apply(tx *types.Transaction) (*ExecutionResult, error) {
	e.t.Helper()
	msg, err := TransactionToMessage(tx, e.signer(), e.header.BaseFee)
	if err != nil {
		e.t.Fatalf("message: %v", err)
	}
	evm := vm.NewEVM(NewEVMBlockContext(e.header, e.chain, nil), NewEVMTxContext(msg), e.state, e.cfg, vm.Config{})
	return ApplyMessage(evm, msg, new(GasPool).AddGas(e.header.GasLimit))
}

func (e *setCodeEnv) setCodeTx(nonce uint64, to common.Address, auths []types.SetCodeAuthorization) *types.Transaction {
	chainID, _ := uint256.FromBig(e.cfg.ChainID)
	return types.MustSignNewTx(e.senderKey, e.signer(), &types.SetCodeTx{
		ChainID: chainID, Nonce: nonce, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(1e9),
		Gas: 200_000, To: to, Value: uint256.NewInt(0), AuthList: auths,
	})
}

func dorajiTestConfig() *params.ChainConfig {
	c := *params.AllEthashProtocolChanges
	c.DorajiBlock = big.NewInt(0)
	return &c
}

// TestDorajiSetCode: a set-code transaction installs the delegation on the
// authority, bumps its nonce, and a call to the authority runs the target's
// code; the delegated account can still send transactions; a zero-address
// authorization clears the delegation.
func TestDorajiSetCode(t *testing.T) {
	e := newSetCodeEnv(t, dorajiTestConfig())
	word42 := common.LeftPadBytes([]byte{42}, 32)

	// Delegate the authority to the target, and call the authority in the
	// same transaction.
	auth := e.authorization(e.authKey, e.cfg.ChainID.Uint64(), e.target, 0)
	res, err := e.apply(e.setCodeTx(0, e.authority, []types.SetCodeAuthorization{auth}))
	if err != nil || res.Err != nil {
		t.Fatalf("set-code tx: err %v, vm err %v", err, res.Err)
	}
	if !bytes.Equal(res.ReturnData, word42) {
		t.Fatalf("call to the delegated authority returned %x, want 42", res.ReturnData)
	}
	if code := e.state.GetCode(e.authority); !bytes.Equal(code, types.AddressToDelegation(e.target)) {
		t.Fatalf("authority code %x, want the delegation designator", code)
	}
	if n := e.state.GetNonce(e.authority); n != 1 {
		t.Fatalf("authority nonce %d, want 1", n)
	}
	if n := e.state.GetNonce(e.sender); n != 1 {
		t.Fatalf("sender nonce %d, want 1", n)
	}
	// The refund for an existing authority: new-account cost charged in the
	// intrinsic gas, TxAuthTupleGas kept.
	if res.RefundedGas == 0 {
		t.Fatal("no refund for an authority that exists")
	}

	// The delegated account is still an EOA for sending.
	plain := types.MustSignNewTx(e.authKey, e.signer(), &types.LegacyTx{
		Nonce: 1, GasPrice: big.NewInt(1e9), Gas: 100_000, To: &e.target, Value: big.NewInt(0),
	})
	if res, err := e.apply(plain); err != nil || res.Err != nil {
		t.Fatalf("tx from the delegated authority: err %v, vm err %v", err, res.Err)
	} else if !bytes.Equal(res.ReturnData, word42) {
		t.Fatalf("direct call to the target returned %x", res.ReturnData)
	}

	// Clear the delegation with the zero address, at the bumped nonce.
	clear := e.authorization(e.authKey, e.cfg.ChainID.Uint64(), common.Address{}, 2)
	res, err = e.apply(e.setCodeTx(1, e.authority, []types.SetCodeAuthorization{clear}))
	if err != nil || res.Err != nil {
		t.Fatalf("clearing tx: err %v, vm err %v", err, res.Err)
	}
	if code := e.state.GetCode(e.authority); len(code) != 0 {
		t.Fatalf("authority code after clearing: %x", code)
	}
	if len(res.ReturnData) != 0 {
		t.Fatalf("call to a cleared authority returned %x", res.ReturnData)
	}
	if n := e.state.GetNonce(e.authority); n != 3 {
		t.Fatalf("authority nonce %d, want 3", n)
	}
}

// TestDorajiSetCodeInvalidAuthorizations: an authorization for another chain,
// at the wrong nonce, or for an account with real code is skipped without
// failing the transaction; an authorization with chain id 0 applies.
func TestDorajiSetCodeInvalidAuthorizations(t *testing.T) {
	e := newSetCodeEnv(t, dorajiTestConfig())
	otherKey, _ := crypto.GenerateKey()
	other := crypto.PubkeyToAddress(otherKey.PublicKey)
	e.state.SetBalance(other, uint256.NewInt(1))

	auths := []types.SetCodeAuthorization{
		e.authorization(e.authKey, e.cfg.ChainID.Uint64()+1, e.target, 0), // wrong chain
		e.authorization(e.authKey, e.cfg.ChainID.Uint64(), e.target, 5),   // wrong nonce
		e.authorization(otherKey, 0, e.target, 0),                         // chain id 0: valid everywhere
	}
	res, err := e.apply(e.setCodeTx(0, e.authority, auths))
	if err != nil || res.Err != nil {
		t.Fatalf("set-code tx: err %v, vm err %v", err, res.Err)
	}
	if code := e.state.GetCode(e.authority); len(code) != 0 {
		t.Fatalf("an invalid authorization installed code %x", code)
	}
	if n := e.state.GetNonce(e.authority); n != 0 {
		t.Fatalf("an invalid authorization bumped the nonce to %d", n)
	}
	if code := e.state.GetCode(other); !bytes.Equal(code, types.AddressToDelegation(e.target)) {
		t.Fatalf("chain id 0 authorization not applied: code %x", code)
	}

	// An account with real code cannot be delegated: the target itself.
	targetKey, _ := crypto.GenerateKey()
	targetAddr := crypto.PubkeyToAddress(targetKey.PublicKey)
	e.state.SetCode(targetAddr, returns42)
	res, err = e.apply(e.setCodeTx(1, e.authority, []types.SetCodeAuthorization{e.authorization(targetKey, 0, e.authority, 0)}))
	if err != nil || res.Err != nil {
		t.Fatalf("set-code tx: err %v, vm err %v", err, res.Err)
	}
	if code := e.state.GetCode(targetAddr); !bytes.Equal(code, returns42) {
		t.Fatalf("a contract's code was replaced: %x", code)
	}
}

// TestDorajiSetCodeGates: a set-code transaction is refused before Doraji,
// and with an empty authorization list after it.
func TestDorajiSetCodeGates(t *testing.T) {
	pre := newSetCodeEnv(t, params.AllEthashProtocolChanges)
	auth := pre.authorization(pre.authKey, pre.cfg.ChainID.Uint64(), pre.target, 0)
	if _, err := pre.apply(pre.setCodeTx(0, pre.authority, []types.SetCodeAuthorization{auth})); !errors.Is(err, ErrTxTypeNotSupported) {
		t.Fatalf("set-code tx before Doraji: err = %v, want ErrTxTypeNotSupported", err)
	}
	if code := pre.state.GetCode(pre.authority); len(code) != 0 {
		t.Fatalf("delegation installed before the fork: %x", code)
	}

	post := newSetCodeEnv(t, dorajiTestConfig())
	if _, err := post.apply(post.setCodeTx(0, post.authority, []types.SetCodeAuthorization{})); !errors.Is(err, ErrEmptyAuthList) {
		t.Fatalf("empty authorization list: err = %v, want ErrEmptyAuthList", err)
	}
}

// TestDorajiIntrinsicGas: each authorization is charged as a new account up
// front.
func TestDorajiIntrinsicGas(t *testing.T) {
	base, _ := IntrinsicGas(nil, nil, nil, false, true, true, true)
	one, _ := IntrinsicGas(nil, nil, make([]types.SetCodeAuthorization, 1), false, true, true, true)
	three, _ := IntrinsicGas(nil, nil, make([]types.SetCodeAuthorization, 3), false, true, true, true)
	if one-base != params.CallNewAccountGas || three-base != 3*params.CallNewAccountGas {
		t.Fatalf("intrinsic gas: base %d, one %d, three %d", base, one, three)
	}
}

// TestDorajiSetCodeSelfDestructKeepsAuthority: an authority that did not
// exist before the transaction, delegated to code that runs SELFDESTRUCT,
// survives. EIP-6780 deletes only contracts created by CREATE in the same
// transaction; an authorization gives the account code without a CREATE.
// (execution-spec-tests test_set_code_to_self_destruct, balance_0.)
func TestDorajiSetCodeSelfDestructKeepsAuthority(t *testing.T) {
	e := newSetCodeEnv(t, dorajiTestConfig())
	destructor := common.HexToAddress("0x00000000000000000000000000000000000000dd")
	e.state.SetCode(destructor, common.Hex2Bytes("30ff")) // ADDRESS, SELFDESTRUCT
	freshKey, _ := crypto.GenerateKey()
	fresh := crypto.PubkeyToAddress(freshKey.PublicKey)
	if e.state.Exist(fresh) {
		t.Fatal("fresh authority already exists")
	}
	auth := e.authorization(freshKey, e.cfg.ChainID.Uint64(), destructor, 0)
	res, err := e.apply(e.setCodeTx(0, fresh, []types.SetCodeAuthorization{auth}))
	if err != nil || res.Err != nil {
		t.Fatalf("set-code tx: err %v, vm err %v", err, res.Err)
	}
	e.state.Finalise(true)
	if !e.state.Exist(fresh) {
		t.Fatal("SELFDESTRUCT deleted an authority that was not created by CREATE")
	}
	if code := e.state.GetCode(fresh); !bytes.Equal(code, types.AddressToDelegation(destructor)) {
		t.Fatalf("authority code %x, want the delegation designator", code)
	}
	if n := e.state.GetNonce(fresh); n != 1 {
		t.Fatalf("authority nonce %d, want 1", n)
	}
}
