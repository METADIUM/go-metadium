// Copyright 2015 The go-ethereum Authors
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
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/consensus/misc/eip4844"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
)

const (
	// resultQueueSize is the size of channel listening to sealing result.
	resultQueueSize = 10

	// txChanSize is the size of channel listening to NewTxsEvent.
	// The number is referenced from the size of tx pool.
	txChanSize = 102400

	// chainHeadChanSize is the size of channel listening to ChainHeadEvent.
	chainHeadChanSize = 10

	// resubmitAdjustChanSize is the size of resubmitting interval adjustment channel.
	resubmitAdjustChanSize = 10

	// minRecommitInterval is the minimal time interval to recreate the sealing block with
	// any newly arrived transactions.
	minRecommitInterval = 1 * time.Second

	// maxRecommitInterval is the maximum time interval to recreate the sealing block with
	// any newly arrived transactions.
	maxRecommitInterval = 15 * time.Second

	// intervalAdjustRatio is the impact a single interval adjustment has on sealing work
	// resubmitting interval.
	intervalAdjustRatio = 0.1

	// intervalAdjustBias is applied during the new resubmit interval calculation in favor of
	// increasing upper limit or decreasing lower limit so that the limit can be reachable.
	intervalAdjustBias = 200 * 1000.0 * 1000.0

	// staleThreshold is the maximum depth of the acceptable stale block.
	staleThreshold = 7
)

var (
	errBlockInterruptedByNewHead  = errors.New("new head arrived while building block")
	errBlockInterruptedByRecommit = errors.New("recommit interrupt while building block")
	errBlockInterruptedByTimeout  = errors.New("timeout while building block")
)

// environment is the worker's current environment and holds all
// information of the sealing block generation.
type environment struct {
	signer   types.Signer
	state    *state.StateDB // apply state changes here
	tcount   int            // tx count in cycle
	gasPool  *core.GasPool  // available gas used to pack transactions
	coinbase common.Address

	header   *types.Header
	txs      []*types.Transaction
	receipts []*types.Receipt
	sidecars []*types.BlobTxSidecar
	blobs    int

	// metadium parameters
	till                 *time.Time // until when to block generation holds
	blockInterval        int64
	blockGasLimit        *big.Int
	baseFeeMaxChangeRate int64
	gasTargetPercentage  int64
	// Add TRS
	trsListMap   map[common.Address]bool // The trslist addresses map
	trsSubscribe bool                    // The trs subscribe
}

// copy creates a deep copy of environment.
func (env *environment) copy() *environment {
	cpy := &environment{
		signer:   env.signer,
		state:    env.state.Copy(),
		tcount:   env.tcount,
		coinbase: env.coinbase,
		header:   types.CopyHeader(env.header),
		receipts: copyReceipts(env.receipts),
		blobs:    env.blobs,
	}
	if env.gasPool != nil {
		gasPool := *env.gasPool
		cpy.gasPool = &gasPool
	}
	cpy.txs = make([]*types.Transaction, len(env.txs))
	copy(cpy.txs, env.txs)

	cpy.sidecars = make([]*types.BlobTxSidecar, len(env.sidecars))
	copy(cpy.sidecars, env.sidecars)

	return cpy
}

// discard terminates the background prefetcher go-routine. It should
// always be called for all created environment instances otherwise
// the go-routine leak can happen.
func (env *environment) discard() {
	if env.state == nil {
		return
	}
	env.state.StopPrefetcher()
}

// task contains all information for consensus engine sealing and result submitting.
type task struct {
	receipts  []*types.Receipt
	sidecars  []*types.BlobTxSidecar
	state     *state.StateDB
	block     *types.Block
	createdAt time.Time
}

const (
	commitInterruptNone int32 = iota
	commitInterruptNewHead
	commitInterruptResubmit
	commitInterruptTimeout
)

// newWorkReq represents a request for new sealing work submitting with relative interrupt notifier.
type newWorkReq struct {
	interrupt *atomic.Int32
	timestamp int64
}

// newPayloadResult is the result of payload generation.
type newPayloadResult struct {
	err      error
	block    *types.Block
	fees     *big.Int               // total block fees
	sidecars []*types.BlobTxSidecar // collected blobs of blob transactions
}

// getWorkReq represents a request for getting a new sealing work with provided parameters.
type getWorkReq struct {
	params *generateParams
	result chan *newPayloadResult // non-blocking channel
}

// intervalAdjust represents a resubmitting interval adjustment.
type intervalAdjust struct {
	ratio float64
	inc   bool
}

// worker is the main object which takes care of submitting new work to consensus engine
// and gathering the sealing result.
type worker struct {
	config      *Config
	chainConfig *params.ChainConfig
	engine      consensus.Engine
	eth         Backend
	pool        TxPool // full tx pool (may include blob pool in production)
	chain       *core.BlockChain

	// Feeds
	pendingLogsFeed event.Feed

	// Subscriptions
	mux          *event.TypeMux
	txsCh        chan core.NewTxsEvent
	txsSub       event.Subscription
	chainHeadCh  chan core.ChainHeadEvent
	chainHeadSub event.Subscription

	// Channels
	newWorkCh          chan *newWorkReq
	getWorkCh          chan *getWorkReq
	taskCh             chan *task
	resultCh           chan *types.Block
	startCh            chan struct{}
	exitCh             chan struct{}
	resubmitIntervalCh chan time.Duration
	resubmitAdjustCh   chan *intervalAdjust

	wg sync.WaitGroup

	current *environment // An environment for current running cycle.

	mu       sync.RWMutex // The lock used to protect the coinbase and extra fields
	coinbase common.Address
	extra    []byte
	tip      *uint256.Int // Minimum tip needed for non-local transaction to include them

	pendingMu    sync.RWMutex
	pendingTasks map[common.Hash]*task

	snapshotMu       sync.RWMutex // The lock used to protect the snapshots below
	snapshotBlock    *types.Block
	snapshotReceipts types.Receipts
	snapshotState    *state.StateDB

	// atomic status counters
	running    atomic.Bool  // The indicator whether the consensus engine is running or not.
	newTxs     atomic.Int32 // New arrival transaction count since last sealing work submitting.
	syncing    atomic.Bool  // The indicator whether the node is still syncing.
	busyMining int32        // CAS lock for mining thread (per-worker instance).

	// newpayloadTimeout is the maximum timeout allowance for creating payload.
	// The default value is 2 seconds but node operator can set it to arbitrary
	// large value. A large timeout allowance may cause Geth to fail creating
	// a non-empty payload within the specified time and eventually miss the slot
	// in case there are some computation expensive transactions in txpool.
	newpayloadTimeout time.Duration

	// recommit is the time interval to re-create sealing work or to re-build
	// payload in proof-of-stake stage.
	recommit time.Duration

	// External functions
	isLocalBlock func(header *types.Header) bool // Function used to determine whether the specified block is mined by local miner.

	// Test hooks
	newTaskHook  func(*task)                        // Method to call upon receiving a new sealing task.
	skipSealHook func(*task) bool                   // Method to decide whether skipping the sealing.
	fullTaskHook func()                             // Method to call before pushing the full sealing task.
	resubmitHook func(time.Duration, time.Duration) // Method to call upon updating resubmitting interval.
}

// initExcessBlobGas sets ExcessBlobGas on header if Camellia is active and it has not been set
// by the consensus engine's Prepare(). Uses the parent block's BlobGasUsed to correctly
// compute the new ExcessBlobGas per EIP-4844.
func initExcessBlobGas(cfg *params.ChainConfig, header *types.Header, parent *types.Header) {
	if !cfg.IsCamellia(header.Number) || header.ExcessBlobGas != nil {
		return
	}
	parentExcessBlobGas := parent.ExcessBlobGas
	if parentExcessBlobGas == nil {
		parentExcessBlobGas = new(big.Int)
	}
	var parentBlobGasUsed uint64
	if parent.BlobGasUsed != nil {
		parentBlobGasUsed = parent.BlobGasUsed.Uint64()
	}
	header.ExcessBlobGas = types.CalcExcessBlobGas(parentExcessBlobGas, parentBlobGasUsed)
	if header.BlobGasUsed == nil {
		header.BlobGasUsed = new(big.Int)
	}
}

