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

package miner

import (
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
)

// bftProducer is what the PBFT engine (consensus/metabft.Engine) offers the
// miner (docs/pbft-consensus-design.md §7.3).
type bftProducer interface {
	ProposalWanted(height uint64, pendingTxs bool, minGap time.Duration) bool
	ProposalWake() <-chan struct{}
	WakeProposer()
}

// errBftFloorBreach aborts a PBFT build: a transaction left governance with
// fewer nodes than PBFT needs (design §9.3.1).
var errBftFloorBreach = errors.New("transaction breaks the PBFT validator floor")

// bftRetryDelay is how soon a PBFT work request that found a build running
// is tried again.
const bftRetryDelay = 20 * time.Millisecond

// bftFloorCheckTimer times the validator-floor check after each transaction
// of a PBFT build (§11.3 M-09), collected with --metrics.
var bftFloorCheckTimer = metrics.NewRegisteredTimer("miner/bft/floorcheck", nil)

// bftExcluder is the PBFT engine's record of transactions left out by the
// validator floor (consensus/metabft.Engine.ExcludeTx).
type bftExcluder interface {
	ExcludeTx(hash common.Hash, sender common.Address, nonce uint64, reason string) time.Time
	IsExcluded(hash common.Hash) bool
}

// bftExcluded reports whether a PBFT build leaves tx out.
func (w *worker) bftExcluded(env *environment, hash common.Hash) bool {
	x, ok := w.engine.(bftExcluder)
	return ok && w.chainConfig.IsBft(env.header.Number) && x.IsExcluded(hash)
}

// bftCheckFloor runs the engine's post-state rule after a transaction at a
// PBFT height. A transaction that breaks the validator floor would make the
// block invalid; applyTransaction has already finalised its changes, which
// cannot be reverted, so the build is abandoned instead: the transaction is
// excluded for a while and the worker is woken to build again without it.
// That is rare (a governance removal), so its cost does not matter; a copy
// of the state before every transaction would be paid on every block.
func (w *worker) bftCheckFloor(env *environment, tx *types.Transaction) error {
	if !w.chainConfig.IsBft(env.header.Number) {
		return nil
	}
	pv, ok := w.engine.(consensus.PostStateVerifier)
	if !ok {
		return nil
	}
	start := time.Now()
	err := pv.VerifyPostState(w.chain, env.header, env.state)
	bftFloorCheckTimer.UpdateSince(start)
	if err == nil {
		return nil
	}
	from, _ := types.Sender(env.signer, tx)
	if x, ok := w.engine.(bftExcluder); ok {
		until := x.ExcludeTx(tx.Hash(), from, tx.Nonce(), err.Error())
		// The sender's later transactions wait behind this one (nonce
		// order) until the retry; replacing its nonce frees them at once.
		log.Warn("Leaving a transaction out of PBFT proposals: it breaks the validator floor", "hash", tx.Hash(),
			"from", from, "nonce", tx.Nonce(), "number", env.header.Number, "retry", until.Format(time.RFC3339),
			"err", err, "note", "later transactions from this sender wait; replace this nonce to free them")
	}
	if p, ok := w.engine.(bftProducer); ok {
		p.WakeProposer()
	}
	return errBftFloorBreach
}

// bftProposalWanted reports whether this node should build a block for
// height now: it proposes the current round, and either has transactions
// or the empty-block interval has passed.
func (w *worker) bftProposalWanted(height *big.Int) bool {
	p, ok := w.engine.(bftProducer)
	if !ok {
		return false
	}
	pending, _ := w.eth.TxPool().Stats()
	return p.ProposalWanted(height.Uint64(), pending > 0, w.bftMinGap(height))
}

// bftMinGap is how long after the parent a round-0 build with transactions
// may start: blockCreationTime less the collection window (bftTimestamp),
// so the proposal goes out one blockCreationTime after the parent, as on
// PoA (§11.3 M-04). With idleseal on, blocks are sealed as the pool goes
// quiet instead, and nothing is held.
func (w *worker) bftMinGap(height *big.Int) time.Duration {
	if params.BlockIdleSealTime > 0 || w.chainConfig.Bft == nil {
		return 0
	}
	interval, _, _, _, _, err := metaminer.GetBlockBuildParameters(new(big.Int).Sub(height, common.Big1))
	if err != nil || interval <= 0 {
		return 0
	}
	window := bftWindow(interval, w.chainConfig.Bft.TimeDrift)
	return time.Duration(interval)*time.Millisecond - window
}

