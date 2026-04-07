// Copyright 2023 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

// Package blobpool implements EIP-4844 blob transaction (Type 3) sub-pool.
// blob 트랜잭션은 별도 풀에서 관리되며, blobBaseFee 미달 시 즉시 드롭한다.
package blobpool

import (
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

var (
	// ErrNotBlobTx는 blob 트랜잭션이 아닌 경우 반환된다.
	ErrNotBlobTx = errors.New("not a blob transaction")

	// ErrBlobFeeCapTooLow는 MaxFeePerBlobGas가 현재 blobBaseFee보다 낮을 때 반환된다.
	ErrBlobFeeCapTooLow = errors.New("max fee per blob gas less than blob base fee")

	// ErrBlobCountExceeded는 blob 수가 한도를 초과할 때 반환된다.
	ErrBlobCountExceeded = errors.New("blob count exceeds per-transaction limit")

	// ErrNoBlobHashes는 blob hash가 없을 때 반환된다.
	ErrNoBlobHashes = errors.New("blob transaction must have at least one blob hash")

	// ErrNoBlobRecipient는 수신자가 없는 blob tx(컨트랙트 생성)일 때 반환된다.
	ErrNoBlobRecipient = errors.New("blob transaction must have a recipient address")

	// ErrInsufficientFunds는 잔고 부족 시 반환된다.
	ErrInsufficientFunds = errors.New("insufficient funds for blob transaction")

	// ErrNonceTooLow는 nonce가 낮을 때 반환된다.
	ErrNonceTooLow = errors.New("nonce too low")

	// ErrUnderpriced는 gas tip이 최소값 미달일 때 반환된다.
	ErrUnderpriced = errors.New("transaction underpriced")

	// ErrAlreadyKnown은 이미 풀에 있는 트랜잭션일 때 반환된다.
	ErrAlreadyKnown = errors.New("already known")
)

// BlockChain defines the blockchain methods the BlobPool needs.
type BlockChain interface {
	CurrentBlock() *types.Header
	GetBlock(hash common.Hash, number uint64) *types.Block
	StateAt(root common.Hash) (*state.StateDB, error)
	SubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) event.Subscription
	Config() *params.ChainConfig
}

// BlobPool is an in-memory pool for EIP-4844 blob transactions (Type 3).
// 단순 메모리 맵으로 구현하며, 체인 헤드 변경 시 blobBaseFee를 재계산한다.
type BlobPool struct {
	mu       sync.RWMutex
	all      map[common.Hash]*types.Transaction            // 해시 → tx
	sidecars map[common.Hash]*types.BlobTxSidecar          // 해시 → sidecar (optional)
	pending  map[common.Address][]*types.Transaction       // 주소 → nonce 순서 tx 목록
	signer   types.Signer
	gasTip   *big.Int // 최소 gas tip
	chain    BlockChain

	chainHeadCh  chan core.ChainHeadEvent
	chainHeadSub event.Subscription
	txFeed       event.Feed
	scope        event.SubscriptionScope
	wg           sync.WaitGroup
}

// New creates a new BlobPool.
func New(chainconfig *params.ChainConfig, chain BlockChain) *BlobPool {
	p := &BlobPool{
		all:         make(map[common.Hash]*types.Transaction),
		sidecars:    make(map[common.Hash]*types.BlobTxSidecar),
		pending:     make(map[common.Address][]*types.Transaction),
		signer:      types.LatestSigner(chainconfig),
		gasTip:      new(big.Int),
		chain:       chain,
		chainHeadCh: make(chan core.ChainHeadEvent, 10),
	}
	p.chainHeadSub = chain.SubscribeChainHeadEvent(p.chainHeadCh)
	p.wg.Add(1)
	go p.loop()
	return p
}

func (p *BlobPool) loop() {
	defer p.wg.Done()
	for {
		select {
		case ev := <-p.chainHeadCh:
			if ev.Block != nil {
				p.Reset(nil, ev.Block.Header())
			}
		case <-p.chainHeadSub.Err():
			return
		}
	}
}

// --- SubPool interface ---

// Filter returns true only for BlobTxType.
func (p *BlobPool) Filter(tx *types.Transaction) bool {
	return tx.Type() == types.BlobTxType
}