func newWorker(config *Config, chainConfig *params.ChainConfig, engine consensus.Engine, eth Backend, mux *event.TypeMux, isLocalBlock func(header *types.Header) bool, init bool) *worker {
	// Use FullTxPool (blob-aware) if the backend supports it; fall back to legacy pool.
	var pool TxPool
	if bab, ok := eth.(BlobAwareBackend); ok {
		pool = bab.FullTxPool()
	} else {
		pool = eth.TxPool()
	}
	worker := &worker{
		config:             config,
		chainConfig:        chainConfig,
		engine:             engine,
		eth:                eth,
		pool:               pool,
		chain:              eth.BlockChain(),
		mux:                mux,
		isLocalBlock:       isLocalBlock,
		coinbase:           config.Etherbase,
		extra:              config.ExtraData,
		tip:                uint256.MustFromBig(config.GasPrice),
		pendingTasks:       make(map[common.Hash]*task),
		txsCh:              make(chan core.NewTxsEvent, txChanSize),
		chainHeadCh:        make(chan core.ChainHeadEvent, chainHeadChanSize),
		newWorkCh:          make(chan *newWorkReq),
		getWorkCh:          make(chan *getWorkReq),
		taskCh:             make(chan *task),
		resultCh:           make(chan *types.Block, resultQueueSize),
		startCh:            make(chan struct{}, 1),
		exitCh:             make(chan struct{}),
		resubmitIntervalCh: make(chan time.Duration),
		resubmitAdjustCh:   make(chan *intervalAdjust, resubmitAdjustChanSize),
	}
	// Subscribe for transaction insertion events (whether from network or resurrects)
	worker.txsSub = pool.SubscribeTransactions(worker.txsCh, true)
	// Subscribe events for blockchain
	worker.chainHeadSub = eth.BlockChain().SubscribeChainHeadEvent(worker.chainHeadCh)

	// Sanitize recommit interval if the user-specified one is too short.
	recommit := worker.config.Recommit
	if recommit < minRecommitInterval {
		log.Warn("Sanitizing miner recommit interval", "provided", recommit, "updated", minRecommitInterval)
		recommit = minRecommitInterval
	}
	worker.recommit = recommit

	// Sanitize the timeout config for creating payload.
	newpayloadTimeout := worker.config.NewPayloadTimeout
	if newpayloadTimeout == 0 {
		log.Warn("Sanitizing new payload timeout to default", "provided", newpayloadTimeout, "updated", DefaultConfig.NewPayloadTimeout)
		newpayloadTimeout = DefaultConfig.NewPayloadTimeout
	}
	if newpayloadTimeout < time.Millisecond*100 {
		log.Warn("Low payload timeout may cause high amount of non-full blocks", "provided", newpayloadTimeout, "default", DefaultConfig.NewPayloadTimeout)
	}
	worker.newpayloadTimeout = newpayloadTimeout

	worker.wg.Add(4)
	go worker.mainLoop()
	if metaminer.IsPoW() {
		go worker.newWorkLoop(recommit)
	} else {
		go worker.newWorkLoopEx(recommit)
	}
	go worker.resultLoop()
	go worker.taskLoop()

	// Submit first work to initialize pending state.
	if init {
		worker.startCh <- struct{}{}
	}
	return worker
}

// setEtherbase sets the etherbase used to initialize the block coinbase field.
func (w *worker) setEtherbase(addr common.Address) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coinbase = addr
}

// etherbase retrieves the configured etherbase address.
func (w *worker) etherbase() common.Address {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.coinbase
}

func (w *worker) setGasCeil(ceil uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.config.GasCeil = ceil
}

// setExtra sets the content used to initialize the block extra field.
func (w *worker) setExtra(extra []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.extra = extra
}

// setGasTip sets the minimum miner tip needed to include a non-local transaction.
func (w *worker) setGasTip(tip *big.Int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tip = uint256.MustFromBig(tip)
}

// setRecommitInterval updates the interval for miner sealing work recommitting.
func (w *worker) setRecommitInterval(interval time.Duration) {
	select {
	case w.resubmitIntervalCh <- interval:
	case <-w.exitCh:
	}
}

// pending returns the pending state and corresponding block. The returned
// values can be nil in case the pending block is not initialized.
func (w *worker) pending() (*types.Block, *state.StateDB) {
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	if w.snapshotState == nil {
		return nil, nil
	}
	return w.snapshotBlock, w.snapshotState.Copy()
}

// pendingBlock returns pending block. The returned block can be nil in case the
// pending block is not initialized.
func (w *worker) pendingBlock() *types.Block {
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	return w.snapshotBlock
}

// pendingBlockAndReceipts returns pending block and corresponding receipts.
// The returned values can be nil in case the pending block is not initialized.
func (w *worker) pendingBlockAndReceipts() (*types.Block, types.Receipts) {
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	return w.snapshotBlock, w.snapshotReceipts
}

// start sets the running status as 1 and triggers new work submitting.
func (w *worker) start() {
	w.running.Store(true)
	w.startCh <- struct{}{}
}

// stop sets the running status as 0.
func (w *worker) stop() {
	w.running.Store(false)
}

// isRunning returns an indicator whether worker is running or not.
func (w *worker) isRunning() bool {
	return w.running.Load()
}

// close terminates all background threads maintained by the worker.
// Note the worker does not support being closed multiple times.
func (w *worker) close() {
	w.running.Store(false)
	close(w.exitCh)
	w.wg.Wait()
}

// recalcRecommit recalculates the resubmitting interval upon feedback.
func recalcRecommit(minRecommit, prev time.Duration, target float64, inc bool) time.Duration {
	var (
		prevF = float64(prev.Nanoseconds())
		next  float64
	)
	if inc {
		next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target+intervalAdjustBias)
		max := float64(maxRecommitInterval.Nanoseconds())
		if next > max {
			next = max
		}
	} else {
		next = prevF*(1-intervalAdjustRatio) + intervalAdjustRatio*(target-intervalAdjustBias)
		min := float64(minRecommit.Nanoseconds())
		if next < min {
			next = min
		}
	}
	return time.Duration(int64(next))
}

// newWorkLoop is a standalone goroutine to submit new sealing work upon received events.
func (w *worker) newWorkLoop(recommit time.Duration) {
	defer w.wg.Done()
	var (
		interrupt   *atomic.Int32
		minRecommit = recommit // minimal resubmit interval specified by user.
		timestamp   int64      // timestamp for each round of sealing.
	)

	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C // discard the initial tick

	// commit aborts in-flight transaction execution with given signal and resubmits a new one.
	commit := func(s int32) {
		if interrupt != nil {
			interrupt.Store(s)
		}
		interrupt = new(atomic.Int32)
		select {
		case w.newWorkCh <- &newWorkReq{interrupt: interrupt, timestamp: timestamp}:
		case <-w.exitCh:
			return
		}
		timer.Reset(recommit)
		w.newTxs.Store(0)
	}
	// clearPending cleans the stale pending tasks.
	clearPending := func(number uint64) {
		w.pendingMu.Lock()
		for h, t := range w.pendingTasks {
			if t.block.NumberU64()+staleThreshold <= number {
				delete(w.pendingTasks, h)
			}
		}
		w.pendingMu.Unlock()
	}

	for {
		select {
		case <-w.startCh:
			clearPending(w.chain.CurrentBlock().Number.Uint64())
			timestamp = time.Now().Unix()
			commit(commitInterruptNewHead)

		case head := <-w.chainHeadCh:
			clearPending(head.Block.NumberU64())
			timestamp = time.Now().Unix()
			commit(commitInterruptNewHead)

		case <-timer.C:
			// If sealing is running resubmit a new work cycle periodically to pull in
			// higher priced transactions. Disable this overhead for pending blocks.
			if w.isRunning() && (w.chainConfig.Clique == nil || w.chainConfig.Clique.Period > 0) {
				// Short circuit if no new transaction arrives.
				if w.newTxs.Load() == 0 {
					timer.Reset(recommit)
					continue
				}
				commit(commitInterruptResubmit)
			}

		case interval := <-w.resubmitIntervalCh:
			// Adjust resubmit interval explicitly by user.
			if interval < minRecommitInterval {
				log.Warn("Sanitizing miner recommit interval", "provided", interval, "updated", minRecommitInterval)
				interval = minRecommitInterval
			}
			log.Info("Miner recommit interval update", "from", minRecommit, "to", interval)
			minRecommit, recommit = interval, interval

			if w.resubmitHook != nil {
				w.resubmitHook(minRecommit, recommit)
			}

		case adjust := <-w.resubmitAdjustCh:
			// Adjust resubmit interval by feedback.
			if adjust.inc {
				before := recommit
				target := float64(recommit.Nanoseconds()) / adjust.ratio
				recommit = recalcRecommit(minRecommit, recommit, target, true)
				log.Trace("Increase miner recommit interval", "from", before, "to", recommit)
			} else {
				before := recommit
				recommit = recalcRecommit(minRecommit, recommit, float64(minRecommit.Nanoseconds()), false)
				log.Trace("Decrease miner recommit interval", "from", before, "to", recommit)
			}

			if w.resubmitHook != nil {
				w.resubmitHook(minRecommit, recommit)
			}

		case <-w.exitCh:
			return
		}
	}
}

