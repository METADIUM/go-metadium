package metabft

import (
	"github.com/ethereum/go-ethereum/consensus/metabft"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
)

// Peer is a validator connected on metabft/1.
type Peer struct {
	id string
	*p2p.Peer
	rw      p2p.MsgReadWriter
	version uint
	logger  log.Logger
}

// NewPeer wraps a p2p peer.
func NewPeer(version uint, p *p2p.Peer, rw p2p.MsgReadWriter) *Peer {
	id := p.ID().String()
	return &Peer{id: id, Peer: p, rw: rw, version: version, logger: log.New("peer", id[:8])}
}

// ID returns the peer's node ID.
func (p *Peer) ID() string { return p.id }

// Version returns the negotiated protocol version.
func (p *Peer) Version() uint { return p.version }

// Log returns the peer's contextual logger.
func (p *Peer) Log() log.Logger { return p.logger }

// SendConsensus sends one consensus message.
func (p *Peer) SendConsensus(m *metabft.Message) error {
	code, ok := codeOf(m.Type)
	if !ok {
		return errInvalidMsgCode
	}
	return p2p.Send(p.rw, code, m)
}

// RequestSync asks the peer for its (height, round).
func (p *Peer) RequestSync() error {
	return p2p.Send(p.rw, SyncRequestMsg, &SyncRequestPacket{})
}

// ReplySync answers a SyncRequest.
func (p *Peer) ReplySync(height, round uint64) error {
	return p2p.Send(p.rw, SyncReplyMsg, &SyncReplyPacket{Height: height, Round: round})
}
