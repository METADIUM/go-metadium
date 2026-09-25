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
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// bftProducer is what the PBFT engine (consensus/metabft.Engine) offers the
// miner (docs/pbft-consensus-design.md §7.3).
type bftProducer interface {
	ProposalWanted(height uint64, pendingTxs bool) bool
	ProposalWake() <-chan struct{}
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
