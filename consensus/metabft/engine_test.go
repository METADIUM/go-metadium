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
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
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
	sig, err := crypto.Sign(h.Root.Bytes(), net.keys[builder])
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
			h.MinerNodeSig, _ = crypto.Sign(h.Root.Bytes(), outsider.keys[0])
			for _, i := range []int{0, 1, 2} {
				seal, _ := SignCommitSeal(CommitDigest(h.Hash(), 0, testChainID), net.keys[i])
				h.CommitSeals = append(h.CommitSeals, seal)
			}
			return h
		}, errBadProposer},
		{"builder signature by someone else", func() *types.Header {
			h := net.bftHeader(t, parent, 1, 0, nil)
			h.MinerNodeSig, _ = crypto.Sign(h.Root.Bytes(), net.keys[2])
			for _, i := range []int{0, 1, 2} {
				seal, _ := SignCommitSeal(CommitDigest(h.Hash(), 0, testChainID), net.keys[i])
				h.CommitSeals = append(h.CommitSeals, seal)
			}
			return h
		}, errBadProposer},
		{"timestamp before the parent", func() *types.Header {
			h := net.bftHeader(t, parent, 1, 0, nil)
			h.Time = parent.Time - 1
			h.MinerNodeSig, _ = crypto.Sign(h.Root.Bytes(), net.keys[1])
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
