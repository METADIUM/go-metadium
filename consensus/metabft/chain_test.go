package metabft

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/event"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
)

// chainEnv is a PBFT chain from block 1 on a real core.BlockChain, with the
// metadium hooks the PoA engine calls replaced: the builder signs as
// signBlock does, and the reward distribution is a function of the fees.
type chainEnv struct {
	t         *testing.T
	net       *testNet
	coinbases []common.Address
	set       *ValidatorSet
	genesis   *core.Genesis
	builder   int    // whose key SignBlock uses
	nodes     uint64 // governance node count every block leaves
}

func newChainEnv(t *testing.T) *chainEnv {
	t.Helper()
	usePoAMode(t)
	env := &chainEnv{t: t, net: newTestNet(t, 4), nodes: 4}
	for i := range env.net.keys {
		env.coinbases = append(env.coinbases, common.Address{0xc0 + byte(i)})
	}
	var err error
	if env.set, err = env.net.set.WithCoinbases(env.coinbases); err != nil {
		t.Fatal(err)
	}
	config := engineConfig()
	config.BftBlock = big.NewInt(1)
	env.genesis = &core.Genesis{Config: config, GasLimit: 105_000_000, Difficulty: big.NewInt(1),
		Timestamp: uint64(time.Now().Unix()) - 1000}

	oldSign, oldRewards := metaminer.SignBlockFunc, metaminer.CalculateRewardsFunc
	metaminer.SignBlockFunc = func(height *big.Int, hash common.Hash, isPangyo bool) (common.Address, []byte, []byte, error) {
		sig, err := crypto.Sign(ethash.BftBuilderSigHash(height, hash), env.net.keys[env.builder])
		return env.coinbases[env.builder], nil, sig, err
	}
	metaminer.CalculateRewardsFunc = func(num, blockReward, fees *big.Int, addBalance func(common.Address, *big.Int)) (*common.Address, []byte, error) {
		return nil, []byte(`[{"block":` + num.String() + `,"fees":` + fees.String() + `}]`), nil
	}
	t.Cleanup(func() { metaminer.SignBlockFunc, metaminer.CalculateRewardsFunc = oldSign, oldRewards })
	return env
}

func (env *chainEnv) newChain() (*core.BlockChain, *Engine) {
	env.t.Helper()
	engine := NewEngine(ethash.NewFaker(), func(uint64) (*ValidatorSet, error) { return env.set, nil })
	engine.SetNodeCount(func(consensus.ChainHeaderReader, consensus.Engine, *types.Header, *state.StateDB) (uint64, error) {
		return env.nodes, nil
	})
	bc, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), nil, env.genesis, nil, engine, vm.Config{}, nil, nil)
	if err != nil {
		env.t.Fatal(err)
	}
	env.t.Cleanup(bc.Stop)
	return bc, engine
}

// propose builds an empty block on parent as validator builder would.
func (env *chainEnv) propose(bc *core.BlockChain, engine *Engine, parent *types.Header, builder int) *types.Block {
	env.t.Helper()
	statedb, err := bc.StateAt(parent.Root)
	if err != nil {
		env.t.Fatal(err)
	}
	var parentBlobGasUsed uint64
	if parent.BlobGasUsed != nil {
		parentBlobGasUsed = parent.BlobGasUsed.Uint64()
	}
	header := &types.Header{
		ParentHash: parent.Hash(), Number: new(big.Int).Add(parent.Number, common.Big1),
		GasLimit: parent.GasLimit, Time: parent.Time + 1, Fees: new(big.Int),
		ExcessBlobGas: types.CalcExcessBlobGas(parent.ExcessBlobGas, parentBlobGasUsed), BlobGasUsed: new(big.Int),
	}
	header.Difficulty = engine.CalcDifficulty(bc, header.Time, parent)
	env.builder = builder
	block, err := engine.FinalizeAndAssemble(bc, header, statedb, nil, nil, nil, nil)
	if err != nil {
		env.t.Fatal(err)
	}
	return block
}

