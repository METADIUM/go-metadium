package metabft

import (
	"crypto/ecdsa"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/metabft"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/rlp"
)

const testChainID = 638200003

type testBackend struct {
	validator bool
	set       *metabft.ValidatorSet
	height    uint64
	received  []*metabft.Message
	evidence  []*metabft.Evidence
	syncFrom  []uint64
}

func (b *testBackend) IsValidator() bool { return b.validator }
func (b *testBackend) ChainID() uint64   { return testChainID }
func (b *testBackend) ValidatorSet(h uint64) (*metabft.ValidatorSet, bool) {
	return b.set, h == b.height || h == b.height+1
}
func (b *testBackend) HandleConsensus(_ *Peer, m *metabft.Message) error {
	b.received = append(b.received, m)
	return nil
}
func (b *testBackend) HandleEvidence(ev *metabft.Evidence)  { b.evidence = append(b.evidence, ev) }
func (b *testBackend) SyncStatus() (uint64, uint64)         { return b.height, 3 }
func (b *testBackend) HandleSyncReply(_ *Peer, h, r uint64) { b.syncFrom = append(b.syncFrom, h, r) }
func (b *testBackend) RunPeer(p *Peer, h func(*Peer) error) error {
	return h(p)
}
func (b *testBackend) PeerInfo(enode.ID) interface{} { return nil }

type testKeys []*ecdsa.PrivateKey

func newTestSet(t *testing.T, n int) (testKeys, *metabft.ValidatorSet) {
	t.Helper()
	keys := make(testKeys, n)
	pubs := make([][]byte, n)
	for i := range keys {
		k, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[i], pubs[i] = k, crypto.FromECDSAPub(&k.PublicKey)[1:]
	}
	set, err := metabft.NewValidatorSet(pubs)
	if err != nil {
		t.Fatal(err)
	}
	return keys, set
}

func signed(t *testing.T, key *ecdsa.PrivateKey, typ metabft.MsgType, height, round uint64, digest common.Hash) *metabft.Message {
	t.Helper()
	m := &metabft.Message{Type: typ, Height: height, Round: round, ChainID: testChainID, Digest: digest}
	if typ == metabft.MsgCommit {
		seal, err := metabft.SignCommitSeal(metabft.CommitDigest(digest, round, testChainID), key)
		if err != nil {
			t.Fatal(err)
		}
		m.CommitSeal = seal
	}
	if typ == metabft.MsgRoundChange {
		m.Payload, _ = rlp.EncodeToBytes([]interface{}{false, uint64(0)}) // "nothing prepared"
	}
	if err := m.Sign(key); err != nil {
		t.Fatal(err)
	}
	return m
}

// pipe returns a peer to write into and a function that handles one message
// on the receiving side, through the real wire encoding.
func pipe(t *testing.T, b *testBackend, cache *Cache) (*Peer, func() error) {
	t.Helper()
	app, net := p2p.MsgPipe()
	t.Cleanup(func() { app.Close(); net.Close() })
	var id enode.ID
	id[0] = 1
	sender := NewPeer(METABFT1, p2p.NewPeer(id, "sender", nil), app)
	receiver := NewPeer(METABFT1, p2p.NewPeer(id, "receiver", nil), net)
	return sender, func() error { return HandleMessage(b, cache, receiver) }
}

// send writes m and handles it, returning the handler's error.
func send(t *testing.T, sender *Peer, handle func() error, m *metabft.Message) error {
	t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- sender.SendConsensus(m) }()
	err := handle()
	if serr := <-errc; serr != nil {
		t.Fatalf("send: %v", serr)
	}
	return err
}

func TestConsensusRoundTrip(t *testing.T) {
	keys, set := newTestSet(t, 4)
	b := &testBackend{validator: true, set: set, height: 10}
	sender, handle := pipe(t, b, NewCache(DefaultCacheSize))
	for _, typ := range []metabft.MsgType{metabft.MsgPreprepare, metabft.MsgPrepare, metabft.MsgCommit, metabft.MsgRoundChange} {
		m := signed(t, keys[1], typ, 10, 0, common.HexToHash("0xaa"))
		if err := send(t, sender, handle, m); err != nil {
			t.Fatalf("%v: %v", typ, err)
		}
	}
	if len(b.received) != 4 {
		t.Fatalf("received %d messages, want 4", len(b.received))
	}
	if idx, err := b.received[2].Verify(testChainID, set); err != nil || idx != 1 {
		t.Errorf("COMMIT after the wire: %d, %v", idx, err)
	}
}

