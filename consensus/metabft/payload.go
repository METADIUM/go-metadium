package metabft

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

// Proposal is the block being agreed on. The engine (P5) wraps a
// types.Block; the core only needs its height, its digest and its bytes.
type Proposal interface {
	Height() uint64
	Hash() common.Hash // BlockHash, the digest validators sign (design §5.2)
	Encode() ([]byte, error)
}

// preprepareBody is the Payload of a PRE-PREPARE.
type preprepareBody struct {
	Block []byte
	// Round > 0 only: a quorum of ROUND-CHANGE messages for this round, and,
	// if any of them reports a prepared block, the PREPARE quorum for the
	// highest one (design §4.5).
	RoundChanges []Message
	Prepares     []Message
}

// roundChangeClaim is the signed Payload of a ROUND-CHANGE: what, if
// anything, the sender prepared. Message.Digest carries the prepared digest,
// zero when nothing was prepared.
type roundChangeClaim struct {
	Prepared      bool
	PreparedRound uint64
}

// roundChangeExtra is the unsigned Extra of a ROUND-CHANGE: the evidence for
// the claim, so a new proposer can re-propose the block.
type roundChangeExtra struct {
	Block    []byte
	Prepares []Message
}

var (
	errBadPayload       = errors.New("malformed payload")
	errDigestMismatch   = errors.New("digest does not match the block")
	errBadCertificate   = errors.New("invalid certificate")
	errUnjustified      = errors.New("proposal is not justified by the round changes")
	errWrongProposer    = errors.New("not the proposer of this round")
	errWrongHeight      = errors.New("block is for another height")
	errPreparedTooLate  = errors.New("prepared round is not below the round change's round")
	errEmptyPreparedHas = errors.New("unprepared round change carries prepared data")
)

func encodePayload(v interface{}) []byte {
	b, err := rlp.EncodeToBytes(v)
	if err != nil {
		panic(err) // byte slices and messages only
	}
	return b
}

// certificate checks that msgs are at least quorum messages of type typ for
// (height, round, digest) from distinct validators.
func certificate(msgs []Message, typ MsgType, height, round uint64, digest common.Hash, chainID uint64, set *ValidatorSet) error {
	seen := make(map[int]bool, len(msgs))
	for i := range msgs {
		m := &msgs[i]
		if m.Type != typ || m.Height != height || m.Round != round || m.Digest != digest {
			return fmt.Errorf("%w: message %d is %v h%d r%d, want %v h%d r%d", errBadCertificate,
				i, m.Type, m.Height, m.Round, typ, height, round)
		}
		idx, err := m.VerifyAs(Quoted, chainID, set)
		if err != nil {
			return fmt.Errorf("%w: message %d: %v", errBadCertificate, i, err)
		}
		seen[idx] = true
	}
	if len(seen) < set.Quorum() {
		return fmt.Errorf("%w: %d distinct signers, quorum is %d", errBadCertificate, len(seen), set.Quorum())
	}
	return nil
}

// decodeRoundChange decodes a ROUND-CHANGE's claim and, if present, its
// attached evidence.
func decodeRoundChange(m *Message) (*roundChangeClaim, *roundChangeExtra, error) {
	claim := new(roundChangeClaim)
	if err := rlp.DecodeBytes(m.Payload, claim); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errBadPayload, err)
	}
	if !claim.Prepared {
		if m.Digest != (common.Hash{}) || claim.PreparedRound != 0 || m.ExtraHash != (common.Hash{}) {
			return nil, nil, errEmptyPreparedHas
		}
		return claim, nil, nil
	}
	if claim.PreparedRound >= m.Round {
		return nil, nil, errPreparedTooLate
	}
	if len(m.Extra) == 0 {
		return claim, nil, nil
	}
	extra := new(roundChangeExtra)
	if err := rlp.DecodeBytes(m.Extra, extra); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errBadPayload, err)
	}
	return claim, extra, nil
}

// checkRoundChangeEvidence verifies that a prepared claim is backed by its
// block and PREPARE quorum. A claim without them is refused: otherwise a
// Byzantine node could steer the next proposal with an invented one.
func checkRoundChangeEvidence(m *Message, claim *roundChangeClaim, extra *roundChangeExtra,
	decode func([]byte) (Proposal, error), chainID uint64, set *ValidatorSet) (Proposal, error) {
	if !claim.Prepared {
		return nil, nil
	}
	if extra == nil {
		return nil, fmt.Errorf("%w: prepared claim without evidence", errBadCertificate)
	}
	block, err := decode(extra.Block)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errBadPayload, err)
	}
	if block.Hash() != m.Digest {
		return nil, errDigestMismatch
	}
	if err := certificate(extra.Prepares, MsgPrepare, m.Height, claim.PreparedRound, m.Digest, chainID, set); err != nil {
		return nil, err
	}
	return block, nil
}
