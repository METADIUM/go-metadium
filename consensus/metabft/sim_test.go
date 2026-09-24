package metabft

import (
	"container/heap"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// This file is the deterministic simulator of checklist P3: N cores on a
// simulated clock and network, driven by a seeded RNG, with message delay
// and loss, crashes and restarts, partitions and Byzantine validators.
// Every commit on every node is checked against every other: two different
// blocks decided at one height fail the test (P3-S7).

// simBlock is the proposal type of the simulator.
type simBlock struct {
	H       uint64
	Parent  common.Hash
	Builder uint64
	Round   uint64
	Bad     bool   // fails VerifyProposal everywhere
	Salt    uint64 // lets a Byzantine proposer make a second, different block
	Nonce   uint64 // every build differs, like real blocks (timestamps, tx selection)
}

func (b *simBlock) Height() uint64 { return b.H }
func (b *simBlock) Hash() common.Hash {
	enc, _ := rlp.EncodeToBytes(b)
	return crypto.Keccak256Hash(enc)
}
func (b *simBlock) Encode() ([]byte, error) { return rlp.EncodeToBytes(b) }

func decodeSimBlock(data []byte) (Proposal, error) {
	b := new(simBlock)
	if err := rlp.DecodeBytes(data, b); err != nil {
		return nil, err
	}
	return b, nil
}

type behaviour int

const (
	honest      behaviour = iota
	badProposer           // proposes blocks that fail verification
	equivocator           // proposes two blocks and votes for both, to disjoint halves
	liar                  // lies in its ROUND-CHANGEs, differently to each peer, and re-proposes stale blocks
)

type simEvent struct {
	at  time.Duration
	seq int
	fn  func()
}

type eventQueue []*simEvent

func (q eventQueue) Len() int { return len(q) }
func (q eventQueue) Less(i, j int) bool {
	if q[i].at != q[j].at {
		return q[i].at < q[j].at
	}
	return q[i].seq < q[j].seq
}
func (q eventQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *eventQueue) Push(x interface{}) { *q = append(*q, x.(*simEvent)) }
func (q *eventQueue) Pop() interface{} {
	old := *q
	e := old[len(old)-1]
	*q = old[:len(old)-1]
	return e
}

type simNode struct {
	sim   *sim
	idx   int
	key   *ecdsa.PrivateKey
	core  *Core
	wal   *WAL
	path  string
	up    bool
	kind  behaviour
	chain []*simBlock // committed blocks, index = height-1
	seals map[uint64][][]byte

	// liar: every prepared state its core reached, oldest first, per height.
	preparedSeen map[uint64][]*preparedState
}

type sim struct {
	t     *testing.T
	rng   *rand.Rand
	now   time.Duration
	seq   int
	queue eventQueue
	cfg   Config
	set   *ValidatorSet
	nodes []*simNode

	// network model
	minDelay, maxDelay time.Duration
	dropRate           float64
	partition          func(from, to int) bool             // true: cut
	filter             func(from, to int, m *Message) bool // true: drop this message
	buildDelay         time.Duration                       // time a proposer takes to have its block
	noSyncUntil        time.Duration                       // block sync is off before this time

	decided  map[uint64]common.Hash // height -> the one hash anyone committed
	blocks   map[common.Hash]*simBlock
	alt      map[common.Hash]*simBlock // equivocation: digest -> the other block
	messages int
}

func newSim(t *testing.T, n int, seed int64) *sim {
	t.Helper()
	s := &sim{
		t:          t,
		rng:        rand.New(rand.NewSource(seed)),
		cfg:        Config{EmptyBlockInterval: 5 * time.Second, BaseTimeout: 2 * time.Second, MaxBackoffExp: 5},
		minDelay:   time.Millisecond,
		maxDelay:   20 * time.Millisecond,
		buildDelay: 100 * time.Millisecond,
		decided:    make(map[uint64]common.Hash),
		blocks:     make(map[common.Hash]*simBlock),
		alt:        make(map[common.Hash]*simBlock),
	}
	pubs := make([][]byte, n)
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		pubs[i] = pubKeyOf(key)
		s.nodes = append(s.nodes, &simNode{sim: s, idx: i, key: key, up: true,
			path: filepath.Join(dir, fmt.Sprintf("node%d", i), "wal"), seals: map[uint64][][]byte{}})
	}
	set, err := NewValidatorSet(pubs)
	if err != nil {
		t.Fatal(err)
	}
	s.set = set
	// Validators are ordered by their key in governance; follow the set.
	for _, nd := range s.nodes {
		i, _ := set.IndexOf(pubKeyOf(nd.key))
		nd.idx = i
	}
	byIdx := make([]*simNode, n)
	for _, nd := range s.nodes {
		byIdx[nd.idx] = nd
	}
	s.nodes = byIdx
	return s
}

