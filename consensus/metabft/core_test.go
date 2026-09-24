package metabft

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestDeadline(t *testing.T) {
	c := Config{EmptyBlockInterval: 5 * time.Second, BaseTimeout: 2 * time.Second, MaxBackoffExp: 5}
	committed, start := 100*time.Second, 200*time.Second
	for _, tt := range []struct {
		round uint64
		want  time.Duration
	}{
		{0, 107 * time.Second}, // committedAt + 5s + 2s, the round start is irrelevant
		{1, 204 * time.Second},
		{2, 208 * time.Second},
		{5, 264 * time.Second},
		{6, 264 * time.Second}, // capped at 2^5
		{1000, 264 * time.Second},
	} {
		if got := c.deadline(tt.round, committed, start); got != tt.want {
			t.Errorf("round %d: deadline %v, want %v", tt.round, got, tt.want)
		}
	}
	// A config that bypassed the genesis cap: the deadline saturates in the
	// future instead of wrapping into the past (review on #148).
	wild := Config{BaseTimeout: 2 * time.Second, MaxBackoffExp: 200}
	prev := time.Duration(0)
	for _, r := range []uint64{1, 32, 33, 34, 63, 64, 200, 1 << 40} {
		d := wild.deadline(r, 0, start)
		if d <= start || d < prev {
			t.Errorf("round %d: deadline %v not after the round start %v / previous %v", r, d, start, prev)
		}
		prev = d
	}
}

// TestRequestProposalOnce: the proposer asks the backend for one block per
// round, however many ROUND-CHANGEs arrive (review on #148).
func TestRequestProposalOnce(t *testing.T) {
	net := newTestNet(t, 4)
	// The proposer of (10, 1) is validator (10+1)%4 = 3.
	c, b := newStubCore(t, net, 3)
	c.NewHeight(10, 0)
	c.Tick(100 * time.Second) // into round 1, sends its own ROUND-CHANGE
	for i := 0; i < 3; i++ {
		must(t, c.HandleMessage(net.roundChange(t, i, 10, 1, nil, 0), 100*time.Second))
	}
	if b.requested != 1 {
		t.Errorf("RequestProposal called %d times for one round, want 1", b.requested)
	}
}

func TestOpenNodeWAL(t *testing.T) {
	const bftBlock = 100
	dir := t.TempDir()
	path := func(name string) string { return filepath.Join(dir, name, "wal") }

	// During bootstrap: created, no observer mode.
	w, until, err := OpenNodeWAL(path("bootstrap"), 98, bftBlock)
	if err != nil || until != 0 {
		t.Fatalf("bootstrap: until %d, err %v", until, err)
	}
	w.Close()
	// Reopened later: fine.
	if w, until, err = OpenNodeWAL(path("bootstrap"), 5000, bftBlock); err != nil || until != 0 {
		t.Fatalf("existing WAL: until %d, err %v", until, err)
	}
	w.Close()

	// Missing once a PBFT vote was possible: observe past head+1.
	for _, head := range []uint64{99, 5000} {
		w, until, err := OpenNodeWAL(path("lost"+strings.Repeat("x", int(head%7))), head, bftBlock)
		if err != nil || until != head+1 {
			t.Errorf("missing at head %d: until %d, err %v; want %d", head, until, err, head+1)
		}
		w.Close()
	}

	// Corrupt: set aside, recreated, observer.
	p := path("corrupt")
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte{0, 0, 0, 5, 1, 2, 3, 4, 9, 9, 9, 9, 9}, 0o600)
	w, until, err = OpenNodeWAL(p, 3000, bftBlock)
	if err != nil || until != 3001 {
		t.Fatalf("corrupt: until %d, err %v", until, err)
	}
	w.Close()
	aside, _ := filepath.Glob(p + ".corrupt-*")
	if len(aside) != 1 {
		t.Errorf("corrupt WAL not kept aside: %v", aside)
	}
}

// stubBackend is the minimal Backend for driving a core by hand.
type stubBackend struct {
	set       *ValidatorSet
	sent      []*Message
	requested int
	committed Proposal
}