// newWorkLoopEx is Metadium's standalone goroutine to submit new mining work upon received events.
func (w *worker) newWorkLoopEx(recommit time.Duration) {
	defer w.wg.Done()

	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()

	// commitSimple just starts a new commitNewWork
	commitSimple := func() {
		if atomic.CompareAndSwapInt32(&w.busyMining, 0, 1) {
			w.newWorkCh <- &newWorkReq{interrupt: nil, timestamp: time.Now().Unix()}
			w.newTxs.Store(0)
			atomic.StoreInt32(&w.busyMining, 0)
		}
	}
	// clearPending cleans the stale pending tasks.
	clearPending := func(number uint64) {
		w.pendingMu.Lock()
		for h, t := range w.pendingTasks {
			if t.block.NumberU64()+staleThreshold <= number {
				delete(w.pendingTasks, h)
			}
		}
		w.pendingMu.Unlock()
	}

	for {
		select {
		case <-w.startCh:
			w.refreshPending(false)
			clearPending(w.chain.CurrentBlock().Number.Uint64())
			commitSimple()

		case head := <-w.chainHeadCh:
			clearPending(head.Block.NumberU64())
			commitSimple()

		case <-timer.C:
			commitSimple()
			timer.Reset(1 * time.Second)

		case <-w.resubmitIntervalCh:
		case <-w.resubmitAdjustCh:
		case <-w.exitCh:
			return
		}
	}
}

// mainLoop is responsible for generating and submitting sealing work based on
// the received event. It can support two modes: automatically generate task and
// submit it or return task according to given parameters for various proposes.
func (w *worker) mainLoop() {
	defer w.wg.Done()
	defer w.txsSub.Unsubscribe()
	defer w.chainHeadSub.Unsubscribe()
	defer func() {
		if w.current != nil {
			w.current.discard()
		}
	}()

	for {
		select {
		case req := <-w.newWorkCh:
			w.commitWork(req.interrupt, req.timestamp)

		case req := <-w.getWorkCh:
			req.result <- w.generateWork(req.params)

		case ev := <-w.txsCh:
			// Apply transactions to the pending state if we're not sealing
			//
			// Note all transactions received may not be continuous with transactions
			// already included in the current sealing block. These transactions will
			// be automatically eliminated.
			if !w.isRunning() && w.current != nil {
				// If block is already full, abort
				if gp := w.current.gasPool; gp != nil && gp.Gas() < params.TxGas {
					continue
				}
				txs := make(map[common.Address][]*txpool.LazyTransaction, len(ev.Txs))
				for _, tx := range ev.Txs {
					acc, _ := types.Sender(w.current.signer, tx)
					txs[acc] = append(txs[acc], &txpool.LazyTransaction{
						Pool:      w.eth.TxPool(), // We don't know where this came from, yolo resolve from everywhere
						Hash:      tx.Hash(),
						Tx:        nil, // Do *not* set this! We need to resolve it later to pull blobs in
						Time:      time.Now(),
						GasFeeCap: uint256.MustFromBig(tx.GasFeeCap()),
						GasTipCap: uint256.MustFromBig(tx.GasTipCap()),
						Gas:       tx.Gas(),
						BlobGas:   tx.BlobGas(),
					})
				}
				plainTxs := newTransactionsByPriceAndNonce(w.current.signer, txs, w.current.header.BaseFee) // Mixed bag of everrything, yolo
				blobTxs := newTransactionsByPriceAndNonce(w.current.signer, nil, w.current.header.BaseFee)  // Empty bag, don't bother optimising

				tcount := w.current.tcount
				w.commitTransactions(w.current, plainTxs, blobTxs, nil)

				// Only update the snapshot if any new transactions were added
				// to the pending block
				if tcount != w.current.tcount {
					w.updateSnapshot(w.current)
				}
			} else {
				// Special case, if the consensus engine is 0 period clique(dev mode),
				// submit sealing work here since all empty submission will be rejected
				// by clique. Of course the advance sealing(empty submission) is disabled.
				if w.chainConfig.Clique != nil && w.chainConfig.Clique.Period == 0 {
					w.commitWork(nil, time.Now().Unix())
				}
			}
			w.newTxs.Add(int32(len(ev.Txs)))

		// System stopped
		case <-w.exitCh:
			return
		case <-w.txsSub.Err():
			return
		case <-w.chainHeadSub.Err():
			return
		}
	}
}

// taskLoop is a standalone goroutine to fetch sealing task from the generator and
// push them to consensus engine.
func (w *worker) taskLoop() {
	defer w.wg.Done()
	var (
		stopCh chan struct{}
		prev   common.Hash
	)

	// interrupt aborts the in-flight sealing task.
	interrupt := func() {
		if stopCh != nil {
			close(stopCh)
			stopCh = nil
		}
	}
	for {
		select {
		case task := <-w.taskCh:
			if w.newTaskHook != nil {
				w.newTaskHook(task)
			}
			// Reject duplicate sealing work due to resubmitting.
			sealHash := w.engine.SealHash(task.block.Header())
			if sealHash == prev {
				continue
			}
			// Interrupt previous sealing operation
			interrupt()
			stopCh, prev = make(chan struct{}), sealHash

			if w.skipSealHook != nil && w.skipSealHook(task) {
				continue
			}
			w.pendingMu.Lock()
			w.pendingTasks[sealHash] = task
			w.pendingMu.Unlock()

			if err := w.engine.Seal(w.chain, task.block, w.resultCh, stopCh); err != nil {
				log.Warn("Block sealing failed", "err", err)
				w.pendingMu.Lock()
				delete(w.pendingTasks, sealHash)
				w.pendingMu.Unlock()
			}
		case <-w.exitCh:
			interrupt()
			return
		}
	}
}

// resultLoop is a standalone goroutine to handle sealing result submitting
// and flush relative data to the database.
func (w *worker) resultLoop() {
	defer w.wg.Done()
	for {
		select {
		case block := <-w.resultCh:
			// Short circuit when receiving empty result.
			if block == nil {
				continue
			}
			// Short circuit when receiving duplicate result caused by resubmitting.
			if w.chain.HasBlock(block.Hash(), block.NumberU64()) {
				continue
			}
			var (
				sealhash = w.engine.SealHash(block.Header())
				hash     = block.Hash()
			)
			w.pendingMu.RLock()
			task, exist := w.pendingTasks[sealhash]
			w.pendingMu.RUnlock()
			if !exist {
				log.Error("Block found but no relative pending task", "number", block.Number(), "sealhash", sealhash, "hash", hash)
				continue
			}
			// Different block could share same sealhash, deep copy here to prevent write-write conflict.
			var (
				receipts = make([]*types.Receipt, len(task.receipts))
				logs     []*types.Log
			)
			for i, taskReceipt := range task.receipts {
				receipt := new(types.Receipt)
				receipts[i] = receipt
				*receipt = *taskReceipt

				// add block location fields
				receipt.BlockHash = hash
				receipt.BlockNumber = block.Number()
				receipt.TransactionIndex = uint(i)

				// Update the block hash in all logs since it is now available and not when the
				// receipt/log of individual transactions were created.
				receipt.Logs = make([]*types.Log, len(taskReceipt.Logs))
				for i, taskLog := range taskReceipt.Logs {
					log := new(types.Log)
					receipt.Logs[i] = log
					*log = *taskLog
					log.BlockHash = hash
				}
				logs = append(logs, receipt.Logs...)
			}
			// Commit block and state to database.
			_, err := w.chain.WriteBlockAndSetHead(block, receipts, logs, task.state, true)
			if err != nil {
				log.Error("Failed writing block to chain", "err", err)
				continue
			}
			// Persist blob sidecars for this block (Metadium: no beacon chain).
			if len(task.sidecars) > 0 {
				rawdb.WriteBlobSidecars(w.chain.ChainDb(), hash, block.NumberU64(), task.sidecars)
			}
			log.Info("Successfully sealed new block", "number", block.Number(), "sealhash", sealhash, "hash", hash,
				"elapsed", common.PrettyDuration(time.Since(task.createdAt)))

			// Broadcast the block and announce chain insertion event
			w.mux.Post(core.NewMinedBlockEvent{Block: block})

		case <-w.exitCh:
			return
		}
	}
}