func (s *sim) at(delay time.Duration, fn func()) {
	s.seq++
	heap.Push(&s.queue, &simEvent{at: s.now + delay, seq: s.seq, fn: fn})
}

// simBftBlock is the first PBFT height of the simulated chain.
const simBftBlock = 1

// start runs the bootstrap step of design §6.1 (every validator creates its
// WAL while no PBFT vote is possible yet) and boots every node at the switch.
func (s *sim) start() {
	for _, nd := range s.nodes {
		w, observerUntil, err := OpenNodeWAL(nd.path, 0, simBftBlock+1) // head+1 < bftBlock: bootstrap
		if err != nil || observerUntil != 0 {
			s.t.Fatalf("bootstrap WAL: %v, observerUntil %d", err, observerUntil)
		}
		w.Close()
		nd.boot()
	}
	s.scheduleSync()
}

func (nd *simNode) boot() {
	head := uint64(len(nd.chain))
	w, observerUntil, err := OpenNodeWAL(nd.path, head, simBftBlock)
	if err != nil {
		nd.sim.t.Fatalf("node %d: %v", nd.idx, err)
	}
	nd.wal = w
	nd.core = NewCore(nd.sim.cfg, nd, nd.key, w, observerUntil)
	nd.up = true
	nd.core.NewHeight(head+1, nd.sim.now)
}

func (nd *simNode) crash() {
	nd.up = false
	nd.core = nil
	if nd.wal != nil {
		nd.wal.Close()
		nd.wal = nil
	}
}

// --- Backend ---

func (nd *simNode) ChainID() uint64 { return testChainID }
func (nd *simNode) Validators(uint64) (*ValidatorSet, error) {
	return nd.sim.set, nil
}
func (nd *simNode) DecodeProposal(data []byte) (Proposal, error) { return decodeSimBlock(data) }

func (nd *simNode) headHash() common.Hash {
	if len(nd.chain) == 0 {
		return common.Hash{}
	}
	return nd.chain[len(nd.chain)-1].Hash()
}

func (nd *simNode) VerifyProposal(p Proposal) error {
	b := p.(*simBlock)
	switch {
	case b.Bad:
		return errors.New("bad block")
	case b.H != uint64(len(nd.chain))+1 || b.Parent != nd.headHash():
		return errors.New("not on the local head")
	}
	return nil
}

func (nd *simNode) RequestProposal(height, round uint64) {
	core := nd.core
	nd.sim.at(nd.sim.buildDelay, func() {
		if !nd.up || nd.core != core || core.Height() != height || core.Round() != round {
			return
		}
		b := &simBlock{H: height, Parent: nd.headHash(), Builder: uint64(nd.idx), Round: round, Bad: nd.kind == badProposer,
			Nonce: nd.sim.rng.Uint64()}
		nd.sim.blocks[b.Hash()] = b
		core.Propose(b, nd.sim.now)
	})
}

func (nd *simNode) Broadcast(m *Message) {
	s := nd.sim
	switch nd.kind {
	case equivocator:
		nd.broadcastEquivocating(m)
		return
	case liar:
		nd.broadcastLying(m)
		return
	}
	for _, peer := range s.nodes {
		if peer != nd {
			s.send(nd.idx, peer, m)
		}
	}
}

