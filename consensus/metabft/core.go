package metabft

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

// Backend connects the core to the chain and the network. The core never
// touches either directly, so it can run under a deterministic simulator
// (design §6).
type Backend interface {
	ChainID() uint64
	// Validators returns the set that agrees on height (from the state at height-1).
	Validators(height uint64) (*ValidatorSet, error)
	DecodeProposal(data []byte) (Proposal, error)
	// VerifyProposal runs the chain-side checks of design §4.8 (header,
	// timestamp bounds, execution, rewards; N >= 4 is P5-13). The parent must be the
	// local head. fresh is false for a re-proposal of a prepared block: it
	// keeps its original header, Time included, and a quorum already
	// checked that time when it was new, so the local-clock bound (§4.5)
	// does not apply to it again. Applying it could refuse the only block
	// that may safely be proposed, round after round.
	VerifyProposal(p Proposal, fresh bool) error
	// RequestProposal asks for a new block for (height, round); the answer
	// comes back through Core.Propose, whenever the block is ready.
	RequestProposal(height, round uint64)
	// Broadcast sends a signed message to the other validators.
	Broadcast(m *Message)
	// Commit writes the decided block with its seals. The backend then calls
	// Core.NewHeight for the next height.
	Commit(p Proposal, round uint64, seals [][]byte)
}

// Stats counts what a core did; the simulator and metrics read it.
type Stats struct {
	RoundChanges int // ROUND-CHANGEs this node signed
	Reproposals  int // round > 0 proposals that had to re-propose a prepared block
	Commits      int // heights this core decided
}

// Core is the PBFT state machine for one node (design §4.4–§4.5). It is not
// safe for concurrent use: one event loop feeds it, passing the local
// monotonic time with every call.
type Core struct {
	cfg     Config
	backend Backend
	key     *ecdsa.PrivateKey
	self    []byte
	wal     *WAL
	log     log.Logger

	observerUntil uint64 // > 0: do not sign until this height is committed

	height      uint64
	set         *ValidatorSet
	selfIdx     int // -1 when not a validator at this height
	round       uint64
	roundStart  time.Duration
	committedAt time.Duration
	decided     bool

	proposals      map[uint64]Proposal // accepted proposal per round
	sentPreprepare map[uint64]bool
	requested      map[uint64]bool // RequestProposal already asked for this round
	sentPrepare    map[uint64]bool
	sentCommit     map[uint64]bool
	sentRC         map[uint64]bool
	prepares       map[uint64]map[int]*Message
	commits        map[uint64]map[int]*Message
	rcs            map[uint64]map[int]*Message
	rcEvidence     map[uint64]map[int]Proposal // prepared block attached to an RC
	rcRound        map[int]uint64              // highest RC round seen per sender
	farRC          map[int]*Message            // latest ROUND-CHANGE per sender beyond the round window
	prepared       *preparedState              // the highest round this node prepared in

	future []*Message // messages for height+1
	stats  Stats
}

type preparedState struct {
	round    uint64
	proposal Proposal
	cert     []Message // the PREPARE quorum
}

const (
	// maxFuture bounds buffered next-height messages.
	maxFuture = 4096

	// maxRoundsAhead bounds how far past the current round PRE-PREPAREs,
	// PREPAREs and COMMITs are kept, so one validator cannot grow a map per
	// round for the whole height. ROUND-CHANGEs beyond it still count for f+1
	// amplification (see HandleMessage): a validator back from a long outage
	// can be any number of rounds behind.
	maxRoundsAhead = 64

	// walPruneEvery prunes the WAL every this many heights rather than on
	// every decision: a prune rewrites the file and syncs it and its directory.
	walPruneEvery = 16
)

// NewCore creates a core. observerUntil > 0 starts it as an observer that
// signs nothing until that height is committed (design §6.1, OpenNodeWAL).
// key may be nil for a node that never signs.
func NewCore(cfg Config, backend Backend, key *ecdsa.PrivateKey, wal *WAL, observerUntil uint64) *Core {
	c := &Core{cfg: cfg, backend: backend, key: key, wal: wal, observerUntil: observerUntil, selfIdx: -1}
	if key != nil {
		c.self = crypto.FromECDSAPub(&key.PublicKey)[1:]
		c.log = log.New("metabft", fmt.Sprintf("%x", c.self[:4]))
	} else {
		c.log = log.New("metabft", "observer")
	}
	return c
}

// Stats returns the counters.
func (c *Core) Stats() Stats { return c.stats }

