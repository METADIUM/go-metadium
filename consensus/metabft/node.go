package metabft

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

// Chain is what a Node needs from the blockchain (design §6: the core never
// sees the chain; the node adapts it).
type Chain interface {
	CurrentHeader() *types.Header
	// VerifyBlock runs the chain-side checks of design §4.8 (steps 3-6) on a
	// proposal whose parent is the current head: header rules except the
	// seals, timestamp bounds (fresh proposals only, see
	// Backend.VerifyProposal), execution, rewards, and N >= 4 after
	// execution (Engine.VerifyPostState).
	VerifyBlock(block *types.Block, fresh bool) error
	// InsertBlock writes a decided block, seals attached, as the new head.
	InsertBlock(block *types.Block) error
	// SubscribeHeads reports every new head, however the chain got it: this
	// node's commits and blocks imported by sync alike.
	SubscribeHeads(ch chan<- *types.Header) event.Subscription
}

// NodeConfig configures a Node.
type NodeConfig struct {
	Config   Config
	ChainID  uint64
	BftBlock uint64 // first PBFT height; below it the node stays idle
	// Key signs messages; nil for a node that never signs.
	Key *ecdsa.PrivateKey
	// WAL and ObserverUntil come from OpenNodeWAL.
	WAL           *WAL
	ObserverUntil uint64
	Validators    ValidatorsFunc
	// Broadcast sends a signed message to the other validators. It must not
	// block: it is called from the node's event loop.
	Broadcast func(m *Message)
	// OnProposalWanted, if set, is called whenever ProposalWanted may have
	// become true: a new request, or EmptyBlockInterval passing on one. It
	// must not block (Engine.WakeProposer).
	OnProposalWanted func()
	Clock            mclock.Clock // nil: the system's monotonic clock
}

// Node runs a Core against a Chain (design §7.3). One goroutine owns the
// core and feeds it messages, blocks, heads and timeouts with the local
// monotonic time; everything else talks to it through channels.
//
// Proposals are asynchronous: when the core asks for a block, ProposalWanted
// tells the block builder (the miner) to build one on the head, and the
// builder hands it back with SubmitBlock. A decided block is written by the
// node, not by the builder, since it may be another validator's.
type Node struct {
	cfg   NodeConfig
	chain Chain
	clock mclock.Clock
	core  *Core
	log   log.Logger

	msgs   chan *Message
	blocks chan *types.Block
	quit   chan struct{}
	wg     sync.WaitGroup

	insertFailures atomic.Uint64
	droppedMsgs    atomic.Uint64

	// Loop-owned.
	head      *types.Header
	started   uint64        // the height the core was last started on; 0 before the switch
	committed *types.Header // written by Commit, started once the core call returns
	// proposedAt is when a proposal for the running height (proposedFor)
	// was last made here or accepted from its proposer.
	proposedAt  time.Duration
	proposedFor uint64

	// Shared with ProposalWanted and Status.
	mu          sync.Mutex
	want        *proposalRequest
	committedAt time.Duration
	paceFrom    time.Duration // round-0 pacing counts from here (onHead)
	height      uint64
	round       uint64
	observer    bool
	lastReject  *Rejection
	emptyTimer  mclock.Timer // wakes the builder when EmptyBlockInterval passes
	paceTimer   mclock.Timer // wakes the builder when a paced round 0 may build
	paceAt      time.Duration
}

// Rejection is the last proposal this node refused, for the status RPC
// (design §9.3.1: every rejection leaves a reason in metabft_status).
type Rejection struct {
	Height uint64
	Hash   common.Hash
	Reason string
	At     time.Time
}

// NodeStatus is a node's state for the status RPC.
type NodeStatus struct {
	Height, Round uint64
	// Observer: signing nothing until Height commits without this node
	// (a missing or corrupt WAL, design §6.1).
	Observer      bool
	LastRejection *Rejection
	NodeCounters
}

type proposalRequest struct{ height, round uint64 }

