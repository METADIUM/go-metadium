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
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
)

// The tests below pair the two directions against the same message: a payload
// that cannot decode into the response type. A solicited id must reach the
// decoder and report that failure; an unsolicited id must be dropped before the
// payload is ever looked at, so the same message comes back as a nil error.
// Using an undecodable payload is what makes these tests fail if the gate is
// removed, rather than merely covering it.
//
// Each bad payload has the element count its response type expects, so the
// decode fails on the contents rather than on the arity -- a list where a byte
// string or a struct is expected. Getting this wrong is easy: a plain
// []string does decode into ByteCodesPacket.Codes ([][]byte), which is how the
// first version of this test passed while proving nothing for two of the four.
func gatedMessage(t *testing.T, code, id uint64, body []any) (p2p.Msg, func()) {
	t.Helper()

	enc, err := rlp.EncodeToBytes(append([]any{id}, body...))
	if err != nil {
		t.Fatalf("encoding the test message failed: %v", err)
	}
	app, net := p2p.MsgPipe()
	go func() {
		p2p.Send(app, code, rlp.RawValue(enc))
	}()
	msg, err := net.ReadMsg()
	if err != nil {
		app.Close()
		net.Close()
		t.Fatalf("reading the test message failed: %v", err)
	}
	return msg, func() {
		msg.Discard()
		app.Close()
		net.Close()
	}
}

// newGatePeer is a peer with nothing but a writable pipe: the gate never sends
// anything, it only consults the index.
func newGatePeer() *Peer {
	_, net := p2p.MsgPipe()
	return NewFakePeer(1, "0123456789abcdef", net)
}

// responseCases enumerates the four response codes the gate covers, each with a
// freshly allocated destination and a body that cannot decode into it.
func responseCases() []struct {
	name string
	code uint64
	res  any
	body []any
} {
	// A list where a struct or a byte string is expected.
	nested := [][]string{{"not a struct"}}

	return []struct {
		name string
		code uint64
		res  any
		body []any
	}{
		{"AccountRange", AccountRangeMsg, new(AccountRangePacket), []any{nested, [][]byte{}}},
		{"StorageRanges", StorageRangesMsg, new(StorageRangesPacket), []any{nested, [][]byte{}}},
		{"ByteCodes", ByteCodesMsg, new(ByteCodesPacket), []any{nested}},
		{"TrieNodes", TrieNodesMsg, new(TrieNodesPacket), []any{nested}},
	}
}

// TestGateDropsUnsolicited is the property the change exists for: a response
// for an id this peer was never sent is dropped without decoding its payload.
func TestGateDropsUnsolicited(t *testing.T) {
	for _, tc := range responseCases() {
		t.Run(tc.name, func(t *testing.T) {
			peer := newGatePeer()
			msg, done := gatedMessage(t, tc.code, 42, tc.body)
			defer done()

			gated, err := gateResponse(peer, msg, tc.code, tc.res)
			if err != nil {
				t.Fatalf("an unsolicited response reached the decoder: %v", err)
			}
			if !gated {
				t.Fatal("an unsolicited response was not gated")
			}
		})
	}
}

// TestGateDecodesSolicited is the other half: the same undecodable payload,
// with the id registered, must reach the decoder and fail there. Without this
// the gate could be passing by rejecting everything.
func TestGateDecodesSolicited(t *testing.T) {
	for _, tc := range responseCases() {
		t.Run(tc.name, func(t *testing.T) {
			peer := newGatePeer()
			peer.trackPending(42)

			msg, done := gatedMessage(t, tc.code, 42, tc.body)
			defer done()

			gated, err := gateResponse(peer, msg, tc.code, tc.res)
			if gated {
				t.Fatal("a solicited response was gated out")
			}
			if err == nil {
				t.Fatal("the undecodable payload was accepted")
			}
			if peer.solicited(42) {
				t.Error("the id was not retired once its answer arrived")
			}
		})
	}
}