// seal attaches commit seals for round by the given validators.
func (env *chainEnv) seal(block *types.Block, round uint64, sealers ...int) *types.Block {
	h := block.Header()
	h.BftRound = round
	for _, i := range sealers {
		seal, err := SignCommitSeal(CommitDigest(h.Hash(), round, testChainID), env.net.keys[i])
		if err != nil {
			env.t.Fatal(err)
		}
		h.CommitSeals = append(h.CommitSeals, seal)
	}
	return block.WithSeal(h)
}

// withHeader returns block with its header changed by edit.
func withHeader(block *types.Block, edit func(*types.Header)) *types.Block {
	h := block.Header()
	edit(h)
	return block.WithSeal(h)
}

// build commits n blocks on bc and returns them.
func (env *chainEnv) build(bc *core.BlockChain, engine *Engine, n int) []*types.Block {
	env.t.Helper()
	var blocks []*types.Block
	for i := 0; i < n; i++ {
		b := env.seal(env.propose(bc, engine, bc.CurrentBlock(), i%4), 0, 0, 1, 2)
		if _, err := bc.InsertChain(types.Blocks{b}); err != nil {
			env.t.Fatalf("block %d: %v", b.NumberU64(), err)
		}
		blocks = append(blocks, b)
	}
	return blocks
}

// TestBlockChainImportsBatch: a node syncing takes PBFT blocks in batches,
// whose headers are verified before their parents are executed; the signer
// checks then run at body validation (engine.VerifyUncles).
func TestBlockChainImportsBatch(t *testing.T) {
	env := newChainEnv(t)
	src, srcEngine := env.newChain()
	blocks := env.build(src, srcEngine, 4)

	dst, _ := env.newChain()
	if n, err := dst.InsertChain(blocks); err != nil {
		t.Fatalf("batch import stopped at %d: %v", n, err)
	}
	if head := dst.CurrentBlock(); head.Hash() != blocks[3].Hash() {
		t.Fatalf("head %d, want %d", head.Number, blocks[3].NumberU64())
	}

	for name, bad := range map[string]*types.Block{
		"too few seals": env.seal(withHeader(blocks[2], func(h *types.Header) { h.CommitSeals = nil }), 0, 0, 1),
		"wrong rewards": env.seal(withHeader(blocks[2], func(h *types.Header) { h.CommitSeals, h.Rewards = nil, []byte("[]") }), 0, 0, 1, 2),
	} {
		dst, _ := env.newChain()
		batch := types.Blocks{blocks[0], blocks[1], bad}
		if bad.Hash() == blocks[2].Hash() { // seals are outside the hash: the batch still links
			batch = append(batch, blocks[3])
		}
		n, err := dst.InsertChain(batch)
		if err == nil || n != 2 {
			t.Errorf("%s: batch import stopped at %d with %v, want a failure at 2", name, n, err)
		}
		if head := dst.CurrentBlock().Number.Uint64(); head != 2 {
			t.Errorf("%s: head %d after the failure, want 2", name, head)
		}
	}
}

