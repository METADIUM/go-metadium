package metabft

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// MsgType is the type of a consensus message (design §4.6).
type MsgType uint8

const (
	MsgPreprepare  MsgType = 1
	MsgPrepare     MsgType = 2
	MsgCommit      MsgType = 3
	MsgRoundChange MsgType = 4
)

func (t MsgType) String() string {
	switch t {
	case MsgPreprepare:
		return "PRE-PREPARE"
	case MsgPrepare:
		return "PREPARE"
	case MsgCommit:
		return "COMMIT"
	case MsgRoundChange:
		return "ROUND-CHANGE"
	}
	return fmt.Sprintf("MsgType(%d)", uint8(t))
}

// commitSealDomain separates commit-seal signatures from message signatures,
// so neither can be passed off as the other (design §4.6).
const commitSealDomain = 0x02

// Message is a signed consensus message. Digest is the BlockHash of the
// proposal (types.Header.Hash, design §5.2); for ROUND-CHANGE it is the
// prepared digest, or zero when nothing was prepared.
type Message struct {
	Type       MsgType
	Height     uint64
	Round      uint64
	ChainID    uint64 // no cross-network reuse (design §4.6, §9.5)
	Digest     common.Hash
	Payload    []byte // PRE-PREPARE: block RLP + RC certificate; ROUND-CHANGE: certificate
	CommitSeal []byte // COMMIT only
	// ExtraHash is keccak256(Extra) for a ROUND-CHANGE that attaches evidence,
	// zero otherwise. It is signed, so Extra is committed to without being
	// carried: a quoted ROUND-CHANGE keeps it and drops Extra.
	ExtraHash common.Hash
	Signature []byte

	// Extra travels outside the signature, bound to it by ExtraHash. Only a
	// ROUND-CHANGE uses it, for its prepared block and PREPARE quorum. A
	// message received directly must carry exactly the Extra its ExtraHash
	// names, so a relay can neither alter nor strip it and still pass
	// verification; a message quoted inside another must carry none.
	Extra []byte
}

// VerifyMode says where a message was found.
type VerifyMode int

const (
	// Direct: received on its own. Extra must match ExtraHash exactly.
	Direct VerifyMode = iota
	// Quoted: inside another message's certificate. Extra must be empty.
	Quoted
	// Stored: either of the above, as kept in evidence.
	Stored
)

// signedFields is everything the message signature covers.
type signedFields struct {
	Type       MsgType
	Height     uint64
	Round      uint64
	ChainID    uint64
	Digest     common.Hash
	Payload    []byte
	CommitSeal []byte
	ExtraHash  common.Hash
}

var (
	errUnknownMsgType   = errors.New("unknown message type")
	errUnexpectedSeal   = errors.New("commit seal on a non-COMMIT message")
	errMissingSeal      = errors.New("COMMIT without a commit seal")
	errWrongChainID     = errors.New("message for another chain")
	errUnknownSigner    = errors.New("signer is not a validator")
	errBadSignature     = errors.New("invalid signature")
	errNonCanonicalSig  = errors.New("non-canonical signature (high s)")
	errSealWrongSigner  = errors.New("commit seal is not by the message signer")
	errBadSignatureSize = fmt.Errorf("signature must be %d bytes", crypto.SignatureLength)
	errUnexpectedExtra  = errors.New("unsigned attachment on a message that takes none")
	errExtraMismatch    = errors.New("attachment does not match its signed hash")
)

// SigningHash is the hash the message signature is over.
func (m *Message) SigningHash() common.Hash {
	enc, err := rlp.EncodeToBytes(&signedFields{m.Type, m.Height, m.Round, m.ChainID, m.Digest, m.Payload, m.CommitSeal, m.ExtraHash})
	if err != nil {
		panic(err) // only fixed-size fields and byte slices: cannot fail
	}
	return crypto.Keccak256Hash(enc)
}

// Sign sets Signature with the given node key.
func (m *Message) Sign(key *ecdsa.PrivateKey) error {
	sig, err := crypto.Sign(m.SigningHash().Bytes(), key)
	if err != nil {
		return err
	}
	m.Signature = sig
	return nil
}

// checkShape validates the fields that do not need a key.
func (m *Message) checkShape() error {
	switch m.Type {
	case MsgPreprepare, MsgPrepare, MsgRoundChange:
		// By length: RLP decodes an absent seal as an empty, non-nil slice.
		if len(m.CommitSeal) != 0 {
			return errUnexpectedSeal
		}
	case MsgCommit:
		// A seal is a plain signature (types.CommitSealLength in the header).
		if len(m.CommitSeal) != crypto.SignatureLength {
			return errMissingSeal
		}
	default:
		return fmt.Errorf("%w: %d", errUnknownMsgType, m.Type)
	}
	return nil
}