// TestGateRejectsMalformedEnvelope checks that a message which is not even a
// list with a leading integer is refused by the id peek, before any attempt to
// decode it as a response.
func TestGateRejectsMalformedEnvelope(t *testing.T) {
	peer := newGatePeer()

	enc, err := rlp.EncodeToBytes("not a list")
	if err != nil {
		t.Fatalf("encoding failed: %v", err)
	}
	app, net := p2p.MsgPipe()
	defer app.Close()
	defer net.Close()
	go func() {
		p2p.Send(app, AccountRangeMsg, rlp.RawValue(enc))
	}()
	msg, err := net.ReadMsg()
	if err != nil {
		t.Fatalf("reading the test message failed: %v", err)
	}
	defer msg.Discard()

	if _, err := gateResponse(peer, msg, AccountRangeMsg, new(AccountRangePacket)); err == nil {
		t.Error("a message with no request id was accepted")
	}
}

// TestPendingIndexBounded pins the ring: the index never grows past its
// capacity, and what falls out is the oldest id.
func TestPendingIndexBounded(t *testing.T) {
	peer := newGatePeer()
	for id := uint64(1); id <= maxPendingIDs+10; id++ {
		peer.trackPending(id)
	}
	if got := len(peer.pendingIDs); got != maxPendingIDs {
		t.Errorf("the index holds %d ids, want %d", got, maxPendingIDs)
	}
	if peer.solicited(1) {
		t.Error("the oldest id survived eviction")
	}
	if !peer.solicited(maxPendingIDs + 10) {
		t.Error("the newest id was not retained")
	}
}

// TestTrackPendingIdempotent checks that re-registering a live id does not
// consume a second ring slot, which would evict an unrelated live id.
func TestTrackPendingIdempotent(t *testing.T) {
	peer := newGatePeer()
	peer.trackPending(7)
	pos := peer.pendingPos
	peer.trackPending(7)

	if peer.pendingPos != pos {
		t.Error("re-registering a live id consumed a ring slot")
	}
	if !peer.solicited(7) {
		t.Error("the id was lost")
	}
}

// TestRequestRegistersPendingID covers the wiring rather than the gate itself:
// that each request method registers its id, and that a request which never
// reaches the wire does not leave one behind.
//
// This is worth its own test because the package's sync tests cannot reach it.
// Their testPeer implements the Request* methods itself and calls the syncer's
// OnAccounts and friends directly, so nothing in that suite crosses p2p
// messaging or handleMessage -- a gate that rejected every legitimate response
// would still leave them green.
func TestRequestRegistersPendingID(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(*Peer, uint64) error
	}{
		{"AccountRange", func(p *Peer, id uint64) error {
			return p.RequestAccountRange(id, common.Hash{}, common.Hash{}, common.Hash{}, 1024)
		}},
		{"StorageRanges", func(p *Peer, id uint64) error {
			return p.RequestStorageRanges(id, common.Hash{}, []common.Hash{{}}, nil, nil, 1024)
		}},
		{"ByteCodes", func(p *Peer, id uint64) error {
			return p.RequestByteCodes(id, []common.Hash{{}}, 1024)
		}},
		{"TrieNodes", func(p *Peer, id uint64) error {
			return p.RequestTrieNodes(id, common.Hash{}, nil, 1024)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, net := p2p.MsgPipe()
			defer app.Close()
			defer net.Close()

			// MsgPipe is synchronous, so the request needs a reader.
			read := make(chan error, 1)
			go func() {
				msg, err := app.ReadMsg()
				if err == nil {
					msg.Discard()
				}
				read <- err
			}()

			peer := NewFakePeer(1, "0123456789abcdef", net)
			if err := tc.send(peer, 99); err != nil {
				t.Fatalf("sending the request failed: %v", err)
			}
			if err := <-read; err != nil {
				t.Fatalf("reading the request failed: %v", err)
			}
			if !peer.solicited(99) {
				t.Error("the request id was not registered, so its response would be gated out")
			}
		})
	}

	t.Run("failed send untracks", func(t *testing.T) {
		app, net := p2p.MsgPipe()
		app.Close()
		net.Close()

		peer := NewFakePeer(1, "0123456789abcdef", net)
		if err := peer.RequestByteCodes(99, []common.Hash{{}}, 1024); err == nil {
			t.Fatal("sending on a closed pipe succeeded")
		}
		if peer.solicited(99) {
			t.Error("a request that never reached the wire left its id in the index")
		}
	})
}
