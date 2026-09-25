package metabft

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
	"github.com/ethereum/go-ethereum/rpc"
)

// ValidatorsFunc returns the validator set that agrees on height.
type ValidatorsFunc func(height uint64) (*ValidatorSet, error)

// GovernanceValidators reads the set from the Metadium governance contract
// (metadium/miner.BftValidators): the state at height-1, governance order,
// with the nodes' coinbases.
//
// Fewer than MinValidators nodes is no set at all: at bftBlock that stops
// the chain rather than letting it run BFT with f = 0 (design §9.3); after
// the switch VerifyPostState keeps it from happening (§9.3.1).
func GovernanceValidators(height uint64) (*ValidatorSet, error) {
	keys, coinbases, err := metaminer.BftValidators(new(big.Int).SetUint64(height))
	if err != nil {
		return nil, err
	}
	if len(keys) < MinValidators {
		return nil, fmt.Errorf("%w: %d governance nodes at block %d, PBFT needs %d", errTooFewValidators, len(keys), height-1, MinValidators)
	}
	set, err := NewValidatorSet(keys)
	if err != nil {
		return nil, err
	}
	return set.WithCoinbases(coinbases)
}

// RewardsFunc computes the rewards field of block number from its fees, as
// the PoA engine's accumulateRewards does (metadium/miner.CalculateRewards
// with no block reward and no crediting).
type RewardsFunc func(number, fees *big.Int) (rewards []byte, err error)

// GovernanceRewards is the Metadium reward distribution.
func GovernanceRewards(number, fees *big.Int) ([]byte, error) {
	_, rewards, err := metaminer.CalculateRewards(number, new(big.Int), fees, nil)
	return rewards, err
}

// Engine is the consensus engine of a PBFT network (design §7.2). One chain
// holds a PoA bootstrap segment below bftBlock and PBFT above it, and a node
// syncing from genesis verifies both, so the engine wraps the PoA engine:
// heights below bftBlock go to it unchanged, heights from bftBlock on are
// verified here.
type Engine struct {
	legacy     *ethash.Ethash
	validators ValidatorsFunc
	rewards    RewardsFunc
	nodeCount  NodeCountFunc

	mu       sync.RWMutex
	proposer Proposer
	wake     chan struct{}
}

// Proposer is the consensus side of block production (Node): it says when
// this node should build a block, and takes the block once built.
type Proposer interface {
	ProposalWanted(height uint64, pendingTxs bool) bool
	SubmitBlock(block *types.Block) error
}

// NewEngine wraps the PoA engine.
func NewEngine(legacy *ethash.Ethash, validators ValidatorsFunc) *Engine {
	return &Engine{legacy: legacy, validators: validators, rewards: GovernanceRewards, nodeCount: GovernanceNodeCount,
		wake: make(chan struct{}, 1)}
}

// SetNodeCount replaces how the post-execution governance node count is
// read (GovernanceNodeCount), for tests and tools without governance.
func (e *Engine) SetNodeCount(f NodeCountFunc) { e.nodeCount = f }

// VerifyPostState implements consensus.PostStateVerifier: at a PBFT height,
// the state a block leaves must keep at least MinValidators governance nodes
// (design §9.3.1, §4.8 step 6). Block import runs it in ValidateState, and
// so does proposal verification; the miner runs it after each transaction
// and leaves out one that would break it. A count that cannot be read is a
// failure too: there is no fallback, as for the validator set.
func (e *Engine) VerifyPostState(chain consensus.ChainHeaderReader, header *types.Header, statedb *state.StateDB) error {
	if !isBft(chain, header.Number) {
		return nil
	}
	n, err := e.nodeCount(chain, e, header, statedb)
	if err != nil {
		return fmt.Errorf("%w: %v", errNodeCountUnreadable, err)
	}
	if n < MinValidators {
		return fmt.Errorf("%w: %d, minimum %d", errTooFewValidators, n, MinValidators)
	}
	return nil
}

// SetProposer connects the node that runs consensus. Until it is set, the
// engine wants no blocks and refuses to seal at PBFT heights.
func (e *Engine) SetProposer(p Proposer) {
	e.mu.Lock()
	e.proposer = p
	e.mu.Unlock()
	e.WakeProposer()
}

func (e *Engine) getProposer() Proposer {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.proposer
}

