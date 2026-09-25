package metabft

import (
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

var nodeTestConfig = Config{EmptyBlockInterval: time.Second, BaseTimeout: 2 * time.Second, MaxBackoffExp: 3}

// memChain is a chain that takes any block on its head.
type memChain struct {
	mu      sync.Mutex
	headers []*types.Header // by number
	feed    event.Feed
	verify  func(*types.Block) error
}

func newMemChain() *memChain {
	genesis := &types.Header{Number: new(big.Int), Difficulty: big.NewInt(1), Time: 1_700_000_000}
	return &memChain{headers: []*types.Header{genesis}}
}

func (c *memChain) CurrentHeader() *types.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.headers[len(c.headers)-1]
}

func (c *memChain) header(n uint64) *types.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n >= uint64(len(c.headers)) {
		return nil
	}
	return c.headers[n]
}

func (c *memChain) VerifyBlock(b *types.Block, fresh bool) error {
	if c.verify != nil {
		return c.verify(b)
	}
	return nil
}

func (c *memChain) InsertBlock(b *types.Block) error {
	c.mu.Lock()
	head := c.headers[len(c.headers)-1]
	if b.ParentHash() != head.Hash() {
		c.mu.Unlock()
		return fmt.Errorf("block %d is not on head %d", b.NumberU64(), head.Number)
	}
	c.headers = append(c.headers, b.Header())
	c.mu.Unlock()
	c.feed.Send(b.Header())
	return nil
}

func (c *memChain) SubscribeHeads(ch chan<- *types.Header) event.Subscription {
	return c.feed.Subscribe(ch)
}

// nodeCluster runs validators over an in-memory network on one simulated
// clock, each with a builder that proposes whenever its node asks.
type nodeCluster struct {
	t      *testing.T
	net    *testNet
	clock  *mclock.Simulated
	nodes  []*Node
	chains []*memChain
	down   map[int]bool // never started, receives nothing
	stop   chan struct{}
	wg     sync.WaitGroup
	built  atomic.Int64
}

func newNodeCluster(t *testing.T, net *testNet, bftBlock uint64, down map[int]bool, validators ValidatorsFunc) *nodeCluster {
	t.Helper()
	c := &nodeCluster{t: t, net: net, clock: new(mclock.Simulated), down: down, stop: make(chan struct{})}
	if validators == nil {
		validators = func(uint64) (*ValidatorSet, error) { return c.net.set, nil }
	}
	for i := range net.keys {
		wal, _, err := OpenWAL(filepath.Join(t.TempDir(), "wal"), true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { wal.Close() })
		i := i
		chain := newMemChain()
		c.chains = append(c.chains, chain)
		c.nodes = append(c.nodes, NewNode(NodeConfig{
			Config: nodeTestConfig, ChainID: testChainID, BftBlock: bftBlock,
			Key: c.net.keys[i], WAL: wal, Validators: validators, Clock: c.clock,
			Broadcast: func(m *Message) { c.deliver(i, m) },
		}, chain))
	}
	for i, nd := range c.nodes {
		if down[i] {
			continue
		}
		nd.Start()
		c.wg.Add(1)
		go c.builder(i)
	}
	t.Cleanup(c.close)
	return c
}

func (c *nodeCluster) close() {
	close(c.stop)
	c.wg.Wait()
	for i, nd := range c.nodes {
		if !c.down[i] {
			nd.Stop()
		}
	}
}

func (c *nodeCluster) deliver(from int, m *Message) {
	for i, nd := range c.nodes {
		if i != from && !c.down[i] {
			cpy := *m
			nd.HandleMessage(&cpy)
		}
	}
}

// builder stands in for the miner: it builds on the head whenever the node
// wants a block, with a state root unique to the builder so every proposal
// is a different block.
func (c *nodeCluster) builder(i int) {
	defer c.wg.Done()
	for {
		select {
		case <-c.stop:
			return
		case <-time.After(time.Millisecond):
		}
		head := c.chains[i].CurrentHeader()
		next := head.Number.Uint64() + 1
		if !c.nodes[i].ProposalWanted(next, false) {
			continue
		}
		h := &types.Header{
			ParentHash: head.Hash(), Number: new(big.Int).SetUint64(next), Difficulty: big.NewInt(1),
			Time: head.Time + 1, Root: common.BigToHash(big.NewInt(int64(1000*i) + int64(next))),
			MinerNodeId: pubKeyOf(c.net.keys[i]),
		}
		c.built.Add(1)
		c.nodes[i].SubmitBlock(types.NewBlockWithHeader(h))
	}
}