// SetExtra attaches extra and commits to it in the signed fields. Call it
// before Sign.
func (m *Message) SetExtra(extra []byte) {
	m.Extra = extra
	if len(extra) == 0 {
		m.ExtraHash = common.Hash{}
	} else {
		m.ExtraHash = crypto.Keccak256Hash(extra)
	}
}

// checkExtra enforces where an attachment may appear (see VerifyMode).
func (m *Message) checkExtra(mode VerifyMode) error {
	if m.Type != MsgRoundChange {
		if len(m.Extra) != 0 || m.ExtraHash != (common.Hash{}) {
			return errUnexpectedExtra
		}
		return nil
	}
	switch {
	case mode == Quoted && len(m.Extra) != 0:
		return errUnexpectedExtra
	case len(m.Extra) != 0 && crypto.Keccak256Hash(m.Extra) != m.ExtraHash:
		return errExtraMismatch
	case mode == Direct && len(m.Extra) == 0 && m.ExtraHash != (common.Hash{}):
		return fmt.Errorf("%w: attachment missing", errExtraMismatch)
	}
	return nil
}

// Signer recovers the public key that signed the message.
func (m *Message) Signer() ([]byte, error) {
	return recoverPubKey(m.SigningHash(), m.Signature)
}

// Verify checks a message received on its own; see VerifyAs.
func (m *Message) Verify(chainID uint64, set *ValidatorSet) (int, error) {
	return m.VerifyAs(Direct, chainID, set)
}

// VerifyAs checks the message's shape and attachment for where it was found,
// its chain ID and signature, and that the signer is in the set (design
// §7.1: before any cache lookup). For COMMIT it also checks that the seal is
// by the same validator. It returns the signer's index.
func (m *Message) VerifyAs(mode VerifyMode, chainID uint64, set *ValidatorSet) (int, error) {
	if err := m.checkShape(); err != nil {
		return 0, err
	}
	if err := m.checkExtra(mode); err != nil {
		return 0, err
	}
	if m.ChainID != chainID {
		return 0, fmt.Errorf("%w: have %d, want %d", errWrongChainID, m.ChainID, chainID)
	}
	signer, err := m.Signer()
	if err != nil {
		return 0, err
	}
	idx, ok := set.IndexOf(signer)
	if !ok {
		return 0, errUnknownSigner
	}
	if m.Type == MsgCommit {
		sealer, err := RecoverSealSigner(CommitDigest(m.Digest, m.Round, m.ChainID), m.CommitSeal)
		if err != nil {
			return 0, fmt.Errorf("commit seal: %w", err)
		}
		if i, ok := set.IndexOf(sealer); !ok || i != idx {
			return 0, errSealWrongSigner
		}
	}
	return idx, nil
}

// CommitDigest is what a commit seal signs:
// keccak256(rlp([BlockHash, Round, ChainID, 0x02])) (design §4.6).
func CommitDigest(blockHash common.Hash, round, chainID uint64) common.Hash {
	enc, err := rlp.EncodeToBytes([]interface{}{blockHash, round, chainID, uint8(commitSealDomain)})
	if err != nil {
		panic(err)
	}
	return crypto.Keccak256Hash(enc)
}

// SignCommitSeal produces a commit seal over digest.
func SignCommitSeal(digest common.Hash, key *ecdsa.PrivateKey) ([]byte, error) {
	return crypto.Sign(digest.Bytes(), key)
}

// RecoverSealSigner returns the public key that produced a commit seal.
func RecoverSealSigner(digest common.Hash, seal []byte) ([]byte, error) {
	return recoverPubKey(digest, seal)
}

var secp256k1HalfN = new(big.Int).Rsh(crypto.S256().Params().N, 1)

// recoverPubKey recovers a 64-byte public key and rejects high-s
// signatures, so one signer cannot produce two different valid encodings
// of the same seal or message signature.
func recoverPubKey(hash common.Hash, sig []byte) ([]byte, error) {
	if len(sig) != crypto.SignatureLength {
		return nil, errBadSignatureSize
	}
	if new(big.Int).SetBytes(sig[32:64]).Cmp(secp256k1HalfN) > 0 {
		return nil, errNonCanonicalSig
	}
	pub, err := crypto.Ecrecover(hash.Bytes(), sig)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errBadSignature, err)
	}
	return pub[1:], nil // drop the 0x04 prefix
}
