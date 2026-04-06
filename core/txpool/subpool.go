// Copyright 2023 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

// Package txpool implements the Ethereum transaction pool.
// SubPool 인터페이스와 공통 타입을 정의한다.
package txpool

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
)

// BlockChain defines the minimal blockchain interface the pool needs.
// *core.BlockChain satisfies this interface.
type BlockChain interface {
	CurrentBlock() *types.Block
	GetBlock(hash common.Hash, number uint64) *types.Block
	StateAt(root common.Hash) (*state.StateDB, error)
	SubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) event.Subscription
	Config() *params.ChainConfig
}

// SubPool defines the interface that each transaction sub-pool must implement.
// TxPool orchestrates multiple SubPool instances routing by transaction type.
type SubPool interface {
	// Filter returns true if the sub-pool can handle this transaction type.
	Filter(tx *types.Transaction) bool

	// Init initializes the sub-pool with the given minimum gas tip and current chain head.
	Init(gasTip *big.Int, head *types.Header) error

	// Close terminates the sub-pool, releasing held resources.
	Close() error

	// Reset responds to a new head event, re-evaluating pending transactions.
	Reset(oldHead, newHead *types.Header)

	// SetGasTip updates the minimum gas tip required by the sub-pool.
	SetGasTip(tip *big.Int)

	// Has returns true if the sub-pool contains a transaction with the given hash.
	Has(hash common.Hash) bool

	// Get returns a transaction if it exists in the pool, or nil.
	Get(hash common.Hash) *types.Transaction

	// Add validates and adds a batch of transactions to the pool.
	// Returns one error per transaction (nil on success).
	Add(txs []*types.Transaction, local bool) []error

	// Pending returns processable transactions grouped by sender, sorted by nonce.
	Pending(enforceTips bool) map[common.Address]types.Transactions

	// SubscribeNewTxsEvent subscribes to new transaction events.
	SubscribeNewTxsEvent(ch chan<- core.NewTxsEvent) event.Subscription

	// Nonce returns the next expected nonce for an address.
	Nonce(addr common.Address) uint64

	// Stats returns the current number of pending and queued transactions.
	Stats() (int, int)

	// Content returns all pending and queued transactions, grouped by sender.
	Content() (map[common.Address]types.Transactions, map[common.Address]types.Transactions)

	// ContentFrom returns pending and queued transactions for a specific address.
	ContentFrom(addr common.Address) (types.Transactions, types.Transactions)

	// Locals returns the list of locally-tracked sender addresses.
	Locals() []common.Address

	// Status returns the status for the given transaction hashes.
	Status(hashes []common.Hash) []core.TxStatus
}
