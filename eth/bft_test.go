package eth

import (
	"bytes"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/consensus/metabft"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	bftproto "github.com/ethereum/go-ethereum/eth/protocols/metabft"
	"github.com/ethereum/go-ethereum/event"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
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
	if err := s.HandleConsensus(stranger, nil, relayed); err != nil || len(delivered) != 1 || delivered[0] != relayed {
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

// TestBftServiceRelaysVotes: a fresh vote is relayed once to the other
// admitted validators, not back to the peer it came from; a PRE-PREPARE is
// not relayed, and a vote under this node's own key is neither counted nor
// relayed (§11.2 S-13).
func TestBftServiceRelaysVotes(t *testing.T) {
	s, keys, _ := newTestBftService(t, 4)
	peers := make([]*bftPeer, 3)
	for i := range peers {
		peer, _ := testPeer(t, s, keys[i+1], byte(i+1))
		peers[i] = &bftPeer{Peer: peer, key: crypto.FromECDSAPub(&keys[i+1].PublicKey)[1:], queue: make(chan *metabft.Message, bftPeerQueue)}
		peers[i].admitted.Store(i < 2) // the third is not a validator
		s.peers[peer.ID()] = peers[i]
	}
	var delivered []*metabft.Message
	s.deliver = func(m *metabft.Message) { delivered = append(delivered, m) }
	queued := func() []int {
		n := make([]int, len(peers))
		for i, p := range peers {
			n[i] = len(p.queue)
			for len(p.queue) > 0 {
				<-p.queue
			}
		}
		return n
	}
	for _, typ := range []metabft.MsgType{metabft.MsgPrepare, metabft.MsgCommit, metabft.MsgRoundChange} {
		m := &metabft.Message{Type: typ, Height: 1}
		if err := s.HandleConsensus(peers[0].Peer, peers[0].key, m); err != nil {
			t.Fatal(err)
		}
		if n := queued(); n[0] != 0 || n[1] != 1 || n[2] != 0 {
			t.Errorf("%v relayed as %v, want [0 1 0]: to the other admitted validator only", typ, n)
		}
	}
	if len(delivered) != 3 {
		t.Errorf("%d votes counted, want 3", len(delivered))
	}
	if err := s.HandleConsensus(peers[0].Peer, peers[0].key, &metabft.Message{Type: metabft.MsgPreprepare, Height: 1}); err != nil {
		t.Fatal(err)
	}
	if n := queued(); n[1] != 0 {
		t.Errorf("a PRE-PREPARE was relayed")
	}
	delivered = nil
	if err := s.HandleConsensus(peers[0].Peer, s.self, &metabft.Message{Type: metabft.MsgPrepare, Height: 2}); err != nil {
		t.Fatal(err)
	}
	if n := queued(); len(delivered) != 0 || n[1] != 0 {
		t.Errorf("a vote under this node's own key was counted (%d) or relayed (%v)", len(delivered), n)
	}
}

// TestBftServiceTwinKey: two servers with one key prepare different blocks.
// Whichever of the two PREPAREs reaches the cache first, the other is
// evidence against this node's own key, and is relayed (§11.2 S-13).
func TestBftServiceTwinKey(t *testing.T) {
	for _, ownFirst := range []bool{true, false} {
		s, keys, _ := newTestBftService(t, 4)
		prepare := func(digest byte) *metabft.Message {
			m := &metabft.Message{Type: metabft.MsgPrepare, Height: 1, ChainID: bftTestChainID, Digest: common.Hash{digest}}
			if err := m.Sign(keys[0]); err != nil {
				t.Fatal(err)
			}
			return m
		}
		own, twin := prepare(1), prepare(2)
		relay, _ := testPeer(t, s, keys[1], 1)
		other := &bftPeer{Peer: relay, key: crypto.FromECDSAPub(&keys[1].PublicKey)[1:], queue: make(chan *metabft.Message, bftPeerQueue)}
		other.admitted.Store(true)
		s.peers["other"] = other
		// What the protocol handler does with the twin's vote, relayed.
		arrive := func() {
			switch verdict, ev := s.cache.Add(s.self, twin); verdict {
			case bftproto.Fresh:
				s.HandleConsensus(relay, s.self, twin)
			case bftproto.Conflict:
				s.HandleEvidence(ev)
			}
		}
		if ownFirst {
			s.broadcast(own)
			arrive()
		} else {
			arrive()
			s.broadcast(own)
		}
		evs, err := s.evidence.List()
		if err != nil || len(evs) != 1 {
			t.Fatalf("own first %v: %d pieces of evidence, want 1 (%v)", ownFirst, len(evs), err)
		}
		signer, _ := evs[0].Second.Signer()
		if !bytes.Equal(signer, s.self) {
			t.Errorf("own first %v: the evidence is against another key", ownFirst)
		}
		// Both votes went out: own by broadcast, the other as the second of
		// the stored pair, so peers that saw only one see the conflict too.
		sent := map[common.Hash]bool{}
		for len(other.queue) > 0 {
			sent[(<-other.queue).Digest] = true
		}
		if !sent[own.Digest] || !sent[twin.Digest] {
			t.Errorf("own first %v: sent %v, want both digests", ownFirst, sent)
		}
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

// TestBftServiceBlockCreationTime: governance's blockCreationTime longer
// than the genesis empty-block interval is warned about, once per value
// (design §4.5).
func TestBftServiceBlockCreationTime(t *testing.T) {
	s, _, _ := newTestBftService(t, 4) // emptyBlockInterval 5s
	old := metaminer.GetBlockBuildParametersFunc
	t.Cleanup(func() { metaminer.GetBlockBuildParametersFunc = old })
	interval := int64(2000)
	metaminer.GetBlockBuildParametersFunc = func(*big.Int) (int64, *big.Int, *big.Int, int64, int64, error) {
		return interval, new(big.Int), new(big.Int), 0, 100, nil
	}
	head := big.NewInt(7)
	if s.checkBlockCreationTime(head) {
		t.Error("warned about 2s under a 5s empty-block interval")
	}
	interval = 10_000
	if !s.checkBlockCreationTime(head) {
		t.Error("no warning for 10s over a 5s empty-block interval")
	}
	if s.checkBlockCreationTime(head) {
		t.Error("warned twice for the same value")
	}
}

// TestPbftNodeStarts: with the startup guard gone, a node on a PBFT genesis
// starts, runs the PBFT service, advertises metabft/1 and keeps its WAL in
// the data directory. It advertises because governance cannot be read when
// eth.New runs (the metadium admin starts later), not because it knows it is
// a validator; peers admit it only once it is in their set.
func TestPbftNodeStarts(t *testing.T) {
	old := params.ConsensusMethod
	params.ConsensusMethod = params.ConsensusPoA
	t.Cleanup(func() { params.ConsensusMethod = old })

	s, _, _ := newTestBftService(t, 4) // for its chain config
	genesis := &core.Genesis{Config: s.bc.Config(), Difficulty: big.NewInt(1), GasLimit: 10_000_000}
	dir := t.TempDir()
	stack, err := node.New(&node.Config{DataDir: dir, P2P: p2p.Config{NoDiscovery: true, MaxPeers: 0}})
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()
	config := ethconfig.Defaults
	config.Genesis = genesis
	backend, err := New(stack, &config)
	if err != nil {
		t.Fatalf("a node on a PBFT genesis: %v", err)
	}
	if backend.bft == nil {
		t.Fatal("no PBFT service on a PBFT chain")
	}
	advertised := false
	for _, p := range backend.Protocols() {
		advertised = advertised || p.Name == bftproto.ProtocolName
	}
	if !advertised {
		t.Error("metabft/1 not among the protocols")
	}
	if err := stack.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stack.ResolvePath(filepath.Join("metabft", "wal"))); err != nil {
		t.Errorf("WAL: %v", err)
	}
}

// TestBftAPI: the metabft namespace on a node before the switch.
func TestBftAPI(t *testing.T) {
	s, keys, set := newTestBftService(t, 4)
	api := &BftAPI{s}

	vals, err := api.GetValidators(nil)
	if err != nil || len(vals) != 4 || !bytes.Equal(vals[0].NodeID, crypto.FromECDSAPub(&keys[0].PublicKey)[1:]) {
		t.Fatalf("validators: %v, %v", vals, err)
	}
	// A historical query reads governance directly, leaving the cache alone.
	before := s.sets.Len()
	h := hexutil.Uint64(7)
	if vals, err := api.GetValidators(&h); err != nil || len(vals) != 4 || s.sets.Len() != before {
		t.Errorf("validators at 7: %d, %v; cache %d -> %d", len(vals), err, before, s.sets.Len())
	}
	if rs := api.GetRoundState(); rs.Height != 0 || rs.Proposer != nil {
		t.Errorf("round state before the node runs: %+v", rs)
	}
	st := api.Status()
	if !st.Validator || st.Peers != 0 || st.LastRejection != nil || len(st.ExcludedTxs) != 0 {
		t.Errorf("status: %+v", st)
	}
	s.engine.ExcludeTx(common.Hash{0xe1}, common.Address{0xa1}, 7, "leaves 3 governance nodes")
	if ex := api.Status().ExcludedTxs; len(ex) != 1 || ex[0].Nonce != 7 || ex[0].From != (common.Address{0xa1}) {
		t.Errorf("excluded transactions in status: %+v", ex)
	}

	r := api.Readiness()
	if !r.Ready || !r.Governance || r.Validators != 4 || !r.InSet || r.BlocksLeft != 1 {
		t.Errorf("readiness with four validators: %+v", r)
	}
	// Three governance nodes: the switch would stop the chain (design §9.3).
	s.validators = func(uint64) (*metabft.ValidatorSet, error) {
		return nil, fmt.Errorf("%w: 3 governance nodes", metabft.ErrTooFewValidators)
	}
	if r := api.Readiness(); r.Ready || !r.Governance || len(r.Problems) != 1 {
		t.Errorf("readiness with three: %+v", r)
	}
	s.validators = func(uint64) (*metabft.ValidatorSet, error) { return nil, errors.New("not initialized") }
	if r := api.Readiness(); r.Ready || r.Governance {
		t.Errorf("readiness without governance: %+v", r)
	}

	if ev, err := api.GetEvidence(); err != nil || len(ev) != 0 {
		t.Fatalf("evidence on a fresh node: %v, %v", ev, err)
	}
	a := &metabft.Message{Type: metabft.MsgPrepare, Height: 3, ChainID: bftTestChainID, Digest: common.Hash{1}}
	b := &metabft.Message{Type: metabft.MsgPrepare, Height: 3, ChainID: bftTestChainID, Digest: common.Hash{2}}
	a.Sign(keys[1])
	b.Sign(keys[1])
	s.validators = func(uint64) (*metabft.ValidatorSet, error) { return set, nil }
	s.sets.Purge()
	s.HandleEvidence(&metabft.Evidence{First: *a, Second: *b})
	ev, err := api.GetEvidence()
	if err != nil || len(ev) != 1 || ev[0].First != a.Digest || ev[0].Second != b.Digest || ev[0].Type != "PREPARE" {
		t.Fatalf("evidence after an equivocation: %v, %v", ev, err)
	}
	if !bytes.Equal(ev[0].Signer, crypto.FromECDSAPub(&keys[1].PublicKey)[1:]) {
		t.Errorf("evidence signer %x", ev[0].Signer)
	}
	// Raw is the evidence itself: it decodes and verifies on its own.
	var decoded metabft.Evidence
	if err := rlp.DecodeBytes(ev[0].Raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err := decoded.Verify(bftTestChainID, set); err != nil {
		t.Errorf("raw evidence does not verify: %v", err)
	}
}