// TestForgedSenderCannotPreempt (P4-T2, P4-T3): a message signed by someone
// outside the set is dropped before the cache, so the genuine one that
// follows is still fresh.
func TestForgedSenderCannotPreempt(t *testing.T) {
	keys, set := newTestSet(t, 4)
	outsider, _ := newTestSet(t, 1)
	b := &testBackend{validator: true, set: set, height: 10}
	cache := NewCache(DefaultCacheSize)
	sender, handle := pipe(t, b, cache)

	forged := signed(t, outsider[0], metabft.MsgPrepare, 10, 0, common.HexToHash("0xbad"))
	if err := send(t, sender, handle, forged); err != nil {
		t.Fatalf("a non-validator's message disconnected the relaying peer: %v", err)
	}
	if cache.Len() != 0 || len(b.received) != 0 {
		t.Fatal("a non-validator's message reached the cache or the engine")
	}
	genuine := signed(t, keys[2], metabft.MsgPrepare, 10, 0, common.HexToHash("0xaa"))
	if err := send(t, sender, handle, genuine); err != nil || len(b.received) != 1 {
		t.Fatalf("genuine message after the forgery: %v, %d received", err, len(b.received))
	}

	// A broken signature is not an honest mistake: the peer is dropped.
	broken := signed(t, keys[2], metabft.MsgPrepare, 10, 1, common.HexToHash("0xaa"))
	broken.Signature = broken.Signature[:10]
	if err := send(t, sender, handle, broken); !errors.Is(err, errMisbehaving) {
		t.Errorf("truncated signature: %v", err)
	}
}

// TestEquivocationEvidence (P4-T4): two PREPAREs from one validator for one
// round produce evidence; only the first reaches the engine.
func TestEquivocationEvidence(t *testing.T) {
	keys, set := newTestSet(t, 4)
	b := &testBackend{validator: true, set: set, height: 10}
	sender, handle := pipe(t, b, NewCache(DefaultCacheSize))

	a := signed(t, keys[3], metabft.MsgPrepare, 10, 0, common.HexToHash("0xaa"))
	bb := signed(t, keys[3], metabft.MsgPrepare, 10, 0, common.HexToHash("0xbb"))
	for _, m := range []*metabft.Message{a, a, bb} {
		if err := send(t, sender, handle, m); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.received) != 1 || b.received[0].Digest != a.Digest {
		t.Fatalf("engine got %d messages, want only the first", len(b.received))
	}
	if len(b.evidence) != 1 {
		t.Fatalf("%d pieces of evidence, want 1", len(b.evidence))
	}
	if idx, err := b.evidence[0].Verify(testChainID, set); err != nil || idx != 3 {
		t.Errorf("evidence does not verify against validator 3: %d, %v", idx, err)
	}

	// ROUND-CHANGE: same (zero) digest, different content is an equivocation too.
	rc1 := signed(t, keys[1], metabft.MsgRoundChange, 10, 2, common.Hash{})
	rc2 := &metabft.Message{Type: metabft.MsgRoundChange, Height: 10, Round: 2, ChainID: testChainID, Payload: []byte{0xc1, 0x80}}
	if err := rc2.Sign(keys[1]); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*metabft.Message{rc1, rc2} {
		if err := send(t, sender, handle, m); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.evidence) != 2 {
		t.Errorf("two ROUND-CHANGEs for one round: %d pieces of evidence, want 2", len(b.evidence))
	}
}

// TestRelayedCopyDisconnects: a relayed ROUND-CHANGE with its attachment
// stripped or altered fails Verify, so it can never take the genuine
// message's cache slot, and the relay is dropped (review on #148).
func TestRelayedCopyDisconnects(t *testing.T) {
	keys, set := newTestSet(t, 4)
	b := &testBackend{validator: true, set: set, height: 10}
	cache := NewCache(DefaultCacheSize)
	sender, handle := pipe(t, b, cache)

	genuine := &metabft.Message{Type: metabft.MsgRoundChange, Height: 10, Round: 1, ChainID: testChainID,
		Digest: common.HexToHash("0xaa"), Payload: []byte{0xc2, 0x01, 0x80}}
	genuine.SetExtra([]byte{0xc0})
	if err := genuine.Sign(keys[1]); err != nil {
		t.Fatal(err)
	}
	for name, extra := range map[string][]byte{"stripped": nil, "altered": {0xc1, 0x80}} {
		cpy := *genuine
		cpy.Extra = extra
		if err := send(t, sender, handle, &cpy); !errors.Is(err, errMisbehaving) {
			t.Errorf("%s copy: %v", name, err)
		}
	}
	if cache.Len() != 0 {
		t.Fatal("a relayed copy took a cache slot")
	}
	if err := send(t, sender, handle, genuine); err != nil || len(b.received) != 1 {
		t.Errorf("genuine after the copies: %v, %d received", err, len(b.received))
	}
}