// makeEnv creates a new environment for the sealing block.
func (w *worker) makeEnv(parent *types.Header, header *types.Header, coinbase common.Address) (*environment, error) {
	// Retrieve the parent state to execute on top and start a prefetcher for
	// the miner to speed block sealing up a bit.
	state, err := w.chain.StateAt(parent.Root)
	if err != nil {
		return nil, err
	}
	state.StartPrefetcher("miner")

	// Note the passed coinbase may be different with header.Coinbase.
	env := &environment{
		signer:   types.MakeSigner(w.chainConfig, header.Number, header.Time),
		state:    state,
		coinbase: coinbase,
		header:   header,
	}
	// Keep track of transactions which return errors so they can be removed
	env.tcount = 0
	return env, nil
}



// updateSnapshot updates pending snapshot block, receipts and state.
func (w *worker) updateSnapshot(env *environment) {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()

	w.snapshotBlock = types.NewBlock(
		env.header,
		env.txs,
		nil,
		env.receipts,
		trie.NewStackTrie(nil),
	)
	w.snapshotReceipts = copyReceipts(env.receipts)
	w.snapshotState = env.state.Copy()
}

func (w *worker) commitTransaction(env *environment, tx *types.Transaction) ([]*types.Log, error) {
	if tx.Type() == types.BlobTxType {
		return w.commitBlobTransaction(env, tx)
	}
	receipt, err := w.applyTransaction(env, tx)
	if err != nil {
		return nil, err
	}
	env.txs = append(env.txs, tx)
	env.receipts = append(env.receipts, receipt)
	return receipt.Logs, nil
}

func (w *worker) commitBlobTransaction(env *environment, tx *types.Transaction) ([]*types.Log, error) {
	sc := tx.BlobTxSidecar()
	if sc == nil {
		panic("blob transaction without blobs in miner")
	}
	// Checking against blob gas limit: It's kind of ugly to perform this check here, but there
	// isn't really a better place right now. The blob gas limit is checked at block validation time
	// and not during execution. This means core.ApplyTransaction will not return an error if the
	// tx has too many blobs. So we have to explicitly check it here.
	if (env.blobs+len(sc.Blobs))*params.BlobTxBlobGasPerBlob > params.MaxBlobGasPerBlock {
		return nil, errors.New("max data blobs reached")
	}
	receipt, err := w.applyTransaction(env, tx)
	if err != nil {
		return nil, err
	}
	env.txs = append(env.txs, tx.WithoutBlobTxSidecar())
	env.receipts = append(env.receipts, receipt)
	env.sidecars = append(env.sidecars, sc)
	env.blobs += len(sc.Blobs)
	if env.header.BlobGasUsed == nil {
		env.header.BlobGasUsed = new(big.Int)
	}
	// receipt.BlobGasUsed is not populated in Metadium's applyTransaction,
	// so compute blob gas directly from the sidecar blob count.
	blobGas := uint64(len(sc.Blobs)) * params.BlobTxBlobGasPerBlob
	env.header.BlobGasUsed.Add(env.header.BlobGasUsed, new(big.Int).SetUint64(blobGas))
	return receipt.Logs, nil
}

// applyTransaction runs the transaction. If execution fails, state and gas pool are reverted.
func (w *worker) applyTransaction(env *environment, tx *types.Transaction) (*types.Receipt, error) {
	var (
		snap = env.state.Snapshot()
		gp   = env.gasPool.Gas()
	)
	receipt, err := core.ApplyTransaction(w.chainConfig, w.chain, &env.coinbase, env.gasPool, env.state, env.header, tx, &env.header.GasUsed, env.header.Fees, *w.chain.GetVMConfig())
	if err != nil {
		env.state.RevertToSnapshot(snap)
		env.gasPool.SetGas(gp)
	}
	return receipt, err
}

func (w *worker) commitTransactions(env *environment, plainTxs, blobTxs *transactionsByPriceAndNonce, interrupt *atomic.Int32) error {
	gasLimit := env.header.GasLimit
	if env.gasPool == nil {
		env.gasPool = new(core.GasPool).AddGas(gasLimit)
	}
	var coalescedLogs []*types.Log

	for {
		// Check interruption signal and abort building if it's fired.
		if interrupt != nil {
			if signal := interrupt.Load(); signal != commitInterruptNone {
				return signalToErr(signal)
			}
		}
		// If we don't have enough gas for any further transactions then we're done.
		if env.gasPool.Gas() < params.TxGas {
			log.Trace("Not enough gas for further transactions", "have", env.gasPool, "want", params.TxGas)
			break
		}
		// Metadium: per-block transaction count limit
		if params.MaxTxsPerBlock > 0 && env.tcount >= params.MaxTxsPerBlock {
			log.Trace("Transaction count limit reached", "have", env.tcount, "max", params.MaxTxsPerBlock)
			break
		}
		// If we don't have enough blob space for any further blob transactions,
		// skip that list altogether
		if !blobTxs.Empty() && env.blobs*params.BlobTxBlobGasPerBlob >= params.MaxBlobGasPerBlock {
			log.Trace("Not enough blob space for further blob transactions")
			blobTxs.Clear()
			// Fall though to pick up any plain txs
		}
		// Retrieve the next transaction and abort if all done.
		var (
			ltx *txpool.LazyTransaction
			txs *transactionsByPriceAndNonce
		)
		pltx, ptip := plainTxs.Peek()
		bltx, btip := blobTxs.Peek()

		switch {
		case pltx == nil:
			txs, ltx = blobTxs, bltx
		case bltx == nil:
			txs, ltx = plainTxs, pltx
		default:
			if ptip.Lt(btip) {
				txs, ltx = blobTxs, bltx
			} else {
				txs, ltx = plainTxs, pltx
			}
		}
		if ltx == nil {
			break
		}
		// If we don't have enough space for the next transaction, skip the account.
		if env.gasPool.Gas() < ltx.Gas {
			log.Trace("Not enough gas left for transaction", "hash", ltx.Hash, "left", env.gasPool.Gas(), "needed", ltx.Gas)
			txs.Pop()
			continue
		}
		if left := uint64(params.MaxBlobGasPerBlock - env.blobs*params.BlobTxBlobGasPerBlob); left < ltx.BlobGas {
			log.Trace("Not enough blob gas left for transaction", "hash", ltx.Hash, "left", left, "needed", ltx.BlobGas)
			txs.Pop()
			continue
		}
		// Transaction seems to fit, pull it up from the pool
		tx := ltx.Resolve()
		if tx == nil {
			log.Trace("Ignoring evicted transaction", "hash", ltx.Hash)
			txs.Pop()
			continue
		}
		// Error may be ignored here. The error has already been checked
		// during transaction acceptance is the transaction pool.
		from, _ := types.Sender(env.signer, tx)

		// Metadium: TRS (Transaction Restriction Service) filtering
		if env.trsListMap != nil && len(env.trsListMap) > 0 && env.trsSubscribe {
			if env.trsListMap[from] || (tx.To() != nil && env.trsListMap[*tx.To()]) {
				log.Debug("included in trsList", "hash", tx.Hash(), "from", from)
				txs.Pop()
				continue
			}
		}

		// Check whether the tx is replay protected. If we're not in the EIP155 hf
		// phase, start ignoring the sender until we do.
		if tx.Protected() && !w.chainConfig.IsEIP155(env.header.Number) {
			log.Trace("Ignoring replay protected transaction", "hash", ltx.Hash, "eip155", w.chainConfig.EIP155Block)
			txs.Pop()
			continue
		}
		// Start executing the transaction
		env.state.SetTxContext(tx.Hash(), env.tcount)

		var (
			logs []*types.Log
			err  error
		)
		if tx.Type() == types.BlobTxType {
			logs, err = w.commitBlobTransaction(env, tx)
		} else {
			logs, err = w.commitTransaction(env, tx)
		}
		switch {
		case errors.Is(err, core.ErrNonceTooLow):
			// New head notification data race between the transaction pool and miner, shift
			log.Trace("Skipping transaction with low nonce", "hash", ltx.Hash, "sender", from, "nonce", tx.Nonce())
			txs.Shift()

		case errors.Is(err, nil):
			// Everything ok, collect the logs and shift in the next transaction from the same account
			coalescedLogs = append(coalescedLogs, logs...)
			env.tcount++
			txs.Shift()

		default:
			// Transaction is regarded as invalid, drop all consecutive transactions from
			// the same sender because of `nonce-too-high` clause.
			log.Debug("Transaction failed, account skipped", "hash", ltx.Hash, "err", err)
			txs.Pop()
		}
	}
	if !w.isRunning() && len(coalescedLogs) > 0 {
		// We don't push the pendingLogsEvent while we are sealing. The reason is that
		// when we are sealing, the worker will regenerate a sealing block every 3 seconds.
		// In order to avoid pushing the repeated pendingLog, we disable the pending log pushing.

		// make a copy, the state caches the logs and these logs get "upgraded" from pending to mined
		// logs by filling in the block hash when the block was mined by the local miner. This can
		// cause a race condition if a log was "upgraded" before the PendingLogsEvent is processed.
		cpy := make([]*types.Log, len(coalescedLogs))
		for i, l := range coalescedLogs {
			cpy[i] = new(types.Log)
			*cpy[i] = *l
		}
		w.pendingLogsFeed.Send(cpy)
	}
	// Notify resubmit loop to decrease resubmitting interval if current interval is larger
	// than the user-specified one.
	if interrupt != nil {
		w.resubmitAdjustCh <- &intervalAdjust{inc: false}
	}
	return nil
}