// Height returns the height being agreed on.
func (c *Core) Height() uint64 { return c.height }

// Round returns the current round.
func (c *Core) Round() uint64 { return c.round }

// Observer reports whether the core is still refusing to sign.
func (c *Core) Observer() bool { return c.observerUntil > 0 }

// HasValidatorSet reports whether the current height has a validator set;
// without one the core ignores everything until NewHeight is called again.
func (c *Core) HasValidatorSet() bool { return c.set != nil }

func (c *Core) canSign() bool {
	return c.key != nil && c.observerUntil == 0 && c.selfIdx >= 0
}

// NewHeight starts agreement on height: the parent became the local head at
// now, by this core's commit or by import. It restores what the WAL says
// this node already signed at height.
func (c *Core) NewHeight(height uint64, now time.Duration) {
	c.height, c.committedAt, c.decided = height, now, false
	c.proposals = make(map[uint64]Proposal)
	c.sentPreprepare = make(map[uint64]bool)
	c.requested = make(map[uint64]bool)
	c.sentPrepare = make(map[uint64]bool)
	c.sentCommit = make(map[uint64]bool)
	c.sentRC = make(map[uint64]bool)
	c.prepares = make(map[uint64]map[int]*Message)
	c.commits = make(map[uint64]map[int]*Message)
	c.rcs = make(map[uint64]map[int]*Message)
	c.rcEvidence = make(map[uint64]map[int]Proposal)
	c.rcRound = make(map[int]uint64)
	c.farRC = make(map[int]*Message)
	c.prepared = nil

	if c.observerUntil > 0 && height > c.observerUntil {
		c.log.Info("Leaving observer mode", "height", height)
		c.observerUntil = 0
	}
	set, err := c.backend.Validators(height)
	if err != nil {
		c.log.Error("No validator set; not participating", "height", height, "err", err)
		c.set, c.selfIdx = nil, -1
		return
	}
	c.set, c.selfIdx = set, -1
	if c.self != nil {
		if i, ok := set.IndexOf(c.self); ok {
			c.selfIdx = i
		}
	}
	c.restoreLock()

	round := uint64(0)
	if r, ok := c.wal.MaxRound(height); ok {
		round = r // resume where this node left off, never below it
	}
	c.startRound(round, now)

	future := c.future
	c.future = nil
	for _, m := range future {
		if m.Height == height {
			c.HandleMessage(m, now)
		}
	}
}

func (c *Core) restoreLock() {
	lock, ok := c.wal.Lock(c.height)
	if !ok {
		return
	}
	block, err := c.backend.DecodeProposal(lock.Block)
	if err != nil || block.Hash() != lock.Digest {
		c.log.Error("Unusable lock in WAL", "height", c.height, "err", err)
		return
	}
	cert := make([]Message, len(lock.Certificate))
	for i, enc := range lock.Certificate {
		if err := rlp.DecodeBytes(enc, &cert[i]); err != nil {
			c.log.Error("Unusable lock certificate in WAL", "height", c.height, "err", err)
			return
		}
	}
	c.prepared = &preparedState{round: lock.Round, proposal: block, cert: cert}
}

// Deadline returns when the current round times out, and false when no
// timer is running.
func (c *Core) Deadline() (time.Duration, bool) {
	if c.decided || c.set == nil {
		return 0, false
	}
	return c.cfg.deadline(c.round, c.committedAt, c.roundStart), true
}

// Tick handles the passage of time.
func (c *Core) Tick(now time.Duration) {
	if d, ok := c.Deadline(); ok && now >= d {
		c.moveToRound(c.round+1, now)
	}
}

func (c *Core) startRound(round uint64, now time.Duration) {
	c.round, c.roundStart = round, now
	c.proposeIfProposer()
}

// moveToRound enters a higher round and announces it (design §4.5).
func (c *Core) moveToRound(round uint64, now time.Duration) {
	if round <= c.round {
		return // rounds only move forward
	}
	c.round, c.roundStart = round, now
	if c.canSign() && !c.sentRC[round] {
		c.sendRoundChange(round)
	}
	c.replayFarRoundChanges(now)
	c.proposeIfProposer()
}

func (c *Core) isProposer(round uint64) bool {
	return c.selfIdx >= 0 && c.set.Proposer(c.height, round) == c.set.At(c.selfIdx)
}

