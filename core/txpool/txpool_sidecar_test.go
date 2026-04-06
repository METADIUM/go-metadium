// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package txpool

import (
	"math/big"
	"sync"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

// mockBlobSubPool is a minimal SubPool that also implements the sidecarAdder and
// sidecarProvider duck-type interfaces — the same interfaces TxPool checks at runtime.
type mockBlobSubPool struct {
	mu       sync.RWMutex
	sidecars map[common.Hash]*types.BlobTxSidecar
	txs      map[common.Hash]*types.Transaction
}

func newMockBlobSubPool() *mockBlobSubPool {
	return &mockBlobSubPool{
		sidecars: make(map[common.Hash]*types.BlobTxSidecar),
		txs:      make(map[common.Hash]*types.Transaction),
	}
}

// SubPool interface — minimal stubs sufficient for TxPool registration.
func (m *mockBlobSubPool) Filter(tx *types.Transaction) bool {
	return tx.Type() == types.BlobTxType
}
func (m *mockBlobSubPool) Init(*big.Int, *types.Header) error  { return nil }
func (m *mockBlobSubPool) Close() error                        { return nil }
func (m *mockBlobSubPool) Reset(_, _ *types.Header)            {}
func (m *mockBlobSubPool) SetGasTip(*big.Int)                  {}
func (m *mockBlobSubPool) Has(hash common.Hash) bool           { _, ok := m.txs[hash]; return ok }
func (m *mockBlobSubPool) Get(hash common.Hash) *types.Transaction {
	m.mu.RLock(); defer m.mu.RUnlock(); return m.txs[hash]
}
func (m *mockBlobSubPool) Add(txs []*types.Transaction, _ bool) []error {
	errs := make([]error, len(txs))
	m.mu.Lock(); defer m.mu.Unlock()
	for i, tx := range txs {
		m.txs[tx.Hash()] = tx
		errs[i] = nil
	}
	return errs
}
func (m *mockBlobSubPool) Pending(_ bool) map[common.Address]types.Transactions { return nil }
func (m *mockBlobSubPool) SubscribeNewTxsEvent(ch chan<- core.NewTxsEvent) event.Subscription {
	return event.NewSubscription(func(quit <-chan struct{}) error { <-quit; return nil })
}
func (m *mockBlobSubPool) Nonce(common.Address) uint64 { return 0 }
func (m *mockBlobSubPool) Stats() (int, int)           { return 0, 0 }
func (m *mockBlobSubPool) Content() (map[common.Address]types.Transactions, map[common.Address]types.Transactions) {
	return nil, nil
}
func (m *mockBlobSubPool) ContentFrom(common.Address) (types.Transactions, types.Transactions) {
	return nil, nil
}
func (m *mockBlobSubPool) Locals() []common.Address        { return nil }
func (m *mockBlobSubPool) Status([]common.Hash) []core.TxStatus { return nil }

// sidecarAdder duck-type — matches the interface checked in TxPool.AddBlobWithSidecar.
func (m *mockBlobSubPool) AddWithSidecar(tx *types.Transaction, sidecar *types.BlobTxSidecar) error {
	m.mu.Lock(); defer m.mu.Unlock()
	m.txs[tx.Hash()] = tx
	if sidecar != nil {
		m.sidecars[tx.Hash()] = sidecar
	}
	return nil
}

// sidecarProvider duck-type — matches the interface checked in TxPool.GetSidecar.
func (m *mockBlobSubPool) GetSidecar(hash common.Hash) *types.BlobTxSidecar {
	m.mu.RLock(); defer m.mu.RUnlock()
	return m.sidecars[hash]
}

// makeMockBlobTx returns a minimal blob tx (no valid signature required for routing tests).
func makeMockBlobTx(blobHash common.Hash) *types.Transaction {
	to := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	inner := &types.BlobTx{
		ChainID:          new(uint256.Int).SetUint64(1337),
		Nonce:            0,
		GasTipCap:        new(uint256.Int).SetUint64(1e9),
		GasFeeCap:        new(uint256.Int).SetUint64(2e9),
		Gas:              21000,
		To:               &to,
		Value:            new(uint256.Int),
		MaxFeePerBlobGas: new(uint256.Int).SetUint64(1e9),
		BlobHashes:       []common.Hash{blobHash},
		V:                new(uint256.Int).SetUint64(1),
		R:                new(uint256.Int).SetUint64(1),
		S:                new(uint256.Int).SetUint64(1),
	}
	return types.NewTx(inner)
}

// TestTxPool_AddBlobWithSidecar_RoutesToBlobPool verifies that TxPool.AddBlobWithSidecar
// dispatches to the sub-pool that implements the sidecarAdder duck-type interface,
// and that TxPool.GetSidecar retrieves the stored sidecar via sidecarProvider.
func TestTxPool_AddBlobWithSidecar_RoutesToBlobPool(t *testing.T) {
	blobPool := newMockBlobSubPool()
	pool := &TxPool{subpools: []SubPool{blobPool}}

	blobHash := common.Hash{0x01}
	tx := makeMockBlobTx(blobHash)
	sidecar := &types.BlobTxSidecar{
		Blobs:       [][]byte{make([]byte, 131072)},
		Commitments: [][]byte{make([]byte, 48)},
		Proofs:      [][]byte{make([]byte, 48)},
	}

	if err := pool.AddBlobWithSidecar(tx, sidecar); err != nil {
		t.Fatalf("AddBlobWithSidecar failed: %v", err)
	}

	stored := pool.GetSidecar(tx.Hash())
	if stored == nil {
		t.Fatal("GetSidecar returned nil — routing did not reach BlobPool")
	}
	if len(stored.Blobs) != 1 {
		t.Errorf("expected 1 blob, got %d", len(stored.Blobs))
	}
	t.Log("PASS: AddBlobWithSidecar routed to BlobPool, GetSidecar returned stored sidecar")
}

// TestTxPool_GetSidecar_NoBlobPool verifies that GetSidecar returns nil when
// no sub-pool implements sidecarProvider.
func TestTxPool_GetSidecar_NoBlobPool(t *testing.T) {
	pool := &TxPool{subpools: []SubPool{}} // empty — no blob pool registered

	result := pool.GetSidecar(common.Hash{0x99})
	if result != nil {
		t.Errorf("expected nil from empty TxPool, got non-nil")
	}
	t.Log("PASS: GetSidecar returns nil when no blob pool is registered")
}
