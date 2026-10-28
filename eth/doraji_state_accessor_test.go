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

package eth

import (
	"bytes"
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// TestDorajiStateAtTransaction: the state handed to the tracers for a
// transaction of a Doraji block already carries the block's pre-transaction
// system calls: the history contract from the fork block on, and the parent's
// hash in its slot. Before the fork the state carries neither.
func TestDorajiStateAtTransaction(t *testing.T) {
	const forkBlock = 3
	cfg := *params.AllEthashProtocolChanges
	cfg.DorajiBlock = big.NewInt(forkBlock)
	gspec := &core.Genesis{Config: &cfg, Alloc: types.GenesisAlloc{}}
	_, blocks, _ := core.GenerateChainWithGenesis(gspec, ethash.NewFaker(), 5, nil)

	db := rawdb.NewMemoryDatabase()
	chain, err := core.NewBlockChain(db, nil, gspec, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	if i, err := chain.InsertChain(blocks); err != nil {
		t.Fatalf("import of block %d: %v", i, err)
	}
	eth := &Ethereum{blockchain: chain, chainDb: db}
	slot := func(n uint64) common.Hash {
		return common.BigToHash(new(big.Int).SetUint64(n % params.HistoryServeWindow))
	}

	for _, block := range blocks {
		number := block.NumberU64()
		// Index 0 of a block with no transactions: the state before the
		// first transaction, which is after the system calls.
		_, _, statedb, release, err := eth.stateAtTransaction(context.Background(), block, 0, 128)
		if err != nil {
			t.Fatalf("block %d: %v", number, err)
		}
		code := statedb.GetCode(params.HistoryStorageAddress)
		parentSlot := statedb.GetState(params.HistoryStorageAddress, slot(number-1))
		release()

		if number < forkBlock {
			if len(code) != 0 {
				t.Errorf("block %d: history contract present before the fork", number)
			}
			continue
		}
		if !bytes.Equal(code, params.HistoryStorageCode) {
			t.Errorf("block %d: history contract missing from the state at the transaction", number)
		}
		if parentSlot != block.ParentHash() {
			t.Errorf("block %d: parent slot = %v, want %v", number, parentSlot, block.ParentHash())
		}
	}
}
