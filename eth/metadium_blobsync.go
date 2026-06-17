// Copyright 2024 The go-ethereum Authors
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

package eth

// meta/69 blob-sidecar fetching (M5).
//
// A node that learns a block via propagation but never mempool-fetched its blob
// txs ends up with permanently missing sidecar data availability
// (core/blockchain.go: BlobSidecarFn returns nil → not stored). This file backs
// the data-availability gap: when block import reports missing sidecars
// (MissingBlobSidecarFn), it fetches them from a meta/69 peer, validates them
// against the block's blob txs, and persists them. It is the fallback for the
// primary in-pool path; it engages only with meta/69 peers and only for blocks
// that actually carry blob txs (i.e. post-Camellia).

import (
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

const (
	// blobSidecarFetchTimeout bounds how long a single GetBlobSidecars request
	// waits for its reply before trying another peer.
	blobSidecarFetchTimeout = 10 * time.Second

	// blobSidecarMaxQueue bounds the import-time fetch queue. Overflow is dropped
	// (logged) rather than blocking the block-import path.
	blobSidecarMaxQueue = 256

	// blobSidecarMaxAttempts bounds the number of distinct peers tried per block.
	blobSidecarMaxAttempts = 4
)

var (
	errBlobFetchTimeout = errors.New("blob sidecar request timed out")
	errBlobFetchQuit    = errors.New("shutting down")
)

// blobSidecarRequest is an in-flight GetBlobSidecars request awaiting its reply.
type blobSidecarRequest struct {
	deliver chan []*types.BlobTxSidecar
}

// enqueueBlobSidecarFetch is the core.BlockChain.MissingBlobSidecarFn callback,
// invoked from the block-import path. It must not block, so it enqueues without
// waiting and drops on a full queue (the data can be re-fetched later).
func (h *handler) enqueueBlobSidecarFetch(block *types.Block) {
	select {
	case h.blobFetchCh <- block:
	default:
		log.Warn("Blob sidecar fetch queue full, dropping", "number", block.NumberU64(), "hash", block.Hash())
	}
}

// blobSidecarLoop services queued blob-sidecar fetches serially. Blob blocks are
// rare on Metadium (<=2 blobs/block) so serial fetching keeps the resource
// footprint bounded; per-block stalls are capped by blobSidecarFetchTimeout.
func (h *handler) blobSidecarLoop() {
	defer h.wg.Done()
	for {
		select {
		case <-h.quitSync:
			return
		case block := <-h.blobFetchCh:
			h.fetchBlobSidecars(block)
		}
	}
}

// fetchBlobSidecars obtains and persists the blob sidecars for a block from a
// meta/69 peer, validating the reply and dropping peers that return bad data.
func (h *handler) fetchBlobSidecars(block *types.Block) {
	// Collect the block's blob txs in order; the reply must line up with them.
	var blobTxs []*types.Transaction
	for _, tx := range block.Transactions() {
		if tx.Type() == types.BlobTxType {
			blobTxs = append(blobTxs, tx)
		}
	}
	if len(blobTxs) == 0 {
		return
	}
	// Skip if a complete set was persisted in the meantime (e.g. via the pool).
	if existing := h.chain.GetBlobSidecars(block.Hash()); len(existing) >= len(blobTxs) {
		return
	}
	tried := make(map[string]struct{})
	for attempt := 0; attempt < blobSidecarMaxAttempts; attempt++ {
		select {
		case <-h.quitSync:
			return
		default:
		}
		peer := h.randomBlobSidecarPeer(tried)
		if peer == nil {
			log.Debug("No meta/69 peer to fetch blob sidecars from", "number", block.NumberU64(), "hash", block.Hash())
			return
		}
		tried[peer.ID()] = struct{}{}

		sidecars, err := h.requestBlobSidecars(peer, block.Hash())
		if err != nil {
			if errors.Is(err, errBlobFetchQuit) {
				return
			}
			peer.Log().Debug("Blob sidecar request failed", "number", block.NumberU64(), "err", err)
			continue
		}
		if err := validateBlobSidecarsForBlock(blobTxs, sidecars); err != nil {
			// Structurally or cryptographically invalid reply: drop the peer (#31219).
			peer.Log().Warn("Dropping peer for invalid blob sidecars", "number", block.NumberU64(), "err", err)
			h.removePeer(peer.ID())
			continue
		}
		rawdb.WriteBlobSidecars(h.database, block.Hash(), block.NumberU64(), sidecars)
		log.Debug("Fetched missing blob sidecars", "number", block.NumberU64(), "hash", block.Hash(), "blobs", len(sidecars))
		return
	}
	log.Debug("Gave up fetching blob sidecars", "number", block.NumberU64(), "hash", block.Hash())
}

// requestBlobSidecars sends a single-block GetBlobSidecars request and waits for
// the correlated reply, bounded by blobSidecarFetchTimeout.
func (h *handler) requestBlobSidecars(peer *ethPeer, hash common.Hash) ([]*types.BlobTxSidecar, error) {
	id := h.blobReqIDGen.Add(1)
	req := &blobSidecarRequest{deliver: make(chan []*types.BlobTxSidecar, 1)}

	h.blobReqLock.Lock()
	h.blobPending[id] = req
	h.blobReqLock.Unlock()
	defer func() {
		h.blobReqLock.Lock()
		delete(h.blobPending, id)
		h.blobReqLock.Unlock()
	}()

	if err := peer.RequestBlobSidecars(id, []common.Hash{hash}); err != nil {
		return nil, err
	}
	timer := time.NewTimer(blobSidecarFetchTimeout)
	defer timer.Stop()
	select {
	case sidecars := <-req.deliver:
		return sidecars, nil
	case <-timer.C:
		return nil, errBlobFetchTimeout
	case <-h.quitSync:
		return nil, errBlobFetchQuit
	}
}

// deliverBlobSidecars correlates a BlobSidecars reply with its pending request.
// It is called from the peer message-handling path (ethHandler.Handle). Each
// request asks for a single block hash, so only the first sidecar slice is used.
func (h *handler) deliverBlobSidecars(id uint64, sidecars [][]*types.BlobTxSidecar) {
	h.blobReqLock.Lock()
	req := h.blobPending[id]
	h.blobReqLock.Unlock()
	if req == nil {
		return
	}
	var sc []*types.BlobTxSidecar
	if len(sidecars) > 0 {
		sc = sidecars[0]
	}
	select {
	case req.deliver <- sc:
	default:
	}
}

// randomBlobSidecarPeer returns a connected meta/69 peer not already tried for
// this block, or nil if none are available.
func (h *handler) randomBlobSidecarPeer(tried map[string]struct{}) *ethPeer {
	for _, p := range h.peers.peerList() {
		if p.Version() < eth.ETH69 {
			continue
		}
		if _, ok := tried[p.ID()]; ok {
			continue
		}
		return p
	}
	return nil
}

// validateBlobSidecarsForBlock checks a received sidecar set against the block's
// ordered blob txs: the counts must match, each sidecar's versioned hashes must
// equal the corresponding tx's, and each sidecar must pass KZG/commitment
// verification. The block's blob txs were already consensus-validated, so the
// per-block blob count is inherently bounded (#32246) by what the block carries.
func validateBlobSidecarsForBlock(blobTxs []*types.Transaction, sidecars []*types.BlobTxSidecar) error {
	if len(sidecars) != len(blobTxs) {
		return errors.New("blob sidecar count mismatch")
	}
	for i, tx := range blobTxs {
		sc := sidecars[i]
		if sc == nil {
			return errors.New("nil blob sidecar")
		}
		hashes := tx.BlobHashes()
		if len(sc.BlobHashes) != len(hashes) {
			return errors.New("blob sidecar versioned-hash count mismatch")
		}
		for j, want := range hashes {
			if sc.BlobHashes[j] != want {
				return errors.New("blob sidecar versioned-hash mismatch")
			}
		}
		if err := txpool.ValidateBlobSidecar(hashes, sc); err != nil {
			return err
		}
	}
	return nil
}