func TestHandlerRejects(t *testing.T) {
	keys, set := newTestSet(t, 4)
	b := &testBackend{validator: true, set: set, height: 10}
	sender, handle := pipe(t, b, NewCache(DefaultCacheSize))

	// A PREPARE sent under the COMMIT code.
	m := signed(t, keys[0], metabft.MsgPrepare, 10, 0, common.HexToHash("0xaa"))
	errc := make(chan error, 1)
	go func() { errc <- p2p.Send(sender.rw, CommitMsg, m) }()
	if err := handle(); !errors.Is(err, errCodeMismatch) {
		t.Errorf("code mismatch: %v", err)
	}
	<-errc

	// A message for a height the node does not take is dropped quietly.
	far := signed(t, keys[0], metabft.MsgPrepare, 50, 0, common.HexToHash("0xaa"))
	if err := send(t, sender, handle, far); err != nil || len(b.received) != 0 {
		t.Errorf("far height: %v, %d received", err, len(b.received))
	}
	// Another chain's message is not a peer we want.
	other := signed(t, keys[0], metabft.MsgPrepare, 10, 0, common.HexToHash("0xaa"))
	other.ChainID = 11
	other.Sign(keys[0])
	if err := send(t, sender, handle, other); !errors.Is(err, errMisbehaving) {
		t.Errorf("other chain: %v", err)
	}
}

func TestSyncRoundTrip(t *testing.T) {
	_, set := newTestSet(t, 4)
	b := &testBackend{validator: true, set: set, height: 42}
	app, net := p2p.MsgPipe()
	defer app.Close()
	var id enode.ID
	a := NewPeer(METABFT1, p2p.NewPeer(id, "a", nil), app)
	z := NewPeer(METABFT1, p2p.NewPeer(id, "z", nil), net)
	cache := NewCache(DefaultCacheSize)

	// MsgPipe writes block until read, so each side runs concurrently.
	go a.RequestSync()
	replied := make(chan error, 1)
	go func() { replied <- HandleMessage(b, cache, z) }() // z answers the request
	if err := HandleMessage(b, cache, a); err != nil {    // a reads the reply
		t.Fatal(err)
	}
	if err := <-replied; err != nil {
		t.Fatal(err)
	}
	if len(b.syncFrom) != 2 || b.syncFrom[0] != 42 || b.syncFrom[1] != 3 {
		t.Errorf("sync reply %v, want [42 3]", b.syncFrom)
	}
}

// TestNonValidatorAdvertisesNothing (P4-T5).
func TestNonValidatorAdvertisesNothing(t *testing.T) {
	if p := MakeProtocols(&testBackend{validator: false}, NewCache(1)); len(p) != 0 {
		t.Errorf("a non-validator advertises %d metabft protocols", len(p))
	}
	p := MakeProtocols(&testBackend{validator: true}, NewCache(1))
	if len(p) != 1 || p[0].Name != "metabft" || p[0].Version != 1 || p[0].Length != 8 {
		t.Errorf("validator protocols: %+v", p)
	}
}

func TestCacheBoundsAndPrune(t *testing.T) {
	keys, _ := newTestSet(t, 2)
	signer := crypto.FromECDSAPub(&keys[0].PublicKey)[1:]
	c := NewCache(2)
	for h := uint64(1); h <= 2; h++ {
		if v, _ := c.Add(signer, signed(t, keys[0], metabft.MsgPrepare, h, 0, common.HexToHash("0xaa"))); v != Fresh {
			t.Fatalf("height %d: %v", h, v)
		}
	}
	if v, _ := c.Add(signer, signed(t, keys[0], metabft.MsgPrepare, 3, 0, common.HexToHash("0xaa"))); v != Full {
		t.Errorf("third entry into a cache of two: %v", v)
	}
	c.Prune(2)
	if c.Len() != 1 {
		t.Errorf("after pruning below 2: %d entries", c.Len())
	}
	if v, _ := c.Add(signer, signed(t, keys[0], metabft.MsgPrepare, 3, 0, common.HexToHash("0xaa"))); v != Fresh {
		t.Errorf("after pruning: %v", v)
	}
}