// ProposalWanted reports whether the miner should build a block for height
// now (Node.ProposalWanted).
func (e *Engine) ProposalWanted(height uint64, pendingTxs bool) bool {
	if p := e.getProposer(); p != nil {
		return p.ProposalWanted(height, pendingTxs)
	}
	return false
}

// ProposalWake fires when ProposalWanted may have become true, so the miner
// need not poll for it. It is created with the engine, before any node is
// connected, so the miner can take it at start-up.
func (e *Engine) ProposalWake() <-chan struct{} { return e.wake }

// WakeProposer signals ProposalWake without blocking; wake-ups collapse.
func (e *Engine) WakeProposer() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

var (
	errNoValidatorSet    = errors.New("metabft: no validator set for this height")
	errBadProposer       = errors.New("metabft: proposer signature is not by a validator")
	errTimeBeforeParent  = errors.New("metabft: timestamp before the parent's")
	errNotEnoughSeals    = errors.New("metabft: not enough commit seals")
	errBadSeal           = errors.New("metabft: invalid commit seal")
	errDuplicateSeal     = errors.New("metabft: two commit seals from one validator")
	errUnclesAtPBFT      = errors.New("metabft: uncles at a PBFT height")
	errBadRewards        = errors.New("metabft: rewards field does not match the reward distribution")
	errSealedProposal    = errors.New("metabft: proposal carries commit data")
	errSealingNotRunning = errors.New("metabft: no consensus node connected")
)

func isBft(chain consensus.ChainHeaderReader, number *big.Int) bool {
	return chain.Config().IsBft(number)
}

// Author implements consensus.Engine.
func (e *Engine) Author(header *types.Header) (common.Address, error) {
	return header.Coinbase, nil
}

// VerifyHeader implements consensus.Engine.
func (e *Engine) VerifyHeader(chain consensus.ChainHeaderReader, header *types.Header) error {
	if !isBft(chain, header.Number) {
		return e.legacy.VerifyHeader(chain, header)
	}
	number := header.Number.Uint64()
	if chain.GetHeader(header.Hash(), number) != nil {
		return nil
	}
	parent := chain.GetHeader(header.ParentHash, number-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	return e.verifyBftHeader(chain, header, parent)
}

// VerifyHeaders implements consensus.Engine, in order, on one goroutine.
func (e *Engine) VerifyHeaders(chain consensus.ChainHeaderReader, headers []*types.Header) (chan<- struct{}, <-chan error) {
	abort := make(chan struct{})
	results := make(chan error, len(headers))
	go func() {
		for i, header := range headers {
			var err error
			switch {
			case !isBft(chain, header.Number):
				err = e.legacy.VerifyHeader(chain, header)
			default:
				var parent *types.Header
				if i == 0 {
					parent = chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
				} else if headers[i-1].Hash() == header.ParentHash {
					parent = headers[i-1]
				}
				if parent == nil {
					err = consensus.ErrUnknownAncestor
				} else {
					err = e.verifyBftHeader(chain, header, parent)
				}
			}
			select {
			case <-abort:
				return
			case results <- err:
			}
		}
	}()
	return abort, results
}

// verifyBftHeader applies design §5.3 at a PBFT height: the PoA engine's own
// header checks, then the proposer signature and the commit seals against
// the validator set of the parent state.
//
// The set is read from the parent state, and block import verifies headers
// ahead of executing them: InsertChain checks a whole batch while the first
// block is still being processed. So when the chain says the parent state
// is not there yet, the signer checks are left to VerifyUncles, which block
// import runs on each block once its parent is written (core.BlockValidator
// .ValidateBody). Nothing is accepted on that basis alone: a block reaches
// the chain's state only through ValidateBody. This is not the PoA
// engine's fallback (acceptUnverifiableBlock), which accepts the block
// unverified; where the state is there but no set can be read, the height
// is refused (design §7.7).
func (e *Engine) verifyBftHeader(chain consensus.ChainHeaderReader, header, parent *types.Header) error {
	if err := e.legacy.VerifyHeaderPBFT(chain, header, parent); err != nil {
		return err
	}
	if header.Time < parent.Time {
		return fmt.Errorf("%w: %d < %d", errTimeBeforeParent, header.Time, parent.Time)
	}
	if parentStateMissing(chain, parent) {
		return nil
	}
	return e.verifySigners(chain, header)
}

// stateReader is implemented by core.BlockChain.
type stateReader interface {
	HasBlockAndState(hash common.Hash, number uint64) bool
}

// parentStateMissing reports whether the chain says parent's state is not
// available (yet). A chain that cannot say, such as the header chain of a
// header-only sync, gets the full check, which then needs the state.
func parentStateMissing(chain consensus.ChainHeaderReader, parent *types.Header) bool {
	sr, ok := chain.(stateReader)
	return ok && !sr.HasBlockAndState(parent.Hash(), parent.Number.Uint64())
}

// verifySigners runs the checks that read the parent state: the builder
// against the validator set, the rewards field, and the commit seals. A
// height whose set cannot be read is not accepted: there is no bootstrap
// fallback here.
func (e *Engine) verifySigners(chain consensus.ChainHeaderReader, header *types.Header) error {
	set, err := e.verifyBuilt(header)
	if err != nil {
		return err
	}
	return VerifySeals(header, chain.Config().ChainID.Uint64(), set)
}

// verifyBuilt checks what the builder is responsible for and returns the
// validator set it was checked against.
func (e *Engine) verifyBuilt(header *types.Header) (*ValidatorSet, error) {
	set, err := e.validators(header.Number.Uint64())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoValidatorSet, err)
	}
	if err := verifyProposerSig(header, set); err != nil {
		return nil, err
	}
	return set, e.verifyRewards(header)
}

