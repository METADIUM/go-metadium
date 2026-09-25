package metabft

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

var testDigest = common.HexToHash("0xabcdef")

func TestMessageSignVerify(t *testing.T) {
	net := newTestNet(t, 4)
	for _, typ := range []MsgType{MsgPreprepare, MsgPrepare, MsgCommit, MsgRoundChange} {
		m := net.signed(t, 2, typ, 17280, 1, testDigest)
		idx, err := m.Verify(testChainID, net.set)
		if err != nil || idx != 2 {
			t.Errorf("%v: Verify = %d, %v; want 2, nil", typ, idx, err)
		}
		// Survives the wire.
		enc, err := rlp.EncodeToBytes(m)
		if err != nil {
			t.Fatal(err)
		}
		var dec Message
		if err := rlp.DecodeBytes(enc, &dec); err != nil {
			t.Fatal(err)
		}
		if idx, err := dec.Verify(testChainID, net.set); err != nil || idx != 2 {
			t.Errorf("%v after RLP: %d, %v", typ, idx, err)
		}
	}
}

// TestMessageTamper: changing any signed field breaks the signature (the
// recovered signer is no longer validator 1).
func TestMessageTamper(t *testing.T) {
	net := newTestNet(t, 4)
	for name, mutate := range map[string]func(m *Message){
		"type":    func(m *Message) { m.Type = MsgRoundChange },
		"height":  func(m *Message) { m.Height++ },
		"round":   func(m *Message) { m.Round++ },
		"digest":  func(m *Message) { m.Digest[0] ^= 1 },
		"payload": func(m *Message) { m.Payload = []byte{1} },
	} {
		m := net.signed(t, 1, MsgPrepare, 10, 0, testDigest)
		mutate(m)
		if idx, err := m.Verify(testChainID, net.set); err == nil && idx == 1 {
			t.Errorf("%s changed but the message still verifies as validator 1", name)
		}
	}
}

func TestMessageVerifyRejects(t *testing.T) {
	net := newTestNet(t, 4)
	other := newTestNet(t, 1)

	m := net.signed(t, 0, MsgPrepare, 10, 0, testDigest)
	if _, err := m.Verify(testChainID+1, net.set); !errors.Is(err, errWrongChainID) {
		t.Errorf("other chain: %v", err)
	}
	if _, err := other.signed(t, 0, MsgPrepare, 10, 0, testDigest).Verify(testChainID, net.set); !errors.Is(err, ErrUnknownSigner) {
		t.Errorf("non-validator: %v", err)
	}

	withSeal := net.signed(t, 0, MsgPrepare, 10, 0, testDigest)
	withSeal.CommitSeal = make([]byte, crypto.SignatureLength)
	withSeal.Sign(net.keys[0])
	if _, err := withSeal.Verify(testChainID, net.set); !errors.Is(err, errUnexpectedSeal) {
		t.Errorf("PREPARE with a seal: %v", err)
	}

	noSeal := &Message{Type: MsgCommit, Height: 10, ChainID: testChainID, Digest: testDigest}
	noSeal.Sign(net.keys[0])
	if _, err := noSeal.Verify(testChainID, net.set); !errors.Is(err, errMissingSeal) {
		t.Errorf("COMMIT without a seal: %v", err)
	}

	// A COMMIT carrying another validator's seal.
	stolen := net.signed(t, 0, MsgCommit, 10, 0, testDigest)
	stolen.CommitSeal = net.signed(t, 1, MsgCommit, 10, 0, testDigest).CommitSeal
	stolen.Sign(net.keys[0])
	if _, err := stolen.Verify(testChainID, net.set); !errors.Is(err, errSealWrongSigner) {
		t.Errorf("someone else's seal: %v", err)
	}

	// A seal for another round does not recover to the signer.
	stale := net.signed(t, 0, MsgCommit, 10, 1, testDigest)
	stale.CommitSeal = net.signed(t, 0, MsgCommit, 10, 0, testDigest).CommitSeal
	stale.Sign(net.keys[0])
	if _, err := stale.Verify(testChainID, net.set); !errors.Is(err, errSealWrongSigner) {
		t.Errorf("seal from another round: %v", err)
	}

	unknown := &Message{Type: 9, ChainID: testChainID}
	unknown.Sign(net.keys[0])
	if _, err := unknown.Verify(testChainID, net.set); !errors.Is(err, errUnknownMsgType) {
		t.Errorf("unknown type: %v", err)
	}
}

// TestHighSRejected: (r, n-s) with the flipped recovery id is the same
// signer's other valid encoding; only the low-s form is accepted.
func TestHighSRejected(t *testing.T) {
	net := newTestNet(t, 1)
	m := net.signed(t, 0, MsgPrepare, 1, 0, testDigest)
	s := new(big.Int).SetBytes(m.Signature[32:64])
	flipped := make([]byte, len(m.Signature))
	copy(flipped, m.Signature)
	new(big.Int).Sub(crypto.S256().Params().N, s).FillBytes(flipped[32:64])
	flipped[64] ^= 1
	m.Signature = flipped
	if _, err := m.Verify(testChainID, net.set); !errors.Is(err, errNonCanonicalSig) {
		t.Errorf("high-s signature: %v", err)
	}
}

func TestCommitDigest(t *testing.T) {
	d := CommitDigest(testDigest, 1, testChainID)
	for name, other := range map[string]common.Hash{
		"other round":   CommitDigest(testDigest, 2, testChainID),
		"other chain":   CommitDigest(testDigest, 1, testChainID+1),
		"other block":   CommitDigest(common.HexToHash("0x01"), 1, testChainID),
		"message hash":  (&Message{Type: MsgCommit, Round: 1, ChainID: testChainID, Digest: testDigest}).SigningHash(),
		"bare block id": testDigest,
	} {
		if d == other {
			t.Errorf("commit digest equals the %s", name)
		}
	}
	net := newTestNet(t, 1)
	seal, err := SignCommitSeal(d, net.keys[0])
	if err != nil {
		t.Fatal(err)
	}
	pub, err := RecoverSealSigner(d, seal)
	if err != nil {
		t.Fatal(err)
	}
	if i, ok := net.set.IndexOf(pub); !ok || i != 0 {
		t.Error("seal does not recover to its signer")
	}
}
