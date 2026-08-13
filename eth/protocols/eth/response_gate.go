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

package eth

import "github.com/ethereum/go-ethereum/rlp"

// This file keeps a response from being decoded before we know we asked for it.
//
// Every response message on this protocol is `[request-id, payload]`. The
// handlers used to decode the whole thing and only then hand it to the
// dispatcher, which is where the request id is matched against the in-flight
// requests. A peer that never received a request from us could therefore make
// us expand an arbitrary payload — bounded in wire size by maxMessageSize, but
// not in the size of the Go values it decodes into, which is the amplification
// that makes it worth anything to an attacker.
//
// The gate below is the same property upstream go-ethereum adopted in
// "eth/protocols/eth, eth/protocols/snap: delayed p2p message decoding"
// (0cba803fb, v1.17.0, CVE-2026-26313): confirm the response belongs to an
// active request first, decode second. Upstream's version reaches further —
// it also defers decoding until the response is checked against the request's
// limits, and it restructured p2p/tracker into a per-peer instance to do it.
// That rework sits on the eth/69-72 protocol layout and does not transplant
// onto this tree; what is here is the part that closes the unsolicited-message
// path, applied to the dispatcher this fork actually runs.
//
// The gate is deliberately keyed on the request id alone. A response whose id
// is live but whose message code is wrong keeps its existing treatment (the
// dispatcher reports errMismatchingResponseType and the peer is dropped), so
// this change only ever converts work that used to happen into work that is
// skipped.

// maxPendingIDs bounds the gate's index. Not every request path can untrack its
// id: the dispatcher-managed ones (headers, bodies, receipts) are removed
// exactly, but pooled transactions and blob sidecars are sent straight to the
// wire and are only untracked when an answer arrives, so an unanswered request
// would otherwise sit in the index for the life of the connection.
//
// The bound is a ring: inserting past capacity evicts the oldest id. Real
// concurrency per peer is a handful of requests (the dispatcher's in-flight
// set, one pooled-transaction retrieval, one serial blob fetch), so eviction
// only ever reaches ids that have long since gone unanswered. Evicting a live
// id would drop a legitimate response, which is why the capacity is far above
// what the request paths can produce.
const maxPendingIDs = 256

// responseEnvelope decodes the request id of a response message while leaving
// the payload as raw RLP. The payload is decoded separately, and only once the
// gate has passed.
type responseEnvelope struct {
	RequestId uint64
	Payload   rlp.RawValue
}

// trackPending records that a request id was sent to this peer.
//
// Registration happens before the request is written to the wire, so a peer
// that answers immediately cannot have its response gated out by an index that
// has not caught up yet. The index may therefore hold an id whose send
// subsequently failed; that is the harmless direction, and untrackPending
// cleans it up.
func (p *Peer) trackPending(id uint64) {
	p.pendingLock.Lock()
	defer p.pendingLock.Unlock()

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
// processing it. Dropping such a response silently is the pre-existing
// behaviour: the dispatcher answered an untracked id with errDanglingResponse,
// which dispatchResponse turned into a nil return. The tracker is still told,
// so the dangling-response metrics keep counting what they used to count.
func (p *Peer) dropUnsolicited(code, id uint64) {
	requestTracker.Fulfil(p.id, p.version, code, id)
}
