// Package metabft implements the metabft/1 devp2p sub-protocol that carries
// PBFT consensus messages between validators (docs/pbft-consensus-design.md
// §7.1). It is separate from meta/6x so non-validators never negotiate it and
// the eth protocol needs no version bump.
package metabft

import (
	"errors"

	"github.com/ethereum/go-ethereum/consensus/metabft"
)

// Protocol identity.
const (
	ProtocolName = "metabft"
	METABFT1     = 1
)

// ProtocolVersions are the supported versions, newest first.
var ProtocolVersions = []uint{METABFT1}

var protocolLengths = map[uint]uint64{METABFT1: 8}

// maxMessageSize bounds one message. A PRE-PREPARE carries a block (the eth
// protocol's 10 MB bound) plus two certificates of signed messages, and a
// ROUND-CHANGE may attach a block too.
const maxMessageSize = 12 * 1024 * 1024

// Message codes (design §7.1).
const (
	PreprepareMsg  = 0x00
	PrepareMsg     = 0x01
	CommitMsg      = 0x02
	RoundChangeMsg = 0x03
	SyncRequestMsg = 0x04
	SyncReplyMsg   = 0x05
)

var (
	errMsgTooLarge    = errors.New("message too long")
	errDecode         = errors.New("invalid message")
	errInvalidMsgCode = errors.New("invalid message code")
	errCodeMismatch   = errors.New("message code does not match the message type")
)

// codeOf maps a consensus message type to its wire code.
func codeOf(t metabft.MsgType) (uint64, bool) {
	switch t {
	case metabft.MsgPreprepare:
		return PreprepareMsg, true
	case metabft.MsgPrepare:
		return PrepareMsg, true
	case metabft.MsgCommit:
		return CommitMsg, true
	case metabft.MsgRoundChange:
		return RoundChangeMsg, true
	}
	return 0, false
}

// SyncRequestPacket asks a validator where it is, so a lagging one can
// catch up (design §7.7).
type SyncRequestPacket struct{}

// SyncReplyPacket answers with the height being agreed on and the round.
// Committed blocks themselves come through the eth protocol's block sync.
type SyncReplyPacket struct {
	Height uint64
	Round  uint64
}