func (b *stubBackend) ChainID() uint64                              { return testChainID }
func (b *stubBackend) Validators(uint64) (*ValidatorSet, error)     { return b.set, nil }
func (b *stubBackend) DecodeProposal(data []byte) (Proposal, error) { return decodeSimBlock(data) }
func (b *stubBackend) VerifyProposal(Proposal) error                { return nil }
func (b *stubBackend) RequestProposal(uint64, uint64)               { b.requested++ }
func (b *stubBackend) Broadcast(m *Message)                         { b.sent = append(b.sent, m) }
func (b *stubBackend) Commit(p Proposal, _ uint64, _ [][]byte)      { b.committed = p }

func newStubCore(t *testing.T, net *testNet, idx int) (*Core, *stubBackend) {
	t.Helper()
	w, _, err := OpenWAL(filepath.Join(t.TempDir(), "wal"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	b := &stubBackend{set: net.set}
	c := NewCore(Config{EmptyBlockInterval: 5 * time.Second, BaseTimeout: 2 * time.Second, MaxBackoffExp: 5}, b, net.keys[idx], w, 0)
	return c, b
}

// roundChange builds validator i's ROUND-CHANGE for (height, round), prepared
// with block in preparedRound when block is non-nil.
func (net *testNet) roundChange(t *testing.T, i int, height, round uint64, block *simBlock, preparedRound uint64) *Message {
	t.Helper()
	claim := &roundChangeClaim{}
	m := &Message{Type: MsgRoundChange, Height: height, Round: round, ChainID: testChainID}
	if block != nil {
		claim.Prepared, claim.PreparedRound = true, preparedRound
		m.Digest = block.Hash()
		data, _ := block.Encode()
		m.SetExtra(encodePayload(&roundChangeExtra{Block: data, Prepares: net.prepareCert(t, height, preparedRound, block.Hash(), net.set.Quorum())}))
	}
	m.Payload = encodePayload(claim)
	must(t, m.Sign(net.keys[i]))
	return m
}

func (net *testNet) prepareCert(t *testing.T, height, round uint64, digest common.Hash, n int) []Message {
	cert := make([]Message, n)
	for i := 0; i < n; i++ {
		cert[i] = *net.signed(t, i, MsgPrepare, height, round, digest)
	}
	return cert
}

func stripped(ms ...*Message) []Message {
	out := make([]Message, len(ms))
	for i, m := range ms {
		out[i] = *m
		out[i].Extra = nil
	}
	return out
}

// TestJustify covers each way a round > 0 proposal can fail its
// justification (design §4.5), and the ways it passes.
func TestJustify(t *testing.T) {
	net := newTestNet(t, 4) // quorum 3
	c, _ := newStubCore(t, net, 0)
	c.NewHeight(10, 0)

	blockA := &simBlock{H: 10, Nonce: 1}
	blockB := &simBlock{H: 10, Nonce: 2}
	const round = 2

	fresh := stripped(net.roundChange(t, 1, 10, round, nil, 0), net.roundChange(t, 2, 10, round, nil, 0), net.roundChange(t, 3, 10, round, nil, 0))
	withA := stripped(net.roundChange(t, 1, 10, round, blockA, 1), net.roundChange(t, 2, 10, round, nil, 0), net.roundChange(t, 3, 10, round, nil, 0))
	certA := net.prepareCert(t, 10, 1, blockA.Hash(), 3)

	for _, tt := range []struct {
		name   string
		body   *preprepareBody
		digest common.Hash
		ok     bool
	}{
		{"no prepared claims: any block", &preprepareBody{RoundChanges: fresh}, blockB.Hash(), true},
		{"prepared A, re-proposes A with its quorum", &preprepareBody{RoundChanges: withA, Prepares: certA}, blockA.Hash(), true},
		{"prepared A, proposes B", &preprepareBody{RoundChanges: withA, Prepares: certA}, blockB.Hash(), false},
		{"prepared A, no PREPARE quorum", &preprepareBody{RoundChanges: withA}, blockA.Hash(), false},
		{"prepared A, quorum one short", &preprepareBody{RoundChanges: withA, Prepares: certA[:2]}, blockA.Hash(), false},
		{"prepared A, quorum from another round", &preprepareBody{RoundChanges: withA, Prepares: net.prepareCert(t, 10, 0, blockA.Hash(), 3)}, blockA.Hash(), false},
		{"too few round changes", &preprepareBody{RoundChanges: fresh[:2]}, blockB.Hash(), false},
		{"same sender three times", &preprepareBody{RoundChanges: []Message{fresh[0], fresh[0], fresh[0]}}, blockB.Hash(), false},
		{"round changes for another round", &preprepareBody{RoundChanges: stripped(
			net.roundChange(t, 1, 10, round+1, nil, 0), net.roundChange(t, 2, 10, round+1, nil, 0), net.roundChange(t, 3, 10, round+1, nil, 0))}, blockB.Hash(), false},
	} {
		err := c.justify(round, tt.body, tt.digest)
		if tt.ok && err != nil {
			t.Errorf("%s: rejected: %v", tt.name, err)
		}
		if !tt.ok && !errors.Is(err, errUnjustified) {
			t.Errorf("%s: got %v, want errUnjustified", tt.name, err)
		}
	}
}

// TestRoundChangeEvidenceRequired: a ROUND-CHANGE claiming a prepared block
// without its PREPARE quorum is refused, so it cannot steer the next proposal.
func TestRoundChangeEvidenceRequired(t *testing.T) {
	net := newTestNet(t, 4)
	c, _ := newStubCore(t, net, 0)
	c.NewHeight(10, 0)
	block := &simBlock{H: 10, Nonce: 7}

	good := net.roundChange(t, 1, 10, 1, block, 0)
	if err := c.HandleMessage(good, 0); err != nil {
		t.Fatalf("valid prepared round change: %v", err)
	}
	// A prepared claim signed without any attachment.
	noEvidence := &Message{Type: MsgRoundChange, Height: 10, Round: 1, ChainID: testChainID, Digest: block.Hash(),
		Payload: encodePayload(&roundChangeClaim{Prepared: true})}
	must(t, noEvidence.Sign(net.keys[2]))
	if err := c.HandleMessage(noEvidence, 0); !errors.Is(err, errBadCertificate) {
		t.Errorf("claim without evidence: %v", err)
	}
	// A claim signed with a quorum one short.
	shortCert := &Message{Type: MsgRoundChange, Height: 10, Round: 1, ChainID: testChainID, Digest: block.Hash(),
		Payload: encodePayload(&roundChangeClaim{Prepared: true})}
	data, _ := block.Encode()
	shortCert.SetExtra(encodePayload(&roundChangeExtra{Block: data, Prepares: net.prepareCert(t, 10, 0, block.Hash(), 2)}))
	must(t, shortCert.Sign(net.keys[3]))
	if err := c.HandleMessage(shortCert, 0); !errors.Is(err, errBadCertificate) {
		t.Errorf("claim with a short quorum: %v", err)
	}
	if len(c.rcs[1]) != 1 {
		t.Errorf("%d round changes stored, want only the valid one", len(c.rcs[1]))
	}
}

// TestLateProposalStillCommits: a node whose timer ran ahead still decides a
// block that a quorum committed in an earlier round.
func TestLateProposalStillCommits(t *testing.T) {
	net := newTestNet(t, 4)
	c, b := newStubCore(t, net, 3)
	c.NewHeight(8, 0)          // proposer of (8, 0) is validator 0
	c.Tick(100 * time.Second)  // times out into round 1
	c.Tick(1000 * time.Second) // and round 2
	if c.Round() < 2 {
		t.Fatalf("round %d, want >= 2", c.Round())
	}
	block := &simBlock{H: 8, Nonce: 3}
	data, _ := block.Encode()
	pp := &Message{Type: MsgPreprepare, Height: 8, Round: 0, ChainID: testChainID, Digest: block.Hash(),
		Payload: encodePayload(&preprepareBody{Block: data})}
	must(t, pp.Sign(net.keys[0]))
	for i := 0; i < 3; i++ {
		must(t, c.HandleMessage(net.signed(t, i, MsgCommit, 8, 0, block.Hash()), 1000*time.Second))
	}
	if b.committed != nil {
		t.Fatal("committed without knowing the block")
	}
	must(t, c.HandleMessage(pp, 1000*time.Second))
	if b.committed == nil || b.committed.Hash() != block.Hash() {
		t.Fatal("did not decide the block committed in round 0")
	}
	for _, m := range b.sent {
		if m.Type == MsgPrepare && m.Round == 0 {
			t.Error("voted in a round it had already left")
		}
	}
}

// TestRelayedRoundChangeCopies covers the relay attack from the review on
// #148: a copy of an honest ROUND-CHANGE with its attachment altered or
// stripped keeps the signing hash, so without ExtraHash it passed Verify
// and could win the dedup slot (design §7.1) ahead of the genuine message.
// Every such copy now fails Verify, before any cache could see it.
func TestRelayedRoundChangeCopies(t *testing.T) {
	net := newTestNet(t, 4)
	block := &simBlock{H: 10, Nonce: 9}
	genuine := net.roundChange(t, 1, 10, 1, block, 0)
	if _, err := genuine.Verify(testChainID, net.set); err != nil {
		t.Fatalf("genuine: %v", err)
	}

	extra := new(roundChangeExtra)
	must(t, rlp.DecodeBytes(genuine.Extra, extra))
	flippedBlock := *extra
	flippedBlock.Block = append([]byte(nil), extra.Block...)
	flippedBlock.Block[len(flippedBlock.Block)-1] ^= 1
	flippedSig := *extra
	flippedSig.Prepares = append([]Message(nil), extra.Prepares...)
	flippedSig.Prepares[0].Signature = append([]byte(nil), flippedSig.Prepares[0].Signature...)
	flippedSig.Prepares[0].Signature[5] ^= 1

	for name, extraBytes := range map[string][]byte{
		"block byte flipped":        encodePayload(&flippedBlock),
		"PREPARE signature flipped": encodePayload(&flippedSig),
		"attachment stripped":       nil,
		"attachment replaced":       []byte{0xc0},
	} {
		cpy := *genuine
		cpy.Extra = extraBytes
		if cpy.SigningHash() != genuine.SigningHash() {
			t.Fatalf("%s: the copy changed the signing hash; the test no longer models a relay", name)
		}
		if _, err := cpy.Verify(testChainID, net.set); !errors.Is(err, errExtraMismatch) {
			t.Errorf("%s: Verify = %v, want errExtraMismatch", name, err)
		}
	}
	// Quoted in a certificate, the stripped form is the valid one.
	quoted := *genuine
	quoted.Extra = nil
	if _, err := quoted.VerifyAs(Quoted, testChainID, net.set); err != nil {
		t.Errorf("stripped copy as a quote: %v", err)
	}
	if _, err := genuine.VerifyAs(Quoted, testChainID, net.set); !errors.Is(err, errUnexpectedExtra) {
		t.Errorf("unstripped quote: %v", err)
	}
}

// TestNestedExtraRejected: an attachment on a PREPARE, whether received
// directly or inside a certificate, is refused, so junk one node adds cannot
// be re-broadcast under the next proposer's signature (review on #148).
func TestNestedExtraRejected(t *testing.T) {
	net := newTestNet(t, 4)
	digest := common.HexToHash("0x42")
	junk := net.signed(t, 0, MsgPrepare, 10, 0, digest)
	junk.Extra = make([]byte, 100<<10)
	if _, err := junk.Verify(testChainID, net.set); !errors.Is(err, errUnexpectedExtra) {
		t.Errorf("PREPARE with an attachment: %v", err)
	}
	cert := net.prepareCert(t, 10, 0, digest, 3)
	cert[1].Extra = []byte{1, 2, 3}
	if err := certificate(cert, MsgPrepare, 10, 0, digest, testChainID, net.set); !errors.Is(err, errBadCertificate) {
		t.Errorf("certificate with an attachment on a member: %v", err)
	}
	hashed := net.signed(t, 0, MsgPrepare, 10, 0, digest)
	hashed.ExtraHash = common.HexToHash("0x01")
	must(t, hashed.Sign(net.keys[0]))
	if _, err := hashed.Verify(testChainID, net.set); !errors.Is(err, errUnexpectedExtra) {
		t.Errorf("PREPARE with an ExtraHash: %v", err)
	}
}