// broadcastEquivocating sends the genuine message to the first half of the
// peers and a conflicting one to the second half: a second block when
// proposing, a vote for the other block of a known equivocation otherwise.
func (nd *simNode) broadcastEquivocating(m *Message) {
	s := nd.sim
	var other *Message
	switch m.Type {
	case MsgPreprepare:
		body := new(preprepareBody)
		rlp.DecodeBytes(m.Payload, body)
		orig, _ := decodeSimBlock(body.Block)
		b := *orig.(*simBlock)
		b.Salt = s.rng.Uint64() | 1
		data, _ := b.Encode()
		s.blocks[b.Hash()] = &b
		s.alt[m.Digest], s.alt[b.Hash()] = &b, orig.(*simBlock)
		other = &Message{Type: m.Type, Height: m.Height, Round: m.Round, ChainID: m.ChainID, Digest: b.Hash(),
			Payload: encodePayload(&preprepareBody{Block: data, RoundChanges: body.RoundChanges, Prepares: body.Prepares})}
	case MsgPrepare, MsgCommit:
		if alt := s.alt[m.Digest]; alt != nil {
			other = &Message{Type: m.Type, Height: m.Height, Round: m.Round, ChainID: m.ChainID, Digest: alt.Hash()}
			if m.Type == MsgCommit {
				other.CommitSeal, _ = SignCommitSeal(CommitDigest(alt.Hash(), m.Round, m.ChainID), nd.key)
			}
		}
	}
	if other != nil {
		other.Sign(nd.key)
	}
	for i, peer := range s.nodes {
		if peer == nd {
			continue
		}
		if other != nil && i >= len(s.nodes)/2 {
			s.send(nd.idx, peer, other)
		} else {
			s.send(nd.idx, peer, m)
		}
	}
}

func (s *sim) send(from int, to *simNode, m *Message) {
	s.messages++
	if s.partition != nil && s.partition(from, to.idx) {
		return
	}
	if s.filter != nil && s.filter(from, to.idx, m) {
		return
	}
	if s.dropRate > 0 && s.rng.Float64() < s.dropRate {
		return
	}
	enc, err := rlp.EncodeToBytes(m)
	if err != nil {
		s.t.Fatal(err)
	}
	delay := s.minDelay + time.Duration(s.rng.Int63n(int64(s.maxDelay-s.minDelay)+1))
	s.at(delay, func() {
		if !to.up {
			return
		}
		var wire Message
		if err := rlp.DecodeBytes(enc, &wire); err != nil {
			s.t.Fatal(err)
		}
		to.core.HandleMessage(&wire, s.now)
	})
}

func (nd *simNode) Commit(p Proposal, round uint64, seals [][]byte) {
	s := nd.sim
	b := p.(*simBlock)
	// The seals must prove the decision on their own (what import checks).
	digest := CommitDigest(b.Hash(), round, testChainID)
	signers := map[int]bool{}
	for _, seal := range seals {
		pub, err := RecoverSealSigner(digest, seal)
		if err != nil {
			s.t.Fatalf("node %d: bad seal at height %d: %v", nd.idx, b.H, err)
		}
		i, ok := s.set.IndexOf(pub)
		if !ok {
			s.t.Fatalf("node %d: seal by a non-validator", nd.idx)
		}
		signers[i] = true
	}
	if len(signers) < s.set.Quorum() {
		s.t.Fatalf("node %d: committed height %d with %d distinct seals, quorum %d", nd.idx, b.H, len(signers), s.set.Quorum())
	}
	nd.record(b, seals)
	core := nd.core
	s.at(0, func() {
		if nd.up && nd.core == core {
			core.NewHeight(b.H+1, s.now)
		}
	})
}

// record appends a decided block and checks the safety invariant.
func (nd *simNode) record(b *simBlock, seals [][]byte) {
	s := nd.sim
	if prev, ok := s.decided[b.H]; ok && prev != b.Hash() {
		s.t.Fatalf("SAFETY VIOLATION: height %d decided as %x and %x (node %d)", b.H, prev[:4], b.Hash().Bytes()[:4], nd.idx)
	}
	s.decided[b.H] = b.Hash()
	s.blocks[b.Hash()] = b
	if b.H != uint64(len(nd.chain))+1 {
		s.t.Fatalf("node %d: committed height %d on a chain of %d", nd.idx, b.H, len(nd.chain))
	}
	nd.chain = append(nd.chain, b)
	nd.seals[b.H] = seals
}

// scheduleSync models block sync: once a second, a node that is behind
// imports decided blocks from a peer that has them.
func (s *sim) scheduleSync() {
	s.at(time.Second, func() {
		for _, nd := range s.nodes {
			if s.now < s.noSyncUntil {
				break
			}
			if !nd.up {
				continue
			}
			for _, peer := range s.nodes {
				if !peer.up || len(peer.chain) <= len(nd.chain) || (s.partition != nil && s.partition(peer.idx, nd.idx)) {
					continue
				}
				for len(nd.chain) < len(peer.chain) {
					h := uint64(len(nd.chain)) + 1
					nd.record(peer.chain[h-1], peer.seals[h])
				}
				nd.core.NewHeight(uint64(len(nd.chain))+1, s.now)
				break
			}
		}
		s.scheduleSync()
	})
}

