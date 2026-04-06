// Copyright 2023 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

// Package txpool implements the Ethereum transaction pool orchestrator.
// TxPool은 여러 SubPool을 묶어 트랜잭션 타입에 따라 라우팅한다.
// - legacypool: Type 0/1/2/22 (기존 Metadium 트랜잭션)
// - blobpool  : Type 3 (EIP-4844 blob 트랜잭션)
package txpool

import (
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
)

// TxPool orchestrates multiple SubPool instances.
// callers that previously used *core.TxPool can switch to *TxPool with minimal change.
type TxPool struct {
	subpools []SubPool // 등록된 서브풀 목록

	scope   event.SubscriptionScope
	txFeed  event.Feed
	subs    []event.Subscription // 각 서브풀의 new-tx 구독

	mu      sync.RWMutex
	gasTip  *big.Int
	stopped bool
	wg      sync.WaitGroup
}

// New creates a new TxPool and initializes all sub-pools.
// gasTip: 최소 gas tip. chain: 체인 인터페이스. subpools: SubPool 구현 목록 (순서 무관).
func New(gasTip *big.Int, chain BlockChain, subpools []SubPool) (*TxPool, error) {
	head := chain.CurrentBlock().Header()

	pool := &TxPool{
		subpools: subpools,
		gasTip:   new(big.Int).Set(gasTip),
	}

	for _, sub := range subpools {
		if err := sub.Init(gasTip, head); err != nil {
			return nil, err
		}
	}

	// 각 서브풀의 new-tx 이벤트를 단일 feed로 합산
	for _, sub := range subpools {
		ch := make(chan core.NewTxsEvent, 100)
		subscription := sub.SubscribeNewTxsEvent(ch)
		pool.subs = append(pool.subs, subscription)

		pool.wg.Add(1)
		go pool.forward(ch, subscription)
	}

	return pool, nil
}

// forward는 서브풀의 new-tx 이벤트를 TxPool.txFeed로 전달한다.
func (p *TxPool) forward(ch <-chan core.NewTxsEvent, sub event.Subscription) {
	defer p.wg.Done()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			p.txFeed.Send(ev)
		case <-sub.Err():
			return
		}
	}
}

// Stop terminates all sub-pools.
func (p *TxPool) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.stopped = true

	p.scope.Close()
	for _, sub := range p.subs {
		sub.Unsubscribe()
	}
	p.wg.Wait()

	for _, sp := range p.subpools {
		if err := sp.Close(); err != nil {
			log.Warn("Failed to close sub-pool", "err", err)
		}
	}
	log.Info("Transaction pool stopped")
}

// SubscribeNewTxsEvent registers a subscription for new transaction events.
func (p *TxPool) SubscribeNewTxsEvent(ch chan<- core.NewTxsEvent) event.Subscription {
	return p.scope.Track(p.txFeed.Subscribe(ch))
}

// GasPrice returns the current minimum gas tip.
func (p *TxPool) GasPrice() *big.Int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return new(big.Int).Set(p.gasTip)
}

// SetGasPrice updates the minimum gas tip for all sub-pools.
func (p *TxPool) SetGasPrice(tip *big.Int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gasTip = new(big.Int).Set(tip)
	for _, sp := range p.subpools {
		sp.SetGasTip(tip)
	}
}

// subpoolFor returns the first sub-pool that can handle the transaction type.
func (p *TxPool) subpoolFor(tx *types.Transaction) SubPool {
	for _, sp := range p.subpools {
		if sp.Filter(tx) {
			return sp
		}
	}
	return nil
}

// Has returns true if any sub-pool contains the transaction.
func (p *TxPool) Has(hash common.Hash) bool {
	for _, sp := range p.subpools {
		if sp.Has(hash) {
			return true
		}
	}
	return false
}

// Get returns the transaction from whichever sub-pool holds it.
func (p *TxPool) Get(hash common.Hash) *types.Transaction {
	for _, sp := range p.subpools {
		if tx := sp.Get(hash); tx != nil {
			return tx
		}
	}
	return nil
}

// addTxs routes each transaction to the appropriate sub-pool and collects errors.
func (p *TxPool) addTxs(txs []*types.Transaction, local bool) []error {
	errs := make([]error, len(txs))

	// 서브풀별로 묶어서 일괄 처리
	type batch struct {
		sub     SubPool
		indices []int
		txs     []*types.Transaction
	}
	batches := make(map[SubPool]*batch)

	for i, tx := range txs {
		sp := p.subpoolFor(tx)
		if sp == nil {
			errs[i] = types.ErrTxTypeNotSupported
			continue
		}
		if _, ok := batches[sp]; !ok {
			batches[sp] = &batch{sub: sp}
		}
		b := batches[sp]
		b.indices = append(b.indices, i)
		b.txs = append(b.txs, tx)
	}

	for _, b := range batches {
		subErrs := b.sub.Add(b.txs, local)
		for j, err := range subErrs {
			errs[b.indices[j]] = err
		}
	}
	return errs
}

// AddLocals adds local transactions (bypasses price limits).
func (p *TxPool) AddLocals(txs []*types.Transaction) []error {
	return p.addTxs(txs, true)
}

