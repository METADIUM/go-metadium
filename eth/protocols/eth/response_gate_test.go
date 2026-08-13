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

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
)

// The tests below distinguish "gated out" from "decoded" by sending a payload
// that is well-formed RLP but cannot decode into the response type. A handler
// that decodes before checking the request id reports errDecode; one that
// gates first returns nil without ever looking at the payload. That makes the
// undecodable payload the observable, so these tests fail if the gate is
// removed rather than merely covering it.
//
// unpaired is such a payload: a list where the response type expects a list of
// structs, and whose single element is a string.
func unpairedPayload(t *testing.T) rlp.RawValue {
	t.Helper()
	enc, err := rlp.EncodeToBytes([]string{"not a header"})
	if err != nil {
		t.Fatalf("encoding the test payload failed: %v", err)
	}
	return enc
}

// acceptTxsBackend is testBackend with transaction processing turned on.
// testBackend.AcceptTxs panics on purpose ("data processing tests should be done
// in the handler package"), and handlePooledTransactions consults it before
// reaching the gate, so the tests below need this much of a backend.
type acceptTxsBackend struct{ *testBackend }

func (b acceptTxsBackend) AcceptTxs() bool { return true }

// deliver writes one message into the peer's pipe and returns what the handler
// made of it.
func deliver(t *testing.T, peer *Peer, code uint64, id uint64, payload rlp.RawValue,
	handler func(Backend, Decoder, *Peer) error, backend Backend) error {
	t.Helper()

	enc, err := rlp.EncodeToBytes(&responseEnvelope{RequestId: id, Payload: payload})
	if err != nil {
		t.Fatalf("encoding the envelope failed: %v", err)
	}
	app, net := p2p.MsgPipe()
	defer app.Close()
	defer net.Close()

	go func() {
		p2p.Send(app, code, rlp.RawValue(enc))
	}()
	msg, err := net.ReadMsg()
	if err != nil {
		t.Fatalf("reading the test message failed: %v", err)
	}
	defer msg.Discard()

	return handler(backend, msg, peer)
}

// TestResponseGateDropsUnsolicited checks that a response nobody asked for is
// dropped before its payload is decoded, for every response message that
// carries a request id.
func TestResponseGateDropsUnsolicited(t *testing.T) {
	backend := newTestBackend(1)
	defer backend.close()

	peer, _ := newTestPeer("gate", ETH69, backend)
	processing := acceptTxsBackend{backend}
	defer peer.close()

	payload := unpairedPayload(t)

	for _, tt := range []struct {
		name    string
		code    uint64
		handler func(Backend, Decoder, *Peer) error
	}{
		{"headers", BlockHeadersMsg, handleBlockHeaders},
		{"bodies", BlockBodiesMsg, handleBlockBodies},
		{"receipts", ReceiptsMsg, handleReceipts},
		{"pooled transactions", PooledTransactionsMsg, handlePooledTransactions},
		{"blob sidecars", BlobSidecarsMsg, handleBlobSidecars69},
	} {
		// 0xdead was never requested from this peer.
		if err := deliver(t, peer.Peer, tt.code, 0xdead, payload, tt.handler, processing); err != nil {
			t.Errorf("%s: unsolicited response was processed instead of dropped: %v", tt.name, err)
		}
	}
}

// TestResponseGateDecodesSolicited is the other half: once the id is in flight,
// the payload is decoded, and a payload that cannot be decoded is reported as
// such. Without this, a gate that dropped everything would pass the test above.
func TestResponseGateDecodesSolicited(t *testing.T) {
	backend := newTestBackend(1)
	defer backend.close()

	peer, _ := newTestPeer("gate", ETH69, backend)
	processing := acceptTxsBackend{backend}
	defer peer.close()

	payload := unpairedPayload(t)

	for _, tt := range []struct {
		name    string
		code    uint64
		handler func(Backend, Decoder, *Peer) error
	}{
		{"headers", BlockHeadersMsg, handleBlockHeaders},
		{"bodies", BlockBodiesMsg, handleBlockBodies},
		{"receipts", ReceiptsMsg, handleReceipts},
		{"pooled transactions", PooledTransactionsMsg, handlePooledTransactions},
		{"blob sidecars", BlobSidecarsMsg, handleBlobSidecars69},
	} {
		const id = 0xbeef
		peer.Peer.trackPending(id)

		err := deliver(t, peer.Peer, tt.code, id, payload, tt.handler, processing)
		if !errors.Is(err, errDecode) {
			t.Errorf("%s: solicited response was not decoded (want errDecode, got %v)", tt.name, err)
		}
		peer.Peer.untrackPending(id)
	}
}

// TestPendingIndexBounded checks that the index cannot grow without bound. The
// paths that bypass the dispatcher only untrack on an answer, so an unanswered
// request must not accumulate for the life of the connection.
func TestPendingIndexBounded(t *testing.T) {
	backend := newTestBackend(1)
	defer backend.close()

	peer, _ := newTestPeer("bounded", ETH69, backend)
	defer peer.close()

	for i := uint64(1); i <= maxPendingIDs*3; i++ {
		peer.Peer.trackPending(i)
	}
	peer.Peer.pendingLock.RLock()
	size := len(peer.Peer.pendingIDs)
	peer.Peer.pendingLock.RUnlock()

	if size > maxPendingIDs {
		t.Fatalf("pending index grew past its bound: %d > %d", size, maxPendingIDs)
	}
	// The most recent ids must have survived the eviction, or the gate would
	// drop responses to requests that are actually in flight.
	for i := uint64(maxPendingIDs*3 - 10); i <= maxPendingIDs*3; i++ {
		if !peer.Peer.solicited(i) {
			t.Fatalf("recent request id %d was evicted", i)
		}
	}
	// And the oldest must be gone, which is what keeps the bound.
	if peer.Peer.solicited(1) {
		t.Fatalf("oldest request id was not evicted")
	}
}

// TestTrackPendingIdempotent guards the ring bookkeeping: registering the same
// id twice must not consume two slots, or a retried request would shrink the
// effective capacity.
func TestTrackPendingIdempotent(t *testing.T) {
	backend := newTestBackend(1)
	defer backend.close()

	peer, _ := newTestPeer("idempotent", ETH69, backend)
	defer peer.close()

	for i := 0; i < maxPendingIDs*2; i++ {
		peer.Peer.trackPending(7)
	}
	peer.Peer.pendingLock.RLock()
	size := len(peer.Peer.pendingIDs)
	peer.Peer.pendingLock.RUnlock()

	if size != 1 {
		t.Fatalf("re-tracking one id filled the index: size %d", size)
	}
	if !peer.Peer.solicited(7) {
		t.Fatalf("re-tracked id was evicted by itself")
	}
}