// collects ancestors' block times for possible throttling
func (w *worker) ancestorTimes(num *big.Int) []int64 {
	ts := make([]int64, 6)
	for i := 0; i < len(ts); i++ {
		bn := num.Int64()
		switch i {
		case 0:
			bn -= 1
		case 1:
			bn -= 10
		case 2:
			bn -= 50
		case 3:
			bn -= 100
		case 4:
			bn -= 500
		case 5:
			bn -= 1000
		}
		if bn <= 0 {
			continue
		}
		if bh := w.chain.GetHeaderByNumber(uint64(bn)); bh != nil {
			ts[i] = int64(bh.Time)
		}
	}
	return ts
}

// returns throttle delay if necessary in seconds & seconds from the parent
// blocks  seconds  seconds per
//
//	  10        1  0.1
//	  50       10  0.2
//	 100       50  0.5
//	 500      500  1
//	1000     2000  2
func (w *worker) throttleMining(ts []int64) (int64, int64) {
	t := time.Now().Unix()
	dt, pt := int64(0), t-ts[0]

	// 1000th
	if dt = t - ts[5]; ts[5] > 0 && dt < 2000 {
		return 2000 - dt, pt
	}
	if dt = t - ts[4]; ts[4] > 0 && dt < 500 {
		return 500 - dt, pt
	}
	if dt = t - ts[3]; ts[3] > 0 && dt < 50 {
		return 50 - dt, pt
	}
	if dt = t - ts[2]; ts[2] > 0 && dt < 10 {
		return 10 - dt, pt
	}
	return 0, pt
}

func (w *worker) commitTransactionsEx(env *environment, interrupt *atomic.Int32, tstart time.Time) bool {

	// committed transactions (tracked by hash to avoid reprocessing)
	committedTxHashes := map[common.Hash]struct{}{}

	txCh := make(chan core.NewTxsEvent, 10000)
	sub := w.pool.SubscribeTransactions(txCh, true)
	defer sub.Unsubscribe()

	for {
		if interrupt != nil && interrupt.Load() != 0 {
			return true
		}

		// Drain any queued tx notifications first, so the subsequent pending
		// snapshot reflects all arrivals that triggered those events.
		for {
			select {
			case <-txCh:
			default:
				goto drained
			}
		}
	drained:

		// Fill the block with all available pending transactions.
		filter := txpool.PendingFilter{}
		if env.header.BaseFee != nil {
			filter.BaseFee = uint256.MustFromBig(env.header.BaseFee)
		}
		filter.OnlyPlainTxs = true
		plainPending := w.pool.Pending(filter)

		filter.OnlyPlainTxs = false
		filter.OnlyBlobTxs = true
		blobPendingLazy := w.pool.Pending(filter)

		// Short circuit if there is no available pending transactions
		if len(plainPending) != 0 {
			// Remove already-committed txs from pending
			if len(committedTxHashes) > 0 {
				for addr, ltxs := range plainPending {
					var filtered []*txpool.LazyTransaction
					for _, ltx := range ltxs {
						if _, done := committedTxHashes[ltx.Hash]; !done {
							filtered = append(filtered, ltx)
						}
					}
					if len(filtered) > 0 {
						plainPending[addr] = filtered
					} else {
						delete(plainPending, addr)
					}
				}
			}

			if len(plainPending) > 0 {
				plainTxs := newTransactionsByPriceAndNonce(env.signer, plainPending, env.header.BaseFee)
				blobTxs := newTransactionsByPriceAndNonce(env.signer, nil, env.header.BaseFee)
				if err := w.commitTransactions(env, plainTxs, blobTxs, interrupt); err != nil {
					return true
				}
				// Track committed tx hashes
				for _, tx := range env.txs {
					committedTxHashes[tx.Hash()] = struct{}{}
				}
			}
		}
		// EIP-4844: commit blob transactions up to MaxBlobGasPerBlock.
		if w.chainConfig.IsCamellia(env.header.Number) && len(blobPendingLazy) > 0 {
			plainTxs := newTransactionsByPriceAndNonce(env.signer, nil, env.header.BaseFee)
			blobTxs := newTransactionsByPriceAndNonce(env.signer, blobPendingLazy, env.header.BaseFee)
			if err := w.commitTransactions(env, plainTxs, blobTxs, interrupt); err != nil {
				return true
			}
		}

		// Wait until env.till or until new transactions arrive.
		if time.Now().After(*env.till) {
			break
		}

		remaining := time.Until(*env.till)
		timer := time.NewTimer(remaining)
		select {
		case <-timer.C:
		case <-txCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			continue
		case <-sub.Err():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return true
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}

	log.Debug("Block", "number", env.header.Number.Int64(), "elapsed", common.PrettyDuration(time.Since(tstart)), "txs", env.tcount)

	return false
}

func (w *worker) isBusyMining() bool {
	return atomic.LoadInt32(&w.busyMining) != 0
}

// generateParams wraps various of settings for generating sealing task.
type generateParams struct {
	timestamp   uint64            // The timestamp for sealing task
	forceTime   bool              // Flag whether the given timestamp is immutable or not
	parentHash  common.Hash       // Parent block hash, empty means the latest chain head
	coinbase    common.Address    // The fee recipient address for including transaction
	random      common.Hash       // The randomness generated by beacon chain, empty before the merge
	withdrawals types.Withdrawals // List of withdrawals to include in block.
	beaconRoot  *common.Hash      // The beacon root (cancun field).
	noTxs       bool              // Flag whether an empty block without any transaction is expected
}

// prepareWork constructs the sealing task according to the given parameters,
// either based on the last chain head or specified parent. In this function
// the pending transactions are not filled yet, only the empty task returned.
func (w *worker) prepareWork(genParams *generateParams) (*environment, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	var till time.Time

	// Find the parent block for sealing task
	var parent *types.Header
	if genParams.parentHash != (common.Hash{}) {
		block := w.chain.GetBlockByHash(genParams.parentHash)
		if block == nil {
			return nil, fmt.Errorf("missing parent")
		}
		parent = block.Header()
	} else {
		parent = w.chain.CurrentBlock()
	}
	if parent == nil {
		return nil, fmt.Errorf("missing parent")
	}
	blockInterval, _, blockGasLimit, baseFeeMaxChangeRate, gasTargetPercentage, _ := metaminer.GetBlockBuildParameters(parent.Number)
	// Sanity check the timestamp correctness, recap the timestamp
	// to parent+1 if the mutation is allowed.
	timestamp := genParams.timestamp
	if metaminer.IsPoW() && parent.Time >= timestamp {
		if genParams.forceTime {
			return nil, fmt.Errorf("invalid timestamp, parent %d given %d", parent.Time, timestamp)
		}
		timestamp = parent.Time + 1
	} else if !metaminer.IsPoW() {
		// metadium: use timeIt for PoA block timing
		timestamp, till = w.timeIt(blockInterval)
	} else if parent.Time >= timestamp {
		if genParams.forceTime {
			return nil, fmt.Errorf("invalid timestamp, parent %d given %d", parent.Time, timestamp)
		}
		timestamp = parent.Time + 1
	}
	// Construct the sealing block header.
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     new(big.Int).Add(parent.Number, common.Big1),
		GasLimit:   core.CalcGasLimit(parent.GasLimit, w.config.GasCeil),
		Time:       timestamp,
		Coinbase:   genParams.coinbase,
		Fees:       big.NewInt(0),
	}
	// Set the extra field.
	if len(w.extra) != 0 {
		header.Extra = w.extra
	}
	if !metaminer.IsPoW() {
		header.GasLimit = core.CalcGasLimit(parent.GasLimit, blockGasLimit.Uint64())
	}
	// Set the randomness field from the beacon chain if it's available.
	if genParams.random != (common.Hash{}) {
		header.MixDigest = genParams.random
	}
	// Set baseFee and GasLimit if we are on an EIP-1559 chain
	if w.chainConfig.IsLondon(header.Number) {
		header.BaseFee = eip1559.CalcBaseFee(w.chainConfig, parent)
		if !w.chainConfig.IsLondon(parent.Number) {
			parentGasLimit := parent.GasLimit * w.chainConfig.ElasticityMultiplier()
			header.GasLimit = core.CalcGasLimit(parentGasLimit, w.config.GasCeil)
		}
	}
	// Apply EIP-4844, EIP-4788.
	if w.chainConfig.IsCancun(header.Number, header.Time) {
		var excessBlobGas uint64
		if w.chainConfig.IsCancun(parent.Number, parent.Time) {
			var prevExcess, prevBlobGasUsed uint64
			if parent.ExcessBlobGas != nil {
				prevExcess = parent.ExcessBlobGas.Uint64()
			}
			if parent.BlobGasUsed != nil {
				prevBlobGasUsed = parent.BlobGasUsed.Uint64()
			}
			excessBlobGas = eip4844.CalcExcessBlobGas(prevExcess, prevBlobGasUsed)
		} else {
			// For the first post-fork block, both parent.data_gas_used and parent.excess_data_gas are evaluated as 0
			excessBlobGas = eip4844.CalcExcessBlobGas(0, 0)
		}
		header.BlobGasUsed = new(big.Int)
		header.ExcessBlobGas = new(big.Int).SetUint64(excessBlobGas)
		header.ParentBeaconRoot = genParams.beaconRoot
	}
	// Run the consensus preparation with the default or customized consensus engine.
	if err := w.engine.Prepare(w.chain, header); err != nil {
		log.Error("Failed to prepare header for sealing", "err", err)
		return nil, err
	}
	// EIP-4844: Initialize ExcessBlobGas for Camellia fork blocks.
	initExcessBlobGas(w.chainConfig, header, parent)
	// Could potentially happen if starting to mine in an odd state.
	// Note genParams.coinbase can be different with header.Coinbase
	// since clique algorithm can modify the coinbase field in header.
	parentBlock := w.chain.GetBlockByHash(parent.Hash())
	if parentBlock == nil {
		return nil, fmt.Errorf("parent block not found: %s", parent.Hash().Hex())
	}
	env, err := w.makeEnv(parentBlock.Header(), header, genParams.coinbase)
	if err != nil {
		log.Error("Failed to create sealing context", "err", err)
		return nil, err
	}
	if !metaminer.IsPoW() {
		env.till = &till
	}
	env.blockInterval = blockInterval
	env.blockGasLimit = blockGasLimit
	env.baseFeeMaxChangeRate = baseFeeMaxChangeRate
	env.gasTargetPercentage = gasTargetPercentage
	if header.ParentBeaconRoot != nil {
		context := core.NewEVMBlockContext(header, w.chain, nil)
		vmenv := vm.NewEVM(context, vm.TxContext{}, env.state, w.chainConfig, vm.Config{})
		core.ProcessBeaconBlockRoot(*header.ParentBeaconRoot, vmenv, env.state)
	}
	return env, nil
}

