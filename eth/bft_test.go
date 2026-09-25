package eth

import (
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/consensus/metabft"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	bftproto "github.com/ethereum/go-ethereum/eth/protocols/metabft"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
)

const bftTestChainID = 638200003

// newTestBftService is the service on a PBFT chain still at its genesis,
// with a validator set of n keys, the first of them this node's.
func newTestBftService(t *testing.T, n int) (*bftService, []*ecdsa.PrivateKey, *metabft.ValidatorSet) {
	t.Helper()
	keys := make([]*ecdsa.PrivateKey, n)
	pubs := make([][]byte, n)
	for i := range keys {
		keys[i], _ = crypto.GenerateKey()
		pubs[i] = crypto.FromECDSAPub(&keys[i].PublicKey)[1:]
	}
	set, err := metabft.NewValidatorSet(pubs)
	if err != nil {
		t.Fatal(err)
	}
	config := &params.ChainConfig{
		ChainID:        big.NewInt(bftTestChainID),
		HomesteadBlock: big.NewInt(0), EIP150Block: big.NewInt(0), EIP155Block: big.NewInt(0), EIP158Block: big.NewInt(0),
		ByzantiumBlock: big.NewInt(0), ConstantinopleBlock: big.NewInt(0), PetersburgBlock: big.NewInt(0), IstanbulBlock: big.NewInt(0),
		CamelliaBlock: big.NewInt(0), BftBlock: big.NewInt(1),
		Bft:    &params.BftConfig{EmptyBlockInterval: 5, BaseTimeout: 2, MaxBackoffExp: 5, TimeDrift: 2},
		Ethash: new(params.EthashConfig),
	}
	validators := func(uint64) (*metabft.ValidatorSet, error) { return set, nil }
	engine := metabft.NewEngine(ethash.NewFaker(), validators)
	bc, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), nil, &core.Genesis{Config: config, Difficulty: big.NewInt(1)}, nil, engine, vm.Config{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bc.Stop)
	s, err := newBftService(t.TempDir(), keys[0], bc, engine, validators, new(event.TypeMux))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.wal.Close() })
	return s, keys, set
}

// testPeer is a metabft/1 peer on a message pipe, keyed as key.
func testPeer(t *testing.T, s *bftService, key *ecdsa.PrivateKey, id byte) (*bftproto.Peer, p2p.MsgReadWriter) {
	t.Helper()
	local, remote := p2p.MsgPipe()
	t.Cleanup(func() { local.Close(); remote.Close() })
	var nid enode.ID
	nid[0] = id
	peer := bftproto.NewPeer(bftproto.METABFT1, p2p.NewPeer(nid, "peer", nil), local)
	keys := s.peerKey
	s.peerKey = func(p *bftproto.Peer) []byte {
		if p == peer {
			return crypto.FromECDSAPub(&key.PublicKey)[1:]
		}
		return keys(p)
	}
	return peer, remote
}

// TestBftServiceAdmitsValidatorsOnly: any node can advertise metabft/1, so
// only a peer whose node key is in the validator set is sent to or heard
// from (review on #149). One that is not stays connected, since ending the
// protocol would end the peer's eth connection too, and is admitted once it
// joins the set.
func TestBftServiceAdmitsValidatorsOnly(t *testing.T) {
	s, _, set := newTestBftService(t, 4)
	if !s.IsValidator() {
		t.Fatal("a validator does not advertise metabft/1")
	}
	outsider, _ := crypto.GenerateKey()
	stranger, remote := testPeer(t, s, outsider, 1)
	running, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.RunPeer(stranger, func(*bftproto.Peer) error { close(running); <-release; return nil })
	}()
	// Everything the stranger receives, in order.
	received := make(chan uint64, 16)
	go func() {
		for {
			msg, err := remote.ReadMsg()
			if err != nil {
				return
			}
			msg.Discard()
			received <- msg.Code
		}
	}()
	next := func(within time.Duration) (uint64, bool) {
		select {
		case code := <-received:
			return code, true
		case <-time.After(within):
			return 0, false
		}
	}
	select {
	case <-running:
	case <-time.After(5 * time.Second):
		t.Fatal("peer handler not run")
	}
	if s.peerAdmitted(stranger) {
		t.Fatal("a non-validator peer is admitted")
	}
	s.broadcast(&metabft.Message{Type: metabft.MsgPrepare, Height: 1})
	if code, ok := next(300 * time.Millisecond); ok {
		t.Fatalf("a non-validator was sent message %#x", code)
	}
	// A message it relays still counts: it was verified against the set
	// before it got here, and the cache has recorded it, so dropping it
	// would lose it for good (review on #156).
	var delivered []*metabft.Message
	s.deliver = func(m *metabft.Message) { delivered = append(delivered, m) }
	relayed := &metabft.Message{Type: metabft.MsgPrepare, Height: 1}
	if err := s.HandleConsensus(stranger, relayed); err != nil || len(delivered) != 1 || delivered[0] != relayed {
		t.Fatalf("a verified message relayed by a non-admitted peer was not passed on: %v, %d", err, len(delivered))
	}

	// Governance adds it: admitted on the next head, and asked where it is.
	pubs := [][]byte{crypto.FromECDSAPub(&outsider.PublicKey)[1:]}
	for _, v := range set.Validators() {
		pubs = append(pubs, append([]byte{}, v.PubKey[:]...))
	}
	grown, err := metabft.NewValidatorSet(pubs)
	if err != nil {
		t.Fatal(err)
	}
	s.validators = func(uint64) (*metabft.ValidatorSet, error) { return grown, nil }
	s.sets.Purge()
	s.onHead(0)
	if !s.peerAdmitted(stranger) {
		t.Fatal("peer not admitted after joining the set")
	}
	if code, ok := next(5 * time.Second); !ok || code != bftproto.SyncRequestMsg {
		t.Fatalf("first message to a newly admitted peer: %#x, %v", code, ok)
	}
	s.broadcast(&metabft.Message{Type: metabft.MsgPrepare, Height: 1})
	if code, ok := next(5 * time.Second); !ok || code != bftproto.PrepareMsg {
		t.Fatalf("broadcast to an admitted peer: %#x, %v", code, ok)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(s.peers) != 0 {
		t.Error("peer still registered after it left")
	}

}

