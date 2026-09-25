package metabft

import (
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
	"github.com/ethereum/go-ethereum/params"
)

const engineBftBlock = 5

// engineConfig is the smallest config whose headers pass the PoA engine's
// checks without governance: no London (no base fee rule), no Avocado (no
// mixHash rule), Camellia on as PBFT requires.
func engineConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:        big.NewInt(testChainID),
		HomesteadBlock: big.NewInt(0), EIP150Block: big.NewInt(0), EIP155Block: big.NewInt(0), EIP158Block: big.NewInt(0),
		ByzantiumBlock: big.NewInt(0), ConstantinopleBlock: big.NewInt(0), PetersburgBlock: big.NewInt(0), IstanbulBlock: big.NewInt(0),
		CamelliaBlock: big.NewInt(0),
		BftBlock:      big.NewInt(engineBftBlock),
		Bft:           &params.BftConfig{EmptyBlockInterval: 5, BaseTimeout: 2, MaxBackoffExp: 5, TimeDrift: 2},
	}
}

type fakeChain struct {
	config  *params.ChainConfig
	headers map[common.Hash]*types.Header
}

func (c *fakeChain) Config() *params.ChainConfig  { return c.config }
func (c *fakeChain) CurrentHeader() *types.Header { return nil }
func (c *fakeChain) GetHeader(h common.Hash, n uint64) *types.Header {
	if hd := c.headers[h]; hd != nil && hd.Number.Uint64() == n {
		return hd
	}
	return nil
}
func (c *fakeChain) GetHeaderByNumber(uint64) *types.Header      { return nil }
func (c *fakeChain) GetHeaderByHash(h common.Hash) *types.Header { return c.headers[h] }
func (c *fakeChain) GetTd(common.Hash, uint64) *big.Int          { return big.NewInt(1) }

func usePoAMode(t *testing.T) {
	old := params.ConsensusMethod
	params.ConsensusMethod = params.ConsensusPoA
	t.Cleanup(func() { params.ConsensusMethod = old })
}

// bftHeader builds a PBFT header at height on parent, built by validator
// builder, sealed at round by the validators in sealers.
func (net *testNet) bftHeader(t *testing.T, parent *types.Header, builder int, round uint64, sealers []int) *types.Header {
	t.Helper()
	h := &types.Header{
		ParentHash:      parent.Hash(),
		UncleHash:       types.EmptyUncleHash,
		Root:            common.HexToHash("0x5157"),
		TxHash:          types.EmptyTxsHash,
		ReceiptHash:     types.EmptyReceiptsHash,
		Difficulty:      big.NewInt(1),
		Number:          new(big.Int).Add(parent.Number, common.Big1),
		GasLimit:        parent.GasLimit,
		Time:            parent.Time,
		WithdrawalsHash: &types.EmptyWithdrawalsHash,
		ExcessBlobGas:   new(big.Int),
		BlobGasUsed:     new(big.Int),
		BftRound:        round,
	}
	h.MinerNodeId = pubKeyOf(net.keys[builder])
	sig, err := crypto.Sign(ethash.BftBuilderSigHash(h.Number, h.Root), net.keys[builder])
	if err != nil {
		t.Fatal(err)
	}
	h.MinerNodeSig = sig
	digest := CommitDigest(h.Hash(), round, testChainID)
	for _, i := range sealers {
		seal, err := SignCommitSeal(digest, net.keys[i])
		if err != nil {
			t.Fatal(err)
		}
		h.CommitSeals = append(h.CommitSeals, seal)
	}
	return h
}

