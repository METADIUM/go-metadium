// Copyright 2023 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

// Package legacypool implements the Ethereum legacy (Type 0/1/2/22) transaction sub-pool.
// 기존 core.TxPool을 SubPool 인터페이스로 래핑한 어댑터이다.
// Metadium 전용 기능(TRS, Type22 fee delegation, SenderResolver)은 core.TxPool 내부에 보존된다.
package legacypool

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
)

// Config is a type alias for core.TxPoolConfig so callers can use either.
type Config = core.TxPoolConfig

// DefaultConfig is the default legacypool configuration.
var DefaultConfig = core.DefaultTxPoolConfig

// BlockChain defines the minimal blockchain interface for the legacy pool.
// *core.BlockChain satisfies this interface.
type BlockChain interface {
	core.LegacyPoolBlockChain
}

// LegacyPool is an adapter that wraps core.TxPool and implements txpool.SubPool.
// Blob transactions (Type 3) are rejected by Filter; all other types are accepted.
type LegacyPool struct {
	inner *core.TxPool
}

// New creates a new LegacyPool backed by core.TxPool.
// config is sanitized internally by core.TxPool.
func New(config Config, chainconfig *params.ChainConfig, chain core.LegacyPoolBlockChain) *LegacyPool {
	return &LegacyPool{
		inner: core.NewTxPool(config, chainconfig, chain),
	}
}

// Inner returns the underlying core.TxPool.
// 필요 시 직접 접근용.
func (p *LegacyPool) Inner() *core.TxPool { return p.inner }

// --- SubPool interface ---

// Filter returns true for all transaction types except BlobTxType.
func (p *LegacyPool) Filter(tx *types.Transaction) bool {
	return tx.Type() != types.BlobTxType
}

// Init initializes the pool. For LegacyPool the underlying core.TxPool is
// already initialized in New; this is a no-op that satisfies the interface.
func (p *LegacyPool) Init(_ *big.Int, _ *types.Header) error { return nil }

// Close stops the pool.
func (p *LegacyPool) Close() error {
	p.inner.Stop()
	return nil
}

// Reset is a no-op for LegacyPool; core.TxPool reacts to ChainHeadEvent internally.
func (p *LegacyPool) Reset(_, _ *types.Header) {}

// SetGasTip updates the minimum gas tip required by the pool.
func (p *LegacyPool) SetGasTip(tip *big.Int) {
	p.inner.SetGasPrice(tip)
}

// Has returns true if the pool contains a transaction with the given hash.
func (p *LegacyPool) Has(hash common.Hash) bool {
	return p.inner.Has(hash)
}

// Get returns a transaction by hash, or nil.
func (p *LegacyPool) Get(hash common.Hash) *types.Transaction {
	return p.inner.Get(hash)
}

// Add validates and adds transactions to the pool.
// local=true marks senders as trusted (bypasses price limits).
func (p *LegacyPool) Add(txs []*types.Transaction, local bool) []error {
	if local {
		return p.inner.AddLocals(txs)
	}
	return p.inner.AddRemotes(txs)
}

// Pending returns all currently processable transactions grouped by sender.
func (p *LegacyPool) Pending(enforceTips bool) map[common.Address]types.Transactions {
	return p.inner.Pending(enforceTips)
}

// SubscribeNewTxsEvent subscribes to new transaction events.
func (p *LegacyPool) SubscribeNewTxsEvent(ch chan<- core.NewTxsEvent) event.Subscription {
	return p.inner.SubscribeNewTxsEvent(ch)
}

// Nonce returns the next expected nonce for an address.
func (p *LegacyPool) Nonce(addr common.Address) uint64 {
	return p.inner.Nonce(addr)
}

// Stats returns the number of pending and queued transactions.
func (p *LegacyPool) Stats() (int, int) {
	return p.inner.Stats()
}

// Content returns all pending and queued transactions.
func (p *LegacyPool) Content() (map[common.Address]types.Transactions, map[common.Address]types.Transactions) {
	return p.inner.Content()
}

// ContentFrom returns pending and queued transactions for a specific address.
func (p *LegacyPool) ContentFrom(addr common.Address) (types.Transactions, types.Transactions) {
	return p.inner.ContentFrom(addr)
}

// Locals returns the list of locally-tracked sender addresses.
func (p *LegacyPool) Locals() []common.Address {
	return p.inner.Locals()
}

// Status returns the status for the given transaction hashes.
func (p *LegacyPool) Status(hashes []common.Hash) []core.TxStatus {
	return p.inner.Status(hashes)
}

// --- Additional methods forwarded from core.TxPool ---

// GasPrice returns the current minimum gas price enforced by the pool.
func (p *LegacyPool) GasPrice() *big.Int {
	return p.inner.GasPrice()
}

// AddLocal adds a single local transaction.
func (p *LegacyPool) AddLocal(tx *types.Transaction) error {
	return p.inner.AddLocal(tx)
}

// AddLocals adds a batch of local transactions.
func (p *LegacyPool) AddLocals(txs []*types.Transaction) []error {
	return p.inner.AddLocals(txs)
}

// AddRemote adds a single remote transaction.
func (p *LegacyPool) AddRemote(tx *types.Transaction) error {
	return p.inner.AddRemote(tx)
}

// AddRemotes adds a batch of remote transactions.
func (p *LegacyPool) AddRemotes(txs []*types.Transaction) []error {
	return p.inner.AddRemotes(txs)
}

// AddRemotesSync adds a batch of remote transactions synchronously (used in tests).
func (p *LegacyPool) AddRemotesSync(txs []*types.Transaction) []error {
	return p.inner.AddRemotesSync(txs)
}

// Stop terminates the pool (alias for Close without error return).
func (p *LegacyPool) Stop() {
	p.inner.Stop()
}