// fillTransactions retrieves the pending transactions from the txpool and fills them
// into the given sealing block. The transaction selection and ordering strategy can
// be customized with the plugin in the future.
func (w *worker) fillTransactions(interrupt *atomic.Int32, env *environment) error {
	w.mu.RLock()
	tip := w.tip
	w.mu.RUnlock()

	// Retrieve the pending transactions pre-filtered by the 1559/4844 dynamic fees
	filter := txpool.PendingFilter{
		MinTip: tip,
	}
	if env.header.BaseFee != nil {
		filter.BaseFee = uint256.MustFromBig(env.header.BaseFee)
	}
	if env.header.ExcessBlobGas != nil {
		filter.BlobFee = uint256.MustFromBig(eip4844.CalcBlobFee(env.header.ExcessBlobGas.Uint64()))
	}
	filter.OnlyPlainTxs, filter.OnlyBlobTxs = true, false
	pendingPlainTxs := w.pool.Pending(filter)

	filter.OnlyPlainTxs, filter.OnlyBlobTxs = false, true
	pendingBlobTxs := w.pool.Pending(filter)

	// Split the pending transactions into locals and remotes.
	localPlainTxs, remotePlainTxs := make(map[common.Address][]*txpool.LazyTransaction), pendingPlainTxs
	localBlobTxs, remoteBlobTxs := make(map[common.Address][]*txpool.LazyTransaction), pendingBlobTxs

	for _, account := range w.pool.Locals() {
		if txs := remotePlainTxs[account]; len(txs) > 0 {
			delete(remotePlainTxs, account)
			localPlainTxs[account] = txs
		}
		if txs := remoteBlobTxs[account]; len(txs) > 0 {
			delete(remoteBlobTxs, account)
			localBlobTxs[account] = txs
		}
	}
	// Fill the block with all available pending transactions.
	if len(localPlainTxs) > 0 || len(localBlobTxs) > 0 {
		plainTxs := newTransactionsByPriceAndNonce(env.signer, localPlainTxs, env.header.BaseFee)
		blobTxs := newTransactionsByPriceAndNonce(env.signer, localBlobTxs, env.header.BaseFee)

		if err := w.commitTransactions(env, plainTxs, blobTxs, interrupt); err != nil {
			return err
		}
	}
	if len(remotePlainTxs) > 0 || len(remoteBlobTxs) > 0 {
		plainTxs := newTransactionsByPriceAndNonce(env.signer, remotePlainTxs, env.header.BaseFee)
		blobTxs := newTransactionsByPriceAndNonce(env.signer, remoteBlobTxs, env.header.BaseFee)

		if err := w.commitTransactions(env, plainTxs, blobTxs, interrupt); err != nil {
			return err
		}
	}
	return nil
}

// refreshPending reinitialize pending state
func (w *worker) refreshPending(locked bool) {
	if !locked {
		if atomic.CompareAndSwapInt32(&w.busyMining, 0, 1) {
			defer atomic.StoreInt32(&w.busyMining, 0)
		} else {
			return
		}
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	parent := w.chain.CurrentBlock()

	blockInterval, _, blockGasLimit, baseFeeMaxChangeRate, gasTargetPercentage, _ := metaminer.GetBlockBuildParameters(parent.Number)
	num := new(big.Int).Set(parent.Number)
	num.Add(num, common.Big1)
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     num,
		GasLimit:   core.CalcGasLimit(parent.GasLimit, w.config.GasCeil),
		Extra:      w.extra,
		Time:       uint64(time.Now().Unix()),
		Fees:       big.NewInt(0),
	}
	if !metaminer.IsPoW() {
		header.GasLimit = core.CalcGasLimit(parent.GasLimit, blockGasLimit.Uint64())
	}
	header.Coinbase = w.coinbase
	// Set baseFee and GasLimit if we are on an EIP-1559 chain
	if w.chainConfig.IsLondon(header.Number) {
		header.BaseFee = eip1559.CalcBaseFee(w.chainConfig, parent)
		if !w.chainConfig.IsLondon(parent.Number) {
			header.GasLimit = parent.GasLimit
		}
	}
	if err := w.engine.Prepare(w.chain, header); err != nil {
		log.Error("Failed to prepare header for mining", "err", err)
		return
	}
	// EIP-4844: Initialize ExcessBlobGas for Camellia fork blocks.
	initExcessBlobGas(w.chainConfig, header, parent)
	parentBlock := w.chain.GetBlockByHash(parent.Hash())
	if parentBlock == nil {
		return
	}
	if env, err := w.makeEnv(parentBlock.Header(), header, header.Coinbase); err == nil {
		env.blockInterval = blockInterval
		env.blockGasLimit = blockGasLimit
		env.baseFeeMaxChangeRate = baseFeeMaxChangeRate
		env.gasTargetPercentage = gasTargetPercentage
		w.updateSnapshot(env)
	}
}

// generateWork generates a sealing block based on the given parameters.
func (w *worker) generateWork(params *generateParams) *newPayloadResult {
	work, err := w.prepareWork(params)
	if err != nil {
		return &newPayloadResult{err: err}
	}
	defer work.discard()

	if !params.noTxs {
		interrupt := new(atomic.Int32)
		timer := time.AfterFunc(w.newpayloadTimeout, func() {
			interrupt.Store(commitInterruptTimeout)
		})
		defer timer.Stop()

		err := w.fillTransactions(interrupt, work)
		if errors.Is(err, errBlockInterruptedByTimeout) {
			log.Warn("Block building is interrupted", "allowance", common.PrettyDuration(w.newpayloadTimeout))
		}
	}
	block, err := w.engine.FinalizeAndAssemble(w.chain, work.header, work.state, work.txs, nil, work.receipts, params.withdrawals)
	if err != nil {
		return &newPayloadResult{err: err}
	}
	return &newPayloadResult{
		block:    block,
		fees:     totalFees(block, work.receipts),
		sidecars: work.sidecars,
	}
}