func newEngineChain(t *testing.T) (*testNet, *Engine, *fakeChain, *types.Header) {
	t.Helper()
	usePoAMode(t)
	net := newTestNet(t, 4)
	engine := NewEngine(ethash.NewFaker(), func(uint64) (*ValidatorSet, error) { return net.set, nil })
	parent := &types.Header{
		Number: big.NewInt(engineBftBlock - 1), Difficulty: big.NewInt(1), GasLimit: 105_000_000,
		Time: uint64(time.Now().Unix()) - 100, WithdrawalsHash: &types.EmptyWithdrawalsHash,
		ExcessBlobGas: new(big.Int), BlobGasUsed: new(big.Int),
	}
	chain := &fakeChain{config: engineConfig(), headers: map[common.Hash]*types.Header{parent.Hash(): parent}}
	return net, engine, chain, parent
}

func TestEngineVerifiesPBFTHeader(t *testing.T) {
	net, engine, chain, parent := newEngineChain(t)
	good := net.bftHeader(t, parent, 1, 0, []int{0, 1, 2})
	if err := engine.VerifyHeader(chain, good); err != nil {
		t.Fatalf("a valid PBFT header was rejected: %v", err)
	}
	// Re-proposed after round changes: built by validator 1, committed in round 3.
	if err := engine.VerifyHeader(chain, net.bftHeader(t, parent, 1, 3, []int{1, 2, 3})); err != nil {
		t.Errorf("a header committed in a later round than it was built in: %v", err)
	}

	for _, tt := range []struct {
		name   string
		header func() *types.Header
		want   error
	}{
		{"no seals", func() *types.Header { return net.bftHeader(t, parent, 1, 0, nil) }, errNotEnoughSeals},
		{"quorum minus one", func() *types.Header { return net.bftHeader(t, parent, 1, 0, []int{0, 1}) }, errNotEnoughSeals},
		{"one validator sealing twice", func() *types.Header { return net.bftHeader(t, parent, 1, 0, []int{0, 1, 1}) }, errDuplicateSeal},
		{"seal for another round", func() *types.Header {
			h := net.bftHeader(t, parent, 1, 0, []int{0, 1, 2})
			h.BftRound = 1 // same block hash, but the seals were over round 0
			return h
		}, errBadSeal},
		{"seal by a non-validator", func() *types.Header {
			h := net.bftHeader(t, parent, 1, 0, []int{0, 1})
			outsider := newTestNet(t, 1)
			seal, _ := SignCommitSeal(CommitDigest(h.Hash(), 0, testChainID), outsider.keys[0])
			h.CommitSeals = append(h.CommitSeals, seal)
			return h
		}, errBadSeal},
		{"built by a non-validator", func() *types.Header {
			outsider := newTestNet(t, 1)
			h := net.bftHeader(t, parent, 1, 0, nil)
			h.MinerNodeId = pubKeyOf(outsider.keys[0])
			h.MinerNodeSig, _ = crypto.Sign(ethash.BftBuilderSigHash(h.Number, h.Root), outsider.keys[0])
			for _, i := range []int{0, 1, 2} {
				seal, _ := SignCommitSeal(CommitDigest(h.Hash(), 0, testChainID), net.keys[i])
				h.CommitSeals = append(h.CommitSeals, seal)
			}
			return h
		}, errBadProposer},
		{"builder signature by someone else", func() *types.Header {
			h := net.bftHeader(t, parent, 1, 0, nil)
			h.MinerNodeSig, _ = crypto.Sign(ethash.BftBuilderSigHash(h.Number, h.Root), net.keys[2])
			for _, i := range []int{0, 1, 2} {
				seal, _ := SignCommitSeal(CommitDigest(h.Hash(), 0, testChainID), net.keys[i])
				h.CommitSeals = append(h.CommitSeals, seal)
			}
			return h
		}, errBadProposer},
		{"timestamp before the parent", func() *types.Header {
			h := net.bftHeader(t, parent, 1, 0, nil)
			h.Time = parent.Time - 1
			h.MinerNodeSig, _ = crypto.Sign(ethash.BftBuilderSigHash(h.Number, h.Root), net.keys[1])
			for _, i := range []int{0, 1, 2} {
				seal, _ := SignCommitSeal(CommitDigest(h.Hash(), 0, testChainID), net.keys[i])
				h.CommitSeals = append(h.CommitSeals, seal)
			}
			return h
		}, errTimeBeforeParent},
	} {
		if err := engine.VerifyHeader(chain, tt.header()); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
}

// TestEngineNoValidatorSetNoAcceptance: where the PoA engine falls back to
// accepting a block without governance data, a PBFT height is refused
// (design §7.7).
func TestEngineNoValidatorSetNoAcceptance(t *testing.T) {
	net, _, chain, parent := newEngineChain(t)
	engine := NewEngine(ethash.NewFaker(), func(uint64) (*ValidatorSet, error) { return nil, errors.New("snap-sync gap") })
	if err := engine.VerifyHeader(chain, net.bftHeader(t, parent, 1, 0, []int{0, 1, 2})); !errors.Is(err, errNoValidatorSet) {
		t.Errorf("no validator set: %v", err)
	}
}

// TestEngineDelegatesBelowSwitch: below bftBlock the PoA engine decides,
// and it still refuses PBFT fields there.
func TestEngineDelegatesBelowSwitch(t *testing.T) {
	net, engine, chain, parent := newEngineChain(t)
	grand := &types.Header{Number: big.NewInt(engineBftBlock - 2), Difficulty: big.NewInt(1), GasLimit: parent.GasLimit, Time: parent.Time - 1,
		WithdrawalsHash: &types.EmptyWithdrawalsHash, ExcessBlobGas: new(big.Int), BlobGasUsed: new(big.Int)}
	chain.headers[grand.Hash()] = grand
	pre := net.bftHeader(t, grand, 1, 0, []int{0, 1, 2}) // a pre-fork height carrying seals
	if err := engine.VerifyHeader(chain, pre); err == nil || !strings.Contains(err.Error(), "PBFT fields") {
		t.Fatalf("a pre-fork header with seals: %v, want the PoA engine's PBFT-fields rejection", err)
	}
}

func TestEngineBatchAndUncles(t *testing.T) {
	net, engine, chain, parent := newEngineChain(t)
	h5 := net.bftHeader(t, parent, 1, 0, []int{0, 1, 2})
	h6 := net.bftHeader(t, h5, 2, 0, []int{1, 2, 3})
	bad := net.bftHeader(t, h6, 3, 0, []int{0}) // too few seals
	_, results := engine.VerifyHeaders(chain, []*types.Header{h5, h6, bad})
	for i, want := range []error{nil, nil, errNotEnoughSeals} {
		if err := <-results; !errors.Is(err, want) && !(want == nil && err == nil) {
			t.Errorf("header %d: %v, want %v", i, err, want)
		}
	}
	block := types.NewBlockWithHeader(h5).WithBody(nil, []*types.Header{parent})
	if err := engine.VerifyUncles(&fakeChainReader{chain}, block); !errors.Is(err, errUnclesAtPBFT) {
		t.Errorf("uncles at a PBFT height: %v", err)
	}
	if err := engine.Seal(chain, types.NewBlockWithHeader(h5), nil, nil); !errors.Is(err, errSealingNotRunning) {
		t.Errorf("sealing at a PBFT height: %v", err)
	}
}

type fakeChainReader struct{ *fakeChain }

func (fakeChainReader) GetBlock(common.Hash, uint64) *types.Block { return nil }

var _ consensus.ChainReader = fakeChainReader{}

// stateChain says which blocks have their state, as core.BlockChain does.
type stateChain struct {
	fakeChainReader
	hasState map[common.Hash]bool
}

func (c stateChain) HasBlockAndState(h common.Hash, n uint64) bool { return c.hasState[h] }

// TestEngineSignersWithParentState: block import verifies headers before
// their parents are executed, so without the parent state the signer checks
// wait for VerifyUncles, which import runs once the parent is written; with
// it, both paths check.
func TestEngineSignersWithParentState(t *testing.T) {
	net, engine, fc, parent := newEngineChain(t)
	chain := stateChain{fakeChainReader{fc}, map[common.Hash]bool{}}
	unsealed := net.bftHeader(t, parent, 1, 0, nil)
	sealed := net.bftHeader(t, parent, 1, 0, []int{0, 1, 2})

	// Parent not executed yet: nothing that needs its state is decided.
	if err := engine.VerifyHeader(chain, unsealed); err != nil {
		t.Errorf("header ahead of its parent's state: %v", err)
	}
	if err := engine.VerifyUncles(chain, types.NewBlockWithHeader(unsealed)); err != nil {
		t.Errorf("body ahead of its parent's state: %v, want nil so ValidateBody reports the ancestor", err)
	}
	// Header rules that need no state still apply.
	early := net.bftHeader(t, parent, 1, 0, nil)
	early.Time = parent.Time - 1
	if err := engine.VerifyHeader(chain, early); !errors.Is(err, errTimeBeforeParent) {
		t.Errorf("stateless rule without the parent state: %v", err)
	}

	chain.hasState[parent.Hash()] = true
	for name, verify := range map[string]func(*types.Header) error{
		"VerifyHeader": func(h *types.Header) error { return engine.VerifyHeader(chain, h) },
		"VerifyUncles": func(h *types.Header) error { return engine.VerifyUncles(chain, types.NewBlockWithHeader(h)) },
	} {
		if err := verify(unsealed); !errors.Is(err, errNotEnoughSeals) {
			t.Errorf("%s, unsealed, parent state present: %v", name, err)
		}
		if err := verify(sealed); err != nil {
			t.Errorf("%s, sealed, parent state present: %v", name, err)
		}
	}

	// In a batch, a header whose parent is earlier in the batch has no
	// parent state yet; import checks it in VerifyUncles.
	h6 := net.bftHeader(t, sealed, 2, 0, nil)
	_, results := engine.VerifyHeaders(chain, []*types.Header{sealed, h6})
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Errorf("batch header %d: %v", i, err)
		}
	}
	fc.headers[sealed.Hash()] = sealed
	chain.hasState[sealed.Hash()] = true
	if err := engine.VerifyUncles(chain, types.NewBlockWithHeader(h6)); !errors.Is(err, errNotEnoughSeals) {
		t.Errorf("unsealed batch header at import: %v", err)
	}
}

