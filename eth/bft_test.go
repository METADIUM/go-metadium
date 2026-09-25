package eth

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
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
// only a peer whose node key is in the validator set is run (review on #149).
func TestBftServiceAdmitsValidatorsOnly(t *testing.T) {
	s, keys, _ := newTestBftService(t, 4)
	if !s.IsValidator() {
		t.Fatal("a validator does not advertise metabft/1")
	}
	outsider, _ := crypto.GenerateKey()
	stranger, _ := testPeer(t, s, outsider, 1)
	refused := make(chan error, 1)
	go func() {
		refused <- s.RunPeer(stranger, func(*bftproto.Peer) error { return errors.New("handler ran") })
	}()
	select {
	case err := <-refused:
		if !errors.Is(err, errNotValidatorPeer) {
			t.Fatalf("non-validator peer: %v", err)
		}
	case <-time.After(5 * time.Second): // admitted: blocked writing the sync request nobody reads
		t.Fatal("non-validator peer admitted")
	}

	validator, remote := testPeer(t, s, keys[1], 2)
	done := make(chan error, 1)
	go func() {
		done <- s.RunPeer(validator, func(*bftproto.Peer) error {
			s.mu.RLock()
			_, registered := s.peers[validator.ID()]
			s.mu.RUnlock()
			if !registered {
				return errors.New("admitted peer not registered")
			}
			return nil
		})
	}()
	msg, err := remote.ReadMsg() // the sync request sent on admission
	if err != nil || msg.Code != bftproto.SyncRequestMsg {
		t.Fatalf("first message to an admitted peer: %v, %v", msg.Code, err)
	}
	msg.Discard()
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
// the data directory.
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