// TestBftServiceBroadcastDoesNotBlock: a peer that reads nothing costs
// dropped messages, not a stalled event loop (review on #151).
func TestBftServiceBroadcastDoesNotBlock(t *testing.T) {
	s, keys, _ := newTestBftService(t, 4)
	peer, _ := testPeer(t, s, keys[1], 1)
	stuck := &bftPeer{Peer: peer, queue: make(chan *metabft.Message, bftPeerQueue)} // nobody drains it
	stuck.admitted.Store(true)
	s.peers["stuck"] = stuck
	done := make(chan struct{})
	go func() {
		for i := 0; i < 3*bftPeerQueue; i++ {
			s.broadcast(&metabft.Message{Type: metabft.MsgPrepare, Height: 1, Round: uint64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("broadcast blocked on a peer that does not read")
	}
	if len(stuck.queue) != bftPeerQueue {
		t.Errorf("queue holds %d, want it full at %d", len(stuck.queue), bftPeerQueue)
	}
}

// TestBftServiceUnknownSignerBudget: messages from signers outside the set
// are tolerated up to a budget per window, then the peer is dropped.
func TestBftServiceUnknownSignerBudget(t *testing.T) {
	p := &bftPeer{}
	now := time.Now()
	for i := 0; i < bftUnknownBudget; i++ {
		if err := p.unknownSigner(now); err != nil {
			t.Fatalf("message %d within the budget: %v", i, err)
		}
	}
	if err := p.unknownSigner(now); err == nil {
		t.Fatal("over the budget, the peer is kept")
	}
	if err := p.unknownSigner(now.Add(bftUnknownWindow + time.Second)); err != nil {
		t.Errorf("a new window: %v", err)
	}
}

// TestBftServicePrunesOnHead runs the dedup cache through more heights than
// it can hold unpruned: pruning on each head keeps it bounded, where
// without it the cache would fill and drop every message (review on #149).
func TestBftServicePrunesOnHead(t *testing.T) {
	s, keys, _ := newTestBftService(t, 4)
	perHeight := 2*len(keys) + 1 // a PREPARE and a COMMIT from each, one PRE-PREPARE
	heights := bftproto.DefaultCacheSize/perHeight + 100
	for h := uint64(1); h <= uint64(heights); h++ {
		for i, k := range keys {
			for _, typ := range []metabft.MsgType{metabft.MsgPrepare, metabft.MsgCommit} {
				pub := crypto.FromECDSAPub(&k.PublicKey)[1:]
				if v, _ := s.cache.Add(pub, &metabft.Message{Type: typ, Height: h, Digest: common.Hash{byte(i)}}); v != bftproto.Fresh {
					t.Fatalf("height %d: message refused (%v) with %d entries", h, v, s.cache.Len())
				}
			}
		}
		pub := crypto.FromECDSAPub(&keys[0].PublicKey)[1:]
		s.cache.Add(pub, &metabft.Message{Type: metabft.MsgPreprepare, Height: h})
		s.onHead(h) // height h is written
	}
	if n := s.cache.Len(); n > perHeight {
		t.Errorf("%d entries left after %d heights", n, heights)
	}
}
