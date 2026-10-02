// Copyright 2024 The go-ethereum Authors
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
)

// signedFeeDelegateTx builds a type-22 transaction from sender to to, with the
// gas paid by feePayer, signed by both.
func signedFeeDelegateTx(t *testing.T, chainID *big.Int, sender, feePayer *ecdsa.PrivateKey, to common.Address) *types.Transaction {
	t.Helper()
	inner := types.DynamicFeeTx{
		ChainID: chainID, Nonce: 0, GasFeeCap: big.NewInt(1e9), GasTipCap: big.NewInt(1e9),
		Gas: 21000, To: &to, Value: big.NewInt(0),
	}
	signedSender, err := types.SignNewTx(sender, types.NewLondonSigner(chainID), &inner)
	if err != nil {
		t.Fatalf("sign sender: %v", err)
	}
	inner.V, inner.R, inner.S = signedSender.RawSignatureValues()
	payer := crypto.PubkeyToAddress(feePayer.PublicKey)
	fd := &types.FeeDelegateDynamicFeeTx{FeePayer: &payer}
	fd.SetSenderTx(inner)
	tx, err := types.SignTx(types.NewTx(fd), types.NewFeeDelegateSigner(chainID), feePayer)
	if err != nil {
		t.Fatalf("sign fee payer: %v", err)
	}
	return tx
}

// applyFeeDelegateAt executes a type-22 transaction at block 1 of a chain with
// the given config and returns ApplyMessage's error.
func applyFeeDelegateAt(t *testing.T, cfg *params.ChainConfig) error {
	t.Helper()
	senderKey, _ := crypto.GenerateKey()
	payerKey, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(senderKey.PublicKey)
	payer := crypto.PubkeyToAddress(payerKey.PublicKey)
	to := common.HexToAddress("0x1234567890123456789012345678901234567890")

	db := rawdb.NewMemoryDatabase()
	gspec := &Genesis{
		Config: cfg,
		Alloc: GenesisAlloc{
			sender: {Balance: big.NewInt(1e18)},
			payer:  {Balance: big.NewInt(1e18)},
		},
	}
	genesis := gspec.MustCommit(db, nil)
	chain, _ := NewBlockChain(db, nil, gspec, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
	defer chain.Stop()

	// An empty block 1 gives us the header (base fee, time) to execute against.
	blocks, _ := GenerateChain(cfg, genesis, ethash.NewFaker(), db, 1, nil)
	header := blocks[0].Header()

	statedb, err := state.New(genesis.Root(), state.NewDatabase(db), nil)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	tx := signedFeeDelegateTx(t, cfg.ChainID, senderKey, payerKey, to)
	msg, err := TransactionToMessage(tx, types.MakeSigner(cfg, header.Number, header.Time), header.BaseFee)
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	evm := vm.NewEVM(NewEVMBlockContext(header, chain, nil), NewEVMTxContext(msg), statedb, cfg, vm.Config{})
	_, err = ApplyMessage(evm, msg, new(GasPool).AddGas(header.GasLimit))
	return err
}

// TestFeeDelegationApplepieGate: a type-22 transaction is not executable
// before applepieBlock and is after it. The gate existed in 0.10.x and was
// lost in the v1.13.14 rebase (#71). Mainnet and testnet set applepieBlock
// in their history, so this only matters for a genesis that sets it later.
func TestFeeDelegationApplepieGate(t *testing.T) {
	before := *params.AllEthashProtocolChanges
	before.ApplepieBlock = big.NewInt(10) // block 1 is before the fork
	if err := applyFeeDelegateAt(t, &before); !errors.Is(err, ErrTxTypeNotSupported) {
		t.Fatalf("before Applepie: err = %v, want ErrTxTypeNotSupported", err)
	}

	after := *params.AllEthashProtocolChanges
	after.ApplepieBlock = big.NewInt(1) // block 1 is the fork block
	if err := applyFeeDelegateAt(t, &after); err != nil {
		t.Fatalf("at Applepie: err = %v, want nil", err)
	}

	unset := *params.AllEthashProtocolChanges
	unset.ApplepieBlock = nil // no fork at all: never active
	if err := applyFeeDelegateAt(t, &unset); !errors.Is(err, ErrTxTypeNotSupported) {
		t.Fatalf("no applepieBlock: err = %v, want ErrTxTypeNotSupported", err)
	}
}