// runUntil advances the simulated clock until cond holds.
func (c *nodeCluster) runUntil(what string, cond func() bool) {
	c.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			c.t.Fatalf("timed out waiting for %s", what)
		}
		c.clock.Run(20 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
}

func (c *nodeCluster) allAt(height uint64) bool {
	for i, chain := range c.chains {
		if !c.down[i] && chain.CurrentHeader().Number.Uint64() < height {
			return false
		}
	}
	return true
}

// checkAgreement: every running node has the same blocks, each proven by
// its own seals.
func (c *nodeCluster) checkAgreement(from, to uint64) {
	c.t.Helper()
	ref := -1
	for i := range c.chains {
		if c.down[i] {
			continue
		}
		if ref < 0 {
			ref = i
		}
		for n := from; n <= to; n++ {
			h, want := c.chains[i].header(n), c.chains[ref].header(n)
			if h.Hash() != want.Hash() {
				c.t.Fatalf("node %d and node %d disagree at height %d", i, ref, n)
			}
			if err := VerifySeals(h, testChainID, c.net.set); err != nil {
				c.t.Fatalf("node %d, height %d: %v", i, n, err)
			}
		}
	}
}

func TestNodeCommitsHeights(t *testing.T) {
	c := newNodeCluster(t, newTestNet(t, 4), 1, nil, nil)
	c.runUntil("height 6", func() bool { return c.allAt(6) })
	c.checkAgreement(1, 6)
}

// TestNodeProposerDown: the round-0 proposer of the first height never
// starts; a round change moves the proposal to the next validator.
func TestNodeProposerDown(t *testing.T) {
	net := newTestNet(t, 4)
	first, _ := indexOfProposer(net, 1)
	c := newNodeCluster(t, net, 1, map[int]bool{first: true}, nil)
	c.runUntil("height 4", func() bool { return c.allAt(4) })
	c.checkAgreement(1, 4)
	var rounds []uint64
	for n := uint64(1); n <= 4; n++ {
		rounds = append(rounds, c.chains[(first+1)%4].header(n).BftRound)
	}
	if rounds[0] == 0 {
		t.Errorf("height 1 committed in round 0 without its proposer; rounds %v", rounds)
	}
}

