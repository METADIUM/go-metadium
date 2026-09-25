package eth

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/consensus/metabft"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/crypto"
	bftproto "github.com/ethereum/go-ethereum/eth/protocols/metabft"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
)

// bftService runs PBFT consensus on a PBFT chain: it owns the consensus node,
// its WAL and evidence store, and speaks metabft/1 to the other validators
// (docs/pbft-consensus-design.md §6, §7.1, §7.3). It implements the
// protocol's Backend.
type bftService struct {
	chainID   uint64
	bc        *core.BlockChain
	engine    *metabft.Engine
	node      *metabft.Node
	wal       *metabft.WAL
	cache     *bftproto.Cache
	evidence  *metabft.EvidenceStore
	self      []byte // this node's public key
	validator bool   // advertises metabft/1

	validators metabft.ValidatorsFunc
	sets       *lru.Cache[uint64, *metabft.ValidatorSet]
	peerKey    func(*bftproto.Peer) []byte // the peer's node key; replaced in tests

	mu    sync.RWMutex
	peers map[string]*bftPeer

	quit chan struct{}
	wg   sync.WaitGroup

	emptyInterval     time.Duration // bft.emptyBlockInterval, from the genesis
	blockIntervalSeen int64         // last governance blockCreationTime checked, ms
}

// bftPeer is a peer on metabft/1 with its outbound queue. Only an admitted
// one, a validator in the current set, is sent to or listened to.
type bftPeer struct {
	*bftproto.Peer
	key      []byte
	admitted atomic.Bool
	queue    chan *metabft.Message

	mu           sync.Mutex
	unknownStart time.Time
	unknownCount int
}

const (
	// bftPeerQueue bounds messages waiting for one peer. Broadcast never
	// blocks: a slow peer loses messages rather than stalling the node's
	// event loop, and round changes recover what it missed (review on #151).
	bftPeerQueue = 256

	// A peer may relay this many validly signed messages from signers
	// outside the set per bftUnknownWindow, each costing a signature
	// recovery here, before it is dropped (review on #149). A set change in
	// flight produces a few; a flood is not that.
	bftUnknownBudget = 256
	bftUnknownWindow = time.Minute

	// bftSetCache bounds the validator sets kept by height.
	bftSetCache = 64
)

var errNotValidatorPeer = errors.New("peer is not a validator")

// newBftService builds the service; Start runs it. dir holds the WAL and
// the evidence store.
func newBftService(dir string, key *ecdsa.PrivateKey, bc *core.BlockChain, engine *metabft.Engine,
	validators metabft.ValidatorsFunc, mux *event.TypeMux) (*bftService, error) {
	config := bc.Config()
	if config.BftBlock == nil || config.Bft == nil {
		return nil, errors.New("not a PBFT chain")
	}
	head := bc.CurrentBlock().Number.Uint64()
	bftBlock := config.BftBlock.Uint64()
	wal, observerUntil, err := metabft.OpenNodeWAL(filepath.Join(dir, "wal"), head, bftBlock)
	if err != nil {
		return nil, fmt.Errorf("PBFT WAL: %w", err)
	}
	if observerUntil > 0 {
		log.Warn("PBFT WAL missing or corrupt: signing nothing until the height commits without this node", "height", observerUntil)
	}
	evidence, err := metabft.OpenEvidenceStore(filepath.Join(dir, "evidence"))
	if err != nil {
		wal.Close()
		return nil, err
	}
	s := &bftService{
		chainID:    config.ChainID.Uint64(),
		bc:         bc,
		engine:     engine,
		wal:        wal,
		cache:      bftproto.NewCache(bftproto.DefaultCacheSize),
		evidence:   evidence,
		self:       crypto.FromECDSAPub(&key.PublicKey)[1:],
		validators: validators,
		sets:       lru.NewCache[uint64, *metabft.ValidatorSet](bftSetCache),
		peers:      make(map[string]*bftPeer),
		quit:       make(chan struct{}),
		peerKey:    nodeKeyOf,

		emptyInterval: time.Duration(config.Bft.EmptyBlockInterval) * time.Second,
	}
	// The genesis sets the empty-block interval of a PBFT chain: it enters
	// every validator's round-0 timeout, so it must not differ between nodes
	// (design §4.5, §8.1). The flag only applies to the PoA segment.
	if params.BlockEmptyInterval > 0 && params.BlockEmptyInterval != int64(config.Bft.EmptyBlockInterval) {
		log.Warn("--metadium.block.emptyinterval differs from the genesis bft.emptyBlockInterval; the genesis applies from bftBlock",
			"flag", params.BlockEmptyInterval, "genesis", config.Bft.EmptyBlockInterval, "bftBlock", bftBlock)
	}
	s.node = metabft.NewNode(metabft.NodeConfig{
		Config: metabft.Config{
			EmptyBlockInterval: time.Duration(config.Bft.EmptyBlockInterval) * time.Second,
			BaseTimeout:        time.Duration(config.Bft.BaseTimeout) * time.Second,
			MaxBackoffExp:      config.Bft.MaxBackoffExp,
		},
		ChainID:          s.chainID,
		BftBlock:         bftBlock,
		Key:              key,
		WAL:              wal,
		ObserverUntil:    observerUntil,
		Validators:       s.validatorSet,
		Broadcast:        s.broadcast,
		OnProposalWanted: engine.WakeProposer,
	}, metabft.NewBlockChain(bc, engine, mux))
	engine.SetProposer(s.node)

	// Capability advertisement is fixed at startup (MakeProtocols). A node
	// whose membership cannot be read yet, early in the PoA bootstrap,
	// advertises: peers admit it only once it is in their set.
	switch set, err := s.validatorSet(head + 1); {
	case err != nil:
		s.validator = true
		log.Info("PBFT validator set not readable yet; advertising metabft/1", "height", head+1, "err", err)
	default:
		_, s.validator = set.IndexOf(s.self)
	}
	return s, nil
}