// proposeIfProposer proposes in the current round when this node is its
// proposer and may: round 0 asks for a new block; later rounds need a quorum
// of round changes, and re-propose the highest prepared block among them.
func (c *Core) proposeIfProposer() {
	r := c.round
	if c.decided || !c.canSign() || !c.isProposer(r) || c.sentPreprepare[r] {
		return
	}
	if _, done := c.wal.Vote(c.height, r, MsgPreprepare); done {
		// Proposed in this round before a restart; proposing again could
		// only be the same block, which is gone. Wait for the timeout.
		return
	}
	if r == 0 {
		c.requestProposal(0)
		return
	}
	rcCert, highest := c.roundChangeCertificate(r)
	if rcCert == nil {
		return // not enough round changes yet
	}
	if highest == nil {
		c.requestProposal(r)
		return
	}
	c.stats.Reproposals++
	c.sendPreprepare(r, highest.proposal, rcCert, highest.cert)
}

// requestProposal asks the backend for a block once per round; every
// ROUND-CHANGE that arrives re-runs proposeIfProposer.
func (c *Core) requestProposal(round uint64) {
	if c.requested[round] {
		return
	}
	c.requested[round] = true
	c.backend.RequestProposal(c.height, round)
}

// Propose supplies the block requested with RequestProposal.
func (c *Core) Propose(p Proposal, now time.Duration) error {
	r := c.round
	switch {
	case c.decided:
		return errors.New("height already decided")
	case !c.canSign() || !c.isProposer(r):
		return errWrongProposer
	case p.Height() != c.height:
		return errWrongHeight
	case c.sentPreprepare[r]:
		return errors.New("already proposed in this round")
	}
	var rcCert, prepCert []Message
	if r > 0 {
		cert, highest := c.roundChangeCertificate(r)
		if cert == nil {
			return errUnjustified
		}
		if highest != nil {
			// A block may be prepared somewhere; only that one is safe.
			p, prepCert = highest.proposal, highest.cert
		}
		rcCert = cert
	}
	c.sendPreprepare(r, p, rcCert, prepCert)
	return nil
}

func (c *Core) sendPreprepare(round uint64, p Proposal, rcCert, prepCert []Message) {
	data, err := p.Encode()
	if err != nil {
		c.log.Error("Cannot encode proposal", "err", err)
		return
	}
	digest := p.Hash()
	if err := c.wal.RecordVote(c.height, round, MsgPreprepare, digest); err != nil {
		c.log.Error("WAL refused PRE-PREPARE", "height", c.height, "round", round, "err", err)
		return
	}
	m := &Message{Type: MsgPreprepare, Height: c.height, Round: round, ChainID: c.backend.ChainID(), Digest: digest,
		Payload: encodePayload(&preprepareBody{Block: data, RoundChanges: rcCert, Prepares: prepCert})}
	if !c.sign(m) {
		return
	}
	c.sentPreprepare[round] = true
	c.backend.Broadcast(m)
	c.acceptProposal(round, p) // the proposer's own block needs no re-verification
}

// highestPrepared is what a round-change quorum says must be re-proposed.
type highestPrepared struct {
	round    uint64
	proposal Proposal
	cert     []Message
}

// roundChangeCertificate returns a quorum of stored round changes for round
// (Extra stripped) and the highest prepared block they report, or nil
// without a quorum.
func (c *Core) roundChangeCertificate(round uint64) ([]Message, *highestPrepared) {
	stored := c.rcs[round]
	if len(stored) < c.set.Quorum() {
		return nil, nil
	}
	idxs := sortedKeys(stored)
	var highest *highestPrepared
	cert := make([]Message, 0, len(idxs))
	for _, i := range idxs {
		m := *stored[i]
		claim, extra, err := decodeRoundChange(&m)
		if err != nil {
			continue // validated on receipt; cannot happen
		}
		if claim.Prepared && (highest == nil || claim.PreparedRound > highest.round) {
			highest = &highestPrepared{round: claim.PreparedRound, proposal: c.rcEvidence[round][i], cert: extra.Prepares}
		}
		m.Extra = nil
		cert = append(cert, m)
	}
	return cert, highest
}