// run advances the simulation until stop holds or the clock reaches limit.
func (s *sim) run(limit time.Duration, stop func() bool) {
	for s.now < limit && !stop() {
		next, isEvent := limit, false
		if len(s.queue) > 0 && s.queue[0].at < next {
			next, isEvent = s.queue[0].at, true
		}
		for _, nd := range s.nodes {
			if !nd.up {
				continue
			}
			if d, ok := nd.core.Deadline(); ok && d < next {
				next, isEvent = d, false
			}
		}
		if next > s.now {
			s.now = next
		}
		if isEvent {
			e := heap.Pop(&s.queue).(*simEvent)
			e.fn()
			continue
		}
		for _, nd := range s.nodes {
			if nd.up {
				nd.core.Tick(s.now)
			}
		}
	}
}

// minHeight is the lowest chain length among the nodes that are up.
func (s *sim) minHeight(filter func(*simNode) bool) int {
	min := -1
	for _, nd := range s.nodes {
		if !nd.up || (filter != nil && !filter(nd)) {
			continue
		}
		if min < 0 || len(nd.chain) < min {
			min = len(nd.chain)
		}
	}
	return min
}

func (s *sim) honest(nd *simNode) bool { return nd.kind == honest }

func (s *sim) reproposals() int {
	n := 0
	for _, nd := range s.nodes {
		if nd.core != nil {
			n += nd.core.Stats().Reproposals
		}
	}
	return n
}

func (s *sim) roundChanges() int {
	n := 0
	for _, nd := range s.nodes {
		if nd.core != nil {
			n += nd.core.Stats().RoundChanges
		}
	}
	return n
}

func (s *sim) reach(height int, limit time.Duration, filter func(*simNode) bool) {
	s.t.Helper()
	s.run(limit, func() bool { return s.minHeight(filter) >= height })
	if got := s.minHeight(filter); got < height {
		s.t.Fatalf("only reached height %d of %d by %v", got, height, s.now)
	}
}

// ---------------------------------------------------------------- tests

var simSizes = []int{4, 7, 10}

func TestSimHappyPath(t *testing.T) {
	for _, n := range simSizes {
		s := newSim(t, n, 1)
		s.start()
		s.reach(30, time.Hour, nil)
		if rc := s.roundChanges(); rc != 0 {
			t.Errorf("N=%d: %d round changes on a healthy network", n, rc)
		}
	}
}

// TestSimIdleProfile is P3-S6: proposers that wait the full empty-block
// interval, and ones that answer in 100ms, both run without round changes.
func TestSimIdleProfile(t *testing.T) {
	for _, build := range []time.Duration{100 * time.Millisecond, 5 * time.Second} {
		s := newSim(t, 7, 2)
		s.buildDelay = build
		s.start()
		s.reach(20, time.Hour, nil)
		if rc := s.roundChanges(); rc != 0 {
			t.Errorf("build %v: %d round changes", build, rc)
		}
	}
}

// TestSimDelayAndLoss is P3-S1.
func TestSimDelayAndLoss(t *testing.T) {
	for _, n := range simSizes {
		for seed := int64(1); seed <= 8; seed++ {
			s := newSim(t, n, seed)
			s.maxDelay, s.dropRate = 300*time.Millisecond, 0.05
			s.start()
			s.reach(10, 6*time.Hour, nil)
		}
	}
}

// TestSimSilentValidators: f stopped validators do not stop the chain;
// f+1 do, without a fork, and it resumes when they return (S-01..S-03).
func TestSimSilentValidators(t *testing.T) {
	for _, n := range simSizes {
		s := newSim(t, n, 3)
		f := s.set.F()
		s.start()
		for i := 0; i < f; i++ {
			s.nodes[i].crash()
		}
		s.reach(10, 6*time.Hour, nil)

		s.nodes[f].crash() // now f+1 are down
		stalled := s.minHeight(nil)
		s.run(s.now+30*time.Minute, func() bool { return false })
		for _, nd := range s.nodes {
			if nd.up && len(nd.chain) > stalled+1 {
				t.Fatalf("N=%d: progressed from %d to %d with f+1 validators down", n, stalled, len(nd.chain))
			}
		}
		for i := 0; i <= f; i++ {
			s.nodes[i].boot()
		}
		s.reach(stalled+5, s.now+6*time.Hour, nil)
	}
}