// Start runs the consensus node and the head loop.
func (s *bftService) Start() {
	s.node.Start()
	s.wg.Add(1)
	go s.headLoop()
}

// Stop ends both and closes the WAL.
func (s *bftService) Stop() {
	close(s.quit)
	s.wg.Wait()
	s.node.Stop()
	s.wal.Close()
}

// headLoop prunes the dedup cache as heights commit. Without it the cache
// fills (DefaultCacheSize) and then drops every message: consensus would
// stop silently a few thousand heights in (review on #149).
func (s *bftService) headLoop() {
	defer s.wg.Done()
	heads := make(chan core.ChainHeadEvent, 16)
	sub := s.bc.SubscribeChainHeadEvent(heads)
	defer sub.Unsubscribe()
	for {
		select {
		case ev := <-heads:
			s.onHead(ev.Block.NumberU64())
			s.checkBlockCreationTime(ev.Block.Number())
		case <-sub.Err():
			return
		case <-s.quit:
			return
		}
	}
}

// onHead drops cache entries below the height now being agreed on, and
// re-decides which peers are admitted: the set may have changed, and in the
// PoA bootstrap it only becomes readable once governance is deployed.
func (s *bftService) onHead(head uint64) {
	s.cache.Prune(head + 1)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.peers {
		s.admit(p)
	}
}

// admits reports whether key is in the current validator set.
func (s *bftService) admits(key []byte) bool {
	set, err := s.validatorSet(s.currentHeight())
	if err != nil {
		return false
	}
	_, ok := set.IndexOf(key)
	return ok
}

// admit updates p's admission; a newly admitted peer is asked where it is.
func (s *bftService) admit(p *bftPeer) {
	now := s.admits(p.key)
	if was := p.admitted.Swap(now); now && !was {
		p.Log().Debug("metabft peer admitted")
		go p.RequestSync() // not from under s.mu; a failure shows in the peer's read loop
	}
}

// peerAdmitted reports whether messages from peer count.
func (s *bftService) peerAdmitted(peer *bftproto.Peer) bool {
	s.mu.RLock()
	p := s.peers[peer.ID()]
	s.mu.RUnlock()
	return p != nil && p.admitted.Load()
}

// checkBlockCreationTime warns when governance's blockCreationTime is longer
// than the empty-block interval (design §4.5): the proposer then waits for
// the former, and the round-0 timeout, measured from the latter, fires
// before an idle chain's empty block. It is read from governance, so it is
// checked as heads arrive and logged once per value.
// It reports whether it warned.
func (s *bftService) checkBlockCreationTime(head *big.Int) bool {
	interval, _, _, _, _, err := metaminer.GetBlockBuildParameters(head)
	if err != nil || interval == s.blockIntervalSeen {
		return false
	}
	s.blockIntervalSeen = interval
	if time.Duration(interval)*time.Millisecond <= s.emptyInterval {
		return false
	}
	log.Warn("Governance blockCreationTime is longer than the PBFT empty-block interval",
		"blockCreationTime", time.Duration(interval)*time.Millisecond, "emptyBlockInterval", s.emptyInterval)
	return true
}

// validatorSet is the node's ValidatorsFunc, cached by height.
func (s *bftService) validatorSet(height uint64) (*metabft.ValidatorSet, error) {
	if set, ok := s.sets.Get(height); ok {
		return set, nil
	}
	set, err := s.validators(height)
	if err != nil {
		return nil, err
	}
	s.sets.Add(height, set)
	return set, nil
}

// currentHeight is the height being agreed on, or the one after the head
// while the node has not started one.
func (s *bftService) currentHeight() uint64 {
	if h, _ := s.node.Status(); h != 0 {
		return h
	}
	return s.bc.CurrentBlock().Number.Uint64() + 1
}

// broadcast queues m for every admitted peer without blocking.
func (s *bftService) broadcast(m *metabft.Message) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.peers {
		if !p.admitted.Load() {
			continue
		}
		select {
		case p.queue <- m:
		default:
			p.Log().Debug("metabft send queue full; dropping", "type", m.Type, "height", m.Height, "round", m.Round)
		}
	}
}