func (c *Core) sendRoundChange(round uint64) {
	claim := &roundChangeClaim{}
	var digest common.Hash
	var extra []byte
	if p := c.prepared; p != nil && p.round < round {
		data, err := p.proposal.Encode()
		if err != nil {
			c.log.Error("Cannot encode prepared block", "err", err)
			return
		}
		claim.Prepared, claim.PreparedRound, digest = true, p.round, p.proposal.Hash()
		extra = encodePayload(&roundChangeExtra{Block: data, Prepares: p.cert})
	}
	m := &Message{Type: MsgRoundChange, Height: c.height, Round: round, ChainID: c.backend.ChainID(), Digest: digest,
		Payload: encodePayload(claim)}
	m.SetExtra(extra)
	if err := c.wal.RecordRoundChange(m); err != nil {
		c.log.Error("WAL refused ROUND-CHANGE", "height", c.height, "round", round, "err", err)
		return
	}
	if !c.sign(m) {
		return
	}
	c.sentRC[round] = true
	c.stats.RoundChanges++
	c.backend.Broadcast(m)
	c.storeRoundChange(m, c.selfIdx, c.prepared)
}

func (c *Core) sign(m *Message) bool {
	if err := m.Sign(c.key); err != nil {
		c.log.Error("Cannot sign", "err", err)
		return false
	}
	return true
}

// HandleMessage processes a message from another validator. The network
// layer has already de-duplicated it and checked it for equivocation
// (design §7.1); Verify runs again here because the core trusts nothing.
func (c *Core) HandleMessage(m *Message, now time.Duration) error {
	switch {
	case m.Height == c.height+1:
		if len(c.future) < maxFuture {
			c.future = append(c.future, m)
		}
		return nil
	case m.Height != c.height || c.set == nil:
		return nil
	}
	if m.Round > c.round+maxRoundsAhead {
		return c.handleFarRoundChange(m, now)
	}
	idx, err := m.Verify(c.backend.ChainID(), c.set)
	if err != nil {
		return err
	}
	if idx == c.selfIdx {
		return nil // own messages are applied when sent
	}
	switch m.Type {
	case MsgPreprepare:
		return c.handlePreprepare(m, idx, now)
	case MsgPrepare:
		c.store(c.prepares, m, idx)
		c.checkPrepared(m.Round)
	case MsgCommit:
		c.store(c.commits, m, idx)
		c.checkCommitted()
	case MsgRoundChange:
		return c.handleRoundChange(m, idx, now)
	}
	return nil
}

func (c *Core) store(into map[uint64]map[int]*Message, m *Message, idx int) {
	byIdx := into[m.Round]
	if byIdx == nil {
		byIdx = make(map[int]*Message)
		into[m.Round] = byIdx
	}
	if _, dup := byIdx[idx]; !dup {
		byIdx[idx] = m // first one counts; conflicts are the handler's evidence
	}
}

func (c *Core) handlePreprepare(m *Message, idx int, now time.Duration) error {
	if c.decided || c.proposals[m.Round] != nil {
		return nil
	}
	if c.set.Proposer(c.height, m.Round) != c.set.At(idx) {
		return errWrongProposer
	}
	body := new(preprepareBody)
	if err := rlp.DecodeBytes(m.Payload, body); err != nil {
		return fmt.Errorf("%w: %v", errBadPayload, err)
	}
	block, err := c.backend.DecodeProposal(body.Block)
	if err != nil {
		return fmt.Errorf("%w: %v", errBadPayload, err)
	}
	if block.Height() != c.height {
		return errWrongHeight
	}
	if block.Hash() != m.Digest {
		return errDigestMismatch
	}
	if m.Round > 0 {
		if err := c.justify(m.Round, body, m.Digest); err != nil {
			return err
		}
	}
	// justify has checked that a proposal with a PREPARE certificate is the
	// prepared block it names.
	fresh := len(body.Prepares) == 0
	if m.Round < c.round {
		// Too late to vote on, but not to learn: the others may have decided
		// this block in that round while this node's timer ran ahead, and a
		// quorum of their COMMITs decides it here too, once the block is known.
		if err := c.backend.VerifyProposal(block, fresh); err != nil {
			return err
		}
		c.proposals[m.Round] = block
		c.checkCommitted()
		return nil
	}
	if m.Round > c.round {
		// A justified proposal for a later round moves this node there.
		c.startRound(m.Round, now)
	}
	if err := c.backend.VerifyProposal(block, fresh); err != nil {
		c.log.Warn("Invalid proposal", "height", c.height, "round", m.Round, "err", err)
		c.moveToRound(c.round+1, now)
		return err
	}
	c.acceptProposal(m.Round, block)
	return nil
}