func (w *worker) timeIt(blockInterval int64) (timestamp uint64, till time.Time) {
	if blockInterval /= 1000; blockInterval <= 0 {
		blockInterval = 2
	}

	maxPeekBack := 86400 / blockInterval // don't look back further than this
	tooBehindMultiple := int64(2)        // ignore if > tooBehindMultiple * height * blockInterval

	parent := w.chain.CurrentBlock()
	num := new(big.Int).Set(parent.Number)
	num.Add(num, common.Big1)
	now := time.Now()
	nowInSeconds := now.Unix()
	nowInMilliSeconds := now.UnixNano() / 1e6 // convert to millisecond

	check := func(heightToPeek int64) (offset int, height, stamp uint64, dt int64) {
		if heightToPeek > maxPeekBack {
			heightToPeek = maxPeekBack
		}
		n := num.Int64() - heightToPeek
		if n < 0 {
			return 0, 0, 0, 0
		}
		h := w.chain.GetHeaderByNumber(uint64(n))
		if h == nil {
			return 0, uint64(n), 0, 0
		}
		offset = 0
		height = uint64(n)
		stamp = h.Time
		dt = nowInSeconds - int64(stamp)
		if heightToPeek*blockInterval < dt && dt < tooBehindMultiple*heightToPeek*blockInterval {
			// behind
			offset = -1
		} else if dt < heightToPeek*blockInterval {
			// ahead
			offset = 1
		}
		return
	}

	ahead := 0
	offset, height, _, dt := check(1)
	log.Debug("time-it", "round", 1, "offset", offset, "height", height, "dt", dt)
	if offset > 0 {
		ahead++
	}
	adjBlocks := params.BlockTimeAdjBlocks / blockInterval
	for i := int64(0); i < params.BlockTimeAdjMultiple; i++ {
		offset, height, _, dt = check(adjBlocks)
		log.Debug("time-it", "round", adjBlocks, "offset", offset, "height", height, "dt", dt)
		if offset < 0 {
			break
		} else if offset > 0 {
			ahead++
		}
		adjBlocks *= 10
	}

	if offset >= 0 && ahead > 0 {
		offset = 1
	}
	timestamp = uint64(nowInSeconds)
	if timestamp < parent.Number.Uint64() {
		timestamp = parent.Number.Uint64()
	}
	switch offset {
	case -1: // behind, i.e. too few blocks so far, need to make more
		tms := nowInMilliSeconds
		if blockInterval <= 1 {
			tms += params.BlockMinBuildTime
		} else {
			tms += (blockInterval-1)*1000 + params.BlockMinBuildTime
		}
		if tms/1000 <= int64(parent.Time) {
			// make sure that no more than 2 blocks have the same timestamp
			tms = (nowInSeconds + 1) * 1000
		}
		till = time.Unix(tms/1e3, (tms%1e3)*1e6)
		log.Debug("time-it", "behind", timestamp, "duration", tms-nowInMilliSeconds)
	case 1: // ahead, i.e. too many blocks, need to slow down
		tms := nowInMilliSeconds + blockInterval*1000 + params.BlockMinBuildTime
		if blockInterval > 1 {
			tms += 500
		}
		if blockInterval <= 1 && tms/1000 > nowInSeconds+blockInterval {
			// make sure time stamp doesn't jump by blockInterval + 1
			tms = (nowInSeconds+blockInterval+1)*1000 - params.BlockTrailTime
		}
		till = time.Unix(tms/1e3, (tms%1e3)*1e6)
		log.Debug("time-it", "ahead", timestamp, "duration", tms-nowInMilliSeconds)
	default: // on schedule
		tms := nowInMilliSeconds + blockInterval*1000 - params.BlockTrailTime
		if tms/1000 > nowInSeconds+blockInterval {
			// make sure time stamp doesn't jump by blockInterval + 1
			tms = (nowInSeconds+blockInterval+1)*1000 - params.BlockTrailTime
		}
		till = time.Unix(tms/1e3, (tms%1e3)*1e6)
		log.Debug("time-it", "on-schedule", timestamp, "duration", tms-nowInMilliSeconds)
	}
	return timestamp, till
}

// commitWork generates several new sealing tasks based on the parent block
// and submit them to the sealer.
func (w *worker) commitWork(interrupt *atomic.Int32, timestamp int64) {
	if atomic.CompareAndSwapInt32(&w.busyMining, 0, 1) {
		defer atomic.StoreInt32(&w.busyMining, 0)
	} else {
		return
	}
	if !metaminer.IsPoW() {
		parent := w.chain.CurrentBlock()
		height := new(big.Int).Add(parent.Number, common.Big1)
		if !w.chain.Config().IsBokbunja(height) {
			if !metaminer.IsMiner() {
				w.refreshPending(true)
				return
			}
		} else {
			ok, err := metaminer.AcquireMiningToken(height, parent.Hash())
			if ok {
				log.Debug("Mining Token, successful", "height", height, "parent-hash", parent.Hash())
			} else {
				log.Debug("Mining Token, failure", "height", height, "parent-hash", parent.Hash(), "error", err)
			}
			if !ok {
				w.refreshPending(true)
				return
			}
		}
	}
	start := time.Now()
	timestamp = start.Unix()

	// Set the coinbase if the worker is running or it's required
	var coinbase common.Address
	if w.isRunning() {
		coinbase = w.etherbase()
		if coinbase == (common.Address{}) {
			log.Error("Refusing to mine without etherbase")
			return
		}
	}
	work, err := w.prepareWork(&generateParams{
		timestamp: uint64(timestamp),
		coinbase:  coinbase,
	})
	if err != nil {
		return
	}
	if !metaminer.IsPoW() { // Metadium
		if coinbase, err := metaminer.GetCoinbase(work.header.Number); err == nil {
			work.coinbase = coinbase
		}
		// Add TRS
		// Set the trsList and the node's trs subscription information to the work.
		if trsListMap, trsSubscribe, _ := metaminer.GetTRSListMap(big.NewInt(work.header.Number.Int64() - 1)); trsListMap != nil && err == nil {
			work.trsListMap = trsListMap
			work.trsSubscribe = trsSubscribe
		}
	}

	if !metaminer.IsPoW() { // Metadium
		if !w.commitTransactionsEx(work, interrupt, start) {
			w.commitEx(work, w.fullTaskHook, true, start)
		}
		return
	}

	// Fill pending transactions from the txpool into the block.
	err = w.fillTransactions(interrupt, work)
	switch {
	case err == nil:
		// The entire block is filled, decrease resubmit interval in case
		// of current interval is larger than the user-specified one.
		w.adjustResubmitInterval(&intervalAdjust{inc: false})

	case errors.Is(err, errBlockInterruptedByRecommit):
		// Notify resubmit loop to increase resubmitting interval if the
		// interruption is due to frequent commits.
		gaslimit := work.header.GasLimit
		ratio := float64(gaslimit-work.gasPool.Gas()) / float64(gaslimit)
		if ratio < 0.1 {
			ratio = 0.1
		}
		w.adjustResubmitInterval(&intervalAdjust{
			ratio: ratio,
			inc:   true,
		})

	case errors.Is(err, errBlockInterruptedByNewHead):
		// If the block building is interrupted by newhead event, discard it
		// totally. Committing the interrupted block introduces unnecessary
		// delay, and possibly causes miner to mine on the previous head,
		// which could result in higher uncle rate.
		work.discard()
		return
	}
	// Submit the generated block for consensus sealing.
	w.commit(work.copy(), w.fullTaskHook, true, start)

	// Swap out the old work with the new one, terminating any leftover
	// prefetcher processes in the mean time and starting a new one.
	if w.current != nil {
		w.current.discard()
	}
	w.current = work
}

