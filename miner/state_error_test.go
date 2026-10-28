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
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
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
	if err := stateReadError(healthy, nil); err != nil {
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
	if err := stateReadError(broken, nil); !errors.Is(err, errStateReadFailure) {
		t.Fatalf("broken state: err = %v, want errStateReadFailure", err)
	}
	// The copy that commitEx assembles on must carry the error too.
	if err := stateReadError(broken.Copy(), nil); !errors.Is(err, errStateReadFailure) {
		t.Fatalf("copy of broken state: err = %v, want errStateReadFailure", err)
	}
}

// TestCommitExRefusesBrokenState goes through commitEx itself, the PoA and
// BFT block production path, with an environment whose state has recorded a
// database error, and checks that no block is assembled.
func TestCommitExRefusesBrokenState(t *testing.T) {
	w, _ := newTestWorker(t, ethashChainConfig, ethash.NewFaker(), rawdb.NewMemoryDatabase(), 0)
	defer w.close()
	// The worker has to be running for commitEx to reach the state check, but
	// a running worker also builds blocks of its own in the background and
	// hands them to the engine. Under the unit tests' PoW default the ethash
	// Seal panics, so skip sealing, as the other ethash tests do.
	w.skipSealHook = func(*task) bool { return true }
	w.start()

	env, err := w.prepareWork(&generateParams{timestamp: uint64(time.Now().Unix()), coinbase: testBankAddress})
	if err != nil {
		t.Fatalf("preparing the environment: %v", err)
	}
	defer env.discard()

	// Make the state record a database error: hand it a database whose
	// trie nodes are gone and read an account.
	db := rawdb.NewMemoryDatabase()
	sdb := state.NewDatabase(db)
	st, err := state.New(types.EmptyRootHash, sdb, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.AddBalance(testBankAddress, uint256.NewInt(1))
	st.AddBalance(common.Address{2}, uint256.NewInt(2))
	root, err := st.Commit(0, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sdb.TrieDB().Commit(root, false); err != nil {
		t.Fatal(err)
	}
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
	broken.GetBalance(testBankAddress)
	if broken.Error() == nil {
		t.Fatal("test premise: the state did not record an error")
	}
	env.state = broken

	if err := w.commitEx(env, nil, false, time.Now()); !errors.Is(err, errStateReadFailure) {
		t.Fatalf("commitEx on a broken state: err = %v, want errStateReadFailure", err)
	}
}