// justify checks a round > 0 proposal against its round-change quorum: if
// any of the quorum reports a prepared block, the proposal must be the one
// prepared in the highest round, backed by that round's PREPARE quorum.
// This is what keeps a block prepared by an honest quorum from being
// replaced in a later round (design §4.5, §4.7).
func (c *Core) justify(round uint64, body *preprepareBody, digest common.Hash) error {
	chainID := c.backend.ChainID()
	signers := make(map[int]bool, len(body.RoundChanges))
	var (
		prepared   bool
		maxRound   uint64
		maxDigests = map[common.Hash]bool{}
	)
	for i := range body.RoundChanges {
		rc := &body.RoundChanges[i]
		if rc.Type != MsgRoundChange || rc.Height != c.height || rc.Round != round {
			return fmt.Errorf("%w: entry %d is not a ROUND-CHANGE for this round", errUnjustified, i)
		}
		idx, err := rc.VerifyAs(Quoted, chainID, c.set)
		if err != nil {
			return fmt.Errorf("%w: entry %d: %v", errUnjustified, i, err)
		}
		claim, _, err := decodeRoundChange(rc)
		if err != nil {
			return fmt.Errorf("%w: entry %d: %v", errUnjustified, i, err)
		}
		signers[idx] = true
		if !claim.Prepared {
			continue
		}
		switch {
		case !prepared || claim.PreparedRound > maxRound:
			prepared, maxRound = true, claim.PreparedRound
			maxDigests = map[common.Hash]bool{rc.Digest: true}
		case claim.PreparedRound == maxRound:
			maxDigests[rc.Digest] = true
		}
	}
	if len(signers) < c.set.Quorum() {
		return fmt.Errorf("%w: %d round changes, quorum is %d", errUnjustified, len(signers), c.set.Quorum())
	}
	if !prepared {
		return nil // nothing can have been decided: any valid block
	}
	if !maxDigests[digest] {
		return fmt.Errorf("%w: proposal is not the block prepared in round %d", errUnjustified, maxRound)
	}
	// Only a genuine PREPARE quorum settles which claim is real.
	if err := certificate(body.Prepares, MsgPrepare, c.height, maxRound, digest, chainID, c.set); err != nil {
		return fmt.Errorf("%w: %v", errUnjustified, err)
	}
	return nil
}

func (c *Core) acceptProposal(round uint64, p Proposal) {
	c.proposals[round] = p
	if round == c.round && c.canSign() && !c.sentPrepare[round] {
		digest := p.Hash()
		if err := c.wal.RecordVote(c.height, round, MsgPrepare, digest); err != nil {
			c.log.Error("WAL refused PREPARE", "height", c.height, "round", round, "err", err)
		} else {
			m := &Message{Type: MsgPrepare, Height: c.height, Round: round, ChainID: c.backend.ChainID(), Digest: digest}
			if c.sign(m) {
				c.sentPrepare[round] = true
				c.backend.Broadcast(m)
				c.store(c.prepares, m, c.selfIdx)
			}
		}
	}
	c.checkPrepared(round)
	c.checkCommitted()
}

// matching returns the messages in byIdx for digest, ordered by validator.
func matching(byIdx map[int]*Message, digest common.Hash) []*Message {
	var out []*Message
	for _, i := range sortedKeys(byIdx) {
		if byIdx[i].Digest == digest {
			out = append(out, byIdx[i])
		}
	}
	return out
}

func (c *Core) checkPrepared(round uint64) {
	p := c.proposals[round]
	if c.decided || round != c.round || p == nil || (c.prepared != nil && c.prepared.round >= round) {
		return
	}
	digest := p.Hash()
	votes := matching(c.prepares[round], digest)
	if len(votes) < c.set.Quorum() {
		return
	}
	cert := make([]Message, len(votes))
	encCert := make([][]byte, len(votes))
	for i, v := range votes {
		cert[i] = *v
		cert[i].Extra = nil
		encCert[i] = encodePayload(&cert[i])
	}
	block, err := p.Encode()
	if err != nil {
		c.log.Error("Cannot encode proposal", "err", err)
		return
	}
	if err := c.wal.RecordLock(c.height, round, digest, encCert, block); err != nil {
		c.log.Error("WAL refused lock", "height", c.height, "round", round, "err", err)
		return
	}
	c.prepared = &preparedState{round: round, proposal: p, cert: cert}

	if c.canSign() && !c.sentCommit[round] {
		if err := c.wal.RecordVote(c.height, round, MsgCommit, digest); err != nil {
			c.log.Error("WAL refused COMMIT", "height", c.height, "round", round, "err", err)
		} else {
			seal, err := SignCommitSeal(CommitDigest(digest, round, c.backend.ChainID()), c.key)
			if err == nil {
				m := &Message{Type: MsgCommit, Height: c.height, Round: round, ChainID: c.backend.ChainID(), Digest: digest, CommitSeal: seal}
				if c.sign(m) {
					c.sentCommit[round] = true
					c.backend.Broadcast(m)
					c.store(c.commits, m, c.selfIdx)
				}
			}
		}
	}
	c.checkCommitted()
}