// verifyRewards compares the rewards field with the distribution computed
// from the parent state and the block's fees (design §7.5; the fees
// themselves are checked against execution by ValidateState). The PoA path
// overwrites the field with its own result on a header copy and never
// compares, so a wrong field only showed where it changed the state root.
// Where the distribution is not set up in governance (ErrNotInitialized)
// the PoA engine credits the fees to the coinbase and leaves the field
// empty; so must the block. Coinbase is bound to the builder by
// verifyProposerSig.
func (e *Engine) verifyRewards(header *types.Header) error {
	fees := header.Fees
	if fees == nil {
		fees = new(big.Int)
	}
	want, err := e.rewards(header.Number, fees)
	switch {
	case errors.Is(err, metaminer.ErrNotInitialized):
		want = nil
	case err != nil:
		return fmt.Errorf("%w: %v", errBadRewards, err)
	}
	if !bytes.Equal(want, header.Rewards) {
		return fmt.Errorf("%w: have %q, want %q", errBadRewards, header.Rewards, want)
	}
	return nil
}

// VerifyProposal checks a proposal's header before the core votes on it
// (design §4.8 step 3): every rule of an imported header except the commit
// seals, which a proposal cannot have yet. The parent must be in the chain
// with its state.
func (e *Engine) VerifyProposal(chain consensus.ChainHeaderReader, header *types.Header) error {
	if !isBft(chain, header.Number) {
		return fmt.Errorf("metabft: block %v is below bftBlock", header.Number)
	}
	if header.BftRound != 0 || len(header.CommitSeals) != 0 {
		return errSealedProposal
	}
	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	if err := e.legacy.VerifyHeaderPBFT(chain, header, parent); err != nil {
		return err
	}
	if header.Time < parent.Time {
		return fmt.Errorf("%w: %d < %d", errTimeBeforeParent, header.Time, parent.Time)
	}
	_, err := e.verifyBuilt(header)
	return err
}

// verifyProposerSig checks the block builder's identity (design §5.3):
// MinerNodeId names a validator, MinerNodeSig is its signature over
// keccak256(number || root) (ethash.BftBuilderSigHash, the Pangyo form),
// and Coinbase is that validator's coinbase, as the PoA check binds it.
// The builder may be a proposer of an earlier round than the committed one
// (a re-proposal keeps the header byte for byte, design §4.5), so
// membership is what is checked, not the committed round's proposer.
func verifyProposerSig(header *types.Header, set *ValidatorSet) error {
	if len(header.MinerNodeId) != PubKeyLength {
		return fmt.Errorf("%w: minerNodeId has %d bytes", errBadProposer, len(header.MinerNodeId))
	}
	idx, ok := set.IndexOf(header.MinerNodeId)
	if !ok {
		return errBadProposer
	}
	pub, err := crypto.Ecrecover(ethash.BftBuilderSigHash(header.Number, header.Root), header.MinerNodeSig)
	if err != nil || len(pub) != PubKeyLength+1 || !bytes.Equal(pub[1:], header.MinerNodeId) {
		return fmt.Errorf("%w: signature does not match minerNodeId", errBadProposer)
	}
	if want := set.At(idx).Coinbase; header.Coinbase != want {
		return fmt.Errorf("%w: coinbase %v, the builder's is %v", errBadProposer, header.Coinbase, want)
	}
	return nil
}

