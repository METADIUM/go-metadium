package metabft

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/consensus/metabft"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

// Backend is what the protocol needs from the node (the PBFT engine, P5).
type Backend interface {
	// IsValidator reports whether this node takes part in consensus. Only
	// validators advertise metabft/1 (design §7.1).
	IsValidator() bool

	ChainID() uint64

	// ValidatorSet returns the set that verifies messages for height, or
	// false for a height the node does not accept messages for right now
	// (long past, or beyond the next one).
	ValidatorSet(height uint64) (*metabft.ValidatorSet, bool)

	// HandleConsensus receives a message that passed verification and the
	// dedup cache: the first of its kind from its signer.
	HandleConsensus(peer *Peer, m *metabft.Message) error

	// HandleEvidence receives an equivocation, already verified.
	HandleEvidence(ev *metabft.Evidence)

	// UnknownSigner is told of a validly signed message whose signer is not
	// in the set. That is normal around a validator-set change, so it is not
	// misbehaviour by itself, but each costs this node a signature recovery:
	// a non-nil error (a peer over its budget) disconnects the peer.
	UnknownSigner(peer *Peer) error

	// SyncStatus is this node's (height, round), for SyncRequest.
	SyncStatus() (height, round uint64)
	// HandleSyncReply receives a peer's (height, round). The reply is not
	// signed, so it is a hint: it may only come from a peer RunPeer
	// admitted, and must not move consensus by itself.
	HandleSyncReply(peer *Peer, height, round uint64)

	// RunPeer runs handler for a connected peer. It must admit only
	// validators: any node can advertise metabft/1, and only consensus
	// messages carry signatures.
	RunPeer(peer *Peer, handler func(*Peer) error) error
	PeerInfo(id enode.ID) interface{}
}

// MakeProtocols returns the metabft/1 protocol, or nothing on a node that
// is not a validator: capability advertisement is fixed at startup, so a
// node added to the validator set by governance needs a restart.
func MakeProtocols(backend Backend, cache *Cache) []p2p.Protocol {
	if !backend.IsValidator() {
		return nil
	}
	protocols := make([]p2p.Protocol, 0, len(ProtocolVersions))
	for _, version := range ProtocolVersions {
		version := version
		protocols = append(protocols, p2p.Protocol{
			Name:    ProtocolName,
			Version: version,
			Length:  protocolLengths[version],
			Run: func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
				return backend.RunPeer(NewPeer(version, p, rw), func(peer *Peer) error {
					return Handle(backend, cache, peer)
				})
			},
			PeerInfo: func(id enode.ID) interface{} { return backend.PeerInfo(id) },
		})
	}
	return protocols
}

// Handle serves a peer until it errors; the peer is then disconnected.
func Handle(backend Backend, cache *Cache, peer *Peer) error {
	for {
		if err := HandleMessage(backend, cache, peer); err != nil {
			peer.Log().Debug("metabft message handling failed", "err", err)
			return err
		}
	}
}

// errMisbehaving marks a message no honest peer sends; the peer is dropped.
var errMisbehaving = errors.New("misbehaving peer")

// HandleMessage reads and processes one message. The order is design §7.1:
// verify the signature and membership first, then the dedup cache, so an
// unverified message can never take a genuine sender's slot.
func HandleMessage(backend Backend, cache *Cache, peer *Peer) error {
	msg, err := peer.rw.ReadMsg()
	if err != nil {
		return err
	}
	if msg.Size > maxMessageSize {
		return fmt.Errorf("%w: %v > %v", errMsgTooLarge, msg.Size, maxMessageSize)
	}
	defer msg.Discard()

	switch msg.Code {
	case PreprepareMsg, PrepareMsg, CommitMsg, RoundChangeMsg:
		m := new(metabft.Message)
		if err := msg.Decode(m); err != nil {
			return fmt.Errorf("%w: message %v: %v", errDecode, msg.Code, err)
		}
		if code, ok := codeOf(m.Type); !ok || code != msg.Code {
			return fmt.Errorf("%w: code %d carries %v", errCodeMismatch, msg.Code, m.Type)
		}
		return handleConsensus(backend, cache, peer, m)

	case SyncRequestMsg:
		var req SyncRequestPacket
		if err := msg.Decode(&req); err != nil {
			return fmt.Errorf("%w: sync request: %v", errDecode, err)
		}
		h, r := backend.SyncStatus()
		return peer.ReplySync(h, r)

	case SyncReplyMsg:
		var rep SyncReplyPacket
		if err := msg.Decode(&rep); err != nil {
			return fmt.Errorf("%w: sync reply: %v", errDecode, err)
		}
		backend.HandleSyncReply(peer, rep.Height, rep.Round)
		return nil
	}
	return fmt.Errorf("%w: %v", errInvalidMsgCode, msg.Code)
}

func handleConsensus(backend Backend, cache *Cache, peer *Peer, m *metabft.Message) error {
	set, ok := backend.ValidatorSet(m.Height)
	if !ok {
		return nil // a height this node does not take messages for: not the sender's fault
	}
	if _, err := m.Verify(backend.ChainID(), set); err != nil {
		if errors.Is(err, metabft.ErrUnknownSigner) {
			// e.g. a validator set change this node has not reached yet;
			// the backend bounds how often a peer may do it.
			return backend.UnknownSigner(peer)
		}
		return fmt.Errorf("%w: %v", errMisbehaving, err)
	}
	signer, err := m.Signer()
	if err != nil {
		return fmt.Errorf("%w: %v", errMisbehaving, err) // Verify recovered it; cannot happen
	}
	switch verdict, ev := cache.Add(signer, m); verdict {
	case Fresh:
		return backend.HandleConsensus(peer, m)
	case Conflict:
		backend.HandleEvidence(ev)
	}
	return nil
}
