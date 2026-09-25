package metabft

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

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
func GovernanceValidators(height uint64) (*ValidatorSet, error) {
	keys, coinbases, err := metaminer.BftValidators(new(big.Int).SetUint64(height))
	if err != nil {
		return nil, err
	}
	set, err := NewValidatorSet(keys)
	if err != nil {
		return nil, err
	}
	return set.WithCoinbases(coinbases)
}

// Engine is the consensus engine of a PBFT network (design §7.2). One chain
// holds a PoA bootstrap segment below bftBlock and PBFT above it, and a node
// syncing from genesis verifies both, so the engine wraps the PoA engine:
// heights below bftBlock go to it unchanged, heights from bftBlock on are
// verified here.
type Engine struct {
	legacy     *ethash.Ethash
	validators ValidatorsFunc
}

// NewEngine wraps the PoA engine.
func NewEngine(legacy *ethash.Ethash, validators ValidatorsFunc) *Engine {
	return &Engine{legacy: legacy, validators: validators}
}

var (
	errNoValidatorSet    = errors.New("metabft: no validator set for this height")
	errBadProposer       = errors.New("metabft: proposer signature is not by a validator")
	errTimeBeforeParent  = errors.New("metabft: timestamp before the parent's")
	errNotEnoughSeals    = errors.New("metabft: not enough commit seals")
	errBadSeal           = errors.New("metabft: invalid commit seal")
	errDuplicateSeal     = errors.New("metabft: two commit seals from one validator")
	errUnclesAtPBFT      = errors.New("metabft: uncles at a PBFT height")
	errSealingNotRunning = errors.New("metabft: PBFT sealing is not wired to the miner yet")
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

// verifySigners checks the proposer signature and the commit seals against
// the validator set of the parent state. A height whose set cannot be read
// is not accepted: there is no bootstrap fallback here.
func (e *Engine) verifySigners(chain consensus.ChainHeaderReader, header *types.Header) error {
	set, err := e.validators(header.Number.Uint64())
	if err != nil {
		return fmt.Errorf("%w: %v", errNoValidatorSet, err)
	}
	if err := verifyProposerSig(header, set); err != nil {
		return err
	}
	return VerifySeals(header, chain.Config().ChainID.Uint64(), set)
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

// Seal implements consensus.Engine. At PBFT heights a block is decided by the
// core, not sealed here; until that is wired (P5b) sealing refuses.
func (e *Engine) Seal(chain consensus.ChainHeaderReader, block *types.Block, results chan<- *types.Block, stop <-chan struct{}) error {
	if !isBft(chain, block.Number()) {
		return e.legacy.Seal(chain, block, results, stop)
	}
	return errSealingNotRunning
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

var _ consensus.Engine = (*Engine)(nil)
