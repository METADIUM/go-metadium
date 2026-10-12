// Copyright 2026 The go-ethereum Authors
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

// applyMessageAt executes a plain value transfer at block 1 of cfg after
// letting mutate add a fork-gated feature to the message, and returns the
// execution error.
func applyMessageAt(t *testing.T, cfg *params.ChainConfig, mutate func(*Message)) error {
	t.Helper()
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	to := common.HexToAddress("0x1234567890123456789012345678901234567890")

	db := rawdb.NewMemoryDatabase()
	gspec := &Genesis{Config: cfg, Alloc: GenesisAlloc{sender: {Balance: big.NewInt(1e18)}}}
	genesis := gspec.MustCommit(db, nil)
	chain, _ := NewBlockChain(db, nil, gspec, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
	defer chain.Stop()

	blocks, _ := GenerateChain(cfg, genesis, ethash.NewFaker(), db, 1, nil)
	header := blocks[0].Header()

	statedb, err := state.New(genesis.Root(), state.NewDatabase(db), nil)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	tx := types.MustSignNewTx(key, types.MakeSigner(cfg, header.Number, header.Time), &types.LegacyTx{
		Nonce: 0, GasPrice: big.NewInt(1e9), Gas: 100_000, To: &to, Value: big.NewInt(0),
	})
	msg, err := TransactionToMessage(tx, types.MakeSigner(cfg, header.Number, header.Time), header.BaseFee)
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	mutate(msg)
	evm := vm.NewEVM(NewEVMBlockContext(header, chain, nil), NewEVMTxContext(msg), statedb, cfg, vm.Config{})
	_, err = ApplyMessage(evm, msg, new(GasPool).AddGas(header.GasLimit))
	return err
}

// TestForkFeatureGates: a message carrying an access list or blob hashes is
// refused with ErrTxTypeNotSupported before the fork that introduced the
// feature, and accepted after it. The pool refuses such transactions on
// admission; execution has to agree so that a block carrying one fails
// instead of running it.
func TestForkFeatureGates(t *testing.T) {
	withAccessList := func(m *Message) { m.AccessList = types.AccessList{} }

	preBerlin := *params.AllEthashProtocolChanges
	preBerlin.BerlinBlock, preBerlin.LondonBlock = nil, nil
	preBerlin.ArrowGlacierBlock, preBerlin.GrayGlacierBlock = nil, nil
	preBerlin.CamelliaBlock = nil
	if err := applyMessageAt(t, &preBerlin, withAccessList); !errors.Is(err, ErrTxTypeNotSupported) {
		t.Fatalf("access list before Berlin: err = %v, want ErrTxTypeNotSupported", err)
	}
	if err := applyMessageAt(t, params.AllEthashProtocolChanges, withAccessList); err != nil {
		t.Fatalf("access list after Berlin: err = %v, want nil", err)
	}

	withBlob := func(m *Message) {
		m.BlobHashes = []common.Hash{{0x01}}
		m.BlobGasFeeCap = big.NewInt(1)
	}
	preCamellia := *params.AllEthashProtocolChanges
	preCamellia.CamelliaBlock = nil // no CancunTime either: blobs are never active
	if err := applyMessageAt(t, &preCamellia, withBlob); !errors.Is(err, ErrTxTypeNotSupported) {
		t.Fatalf("blob before Camellia: err = %v, want ErrTxTypeNotSupported", err)
	}
	// With Camellia active the type gate passes; the hash is then checked for
	// its version, which is the next error on this path and proves the gate
	// was the one that fired before.
	if err := applyMessageAt(t, params.AllEthashProtocolChanges, withBlob); errors.Is(err, ErrTxTypeNotSupported) {
		t.Fatalf("blob after Camellia: unexpected %v", err)
	}
}