// TestSimPartition is S-06: a 4:3 split of 7 stops both sides; healing resumes.
func TestSimPartition(t *testing.T) {
	s := newSim(t, 7, 4)
	s.start()
	s.reach(5, time.Hour, nil)
	s.partition = func(a, b int) bool { return (a < 4) != (b < 4) }
	before := len(s.decided)
	s.run(s.now+30*time.Minute, func() bool { return false })
	if len(s.decided) > before+1 { // a height in flight at the cut may still finish
		t.Fatalf("a side of a 4:3 split decided %d heights", len(s.decided)-before)
	}
	s.partition = nil
	s.reach(before+5, s.now+6*time.Hour, nil)
}

// TestSimBadProposer is S-05: a proposer of invalid blocks is skipped.
func TestSimBadProposer(t *testing.T) {
	s := newSim(t, 4, 5)
	s.nodes[1].kind = badProposer
	s.start()
	s.reach(12, 6*time.Hour, nil)
	for _, nd := range s.nodes {
		for _, b := range nd.chain {
			if b.Bad {
				t.Fatalf("an invalid block was committed at height %d", b.H)
			}
		}
	}
}

// TestSimEquivocation is S-04/P3-S2/P3-S3: up to f validators propose two
// blocks and vote for both, with random delays; nothing forks.
func TestSimEquivocation(t *testing.T) {
	for _, n := range simSizes {
		for seed := int64(1); seed <= 10; seed++ {
			s := newSim(t, n, 100+seed)
			s.maxDelay = 200 * time.Millisecond
			for i := 0; i < s.set.F(); i++ {
				s.nodes[(int(seed)+i)%n].kind = equivocator
			}
			s.start()
			s.reach(10, 12*time.Hour, s.honest)
		}
	}
}

// TestSimCrashRestart is P3-S4: validators crash at random moments and come
// back with their WAL; some come back without it and must observe first.
func TestSimCrashRestart(t *testing.T) {
	for _, n := range []int{4, 7} {
		for seed := int64(1); seed <= 8; seed++ {
			s := newSim(t, n, 200+seed)
			s.maxDelay = 100 * time.Millisecond
			s.start()
			f := s.set.F()
			for step := 0; step < 30; step++ {
				s.run(s.now+time.Duration(s.rng.Int63n(int64(3*time.Second))), func() bool { return false })
				down := 0
				for _, nd := range s.nodes {
					if !nd.up {
						down++
					}
				}
				nd := s.nodes[s.rng.Intn(n)]
				switch {
				case nd.up && down < f:
					nd.crash()
				case !nd.up:
					if s.rng.Intn(4) == 0 {
						os.Remove(nd.path) // lost WAL: must come back as an observer
					}
					nd.boot()
				}
			}
			for _, nd := range s.nodes {
				if !nd.up {
					nd.boot()
				}
			}
			s.reach(s.minHeight(nil)+5, s.now+12*time.Hour, nil)
		}
	}
}

// TestSimCrashAll restarts every validator at once, mid-height, repeatedly.
func TestSimCrashAll(t *testing.T) {
	s := newSim(t, 4, 7)
	s.start()
	for i := 0; i < 10; i++ {
		s.run(s.now+time.Duration(s.rng.Int63n(int64(500*time.Millisecond))), func() bool { return false })
		for _, nd := range s.nodes {
			nd.crash()
		}
		for _, nd := range s.nodes {
			nd.boot()
		}
	}
	s.reach(s.minHeight(nil)+5, s.now+6*time.Hour, nil)
}