// TestBlockChainVerifyBlock is design §4.8 on a proposal: header without
// seals, timestamp bound for fresh proposals, body, execution, rewards.
func TestBlockChainVerifyBlock(t *testing.T) {
	env := newChainEnv(t)
	bc, engine := env.newChain()
	env.build(bc, engine, 1)
	chain := NewBlockChain(bc, engine, nil)
	head := bc.CurrentBlock()
	good := env.propose(bc, engine, head, 1)
	chain.now = func() time.Time { return time.Unix(int64(good.Time()), 0) }

	if err := chain.VerifyBlock(good, true); err != nil {
		t.Fatalf("a valid proposal: %v", err)
	}
	late := func() time.Time { return time.Unix(int64(good.Time())+60, 0) }
	chain.now = late
	if err := chain.VerifyBlock(good, true); !errors.Is(err, errTimeDrift) {
		t.Errorf("a fresh proposal a minute old: %v", err)
	}
	if err := chain.VerifyBlock(good, false); err != nil {
		t.Errorf("a re-proposal a minute old: %v", err)
	}
	chain.now = func() time.Time { return time.Unix(int64(good.Time()), 0) }

	resign := func(h *types.Header, key int) {
		h.MinerNodeSig, _ = crypto.Sign(ethash.BftBuilderSigHash(h.Number, h.Root), env.net.keys[key])
	}
	for _, tt := range []struct {
		name  string
		block *types.Block
		want  error
	}{
		{"sealed", env.seal(good, 0, 0, 1, 2), errSealedProposal},
		{"wrong rewards", withHeader(good, func(h *types.Header) { h.Rewards = []byte("[]") }), errBadRewards},
		{"another validator's coinbase", withHeader(good, func(h *types.Header) { h.Coinbase = env.coinbases[2] }), errBadProposer},
		{"built by a non-validator", withHeader(good, func(h *types.Header) {
			outsider, _ := crypto.GenerateKey()
			h.MinerNodeId = pubKeyOf(outsider)
			h.MinerNodeSig, _ = crypto.Sign(ethash.BftBuilderSigHash(h.Number, h.Root), outsider)
		}), errBadProposer},
		{"wrong state root", withHeader(good, func(h *types.Header) { h.Root = common.Hash{1}; resign(h, 1) }), nil},
		{"wrong fees", withHeader(good, func(h *types.Header) { h.Fees = big.NewInt(1) }), nil},
		{"with an uncle", types.NewBlockWithHeader(good.Header()).WithBody(nil, []*types.Header{head}), nil},
	} {
		err := chain.VerifyBlock(tt.block, true)
		if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
}

// TestBlockChainInsertBlock: a decided block is imported with its seals,
// reported as the new head, and posted for broadcast.
func TestBlockChainInsertBlock(t *testing.T) {
	env := newChainEnv(t)
	bc, engine := env.newChain()
	mux := new(event.TypeMux)
	mined := mux.Subscribe(core.NewMinedBlockEvent{})
	defer mined.Unsubscribe()
	chain := NewBlockChain(bc, engine, mux)
	heads := make(chan *types.Header, 1)
	sub := chain.SubscribeHeads(heads)
	defer sub.Unsubscribe()

	proposal := env.propose(bc, engine, chain.CurrentHeader(), 1)
	if err := chain.InsertBlock(proposal); !errors.Is(err, errNotEnoughSeals) {
		t.Fatalf("inserting an unsealed block: %v", err)
	}
	sealed := env.seal(proposal, 2, 1, 2, 3)
	if err := chain.InsertBlock(sealed); err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-heads:
		if h.Hash() != sealed.Hash() || h.BftRound != 2 || len(h.CommitSeals) != 3 {
			t.Errorf("head %d round %d with %d seals", h.Number, h.BftRound, len(h.CommitSeals))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no head reported")
	}
	select {
	case ev := <-mined.Chan():
		if ev.Data.(core.NewMinedBlockEvent).Block.Hash() != sealed.Hash() {
			t.Error("another block posted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("decided block not posted for broadcast")
	}
}

// TestBlockChainValidatorFloor: a block that leaves governance with fewer
// than MinValidators nodes is refused as a proposal and at import (design
// §9.3.1), and the chain before it is untouched.
func TestBlockChainValidatorFloor(t *testing.T) {
	env := newChainEnv(t)
	src, srcEngine := env.newChain()
	blocks := env.build(src, srcEngine, 2)

	env.nodes = 3 // from here on every block would leave three
	proposal := env.propose(src, srcEngine, src.CurrentBlock(), 3)
	chain := NewBlockChain(src, srcEngine, nil)
	chain.now = func() time.Time { return time.Unix(int64(proposal.Time()), 0) }
	if err := chain.VerifyBlock(proposal, true); !errors.Is(err, errTooFewValidators) {
		t.Errorf("proposal leaving 3 nodes: %v", err)
	}

	env.nodes = 4
	dst, _ := env.newChain()
	if _, err := dst.InsertChain(blocks[:1]); err != nil {
		t.Fatal(err)
	}
	env.nodes = 3
	if n, err := dst.InsertChain(blocks[1:]); !errors.Is(err, errTooFewValidators) || n != 0 {
		t.Errorf("importing a block that leaves 3 nodes: %d, %v", n, err)
	}
	if head := dst.CurrentBlock().Number.Uint64(); head != 1 {
		t.Errorf("head %d, want 1", head)
	}
}