// checkCommitted decides the height once a quorum of COMMITs agrees on a
// block this node has. Any round counts: a quorum of commits in an earlier
// round decides just the same.
func (c *Core) checkCommitted() {
	if c.decided {
		return
	}
	for _, round := range sortedKeys(c.commits) {
		p := c.proposals[round]
		if p == nil && c.prepared != nil && c.prepared.round == round {
			p = c.prepared.proposal
		}
		if p == nil {
			continue // missed the block; sync will bring it
		}
		votes := matching(c.commits[round], p.Hash())
		if len(votes) < c.set.Quorum() {
			continue
		}
		seals := make([][]byte, len(votes))
		for i, v := range votes {
			seals[i] = v.CommitSeal
		}
		c.decided = true
		c.stats.Commits++
		if c.height > 0 && c.height%walPruneEvery == 0 {
			if err := c.wal.Prune(c.height - 1); err != nil {
				c.log.Warn("WAL prune failed", "err", err)
			}
		}
		c.backend.Commit(p, round, seals)
		return
	}
}

func (c *Core) handleRoundChange(m *Message, idx int, now time.Duration) error {
	if c.decided || m.Round < c.round {
		return nil
	}
	claim, extra, err := decodeRoundChange(m)
	if err != nil {
		return err
	}
	block, err := checkRoundChangeEvidence(m, claim, extra, c.backend.DecodeProposal, c.backend.ChainID(), c.set)
	if err != nil {
		return err
	}
	var prepared *preparedState
	if block != nil {
		prepared = &preparedState{round: claim.PreparedRound, proposal: block, cert: extra.Prepares}
	}
	c.storeRoundChange(m, idx, prepared)
	if m.Round > c.round {
		c.amplify(now)
	}
	if m.Round == c.round {
		c.proposeIfProposer()
	}
	return nil
}

// handleFarRoundChange keeps a ROUND-CHANGE beyond the round window: only its
// round counts toward amplification, and only the latest one per sender is
// held, until this node reaches that round. PRE-PREPAREs, PREPAREs and
// COMMITs that far ahead are dropped (maxRoundsAhead).
func (c *Core) handleFarRoundChange(m *Message, now time.Duration) error {
	if m.Type != MsgRoundChange || c.decided {
		return nil
	}
	idx, err := m.Verify(c.backend.ChainID(), c.set)
	if err != nil || idx == c.selfIdx {
		return err
	}
	if m.Round <= c.rcRound[idx] {
		return nil
	}
	c.rcRound[idx] = m.Round
	c.farRC[idx] = m
	c.amplify(now)
	return nil
}

// amplify follows f+1 validators past this round — at least one of them is
// honest — to the highest round that f+1 of them have reached.
func (c *Core) amplify(now time.Duration) {
	var ahead []uint64
	for _, r := range c.rcRound {
		if r > c.round {
			ahead = append(ahead, r)
		}
	}
	if len(ahead) >= c.set.F()+1 {
		sort.Slice(ahead, func(i, j int) bool { return ahead[i] > ahead[j] })
		c.moveToRound(ahead[c.set.F()], now)
	}
}

// replayFarRoundChanges applies the held far ROUND-CHANGEs that are now for
// the current round, with full validation, so its proposer can assemble a
// quorum without waiting for them to be sent again.
func (c *Core) replayFarRoundChanges(now time.Duration) {
	for _, i := range sortedKeys(c.farRC) {
		m := c.farRC[i]
		if m.Round <= c.round+maxRoundsAhead {
			delete(c.farRC, i)
			if m.Round >= c.round {
				c.handleRoundChange(m, i, now)
			}
		}
	}
}

func (c *Core) storeRoundChange(m *Message, idx int, prepared *preparedState) {
	c.store(c.rcs, m, idx)
	if c.rcs[m.Round][idx] != m {
		return // a second one from the same sender does not count
	}
	if prepared != nil {
		if c.rcEvidence[m.Round] == nil {
			c.rcEvidence[m.Round] = make(map[int]Proposal)
		}
		c.rcEvidence[m.Round][idx] = prepared.proposal
	}
	if m.Round > c.rcRound[idx] {
		c.rcRound[idx] = m.Round
	}
}

func sortedKeys[K int | uint64, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