// VerifySeals checks that header carries a quorum of commit seals, from
// distinct validators of set, over CommitDigest(Hash, BftRound, chainID).
// Every seal must be valid, not only a quorum of them.
func VerifySeals(header *types.Header, chainID uint64, set *ValidatorSet) error {
	digest := CommitDigest(header.Hash(), header.BftRound, chainID)
	seen := make(map[int]bool, len(header.CommitSeals))
	for i, seal := range header.CommitSeals {
		pub, err := RecoverSealSigner(digest, seal)
		if err != nil {
			return fmt.Errorf("%w %d: %v", errBadSeal, i, err)
		}
		idx, ok := set.IndexOf(pub)
		if !ok {
			return fmt.Errorf("%w %d: not by a validator", errBadSeal, i)
		}
		if seen[idx] {
			return fmt.Errorf("%w: validator %d", errDuplicateSeal, idx)
		}
		seen[idx] = true
	}
	if len(seen) < set.Quorum() {
		return fmt.Errorf("%w: %d, quorum is %d", errNotEnoughSeals, len(seen), set.Quorum())
	}
	return nil
}

// VerifyUncles implements consensus.Engine. PBFT blocks have none.
//
// Block import calls it from ValidateBody, one block at a time after the
// parent is written, so this is also where the signer checks run with the
// parent state in place (see verifyBftHeader). They run whether or not
// VerifyHeader already ran them. Without the parent state it returns nil:
// ValidateBody then reports the missing ancestor itself, which is what
// import uses to tell a side chain from a gap.
func (e *Engine) VerifyUncles(chain consensus.ChainReader, block *types.Block) error {
	if !isBft(chain, block.Number()) {
		return e.legacy.VerifyUncles(chain, block)
	}
	if len(block.Uncles()) > 0 {
		return errUnclesAtPBFT
	}
	parent := chain.GetHeader(block.ParentHash(), block.NumberU64()-1)
	if parent == nil || parentStateMissing(chain, parent) {
		return nil
	}
	return e.verifySigners(chain, block.Header())
}

// Prepare implements consensus.Engine.
func (e *Engine) Prepare(chain consensus.ChainHeaderReader, header *types.Header) error {
	return e.legacy.Prepare(chain, header)
}

// Finalize implements consensus.Engine.
func (e *Engine) Finalize(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB,
	txs []*types.Transaction, uncles []*types.Header, withdrawals []*types.Withdrawal) {
	e.legacy.Finalize(chain, header, state, txs, uncles, withdrawals)
}

// FinalizeAndAssemble implements consensus.Engine.
func (e *Engine) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB,
	txs []*types.Transaction, uncles []*types.Header, receipts []*types.Receipt, withdrawals []*types.Withdrawal) (*types.Block, error) {
	return e.legacy.FinalizeAndAssemble(chain, header, state, txs, uncles, receipts, withdrawals)
}

// Seal implements consensus.Engine. At PBFT heights a block is not sealed
// here but proposed: it goes to the node, which decides it with the other
// validators and writes it itself. Nothing is ever sent on results then, so
// a caller that waits on it at a PBFT height waits forever; the miner does
// not (proposeBft passes nil) (design §7.3).
func (e *Engine) Seal(chain consensus.ChainHeaderReader, block *types.Block, results chan<- *types.Block, stop <-chan struct{}) error {
	if !isBft(chain, block.Number()) {
		return e.legacy.Seal(chain, block, results, stop)
	}
	p := e.getProposer()
	if p == nil {
		return errSealingNotRunning
	}
	return p.SubmitBlock(block)
}

// SealHash implements consensus.Engine.
func (e *Engine) SealHash(header *types.Header) common.Hash { return e.legacy.SealHash(header) }

// CalcDifficulty implements consensus.Engine.
func (e *Engine) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent *types.Header) *big.Int {
	return e.legacy.CalcDifficulty(chain, time, parent)
}

// APIs implements consensus.Engine.
func (e *Engine) APIs(chain consensus.ChainHeaderReader) []rpc.API { return e.legacy.APIs(chain) }

// Close implements consensus.Engine.
func (e *Engine) Close() error { return e.legacy.Close() }

var (
	_ consensus.Engine            = (*Engine)(nil)
	_ consensus.PostStateVerifier = (*Engine)(nil)
)