const (
	// msgQueue bounds messages waiting for the loop. The network layer has
	// already verified and de-duplicated them, so a full queue means the
	// loop is behind; dropping is safe, since round changes recover.
	msgQueue = 1024

	// validatorRetry is how often a height without a readable validator set
	// is retried (governance not synced yet, design §7.7).
	validatorRetry = time.Second
)

var errNodeStopped = errors.New("metabft node stopped")

// NewNode creates a node; Start runs it.
func NewNode(cfg NodeConfig, chain Chain) *Node {
	if cfg.Clock == nil {
		cfg.Clock = mclock.System{}
	}
	n := &Node{
		cfg:    cfg,
		chain:  chain,
		clock:  cfg.Clock,
		msgs:   make(chan *Message, msgQueue),
		blocks: make(chan *types.Block, 1),
		quit:   make(chan struct{}),
		log:    log.New("module", "metabft"),
	}
	n.core = NewCore(cfg.Config, n, cfg.Key, cfg.WAL, cfg.ObserverUntil)
	return n
}

// Start runs the event loop.
func (n *Node) Start() {
	n.wg.Add(1)
	go n.loop()
}

// Stop ends the event loop and waits for it.
func (n *Node) Stop() {
	close(n.quit)
	n.wg.Wait()
	n.mu.Lock()
	if n.emptyTimer != nil {
		n.emptyTimer.Stop()
	}
	n.mu.Unlock()
}

// HandleMessage queues a message from the network. It does not block.
func (n *Node) HandleMessage(m *Message) {
	select {
	case n.msgs <- m:
	default:
		n.droppedMsgs.Add(1)
		n.log.Warn("Consensus message queue full; dropping", "type", m.Type, "height", m.Height, "round", m.Round)
	}
}

// ProposalWanted reports whether the block builder should build a block for
// height now. Round 0 waits for pending transactions or, without them, for
// EmptyBlockInterval since the parent became the head, so an idle chain
// produces one empty block per interval (design §4.5). With pending
// transactions it also waits for minGap since the parent's proposal (or,
// without one seen here, since the parent became the head), which paces
// blocks to governance's blockCreationTime (§11.3 M-04); the builder is
// woken when minGap passes. A later round wants a block at once.
func (n *Node) ProposalWanted(height uint64, pendingTxs bool, minGap time.Duration) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case n.want == nil || n.want.height != height:
		return false
	case n.want.round > 0:
		return true
	case pendingTxs:
		at := n.paceFrom + minGap
		if wait := at - n.now(); wait > 0 {
			if n.paceTimer == nil || n.paceAt != at {
				if n.paceTimer != nil {
					n.paceTimer.Stop()
				}
				n.paceTimer, n.paceAt = n.clock.AfterFunc(wait, n.wakeBuilder), at
			}
			return false
		}
		return true
	}
	return n.now()-n.committedAt >= n.cfg.Config.EmptyBlockInterval
}

// SubmitBlock hands the core a block built for ProposalWanted. A block that
// no longer fits (the head or the round moved on) is dropped by the loop.
func (n *Node) SubmitBlock(block *types.Block) error {
	select {
	case <-n.quit:
		return errNodeStopped
	default:
	}
	select {
	case n.blocks <- block:
	default:
		// A block is already waiting; the builder retries on its next cycle.
		// Logged, since a loop that stops draining shows up here first.
		n.log.Debug("A proposal is already waiting; dropping this one", "number", block.Number(), "hash", block.Hash())
	}
	return nil
}

// NodeCounters are failures an operator should see (the status RPC, P5-20).
type NodeCounters struct {
	// InsertFailures counts decided blocks this node could not write. The
	// height then waits for sync; a deterministic failure (a local rule the
	// quorum disagrees with) leaves it waiting, and this is where it shows.
	InsertFailures uint64
	// DroppedMessages counts messages the full queue turned away; a dropped
	// COMMIT can cost a round.
	DroppedMessages uint64
}

// Counters returns the failure counters.
func (n *Node) Counters() NodeCounters {
	return NodeCounters{InsertFailures: n.insertFailures.Load(), DroppedMessages: n.droppedMsgs.Load()}
}