// TestNodeIdleBeforeSwitch: below bftBlock the node wants no blocks and
// signs nothing; once sync brings the head to bftBlock-1 it starts.
func TestNodeIdleBeforeSwitch(t *testing.T) {
	net := newTestNet(t, 4)
	c := newNodeCluster(t, net, 3, map[int]bool{1: true, 2: true, 3: true}, nil)
	nd, chain := c.nodes[0], c.chains[0]
	c.clock.Run(10 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if h, _ := nd.Status(); h != 0 || c.built.Load() != 0 {
		t.Fatalf("active below the switch: height %d, %d blocks built", h, c.built.Load())
	}
	for n := uint64(1); n <= 2; n++ { // imported PoA blocks
		head := chain.CurrentHeader()
		if err := chain.InsertBlock(types.NewBlockWithHeader(&types.Header{ParentHash: head.Hash(), Number: new(big.Int).SetUint64(n), Difficulty: big.NewInt(1)})); err != nil {
			t.Fatal(err)
		}
	}
	c.runUntil("height 3 to start", func() bool { h, _ := nd.Status(); return h == 3 })
}

// TestNodeEmptyBlockInterval: with nothing to include, round 0 waits
// EmptyBlockInterval after the parent before it wants a block; pending
// transactions want one at once.
func TestNodeEmptyBlockInterval(t *testing.T) {
	net := newTestNet(t, 4)
	proposer, _ := indexOfProposer(net, 1)
	down := map[int]bool{}
	for i := range net.keys {
		down[i] = true
	}
	c := newNodeCluster(t, net, 1, down, nil)
	nd := c.nodes[proposer]
	var wakes atomic.Int64
	nd.cfg.OnProposalWanted = func() { wakes.Add(1) }
	nd.Start()
	defer nd.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for !nd.ProposalWanted(1, true) {
		if time.Now().After(deadline) {
			t.Fatal("the round-0 proposer never asked for a block")
		}
		time.Sleep(time.Millisecond)
	}
	if nd.ProposalWanted(1, false) {
		t.Fatal("an empty block wanted before EmptyBlockInterval")
	}
	if nd.ProposalWanted(2, true) {
		t.Fatal("a block wanted for a height not running")
	}
	// The builder is woken for the request, and again when the empty block
	// is due, so it need not poll.
	requested := wakes.Load()
	if requested == 0 {
		t.Fatal("builder not woken for the request")
	}
	c.clock.Run(nodeTestConfig.EmptyBlockInterval)
	if !nd.ProposalWanted(1, false) {
		t.Fatal("no empty block wanted after EmptyBlockInterval")
	}
	if wakes.Load() == requested {
		t.Fatal("builder not woken when the empty block became due")
	}
}

// TestNodeRetriesValidatorSet: a height whose validator set cannot be read
// yet (governance still syncing) is retried rather than given up.
func TestNodeRetriesValidatorSet(t *testing.T) {
	net := newTestNet(t, 4)
	var ready atomic.Bool
	validators := func(uint64) (*ValidatorSet, error) {
		if !ready.Load() {
			return nil, errors.New("governance not synced")
		}
		return net.set, nil
	}
	c := newNodeCluster(t, net, 1, nil, validators)
	c.clock.Run(10 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if c.built.Load() != 0 || !c.allAt(0) || c.allAt(1) {
		t.Fatal("progress without a validator set")
	}
	ready.Store(true)
	c.runUntil("height 2", func() bool { return c.allAt(2) })
	c.checkAgreement(1, 2)
}

func indexOfProposer(net *testNet, height uint64) (int, bool) {
	v := net.set.Proposer(height, 0)
	return net.set.IndexOf(v.PubKey[:])
}

type failingChain struct{ *memChain }

func (failingChain) InsertBlock(*types.Block) error { return errors.New("disk full") }

// TestNodeCounters: a decided block that cannot be written and a message
// the full queue turns away are both counted for the status RPC.
func TestNodeCounters(t *testing.T) {
	net := newTestNet(t, 4)
	n := NewNode(NodeConfig{Config: nodeTestConfig, ChainID: testChainID, BftBlock: 1,
		Validators: func(uint64) (*ValidatorSet, error) { return net.set, nil }, Broadcast: func(*Message) {}}, failingChain{newMemChain()})
	// Not started: the loop is not draining, so the queue fills.
	for i := 0; i < msgQueue+2; i++ {
		n.HandleMessage(&Message{Type: MsgPrepare, Height: 1})
	}
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1)})
	n.Commit(blockProposal{block}, 0, nil)
	if c := n.Counters(); c.DroppedMessages != 2 || c.InsertFailures != 1 {
		t.Errorf("counters %+v, want 2 dropped and 1 insert failure", c)
	}
}

// TestNodeRecordsRejection: the last proposal a node refused, and why, is
// kept for the status RPC (design §9.3.1).
func TestNodeRecordsRejection(t *testing.T) {
	net := newTestNet(t, 4)
	chain := newMemChain()
	chain.verify = func(*types.Block) error { return errors.New("leaves 3 governance nodes") }
	n := NewNode(NodeConfig{Config: nodeTestConfig, ChainID: testChainID, BftBlock: 1,
		Validators: func(uint64) (*ValidatorSet, error) { return net.set, nil }, Broadcast: func(*Message) {}}, chain)
	n.head = chain.CurrentHeader() // loop-owned; the loop is not running
	block := types.NewBlockWithHeader(&types.Header{ParentHash: n.head.Hash(), Number: big.NewInt(1), Difficulty: big.NewInt(1)})
	if err := n.VerifyProposal(blockProposal{block}, true); err == nil {
		t.Fatal("proposal accepted")
	}
	r := n.FullStatus().LastRejection
	if r == nil || r.Height != 1 || r.Hash != block.Hash() || r.Reason != "leaves 3 governance nodes" {
		t.Errorf("last rejection %+v", r)
	}
}
