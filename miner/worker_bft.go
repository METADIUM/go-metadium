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
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// bftProducer is what the PBFT engine (consensus/metabft.Engine) offers the
// miner (docs/pbft-consensus-design.md §7.3).
type bftProducer interface {
	ProposalWanted(height uint64, pendingTxs bool) bool
	ProposalWake() <-chan struct{}
	WakeProposer()
}

// errBftFloorBreach aborts a PBFT build: a transaction left governance with
// fewer nodes than PBFT needs (design §9.3.1).
var errBftFloorBreach = errors.New("transaction breaks the PBFT validator floor")

// bftExcludeHeights is how long a transaction that broke the validator
// floor is left out before it is tried again; governance may have changed
// by then.
const bftExcludeHeights = 64

// bftExclusions are the transactions left out of PBFT proposals, with the
// height from which they are tried again.
type bftExclusions struct {
	mu    sync.Mutex
	until map[common.Hash]uint64
}

func (x *bftExclusions) add(hash common.Hash, until uint64) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.until == nil {
		x.until = make(map[common.Hash]uint64)
	}
	x.until[hash] = until
}

func (x *bftExclusions) has(hash common.Hash, height uint64) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	until, ok := x.until[hash]
	if ok && height >= until {
		delete(x.until, hash)
		return false
	}
	return ok
}

// bftExcluded reports whether a PBFT build leaves tx out.
func (w *worker) bftExcluded(env *environment, hash common.Hash) bool {
	return w.chainConfig.IsBft(env.header.Number) && w.bftExcl.has(hash, env.header.Number.Uint64())
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
	err := pv.VerifyPostState(w.chain, env.header, env.state)
	if err == nil {
		return nil
	}
	log.Warn("Leaving a transaction out of PBFT proposals: it breaks the validator floor", "hash", tx.Hash(),
		"number", env.header.Number, "retry", env.header.Number.Uint64()+bftExcludeHeights, "err", err)
	w.bftExcl.add(tx.Hash(), env.header.Number.Uint64()+bftExcludeHeights)
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
	return p.ProposalWanted(height.Uint64(), pending > 0)
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
	window := time.Duration(blockIntervalMs) * time.Millisecond
	if window <= 0 {
		window = 2 * time.Second
	}
	if limit := time.Duration(timeDrift) * time.Second / 2; window > limit {
		window = limit
	}
	return timestamp, now.Add(window)
}

// proposeBft hands a built block to the consensus node through the engine.
// The block is written by the node once it is decided, whoever proposed it;
// this node's blob sidecars are stored now, under the block hash, which the
// decision does not change (commit seals are outside it).
func (w *worker) proposeBft(block *types.Block, env *environment, start time.Time) {
	if err := w.engine.Seal(w.chain, block, nil, nil); err != nil {
		log.Warn("PBFT proposal refused", "number", block.Number(), "err", err)
		return
	}
	if len(env.sidecars) > 0 {
		rawdb.WriteBlobSidecars(w.chain.ChainDb(), block.Hash(), block.NumberU64(), env.sidecars)
	}
	log.Info("Proposed block", "number", block.Number(), "hash", block.Hash(), "txs", env.tcount,
		"gas", block.GasUsed(), "elapsed", common.PrettyDuration(time.Since(start)))
}
