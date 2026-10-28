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

package blobpool

import (
	"crypto/ecdsa"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/holiman/uint256"
)

// TestDorajiBlobDelegationLimit: a sender that is delegated (EIP-7702 code in
// the state) or has a pending authorization in the legacy pool may hold one
// blob transaction in flight: a second nonce is refused, a replacement of the
// same nonce is accepted. A plain sender stacks as before, and so does the
// pending-authorization sender when the pool is built without the source.
func TestDorajiBlobDelegationLimit(t *testing.T) {
	var (
		delegatedKey, _ = crypto.GenerateKey()
		pendingKey, _   = crypto.GenerateKey()
		plainKey, _     = crypto.GenerateKey()
		delegated       = crypto.PubkeyToAddress(delegatedKey.PublicKey)
		pending         = crypto.PubkeyToAddress(pendingKey.PublicKey)
		plain           = crypto.PubkeyToAddress(plainKey.PublicKey)
		balance         = uint256.NewInt(1_000_000_000_000_000_000)
	)
	newPool := func(t *testing.T, hasPendingAuth func(common.Address) bool) *BlobPool {
		t.Helper()
		statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewDatabase(memorydb.New())), nil)
		for _, addr := range []common.Address{delegated, pending, plain} {
			statedb.AddBalance(addr, balance)
		}
		statedb.SetCode(delegated, append(append([]byte{}, types.DelegationPrefix...), common.Address{0xaa}.Bytes()...))
		statedb.Commit(0, true)

		chain := &testBlockChain{
			config:  testChainConfig,
			basefee: uint256.NewInt(1050),
			blobfee: uint256.NewInt(105),
			statedb: statedb,
		}
		pool := New(Config{Datadir: ""}, chain, hasPendingAuth)
		if err := pool.Init(1, chain.CurrentBlock(), makeAddressReserver()); err != nil {
			t.Fatalf("failed to create blob pool: %v", err)
		}
		return pool
	}
	isPending := func(addr common.Address) bool { return addr == pending }

	pool := newPool(t, isPending)
	for _, tc := range []struct {
		name string
		key  *ecdsa.PrivateKey
	}{
		{"delegated sender", delegatedKey},
		{"pending-authorization sender", pendingKey},
	} {
		if err := pool.add(makeTx(0, 1, 1, 1, tc.key)); err != nil {
			t.Fatalf("%s: first blob tx: %v", tc.name, err)
		}
		if err := pool.add(makeTx(1, 1, 1, 1, tc.key)); !errors.Is(err, txpool.ErrInflightTxLimitReached) {
			t.Fatalf("%s: second blob tx: err %v, want %v", tc.name, err, txpool.ErrInflightTxLimitReached)
		}
		if err := pool.add(makeTx(0, 10, 10, 10, tc.key)); err != nil {
			t.Fatalf("%s: replacement of the one in flight: %v", tc.name, err)
		}
		if err := pool.add(makeTx(1, 10, 10, 10, tc.key)); !errors.Is(err, txpool.ErrInflightTxLimitReached) {
			t.Fatalf("%s: second blob tx after the replacement: err %v, want %v", tc.name, err, txpool.ErrInflightTxLimitReached)
		}
	}
	// A plain sender is not limited.
	for nonce := uint64(0); nonce < 3; nonce++ {
		if err := pool.add(makeTx(nonce, 1, 1, 1, plainKey)); err != nil {
			t.Fatalf("plain sender, nonce %d: %v", nonce, err)
		}
	}
	verifyPoolInternals(t, pool)
	pool.Close()

	// Without a pending-authorization source the pool cannot see the pending
	// delegation and the sender stacks; the delegated one is still limited by
	// its code.
	pool = newPool(t, nil)
	defer pool.Close()
	if err := pool.add(makeTx(0, 1, 1, 1, pendingKey)); err != nil {
		t.Fatalf("no source, pending sender, first blob tx: %v", err)
	}
	if err := pool.add(makeTx(1, 1, 1, 1, pendingKey)); err != nil {
		t.Fatalf("no source, pending sender, second blob tx: %v", err)
	}
	if err := pool.add(makeTx(0, 1, 1, 1, delegatedKey)); err != nil {
		t.Fatalf("no source, delegated sender, first blob tx: %v", err)
	}
	if err := pool.add(makeTx(1, 1, 1, 1, delegatedKey)); !errors.Is(err, txpool.ErrInflightTxLimitReached) {
		t.Fatalf("no source, delegated sender, second blob tx: err %v, want %v", err, txpool.ErrInflightTxLimitReached)
	}
	verifyPoolInternals(t, pool)
}