// Init initializes the pool with the given gas tip and chain head.
func (p *BlobPool) Init(gasTip *big.Int, _ *types.Header) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gasTip != nil {
		p.gasTip = new(big.Int).Set(gasTip)
	}
	return nil
}

// Close terminates the pool.
func (p *BlobPool) Close() error {
	p.scope.Close()
	p.chainHeadSub.Unsubscribe()
	p.wg.Wait()
	log.Info("Blob transaction pool stopped")
	return nil
}

// Reset responds to a new head, evicting blob txs whose blobFeeCap is now too low.
func (p *BlobPool) Reset(_, newHead *types.Header) {
	if newHead == nil {
		newHead = p.chain.CurrentBlock()
	}
	// 새 헤드의 blobBaseFee 계산
	var blobBaseFee *big.Int
	if newHead.ExcessBlobGas != nil {
		blobBaseFee = types.CalcBlobBaseFee(newHead.ExcessBlobGas)
	} else {
		blobBaseFee = new(big.Int)
	}

	// 현재 state에서 nonce 확인용
	statedb, err := p.chain.StateAt(newHead.Root)

	p.mu.Lock()
	defer p.mu.Unlock()

	for addr, txs := range p.pending {
		var kept []*types.Transaction
		for _, tx := range txs {
			// nonce 만료 체크
			if err == nil && statedb.GetNonce(addr) > tx.Nonce() {
				delete(p.all, tx.Hash())
				delete(p.sidecars, tx.Hash())
				continue
			}
			// blobFeeCap 체크
			if tx.MaxFeePerBlobGas() != nil && tx.MaxFeePerBlobGas().Cmp(blobBaseFee) < 0 {
				log.Trace("Dropping blob tx: blobFeeCap below blobBaseFee", "hash", tx.Hash())
				delete(p.all, tx.Hash())
				delete(p.sidecars, tx.Hash())
				continue
			}
			kept = append(kept, tx)
		}
		if len(kept) == 0 {
			delete(p.pending, addr)
		} else {
			p.pending[addr] = kept
		}
	}
}

// SetGasTip updates the minimum gas tip.
func (p *BlobPool) SetGasTip(tip *big.Int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gasTip = new(big.Int).Set(tip)

	// tip 미달 tx 제거
	for addr, txs := range p.pending {
		var kept []*types.Transaction
		for _, tx := range txs {
			if tx.GasTipCapIntCmp(p.gasTip) >= 0 {
				kept = append(kept, tx)
			} else {
				delete(p.all, tx.Hash())
				delete(p.sidecars, tx.Hash())
			}
		}
		if len(kept) == 0 {
			delete(p.pending, addr)
		} else {
			p.pending[addr] = kept
		}
	}
}

// Has returns true if the pool contains the transaction.
func (p *BlobPool) Has(hash common.Hash) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.all[hash]
	return ok
}

// Get returns a transaction by hash, or nil.
func (p *BlobPool) Get(hash common.Hash) *types.Transaction {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.all[hash]
}

// Add validates and adds blob transactions to the pool.
func (p *BlobPool) Add(txs []*types.Transaction, local bool) []error {
	errs := make([]error, len(txs))
	for i, tx := range txs {
		errs[i] = p.add(tx, nil, local)
	}
	return errs
}

// AddWithSidecar adds a blob transaction along with its sidecar (blobs, commitments, proofs).
// The sidecar is verified (commitment hashes and KZG proofs) before the tx is accepted.
// The sidecar is stored locally and not propagated via P2P (per EIP-4844 spec).
func (p *BlobPool) AddWithSidecar(tx *types.Transaction, sidecar *types.BlobTxSidecar) error {
	if sidecar != nil {
		// Convert blob hashes from common.Hash to [32]byte as required by the KZG library.
		blobHashes := make([][32]byte, len(tx.BlobHashes()))
		for i, h := range tx.BlobHashes() {
			blobHashes[i] = h
		}
		if err := kzg4844.ValidateBlobSidecar(blobHashes, sidecar.Blobs, sidecar.Commitments, sidecar.Proofs); err != nil {
			return fmt.Errorf("invalid blob sidecar: %w", err)
		}
	}
	// sidecar is passed into add() so tx and sidecar are stored under the same lock (no TOCTOU).
	return p.add(tx, sidecar, true)
}