// TestSimCommitWithheld builds the situation the re-proposal rule exists
// for: at one height only node 0 receives the COMMITs, so it decides block B
// in round 0 while everyone else has B prepared but not decided and moves
// on. A later round must decide B again, not a fresh block (design §4.5).
func TestSimCommitWithheld(t *testing.T) {
	for _, n := range simSizes {
		for _, target := range []uint64{3, 4} {
			s := newSim(t, n, int64(300+target))
			s.filter = func(from, to int, m *Message) bool {
				return m.Type == MsgCommit && m.Height == target && m.Round == 0 && to != 0
			}
			// Without sync the others cannot fetch B from node 0; they must
			// decide the height through consensus.
			s.noSyncUntil = time.Hour
			s.start()
			s.reach(int(target)+5, 6*time.Hour, nil)
			if s.reproposals() == 0 {
				t.Errorf("N=%d height %d: the prepared block was never re-proposed", n, target)
			}
		}
	}
}

// TestSimLossyCommits is the random version: many COMMITs are lost, so
// heights are regularly decided by some nodes and not others.
func TestSimLossyCommits(t *testing.T) {
	for _, n := range simSizes {
		for seed := int64(1); seed <= 6; seed++ {
			s := newSim(t, n, 400+seed)
			s.filter = func(from, to int, m *Message) bool {
				return m.Type == MsgCommit && s.rng.Float64() < 0.3
			}
			s.noSyncUntil = 10 * time.Minute // then sync, for liveness
			s.start()
			s.reach(12, 12*time.Hour, nil)
		}
	}
}

// TestSimAmnesiaAfterCommit is the case the WAL exists for (design §6.1,
// scenario S-11): at one height only node 0 receives the COMMITs and decides
// block A, and at that moment every other validator — the round's proposer
// included, more than f of them — restarts. Remembering what they signed,
// they must decide A again; forgetting it, the proposer would offer a new
// block in the same round and they would decide it.
func TestSimAmnesiaAfterCommit(t *testing.T) {
	for _, loseWAL := range []bool{false, true} {
		const target = 5 // proposer of round 0 is validator 1 of 4
		s := newSim(t, 4, 500)
		s.filter = func(from, to int, m *Message) bool {
			return m.Type == MsgCommit && m.Height == target && m.Round == 0 && to != 0
		}
		s.noSyncUntil = 24 * time.Hour
		s.start()
		s.run(time.Hour, func() bool { return len(s.nodes[0].chain) >= target })
		if len(s.nodes[0].chain) < target || len(s.nodes[1].chain) >= target {
			t.Fatalf("setup: node 0 at %d, node 1 at %d", len(s.nodes[0].chain), len(s.nodes[1].chain))
		}
		for _, nd := range s.nodes[1:] {
			nd.crash()
			if loseWAL {
				os.Remove(nd.path)
			}
			nd.boot()
		}
		if loseWAL {
			// All three restart as observers and cannot vote at the height
			// node 0 decided without them: safe but stuck until sync hands
			// them the block (design §6.1).
			for _, nd := range s.nodes[1:] {
				if !nd.core.Observer() {
					t.Fatal("a validator that lost its WAL did not start as an observer")
				}
			}
			s.noSyncUntil = s.now + time.Minute
		}
		s.reach(target+5, s.now+6*time.Hour, nil)
	}
}

// notePrepared remembers each prepared state a liar's core reaches, so it
// can later offer an older one than it holds.
func (nd *simNode) notePrepared() {
	p := nd.core.prepared
	if p == nil {
		return
	}
	if nd.preparedSeen == nil {
		nd.preparedSeen = map[uint64][]*preparedState{}
	}
	seen := nd.preparedSeen[nd.core.Height()]
	if len(seen) == 0 || seen[len(seen)-1].round != p.round {
		nd.preparedSeen[nd.core.Height()] = append(seen, p)
	}
}

// staleFor returns a prepared state older than the one the liar holds.
func (nd *simNode) staleFor(height uint64) *preparedState {
	seen := nd.preparedSeen[height]
	if len(seen) < 2 {
		return nil
	}
	return seen[0]
}

func (nd *simNode) signedRoundChange(height, round uint64, p *preparedState, withEvidence bool) *Message {
	m := &Message{Type: MsgRoundChange, Height: height, Round: round, ChainID: testChainID}
	claim := &roundChangeClaim{}
	if p != nil {
		claim.Prepared, claim.PreparedRound = true, p.round
		m.Digest = p.proposal.Hash()
		if withEvidence {
			data, _ := p.proposal.Encode()
			m.SetExtra(encodePayload(&roundChangeExtra{Block: data, Prepares: p.cert}))
		}
	}
	m.Payload = encodePayload(claim)
	m.Sign(nd.key)
	return m
}