// TestEngineBuilderIdentity: the builder signs height and root, names
// itself in MinerNodeId, and uses its own governance coinbase.
func TestEngineBuilderIdentity(t *testing.T) {
	net, _, chain, parent := newEngineChain(t)
	coinbases := []common.Address{{0xc0}, {0xc1}, {0xc2}, {0xc3}}
	set, err := net.set.WithCoinbases(coinbases)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(ethash.NewFaker(), func(uint64) (*ValidatorSet, error) { return set, nil })
	build := func(coinbase common.Address, sign func(h *types.Header) []byte) *types.Header {
		h := net.bftHeader(t, parent, 1, 0, nil)
		h.Coinbase = coinbase
		h.MinerNodeSig = sign(h)
		for _, i := range []int{0, 1, 2} {
			seal, _ := SignCommitSeal(CommitDigest(h.Hash(), 0, testChainID), net.keys[i])
			h.CommitSeals = append(h.CommitSeals, seal)
		}
		return h
	}
	pangyo := func(h *types.Header) []byte {
		sig, _ := crypto.Sign(ethash.BftBuilderSigHash(h.Number, h.Root), net.keys[1])
		return sig
	}
	rootOnly := func(h *types.Header) []byte { // the pre-Pangyo form, not bound to the height
		sig, _ := crypto.Sign(h.Root.Bytes(), net.keys[1])
		return sig
	}
	if err := engine.VerifyHeader(chain, build(coinbases[1], pangyo)); err != nil {
		t.Errorf("builder with its own coinbase: %v", err)
	}
	if err := engine.VerifyHeader(chain, build(coinbases[2], pangyo)); !errors.Is(err, errBadProposer) {
		t.Errorf("builder naming another validator's coinbase: %v", err)
	}
	if err := engine.VerifyHeader(chain, build(coinbases[1], rootOnly)); !errors.Is(err, errBadProposer) {
		t.Errorf("signature over the root alone: %v", err)
	}
}