// GetSidecar returns the BlobTxSidecar for the given tx hash, or nil if not stored.
func (p *BlobPool) GetSidecar(hash common.Hash) *types.BlobTxSidecar {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sidecars[hash]
}

// add validates and inserts a blob transaction. sidecar (if non-nil) is stored atomically
// with the transaction to prevent TOCTOU races between Add and sidecar storage.
// Validation (including disk I/O via StateAt) is performed before acquiring the write lock.
func (p *BlobPool) add(tx *types.Transaction, sidecar *types.BlobTxSidecar, _ bool) error {
	if tx.Type() != types.BlobTxType {
		return ErrNotBlobTx
	}

	hash := tx.Hash()

	// Fast duplicate check without write lock.
	p.mu.RLock()
	_, exists := p.all[hash]
	p.mu.RUnlock()
	if exists {
		return ErrAlreadyKnown
	}

	// Validate outside the write lock: validateTx may call StateAt (disk I/O) and
	// types.Sender (ECDSA recovery). Keeping these outside the lock prevents pool-wide
	// stalls when many transactions arrive concurrently.
	from, err := p.validateTx(tx)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Re-check duplicate after acquiring write lock (another goroutine may have inserted
	// the same tx between the RLock check above and now).
	if _, ok := p.all[hash]; ok {
		return ErrAlreadyKnown
	}

	// nonce 순서에 맞게 삽입 (단순 append; 정렬은 Pending에서)
	p.pending[from] = append(p.pending[from], tx)
	sortByNonce(p.pending[from])
	p.all[hash] = tx
	if sidecar != nil {
		p.sidecars[hash] = sidecar
	}

	// 새 tx 이벤트 발행
	go p.txFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{tx}})

	return nil
}

// validateTx performs basic validation for a blob transaction.
// Returns the sender address to avoid a second ECDSA recovery in add().
// Must be called without holding p.mu (may perform disk I/O via StateAt).
func (p *BlobPool) validateTx(tx *types.Transaction) (common.Address, error) {
	// blob hash 필수
	if len(tx.BlobHashes()) == 0 {
		return common.Address{}, ErrNoBlobHashes
	}
	// blob 수 제한
	if uint64(len(tx.BlobHashes())) > params.MaxBlobsPerTransaction {
		return common.Address{}, ErrBlobCountExceeded
	}
	// 컨트랙트 생성 불가
	if tx.To() == nil {
		return common.Address{}, ErrNoBlobRecipient
	}
	// gas tip 체크
	p.mu.RLock()
	gasTip := new(big.Int).Set(p.gasTip)
	p.mu.RUnlock()
	if tx.GasTipCapIntCmp(gasTip) < 0 {
		return common.Address{}, ErrUnderpriced
	}
	// 현재 헤드의 blobBaseFee 체크
	if currentHead := p.chain.CurrentBlock(); currentHead != nil && currentHead.ExcessBlobGas != nil {
		blobBaseFee := types.CalcBlobBaseFee(currentHead.ExcessBlobGas)
		if tx.MaxFeePerBlobGas() == nil || tx.MaxFeePerBlobGas().Cmp(blobBaseFee) < 0 {
			return common.Address{}, ErrBlobFeeCapTooLow
		}
	}
	// sender 복구 (ECDSA): 한 번만 수행하고 호출자에게 반환
	from, err := types.Sender(p.signer, tx)
	if err != nil {
		return common.Address{}, errors.New("invalid sender")
	}
	// StateAt: disk I/O — lock 없이 수행
	statedb, err := p.chain.StateAt(p.chain.CurrentBlock().Root)
	if err != nil {
		return common.Address{}, err
	}
	// nonce 체크
	if statedb.GetNonce(from) > tx.Nonce() {
		return common.Address{}, ErrNonceTooLow
	}
	// 비용 = value + gasFeeCap*gas + maxFeePerBlobGas * blobGasPerBlob * numBlobs
	blobGasCost := new(big.Int).SetUint64(params.BlobTxPerBlobGas * uint64(len(tx.BlobHashes())))
	if tx.MaxFeePerBlobGas() != nil {
		blobGasCost.Mul(blobGasCost, tx.MaxFeePerBlobGas())
	}
	totalCost := new(big.Int).Add(tx.Cost(), blobGasCost)
	if statedb.GetBalance(from).Cmp(totalCost) < 0 {
		return common.Address{}, ErrInsufficientFunds
	}
	return from, nil
}