// broadcastLying sends each peer one of: the truth, a claim of having
// prepared nothing, an older genuine prepared state, or an invented claim
// with no evidence. As proposer it offers the oldest block it prepared, with
// that round's genuine PREPARE quorum, instead of the one it should.
func (nd *simNode) broadcastLying(m *Message) {
	s := nd.sim
	nd.notePrepared()
	for _, peer := range s.nodes {
		if peer == nd {
			continue
		}
		out := m
		switch {
		case m.Type == MsgRoundChange:
			switch s.rng.Intn(4) {
			case 1:
				out = nd.signedRoundChange(m.Height, m.Round, nil, false)
			case 2:
				if stale := nd.staleFor(m.Height); stale != nil && stale.round < m.Round {
					out = nd.signedRoundChange(m.Height, m.Round, stale, true)
				}
			case 3:
				if m.Round > 0 {
					fake := &simBlock{H: m.Height, Parent: nd.headHash(), Builder: uint64(nd.idx), Nonce: s.rng.Uint64()}
					out = nd.signedRoundChange(m.Height, m.Round, &preparedState{round: m.Round - 1, proposal: fake}, false)
				}
			}
		case m.Type == MsgPreprepare && m.Round > 0:
			if stale := nd.staleFor(m.Height); stale != nil && stale.proposal.Hash() != m.Digest {
				body := new(preprepareBody)
				rlp.DecodeBytes(m.Payload, body)
				data, _ := stale.proposal.Encode()
				forged := &Message{Type: MsgPreprepare, Height: m.Height, Round: m.Round, ChainID: testChainID,
					Digest:  stale.proposal.Hash(),
					Payload: encodePayload(&preprepareBody{Block: data, RoundChanges: body.RoundChanges, Prepares: stale.cert})}
				forged.Sign(nd.key)
				out = forged
			}
		}
		s.send(nd.idx, peer, out)
	}
}

// TestSimLiars is P3-S9 (review on #148): up to f validators lie in their
// ROUND-CHANGEs and re-propose stale blocks, while lost COMMITs keep blocks
// prepared-but-undecided so the lies matter. Nothing forks and the chain
// keeps going.
func TestSimLiars(t *testing.T) {
	for _, n := range simSizes {
		for seed := int64(1); seed <= 6; seed++ {
			s := newSim(t, n, 600+seed)
			for i := 0; i < s.set.F(); i++ {
				s.nodes[(int(seed)+i)%n].kind = liar
			}
			// Lost PREPAREs and COMMITs leave different validators prepared on
			// different blocks at one height, which is when a stale claim or a
			// stale re-proposal could do damage. The loss stops after 20
			// minutes: liveness is only promised once the network behaves
			// (design §4.7, "after GST"), and devp2p runs over TCP, so random
			// loss of every fifth message forever is not a network we must
			// survive, only one we must not fork on.
			s.filter = func(from, to int, m *Message) bool {
				if s.now > 20*time.Minute {
					return false
				}
				return (m.Type == MsgCommit && s.rng.Float64() < 0.3) || (m.Type == MsgPrepare && s.rng.Float64() < 0.2)
			}
			s.noSyncUntil = 10 * time.Minute
			s.start()
			s.reach(10, 12*time.Hour, s.honest)
		}
	}
}

// TestSimRejoinAfterOutage is the recovery case from the review on #148:
// f+1 validators are down for 90 minutes, so the rest keep timing out without
// a quorum and end up dozens of rounds ahead of the returning validators'
// WAL round. f+1 amplification must bridge that gap in one step, not one
// 64-second timeout per round.
func TestSimRejoinAfterOutage(t *testing.T) {
	s := newSim(t, 4, 700)
	s.start()
	s.reach(5, time.Hour, nil)
	down := s.nodes[:2]
	for _, nd := range down {
		nd.crash()
	}
	s.run(s.now+90*time.Minute, func() bool { return false })
	if r := s.nodes[3].core.Round(); r <= maxRoundsAhead {
		t.Fatalf("the survivors only reached round %d; the outage does not open a gap beyond the window", r)
	}
	height := s.minHeight(nil)
	for _, nd := range down {
		nd.boot()
	}
	back := s.now
	s.reach(height+3, s.now+6*time.Hour, nil)
	if took := s.now - back; took > 10*time.Minute {
		t.Errorf("rejoining took %v", took)
	}
}