// nolint: govet
// commit runs any post-transaction state modifications, assembles the final block
// and commits new work if consensus engine is running.
// Note the assumption is held that the mutation is allowed to the passed env, do
// the deep copy first.
func (w *worker) commit(env *environment, interval func(), update bool, start time.Time) error {
	if !metaminer.IsPoW() {
		if !w.chain.Config().IsBokbunja(env.header.Number) {
			if !metaminer.IsMiner() {
				return errors.New("Not Miner")
			}
		} else {
			if !metaminer.HasMiningToken() {
				return errors.New("Not Miner")
			}
		}
	}

	if w.isRunning() {
		if interval != nil {
			interval()
		}
		// Create a local environment copy, avoid the data race with snapshot state.
		// https://github.com/ethereum/go-ethereum/issues/24299
		env := env.copy()
		// EIP-4844: set BlobGasUsed header field before block assembly (must be explicit, even when 0).
		if w.chainConfig.IsCamellia(env.header.Number) {
			env.header.BlobGasUsed = new(big.Int).SetUint64(uint64(env.blobs) * params.BlobTxBlobGasPerBlob)
		}
		// Withdrawals are set to nil here, because this is only called in PoW.
		block, err := w.engine.FinalizeAndAssemble(w.chain, env.header, env.state, env.txs, nil, env.receipts, nil)
		if err != nil {
			return err
		}
		// If we're post merge, just ignore
		if !w.isTTDReached(block.Header()) {
			select {
			case w.taskCh <- &task{receipts: env.receipts, sidecars: env.sidecars, state: env.state, block: block, createdAt: time.Now()}:
				fees := totalFees(block, env.receipts)
				feesInEther := new(big.Float).Quo(new(big.Float).SetInt(fees), big.NewFloat(params.Ether))
				log.Info("Commit new sealing work", "number", block.Number(), "sealhash", w.engine.SealHash(block.Header()),
					"txs", env.tcount, "gas", block.GasUsed(), "fees", feesInEther,
					"elapsed", common.PrettyDuration(time.Since(start)))

			case <-w.exitCh:
				log.Info("Worker has exited")
			}
		}
	}
	if update {
		w.updateSnapshot(env)
	}
	return nil
}

// In Metadium, uncles are not welcome and difficulty is so low,
// there's no reason to run miners asynchronously.
func (w *worker) commitEx(env *environment, interval func(), update bool, start time.Time) error {
	if !metaminer.IsPoW() {
		if !w.chain.Config().IsBokbunja(env.header.Number) {
			if !metaminer.IsMiner() {
				return errors.New("Not Miner")
			}
		} else {
			if !metaminer.HasMiningToken() {
				return errors.New("Not Miner")
			}
		}
	}
	if w.isRunning() {
		// Create a local environment copy, avoid the data race with snapshot state.
		// https://github.com/ethereum/go-ethereum/issues/24299
		env := env.copy()
		// EIP-4844: set BlobGasUsed header field before block assembly (must be explicit, even when 0).
		if w.chainConfig.IsCamellia(env.header.Number) {
			env.header.BlobGasUsed = new(big.Int).SetUint64(uint64(env.blobs) * params.BlobTxBlobGasPerBlob)
		}
		block, err := w.engine.FinalizeAndAssemble(w.chain, env.header, env.state, env.txs, nil, env.receipts, nil)
		if err != nil {
			return err
		}
		// If we're post merge, just ignore
		if !w.isTTDReached(block.Header()) {
			createdAt := time.Now()
			// w.unconfirmed.Shift(block.NumberU64() - 1) // unconfirmed tracking removed

			log.Info("Commit new sealing work", "number", block.Number(), "sealhash", w.engine.SealHash(block.Header()),
				"txs", env.tcount,
				"gas", block.GasUsed(), "fees", totalFees(block, env.receipts),
				"elapsed", common.PrettyDuration(time.Since(start)))

			var sealedBlock *types.Block
			stopCh := make(chan struct{})
			resultCh := make(chan *types.Block, 1)
			if err := w.engine.Seal(w.chain, block, resultCh, stopCh); err != nil {
				log.Warn("Block sealing failed", "err", err)
			} else {
				sealedBlock = <-resultCh
			}
			close(stopCh)
			close(resultCh)

			if sealedBlock != nil && !w.chain.HasBlock(sealedBlock.Hash(), sealedBlock.NumberU64()) {
				var (
					sealhash = w.engine.SealHash(sealedBlock.Header())
					hash     = sealedBlock.Hash()
					receipts = make([]*types.Receipt, len(env.receipts))
					logs     []*types.Log
				)
				for i, taskReceipt := range env.receipts {
					receipt := new(types.Receipt)
					receipts[i] = receipt
					*receipt = *taskReceipt

					// add block location fields
					receipt.BlockHash = hash
					receipt.BlockNumber = block.Number()
					receipt.TransactionIndex = uint(i)

					// Update the block hash in all logs since it is now available and not when the
					// receipt/log of individual transactions were created.
					receipt.Logs = make([]*types.Log, len(taskReceipt.Logs))
					for i, taskLog := range taskReceipt.Logs {
						log := new(types.Log)
						receipt.Logs[i] = log
						*log = *taskLog
						log.BlockHash = hash
					}
					logs = append(logs, receipt.Logs...)
				}
				if !metaminer.IsPoW() {
					if !w.chain.Config().IsBokbunja(sealedBlock.Number()) {
						if !metaminer.IsMiner() {
							return errors.New("Not Miner")
						}
						go metaminer.LogBlock(sealedBlock.Number().Int64(), sealedBlock.Hash())
					} else {
						if err = metaminer.ReleaseMiningToken(sealedBlock.Number(), sealedBlock.Hash(), sealedBlock.ParentHash()); err != nil {
							return err
						}
					}
				}
				// Commit block and state to database.
				_, err := w.chain.WriteBlockAndSetHead(sealedBlock, receipts, logs, env.state, true)
				if err != nil {
					log.Error("Failed writing block to chain", "err", err)
					return err
				}
				// Persist blob sidecars for this block (Metadium: no beacon chain).
				if len(env.sidecars) > 0 {
					rawdb.WriteBlobSidecars(w.chain.ChainDb(), hash, sealedBlock.NumberU64(), env.sidecars)
				}
				log.Info("Successfully sealed new block", "number", sealedBlock.Number(), "sealhash", sealhash, "hash", hash,
					"elapsed", common.PrettyDuration(time.Since(createdAt)))

				// Broadcast the block and announce chain insertion event
				w.mux.Post(core.NewMinedBlockEvent{Block: sealedBlock})

				// Insert the block into the set of pending ones to resultLoop for confirmations
				// w.unconfirmed.Insert(sealedBlock.NumberU64(), sealedBlock.Hash()) // unconfirmed tracking removed
			}
		}
	}
	if update {
		w.updateSnapshot(env)
	}
	return nil
}

// getSealingBlock generates the sealing block based on the given parameters.
// The generation result will be passed back via the given channel no matter
// the generation itself succeeds or not.
func (w *worker) getSealingBlock(params *generateParams) *newPayloadResult {
	req := &getWorkReq{
		params: params,
		result: make(chan *newPayloadResult, 1),
	}
	select {
	case w.getWorkCh <- req:
		return <-req.result
	case <-w.exitCh:
		return &newPayloadResult{err: errors.New("miner closed")}
	}
}

// isTTDReached returns the indicator if the given block has reached the total
// terminal difficulty for The Merge transition.
func (w *worker) isTTDReached(header *types.Header) bool {
	td, ttd := w.chain.GetTd(header.ParentHash, header.Number.Uint64()-1), w.chain.Config().TerminalTotalDifficulty
	return td != nil && ttd != nil && td.Cmp(ttd) >= 0
}

// adjustResubmitInterval adjusts the resubmit interval.
func (w *worker) adjustResubmitInterval(message *intervalAdjust) {
	select {
	case w.resubmitAdjustCh <- message:
	default:
		log.Warn("the resubmitAdjustCh is full, discard the message")
	}
}

// copyReceipts makes a deep copy of the given receipts.
func copyReceipts(receipts []*types.Receipt) []*types.Receipt {
	result := make([]*types.Receipt, len(receipts))
	for i, l := range receipts {
		cpy := *l
		result[i] = &cpy
	}
	return result
}

// totalFees computes total consumed miner fees in Wei. Block transactions and receipts have to have the same order.
func totalFees(block *types.Block, receipts []*types.Receipt) *big.Int {
	feesWei := new(big.Int)
	for i, tx := range block.Transactions() {
		minerFee, _ := tx.EffectiveGasTip(block.BaseFee())
		feesWei.Add(feesWei, new(big.Int).Mul(new(big.Int).SetUint64(receipts[i].GasUsed), minerFee))
	}
	return feesWei
}

// signalToErr converts the interruption signal to a concrete error type for return.
// The given signal must be a valid interruption signal.
func signalToErr(signal int32) error {
	switch signal {
	case commitInterruptNewHead:
		return errBlockInterruptedByNewHead
	case commitInterruptResubmit:
		return errBlockInterruptedByRecommit
	case commitInterruptTimeout:
		return errBlockInterruptedByTimeout
	default:
		panic(fmt.Errorf("undefined signal %d", signal))
	}
}