// TestEngineAssemblesVerifiableHeader: what the PoA engine assembles at a
// PBFT height passes the PBFT builder check, with the node's signer as
// metadium provides it (signBlock, Pangyo form).
func TestEngineAssemblesVerifiableHeader(t *testing.T) {
	net, engine, chain, parent := newEngineChain(t)
	builder := net.keys[2]
	coinbase := common.Address{0xc2}
	oldSign := metaminer.SignBlockFunc
	metaminer.SignBlockFunc = func(height *big.Int, hash common.Hash, isPangyo bool) (common.Address, []byte, []byte, error) {
		if !isPangyo {
			t.Errorf("block %v signed in the pre-Pangyo form", height)
		}
		sig, err := crypto.Sign(crypto.Keccak256(append(height.Bytes(), hash.Bytes()...)), builder)
		return coinbase, nil, sig, err // signBlock leaves nodeId out after Pangyo
	}
	t.Cleanup(func() { metaminer.SignBlockFunc = oldSign })

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatal(err)
	}
	header := &types.Header{ParentHash: parent.Hash(), Number: big.NewInt(engineBftBlock), Difficulty: big.NewInt(1),
		GasLimit: parent.GasLimit, Time: parent.Time, ExcessBlobGas: new(big.Int), BlobGasUsed: new(big.Int)}
	block, err := engine.FinalizeAndAssemble(chain, header, statedb, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	set, _ := net.set.WithCoinbases([]common.Address{{}, {}, coinbase, {}})
	if err := verifyProposerSig(block.Header(), set); err != nil {
		t.Errorf("assembled header: %v", err)
	}
}