// AddLocal adds a single local transaction.
func (p *TxPool) AddLocal(tx *types.Transaction) error {
	return p.AddLocals([]*types.Transaction{tx})[0]
}

// AddRemotes adds remote transactions (full price constraints apply).
func (p *TxPool) AddRemotes(txs []*types.Transaction) []error {
	return p.addTxs(txs, false)
}

// AddRemotesSync adds remote transactions synchronously.
func (p *TxPool) AddRemotesSync(txs []*types.Transaction) []error {
	return p.addTxs(txs, false)
}

// AddRemote adds a single remote transaction.
func (p *TxPool) AddRemote(tx *types.Transaction) error {
	return p.AddRemotes([]*types.Transaction{tx})[0]
}

// Pending returns processable transactions from all sub-pools, merged by sender.
func (p *TxPool) Pending(enforceTips bool) map[common.Address]types.Transactions {
	merged := make(map[common.Address]types.Transactions)
	for _, sp := range p.subpools {
		for addr, txs := range sp.Pending(enforceTips) {
			merged[addr] = append(merged[addr], txs...)
		}
	}
	return merged
}

// Content returns all pending and queued transactions from all sub-pools.
func (p *TxPool) Content() (map[common.Address]types.Transactions, map[common.Address]types.Transactions) {
	allPending := make(map[common.Address]types.Transactions)
	allQueued := make(map[common.Address]types.Transactions)
	for _, sp := range p.subpools {
		pending, queued := sp.Content()
		for addr, txs := range pending {
			allPending[addr] = append(allPending[addr], txs...)
		}
		for addr, txs := range queued {
			allQueued[addr] = append(allQueued[addr], txs...)
		}
	}
	return allPending, allQueued
}

// ContentFrom returns pending and queued transactions for an address from all sub-pools.
func (p *TxPool) ContentFrom(addr common.Address) (types.Transactions, types.Transactions) {
	var allPending, allQueued types.Transactions
	for _, sp := range p.subpools {
		pending, queued := sp.ContentFrom(addr)
		allPending = append(allPending, pending...)
		allQueued = append(allQueued, queued...)
	}
	return allPending, allQueued
}

// Nonce returns the highest nonce across all sub-pools for the given address.
func (p *TxPool) Nonce(addr common.Address) uint64 {
	var best uint64
	for _, sp := range p.subpools {
		if n := sp.Nonce(addr); n > best {
			best = n
		}
	}
	return best
}

// Stats returns the total pending and queued count across all sub-pools.
func (p *TxPool) Stats() (int, int) {
	totalPending, totalQueued := 0, 0
	for _, sp := range p.subpools {
		pending, queued := sp.Stats()
		totalPending += pending
		totalQueued += queued
	}
	return totalPending, totalQueued
}

// Locals returns locally-tracked addresses from all sub-pools (deduplicated).
func (p *TxPool) Locals() []common.Address {
	seen := make(map[common.Address]struct{})
	var result []common.Address
	for _, sp := range p.subpools {
		for _, addr := range sp.Locals() {
			if _, ok := seen[addr]; !ok {
				seen[addr] = struct{}{}
				result = append(result, addr)
			}
		}
	}
	return result
}

// Legacy returns the underlying *core.TxPool from the legacypool sub-pool.
// Used by callers that need the raw legacy pool (e.g. miner.Backend interface compatibility).
// Returns nil if no legacypool sub-pool is registered.
func (p *TxPool) Legacy() *core.TxPool {
	type innerProvider interface {
		Inner() *core.TxPool
	}
	for _, sp := range p.subpools {
		if lp, ok := sp.(innerProvider); ok {
			return lp.Inner()
		}
	}
	return nil
}

// Status returns the status of the given transaction hashes across all sub-pools.
func (p *TxPool) Status(hashes []common.Hash) []core.TxStatus {
	status := make([]core.TxStatus, len(hashes))
	for _, sp := range p.subpools {
		subStatus := sp.Status(hashes)
		for i, s := range subStatus {
			if s != core.TxStatusUnknown {
				status[i] = s
			}
		}
	}
	return status
}

// GetSidecar returns the BlobTxSidecar for the given tx hash from the blob sub-pool.
// Returns nil if the tx is not a blob tx or no sidecar was stored.
func (p *TxPool) GetSidecar(hash common.Hash) *types.BlobTxSidecar {
	type sidecarProvider interface {
		GetSidecar(common.Hash) *types.BlobTxSidecar
	}
	for _, sp := range p.subpools {
		if sp2, ok := sp.(sidecarProvider); ok {
			if sc := sp2.GetSidecar(hash); sc != nil {
				return sc
			}
		}
	}
	return nil
}

// AddBlobWithSidecar submits a blob transaction together with its sidecar to the blob sub-pool.
// Returns an error if no blob sub-pool is registered or validation fails.
func (p *TxPool) AddBlobWithSidecar(tx *types.Transaction, sidecar *types.BlobTxSidecar) error {
	type sidecarAdder interface {
		AddWithSidecar(*types.Transaction, *types.BlobTxSidecar) error
	}
	for _, sp := range p.subpools {
		if sa, ok := sp.(sidecarAdder); ok && sp.Filter(tx) {
			return sa.AddWithSidecar(tx, sidecar)
		}
	}
	// Fallback: add without sidecar
	return p.AddLocal(tx)
}
