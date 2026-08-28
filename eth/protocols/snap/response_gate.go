// Copyright 2026 The go-metadium Authors
// This file is part of the go-metadium library.
//
// The go-metadium library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-metadium library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-metadium library. If not, see <http://www.gnu.org/licenses/>.

package snap

import (
	"bytes"
	"fmt"
	"io"

	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
)

// This file keeps a snap response from being decoded before we know we asked
// for it. It is the counterpart of eth/protocols/eth/response_gate.go, and the
// reason it exists separately is that this protocol reaches the same hazard by
// a different route.
//
// Nothing on this network snap-syncs — the fork refuses --syncmode snap — but
// the response handlers are reachable all the same, because what exposes them
// is capability negotiation rather than our sync mode: Ethereum.Protocols
// appends snap/1 whenever SnapshotCache is above zero, and that defaults to
// 102. So a node running --syncmode full advertises snap, and its handlers used
// to decode a response in full before AccountRangePacket.ID was compared
// against anything.
//
// Never snap-syncing is in fact what makes this worse rather than better: with
// no request outstanding, the id can match nothing, and the syncer's handling
// of an id it does not know is to log and return nil — the peer is not dropped,
// so one connection can repeat the same message indefinitely. Bytecodes and
// trie nodes are [][]byte with no ordering constraint to satisfy on the way in,
// which makes them the cheapest shape: the wire size is capped by
// maxMessageSize, the size of the Go values it expands into is not.
//
// The gate reads the leading request id, checks it against the ids this peer
// was actually sent, and decodes the payload only then.
//
// Why not the envelope struct the eth gate uses: there, every response is
// `[request-id, payload]` — two elements, so leaving the second as
// rlp.RawValue is enough. Snap's responses are flat lists whose first element
// happens to be the id (`[id, accounts, proof]`), so there is no payload
// element to hold back. peekRequestID therefore reads the id off a stream over
// the already-buffered message and stops, and the full decode re-reads those
// same bytes once the gate has passed. Buffering the message costs its wire
// size, which the maxMessageSize check above the handler has already bounded;
// it is the expansion that the gate is there to prevent.

// maxPendingIDs bounds the index, as a ring: inserting past capacity evicts the
// oldest id.
//
// Snap has no dispatcher, so a request is only untracked when its answer
// arrives (or when the send fails). An unanswered request would otherwise sit
// in the index for the life of the connection. The syncer's real concurrency is
// a handful of requests per peer, so eviction only ever reaches ids that have
// long gone unanswered — and on this network the index stays empty, since
// nothing sends snap requests at all.
const maxPendingIDs = 256

// peekRequestID reads the leading request id of a response message without
// decoding the rest of it. Every response this protocol defines is a list whose
// first element is the id.
func peekRequestID(raw []byte) (uint64, error) {
	s := rlp.NewStream(bytes.NewReader(raw), uint64(len(raw)))
	if _, err := s.List(); err != nil {
		return 0, err
	}
	return s.Uint64()
}

// gateResponse buffers a response message, checks its request id against the
// ids this peer was sent, and decodes it into res only if it matches.
//
// It reports whether the caller should stop: (true, nil) means the response was
// unsolicited and has been dropped without being decoded, which is what the
// syncer used to do with it after decoding. A non-nil error is a decode failure
// and keeps its previous meaning — the peer is dropped.
func gateResponse(peer *Peer, msg p2p.Msg, code uint64, res any) (bool, error) {
	// Buffering costs the wire size, which handleMessage has already bounded by
	// maxMessageSize. It is the decode that would cost more than that.
	raw, err := io.ReadAll(msg.Payload)
	if err != nil {
		return false, fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	id, err := peekRequestID(raw)
	if err != nil {
		return false, fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	if !peer.solicited(id) {
		peer.dropUnsolicited(code, id)
		return true, nil
	}
	// The answer has arrived; the id is spent whether or not the payload turns
	// out to be well formed.
	peer.untrackPending(id)

	if err := rlp.DecodeBytes(raw, res); err != nil {
		return false, fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	return false, nil
}

// trackPending records that a request id was sent to this peer.
//
// Registration happens before the request reaches the wire, so a peer that
// answers immediately cannot have its response gated out by an index that has
// not caught up. The index may therefore briefly hold an id whose send then
// failed; that is the harmless direction, and the callers untrack it.
func (p *Peer) trackPending(id uint64) {
	p.pendingLock.Lock()
	defer p.pendingLock.Unlock()

	if p.pendingIDs == nil {
		p.pendingIDs = make(map[uint64]struct{})
		p.pendingRing = make([]uint64, maxPendingIDs)
	}
	if _, ok := p.pendingIDs[id]; ok {
		return
	}
	// Reclaim the slot this insert is about to take. The id found there may
	// already have been untracked, in which case the delete is a no-op.
	if victim := p.pendingRing[p.pendingPos]; victim != 0 {
		delete(p.pendingIDs, victim)
	}
	p.pendingRing[p.pendingPos] = id
	p.pendingPos = (p.pendingPos + 1) % len(p.pendingRing)
	p.pendingIDs[id] = struct{}{}
}

// untrackPending forgets an in-flight request id.
func (p *Peer) untrackPending(id uint64) {
	p.pendingLock.Lock()
	defer p.pendingLock.Unlock()
	delete(p.pendingIDs, id)
}

// solicited reports whether the given request id belongs to a request we sent
// to this peer and have not yet accounted for.
func (p *Peer) solicited(id uint64) bool {
	p.pendingLock.RLock()
	defer p.pendingLock.RUnlock()
	_, ok := p.pendingIDs[id]
	return ok
}

// dropUnsolicited reports the response as dangling and tells the caller to stop
// processing it.
//
// Dropping it without disconnecting is the pre-existing behaviour: such a
// response used to be decoded, handed to the syncer, and discarded there with a
// warning and a nil return. The tracker is still told, so the metrics keep
// counting what they counted before.
func (p *Peer) dropUnsolicited(code, id uint64) {
	requestTracker.Fulfil(p.id, p.version, code, id)
}
