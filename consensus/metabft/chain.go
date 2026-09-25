package metabft

import (
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

// BlockChain adapts a core.BlockChain to the node (Chain).
type BlockChain struct {
	bc     *core.BlockChain
	engine *Engine
	body   core.Validator // ValidateBody for proposals: no seals to check
	drift  time.Duration
	mux    *event.TypeMux
	now    func() time.Time

	// FetchSidecars obtains a proposal's missing blob sidecars from peers
	// by deadline and stores them (the eth handler, over meta/69). Nil: a
	// proposal whose sidecars are not held locally is refused.
	FetchSidecars func(block *types.Block, deadline time.Time) error
}

// sidecarWait bounds how long a validator waits for a proposal's missing
// blob sidecars; it is well inside the round's timeout.
const sidecarWait = 2 * time.Second

var (
	errTimeDrift          = errors.New("metabft: proposal timestamp too far from the local clock")
	errSidecarUnavailable = errors.New("metabft: proposal's blob sidecars unavailable")
)

// NewBlockChain adapts bc, whose engine is engine. A decided block is posted
// on mux as a core.NewMinedBlockEvent, which the eth handler broadcasts to
// peers that do not take part in consensus; mux may be nil.
func NewBlockChain(bc *core.BlockChain, engine *Engine, mux *event.TypeMux) *BlockChain {
	var drift time.Duration
	if cfg := bc.Config().Bft; cfg != nil {
		drift = time.Duration(cfg.TimeDrift) * time.Second
	}
	return &BlockChain{
		bc:     bc,
		engine: engine,
		body:   core.NewBlockValidator(bc.Config(), bc, proposalEngine{engine}),
		drift:  drift,
		mux:    mux,
		now:    time.Now,
	}
}

// CurrentHeader implements Chain.
func (c *BlockChain) CurrentHeader() *types.Header { return c.bc.CurrentBlock() }

// SubscribeHeads implements Chain.
func (c *BlockChain) SubscribeHeads(ch chan<- *types.Header) event.Subscription {
	return event.NewSubscription(func(quit <-chan struct{}) error {
		events := make(chan core.ChainHeadEvent, 16)
		sub := c.bc.SubscribeChainHeadEvent(events)
		defer sub.Unsubscribe()
		for {
			select {
			case ev := <-events:
				select {
				case ch <- ev.Block.Header():
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	})
}

// VerifyBlock implements Chain: design §4.8 steps 3-5 on a proposal whose
// parent is the head. The block is executed on a copy of the parent state,
// which is dropped; InsertBlock executes it again once it is decided.
func (c *BlockChain) VerifyBlock(block *types.Block, fresh bool) error {
	header := block.Header()
	if fresh {
		// Wall clock, not the monotonic one: header time is wall time. Only
		// here, not on import, where old blocks are proven by their seals.
		now := c.now().Unix()
		if d := int64(header.Time) - now; d > int64(c.drift/time.Second) || -d > int64(c.drift/time.Second) {
			return fmt.Errorf("%w: %d, local %d, bound %v", errTimeDrift, header.Time, now, c.drift)
		}
	}
	if err := c.engine.VerifyProposal(c.bc, header); err != nil {
		return err
	}
	if err := c.body.ValidateBody(block); err != nil {
		return err
	}
	if err := c.haveSidecars(block); err != nil {
		return err
	}
	parent := c.bc.GetHeader(block.ParentHash(), block.NumberU64()-1)
	statedb, err := c.bc.StateAt(parent.Root)
	if err != nil {
		return err
	}
	receipts, _, usedGas, fees, err := c.bc.Processor().Process(block, statedb, *c.bc.GetVMConfig())
	if err != nil {
		return err
	}
	return c.bc.Validator().ValidateState(block, statedb, receipts, usedGas, fees)
}

// haveSidecars makes sure this node holds the blob sidecars of a proposal
// before it votes for it (design §12): a quorum must not decide a block
// whose blob data no honest validator can serve. They come from the blob
// pool, from an earlier fetch, or from peers now.
func (c *BlockChain) haveSidecars(block *types.Block) error {
	var missing bool
	blobs := 0
	for _, tx := range block.Transactions() {
		if tx.Type() != types.BlobTxType {
			continue
		}
		blobs++
		if c.bc.BlobSidecarFn == nil || c.bc.BlobSidecarFn(tx.Hash()) == nil {
			missing = true
		}
	}
	if !missing {
		return nil
	}
	if stored := c.bc.GetBlobSidecars(block.Hash()); len(stored) >= blobs {
		return nil
	}
	if c.FetchSidecars == nil {
		return errSidecarUnavailable
	}
	if err := c.FetchSidecars(block, c.now().Add(sidecarWait)); err != nil {
		return fmt.Errorf("%w: %v", errSidecarUnavailable, err)
	}
	return nil
}

// InsertBlock implements Chain. The block goes through full import, seals
// included, like one from a peer.
func (c *BlockChain) InsertBlock(block *types.Block) error {
	if _, err := c.bc.InsertChain(types.Blocks{block}); err != nil {
		return err
	}
	if c.mux != nil {
		// TypeMux delivers synchronously; the node's event loop must not
		// wait for the broadcast.
		go c.mux.Post(core.NewMinedBlockEvent{Block: block})
	}
	return nil
}

// proposalEngine is the engine as ValidateBody sees it for a proposal: the
// body rules apply, but the builder and rewards were checked with the
// header (Engine.VerifyProposal) and there are no seals yet.
type proposalEngine struct{ *Engine }

func (e proposalEngine) VerifyUncles(chain consensus.ChainReader, block *types.Block) error {
	if len(block.Uncles()) > 0 {
		return errUnclesAtPBFT
	}
	return nil
}

var _ Chain = (*BlockChain)(nil)
