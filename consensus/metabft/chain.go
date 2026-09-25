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
}

var errTimeDrift = errors.New("metabft: proposal timestamp too far from the local clock")

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