// FullStatus returns the node's state for the status RPC.
func (n *Node) FullStatus() NodeStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return NodeStatus{Height: n.height, Round: n.round, Observer: n.observer, LastRejection: n.lastReject, NodeCounters: n.Counters()}
}

// Status returns the height and round being agreed on.
func (n *Node) Status() (height, round uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.height, n.round
}

func (n *Node) now() time.Duration { return time.Duration(n.clock.Now()) }

func (n *Node) loop() {
	defer n.wg.Done()

	heads := make(chan *types.Header, 16)
	sub := n.chain.SubscribeHeads(heads)
	defer sub.Unsubscribe()

	var (
		timer    mclock.ChanTimer
		timerC   <-chan mclock.AbsTime
		armedFor time.Duration
		armed    bool
	)
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	n.onHead(n.chain.CurrentHeader())
	for {
		// The core calls Commit from inside its own handlers, so the next
		// height starts here, after the call that decided it has returned.
		if h := n.committed; h != nil {
			n.committed = nil
			n.onHead(h)
		}
		n.publish()

		// One timer, re-armed only when the deadline moves.
		d, ok := n.core.Deadline()
		if !ok && n.started != 0 && !n.core.HasValidatorSet() {
			// Keep an armed retry: re-arming on every event would never fire.
			d, ok = armedFor, true
			if !armed {
				d = n.now() + validatorRetry
			}
		}
		if ok != armed || d != armedFor {
			if timer != nil {
				timer.Stop()
				timer, timerC = nil, nil
			}
			if ok {
				wait := d - n.now()
				if wait < 0 {
					wait = 0
				}
				timer = n.clock.NewTimer(wait)
				timerC = timer.C()
			}
			armed, armedFor = ok, d
		}

		select {
		case h := <-heads:
			n.onHead(h)
		case m := <-n.msgs:
			arrived := n.now() // before the proposal check, which takes a while
			if err := n.core.HandleMessage(m, arrived); err != nil {
				n.log.Debug("Consensus message rejected", "type", m.Type, "height", m.Height, "round", m.Round, "err", err)
			} else if m.Type == MsgPreprepare && m.Height == n.started {
				n.proposedAt, n.proposedFor = arrived, m.Height
			}
		case b := <-n.blocks:
			n.propose(b)
		case <-timerC:
			timer, timerC, armed = nil, nil, false
			if n.started != 0 && !n.core.HasValidatorSet() {
				n.core.NewHeight(n.started, n.now())
			} else {
				n.core.Tick(n.now())
			}
		case err := <-sub.Err():
			if err != nil {
				n.log.Error("Chain head subscription failed", "err", err)
			}
			return
		case <-n.quit:
			return
		}
	}
}

// onHead starts the height after a new head. Heads at or below the height
// already running change nothing: this node's own commit arrives here too,
// after Commit has already moved on.
func (n *Node) onHead(h *types.Header) {
	if h == nil {
		return
	}
	next := h.Number.Uint64() + 1
	if next < n.cfg.BftBlock || next <= n.started {
		return
	}
	n.head, n.started = h, next
	n.mu.Lock()
	n.want = nil
	n.committedAt = n.now()
	// Pacing counts from the parent's proposal rather than its commit, so
	// the time the parent took to build, check and decide is inside
	// blockCreationTime, as on PoA, instead of added to it (§11.3 M-04).
	n.paceFrom = n.committedAt
	if n.proposedFor == h.Number.Uint64() {
		n.paceFrom = n.proposedAt
	}
	n.mu.Unlock()
	n.core.NewHeight(next, n.now())
}

func (n *Node) propose(b *types.Block) {
	if n.head == nil || b.ParentHash() != n.head.Hash() {
		n.log.Debug("Dropping a block built on a stale head", "number", b.Number(), "parent", b.ParentHash())
		return
	}
	now := n.now()
	if err := n.core.Propose(blockProposal{b}, now); err != nil {
		n.log.Debug("Proposal not used", "number", b.Number(), "err", err)
		return
	}
	n.proposedAt, n.proposedFor = now, b.NumberU64()
	n.mu.Lock()
	n.want = nil
	n.mu.Unlock()
}

