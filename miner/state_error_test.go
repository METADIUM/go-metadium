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

package miner

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// TestStateReadErrorRefusesBrokenState builds a two-account state, removes
// every trie node but the root from the database, and checks that a read
// through the resulting state is reported by stateReadError. A healthy state
// passes.
func TestStateReadErrorRefusesBrokenState(t *testing.T) {
	var (
		db     = rawdb.NewMemoryDatabase()
		sdb    = state.NewDatabase(db)
		a1, a2 = common.Address{1}, common.Address{2}
	)
	st, err := state.New(types.EmptyRootHash, sdb, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.AddBalance(a1, uint256.NewInt(1))
	st.AddBalance(a2, uint256.NewInt(2))
	root, err := st.Commit(0, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sdb.TrieDB().Commit(root, false); err != nil {
		t.Fatal(err)
	}
	healthy, err := state.New(root, state.NewDatabase(db), nil)
	if err != nil {
		t.Fatal(err)
	}
	healthy.GetBalance(a1)
	if err := stateReadError(healthy); err != nil {
		t.Fatalf("healthy state: %v", err)
	}
	// Drop every trie node except the root, so resolving an account fails.
	it := db.NewIterator(nil, nil)
	for it.Next() {
		if key := it.Key(); len(key) == common.HashLength && common.BytesToHash(key) != root {
			db.Delete(key)
		}
	}
	it.Release()

	broken, err := state.New(root, state.NewDatabase(db), nil)
	if err != nil {
		t.Fatal(err)
	}
	broken.GetBalance(a1)
	if err := stateReadError(broken); !errors.Is(err, errStateReadFailure) {
		t.Fatalf("broken state: err = %v, want errStateReadFailure", err)
	}
}