// Backend of metabft/1.

func (s *bftService) IsValidator() bool { return s.validator }

func (s *bftService) ChainID() uint64 { return s.chainID }

// ValidatorSet accepts the height being agreed on and the next one. The next
// height's own set is in a state not written yet, so its messages are
// checked against the current set; the core checks them again against the
// right one when it starts that height.
func (s *bftService) ValidatorSet(height uint64) (*metabft.ValidatorSet, bool) {
	cur, _ := s.node.Status()
	if cur == 0 || (height != cur && height != cur+1) {
		return nil, false
	}
	set, err := s.validatorSet(cur)
	return set, err == nil
}

func (s *bftService) HandleConsensus(peer *bftproto.Peer, m *metabft.Message) error {
	if s.peerAdmitted(peer) {
		s.node.HandleMessage(m)
	}
	return nil
}

func (s *bftService) HandleEvidence(ev *metabft.Evidence) {
	set, err := s.validatorSet(ev.First.Height)
	if err != nil {
		log.Warn("Equivocation seen, but its validator set is gone; not stored", "height", ev.First.Height, "err", err)
		return
	}
	added, err := s.evidence.Add(ev, s.chainID, set)
	switch {
	case err != nil:
		log.Warn("Equivocation evidence not stored", "height", ev.First.Height, "err", err)
	case added:
		signer, _ := ev.First.Signer()
		log.Error("Validator equivocated", "signer", fmt.Sprintf("%x", signer[:8]), "height", ev.First.Height,
			"round", ev.First.Round, "type", ev.First.Type)
	}
}

func (s *bftService) UnknownSigner(peer *bftproto.Peer) error {
	s.mu.RLock()
	p := s.peers[peer.ID()]
	s.mu.RUnlock()
	if p == nil {
		return errNotValidatorPeer
	}
	return p.unknownSigner(time.Now())
}

func (p *bftPeer) unknownSigner(now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Sub(p.unknownStart) > bftUnknownWindow {
		p.unknownStart, p.unknownCount = now, 0
	}
	p.unknownCount++
	if p.unknownCount > bftUnknownBudget {
		return fmt.Errorf("%d messages from unknown signers within %v", p.unknownCount, bftUnknownWindow)
	}
	return nil
}

func (s *bftService) SyncStatus() (uint64, uint64) { return s.node.Status() }

// HandleSyncReply only logs: the reply is unsigned, and blocks this node is
// missing come through the eth protocol's sync anyway.
func (s *bftService) HandleSyncReply(peer *bftproto.Peer, height, round uint64) {
	if !s.peerAdmitted(peer) {
		return
	}
	if cur, _ := s.node.Status(); height > cur+1 {
		peer.Log().Info("PBFT peer is ahead; waiting for block sync", "peer", height, "local", cur)
	}
}

// RunPeer serves a peer, admitted only while its node key is in the current
// validator set (review on #149): anyone can advertise metabft/1. A peer
// that is not admitted is kept, not dropped, because returning here would
// end the whole connection, eth sync included: in the PoA bootstrap no set
// is readable yet, and every node would cut every other one off. It is sent
// nothing and heard from only as far as the handler's checks go; admission
// is re-decided on every head.
func (s *bftService) RunPeer(peer *bftproto.Peer, handler func(*bftproto.Peer) error) error {
	key := s.peerKey(peer)
	if key == nil {
		return fmt.Errorf("%w: no node key", errNotValidatorPeer)
	}
	p := &bftPeer{Peer: peer, key: key, queue: make(chan *metabft.Message, bftPeerQueue)}
	s.mu.Lock()
	if _, dup := s.peers[peer.ID()]; dup {
		s.mu.Unlock()
		return p2p.DiscAlreadyConnected
	}
	s.peers[peer.ID()] = p
	s.mu.Unlock()

	done := make(chan struct{})
	go p.sendLoop(done)
	defer func() {
		s.mu.Lock()
		delete(s.peers, peer.ID())
		s.mu.Unlock()
		close(done)
	}()
	s.admit(p)
	return handler(peer)
}

// nodeKeyOf returns the peer's 64-byte node key, nil if it has none.
func nodeKeyOf(peer *bftproto.Peer) []byte {
	pub := peer.Node().Pubkey()
	if pub == nil {
		return nil
	}
	return crypto.FromECDSAPub(pub)[1:]
}

// sendLoop writes queued messages until the peer goes away.
func (p *bftPeer) sendLoop(done <-chan struct{}) {
	for {
		select {
		case m := <-p.queue:
			if err := p.SendConsensus(m); err != nil {
				p.Log().Debug("metabft send failed", "err", err)
				return
			}
		case <-done:
			return
		}
	}
}

func (s *bftService) PeerInfo(enode.ID) interface{} { return nil }

var _ bftproto.Backend = (*bftService)(nil)