// publish exposes the core's position to other goroutines and drops a
// proposal request the core has moved past.
func (n *Node) publish() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.height, n.round = n.core.Height(), n.core.Round()
	n.observer = n.core.Observer()
	if n.want != nil && (n.want.height != n.height || n.want.round != n.round) {
		n.want = nil
	}
}

// Backend, called from the loop only.

func (n *Node) ChainID() uint64 { return n.cfg.ChainID }

func (n *Node) Validators(height uint64) (*ValidatorSet, error) { return n.cfg.Validators(height) }

func (n *Node) DecodeProposal(data []byte) (Proposal, error) {
	b := new(types.Block)
	if err := rlp.DecodeBytes(data, b); err != nil {
		return nil, fmt.Errorf("%w: %v", errBadPayload, err)
	}
	// Not covered by the digest; the decided round and seals are set at
	// commit, so a proposal carrying any is malformed.
	if h := b.Header(); h.BftRound != 0 || len(h.CommitSeals) != 0 {
		return nil, fmt.Errorf("%w: proposal carries commit data", errBadPayload)
	}
	return blockProposal{b}, nil
}

func (n *Node) VerifyProposal(p Proposal, fresh bool) error {
	b := p.(blockProposal).Block
	if n.head == nil || b.ParentHash() != n.head.Hash() {
		return errors.New("proposal is not on the local head")
	}
	err := n.chain.VerifyBlock(b, fresh)
	if err != nil {
		n.mu.Lock()
		n.lastReject = &Rejection{Height: b.NumberU64(), Hash: b.Hash(), Reason: err.Error(), At: time.Now()}
		n.mu.Unlock()
	}
	return err
}

func (n *Node) RequestProposal(height, round uint64) {
	n.mu.Lock()
	n.want = &proposalRequest{height, round}
	if n.emptyTimer != nil {
		n.emptyTimer.Stop()
		n.emptyTimer = nil
	}
	if n.paceTimer != nil {
		n.paceTimer.Stop()
		n.paceTimer = nil
	}
	if round == 0 {
		// An idle builder needs a second wake-up, when an empty block is due.
		if wait := n.committedAt + n.cfg.Config.EmptyBlockInterval - n.now(); wait > 0 {
			n.emptyTimer = n.clock.AfterFunc(wait, n.wakeBuilder)
		}
	}
	n.mu.Unlock()
	n.wakeBuilder()
}

func (n *Node) wakeBuilder() {
	if n.cfg.OnProposalWanted != nil {
		n.cfg.OnProposalWanted()
	}
}

func (n *Node) Broadcast(m *Message) { n.cfg.Broadcast(m) }

func (n *Node) Commit(p Proposal, round uint64, seals [][]byte) {
	b := p.(blockProposal).Block
	h := b.Header() // a copy
	h.BftRound, h.CommitSeals = round, seals
	sealed := b.WithSeal(h)
	if err := n.chain.InsertBlock(sealed); err != nil {
		n.insertFailures.Add(1)
		// The height stays decided here; the block comes back through sync
		// from a validator that wrote it.
		n.log.Error("Cannot write the decided block", "number", sealed.Number(), "hash", sealed.Hash(), "err", err)
		return
	}
	n.log.Info("Committed block", "number", sealed.Number(), "hash", sealed.Hash(), "round", round, "seals", len(seals), "txs", len(sealed.Transactions()))
	n.committed = sealed.Header()
}

// blockProposal is a block as the core sees it.
type blockProposal struct{ *types.Block }

func (p blockProposal) Height() uint64          { return p.NumberU64() }
func (p blockProposal) Encode() ([]byte, error) { return rlp.EncodeToBytes(p.Block) }

// Hash is Header.Hash, which leaves out BftRound and CommitSeals (design §5.2).
func (p blockProposal) Hash() common.Hash { return p.Block.Hash() }