// bftEmptyDue reports whether the node wants a block for height even
// without transactions: the empty-block interval has passed, or a later
// round needs a proposal.
//
// A PBFT build that comes out empty neither waits for the rest of its
// collection window nor proposes, unless an empty block is due. Holding the
// window open cost an idle chain the window on every empty block (6.1 s
// intervals at a 5 s emptyBlockInterval), and a build started for a
// transaction the pool had not yet evicted after its block delayed the next
// transaction by up to the window. PoA keeps its behaviour: there a
// withheld round would burn the mining token; PBFT has none, and the height
// keeps asking until it is proposed.
func (w *worker) bftEmptyDue(height *big.Int) bool {
	p, ok := w.engine.(bftProducer)
	return ok && p.ProposalWanted(height.Uint64(), false, 0)
}

// bftProposalWake is the engine's wake-up channel on a PBFT chain, nil
// otherwise.
func (w *worker) bftProposalWake() <-chan struct{} {
	if p, ok := w.engine.(bftProducer); ok && w.chainConfig.BftBlock != nil {
		return p.ProposalWake()
	}
	return nil
}

// bftTimestamp returns a PBFT block's timestamp, max(parent.Time, now)
// (design §7.3), and how long the proposer may collect transactions. The
// time is fixed before the transactions run, since they read it, and
// validators check it against their clocks on arrival (timeDrift, §4.5):
// so the window is capped at half the drift, which keeps a block built at
// the end of a second within the bound when it arrives.
func bftTimestamp(parent *types.Header, blockIntervalMs int64, timeDrift uint64, now time.Time) (uint64, time.Time) {
	timestamp := uint64(now.Unix())
	if timestamp < parent.Time {
		timestamp = parent.Time
	}
	return timestamp, now.Add(bftWindow(blockIntervalMs, timeDrift))
}

// bftWindow is how long a PBFT build collects transactions: the block
// interval, capped at half the proposal time bound.
func bftWindow(blockIntervalMs int64, timeDrift uint64) time.Duration {
	window := time.Duration(blockIntervalMs) * time.Millisecond
	if window <= 0 {
		window = 2 * time.Second
	}
	if limit := time.Duration(timeDrift) * time.Second / 2; window > limit {
		window = limit
	}
	return window
}

// proposeBft hands a built block to the consensus node through the engine.
// The block is written by the node once it is decided, whoever proposed it;
// this node's blob sidecars are stored now, under the block hash, which the
// decision does not change (commit seals are outside it).
//
// The seal gives the block its final hash (the PoA nonce is random), so the
// sidecars are recorded under the sealed block's hash, and before it is
// submitted: validators ask for them as soon as the proposal reaches them.
// Recorded under the unsealed hash, no validator missing them in its pool
// could ever get them from the proposer, and a height that needs such a
// validator's vote stalled (§11.3 M-10).
func (w *worker) proposeBft(block *types.Block, env *environment, start time.Time) {
	sealer, ok := w.engine.(bftSealer)
	if !ok {
		log.Error("PBFT proposal without a PBFT engine", "number", block.Number())
		return
	}
	block = sealer.SealProposal(block)
	if len(env.sidecars) > 0 {
		// Written for the block once decided (the hash is final), and kept
		// in memory for the validators who fetch them before voting.
		rawdb.WriteBlobSidecars(w.chain.ChainDb(), block.Hash(), block.NumberU64(), env.sidecars)
		w.chain.AddProposalSidecars(block.Hash(), env.sidecars)
	}
	if err := sealer.SubmitProposal(block); err != nil {
		log.Warn("PBFT proposal refused", "number", block.Number(), "err", err)
		return
	}
	log.Info("Proposed block", "number", block.Number(), "hash", block.Hash(), "txs", env.tcount,
		"gas", block.GasUsed(), "elapsed", common.PrettyDuration(time.Since(start)))
}

// bftSealer is how the worker hands a proposal to the PBFT engine
// (consensus/metabft.Engine): seal first, then submit.
type bftSealer interface {
	SealProposal(block *types.Block) *types.Block
	SubmitProposal(block *types.Block) error
}