// Pending returns blob txs that are executable (correct nonce, blobFeeCap >= blobBaseFee).
func (p *BlobPool) Pending(_ bool) map[common.Address]types.Transactions {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var blobBaseFee *big.Int
	if head := p.chain.CurrentBlock(); head != nil && head.ExcessBlobGas != nil {
		blobBaseFee = types.CalcBlobBaseFee(head.ExcessBlobGas)
	} else {
		blobBaseFee = new(big.Int)
	}

	statedb, err := p.chain.StateAt(p.chain.CurrentBlock().Root)
	result := make(map[common.Address]types.Transactions)

	for addr, txs := range p.pending {
		var execNonce uint64
		if err == nil {
			execNonce = statedb.GetNonce(addr)
		}
		var executable types.Transactions
		for _, tx := range txs {
			if tx.Nonce() < execNonce {
				continue
			}
			if tx.Nonce() != execNonce {
				break // nonce gap → 이후 tx는 실행 불가
			}
			if tx.MaxFeePerBlobGas() != nil && tx.MaxFeePerBlobGas().Cmp(blobBaseFee) < 0 {
				break
			}
			executable = append(executable, tx)
			execNonce++
		}
		if len(executable) > 0 {
			result[addr] = executable
		}
	}
	return result
}

// SubscribeNewTxsEvent subscribes to new transaction events.
func (p *BlobPool) SubscribeNewTxsEvent(ch chan<- core.NewTxsEvent) event.Subscription {
	return p.scope.Track(p.txFeed.Subscribe(ch))
}

// Nonce returns the next expected nonce for an address (highest pending nonce + 1).
func (p *BlobPool) Nonce(addr common.Address) uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	txs := p.pending[addr]
	if len(txs) == 0 {
		return 0
	}
	return txs[len(txs)-1].Nonce() + 1
}

// Stats returns the number of pending and queued transactions.
// BlobPool은 queue를 별도로 관리하지 않으므로 queued=0.
func (p *BlobPool) Stats() (int, int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	total := 0
	for _, txs := range p.pending {
		total += len(txs)
	}
	return total, 0
}

// Content returns all transactions. BlobPool has no separate queue.
func (p *BlobPool) Content() (map[common.Address]types.Transactions, map[common.Address]types.Transactions) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pending := make(map[common.Address]types.Transactions)
	for addr, txs := range p.pending {
		cp := make(types.Transactions, len(txs))
		copy(cp, txs)
		pending[addr] = cp
	}
	return pending, make(map[common.Address]types.Transactions)
}

// ContentFrom returns pending transactions for the given address.
func (p *BlobPool) ContentFrom(addr common.Address) (types.Transactions, types.Transactions) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	txs := p.pending[addr]
	cp := make(types.Transactions, len(txs))
	copy(cp, txs)
	return cp, nil
}

// Locals returns empty slice (BlobPool does not track locals separately).
func (p *BlobPool) Locals() []common.Address {
	return nil
}

// Status returns the status of the given transaction hashes.
func (p *BlobPool) Status(hashes []common.Hash) []core.TxStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	status := make([]core.TxStatus, len(hashes))
	for i, hash := range hashes {
		if tx, ok := p.all[hash]; ok {
			from, err := types.Sender(p.signer, tx)
			if err != nil {
				continue
			}
			for _, ptx := range p.pending[from] {
				if ptx.Hash() == hash {
					status[i] = core.TxStatusPending
					break
				}
			}
		}
	}
	return status
}

// sortByNonce sorts transactions by nonce in ascending order (insertion sort for small slices).
func sortByNonce(txs []*types.Transaction) {
	for i := 1; i < len(txs); i++ {
		for j := i; j > 0 && txs[j].Nonce() < txs[j-1].Nonce(); j-- {
			txs[j], txs[j-1] = txs[j-1], txs[j]
		}
	}
}
