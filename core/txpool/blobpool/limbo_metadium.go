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
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
)

// limboFinalityDepth is the eviction boundary the blobpool substitutes for
// chain-level finality when the latter is unavailable (see limboFinal).
//
// The value is anchored to the reorg handler's own cutoff in (*BlobPool).reorg
// (blobpool.go, the "64 blocks deep" bail-out): reorgs deeper than that are
// never resurrected there, so evicting limbo sidecars at head-64 cannot
// discard a sidecar the reorg path would still ask for. Do not lower one
// without the other. At Metadium's 2s block interval this is ~128s of depth.
// Should a deeper reorg occur regardless, the failure is bounded and
// non-consensus: reinject logs "Blobs unavailable, dropping reorged tx" and
// the sender resubmits.
const limboFinalityDepth = 64

// limboFinal returns the header below which included blob sidecars may be
// evicted from limbo. Upstream keys this on beacon finality. Metadium has a
// chain-level PoA substitute -- metaFinalHeader (head - govNodeCount/2+1),
// wired into CurrentFinalBlock by b3a5d9f00 -- but it resolves through
// governance contract reads, which fail in real windows: during start-up
// before the metadium admin is initialized, when the governance read errors,
// and permanently on private nets with no governance deployed (#76). In each
// such window Reset would log "Nil finalized block cannot evict old blobs"
// and skip limbo maintenance. Under PoA, substitute head-limboFinalityDepth
// for exactly those windows; a resolved chain-level boundary always passes
// through untouched, and every non-PoA consensus method keeps the upstream
// behavior bit-for-bit, nil included.
func limboFinal(final, head *types.Header) *types.Header {
	if final != nil || metaminer.IsPoW() || head == nil {
		return final
	}
	depth := uint64(limboFinalityDepth)
	if n := head.Number.Uint64(); n >= depth {
		return &types.Header{Number: new(big.Int).SetUint64(n - depth)}
	}
	// Fewer than limboFinalityDepth blocks exist; nothing can be deep enough
	// to evict. Block zero carries no blob txs, so this is a clean no-op that
	// still avoids the nil-finality error path.
	return &types.Header{Number: new(big.Int)}
}