// TestEngineBatchAcrossSwitch: block import verifies a batch whose headers'
// parents are earlier in the batch, not in the chain. That holds for the
// PoA heights too: a batch of the bootstrap segment, running into the
// switch, must verify (found on the private network, where sync stopped at
// "unknown ancestor").
func TestEngineBatchAcrossSwitch(t *testing.T) {
	net, engine, chain, _ := newEngineChain(t)
	old := metaminer.VerifyBlockSigFunc
	metaminer.VerifyBlockSigFunc = func(*big.Int, common.Address, []byte, common.Hash, []byte, bool) bool { return true }
	t.Cleanup(func() { metaminer.VerifyBlockSigFunc = old })

	base := &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1), GasLimit: 105_000_000,
		Time: uint64(time.Now().Unix()) - 100, WithdrawalsHash: &types.EmptyWithdrawalsHash,
		ExcessBlobGas: new(big.Int), BlobGasUsed: new(big.Int)}
	chain.headers[base.Hash()] = base
	batch := []*types.Header{}
	parent := base
	for n := int64(2); n < engineBftBlock; n++ { // PoA heights 2..4
		h := &types.Header{ParentHash: parent.Hash(), UncleHash: types.EmptyUncleHash, Root: common.HexToHash("0x5157"),
			TxHash: types.EmptyTxsHash, ReceiptHash: types.EmptyReceiptsHash, Difficulty: big.NewInt(1),
			Number: big.NewInt(n), GasLimit: parent.GasLimit, Time: parent.Time + 1,
			WithdrawalsHash: &types.EmptyWithdrawalsHash, ExcessBlobGas: new(big.Int), BlobGasUsed: new(big.Int)}
		batch = append(batch, h)
		parent = h
	}
	batch = append(batch, net.bftHeader(t, parent, 1, 0, []int{0, 1, 2})) // PBFT height 5
	_, results := engine.VerifyHeaders(chain, batch)
	for _, h := range batch {
		if err := <-results; err != nil {
			t.Errorf("header %d: %v", h.Number, err)
		}
	}
}
