package metabft

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
)

// Stand-ins for the governance contracts: the registry answers any lookup
// with the governance address, and governance returns storage slot 0 as its
// node count. The method signatures are checked against metadium's ABIs in
// metadium's tests.
func registryCode(gov common.Address) []byte {
	code := append([]byte{0x73}, gov.Bytes()...)                        // PUSH20 gov
	return append(code, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3) // MSTORE at 0, RETURN 32 bytes
}

var govCode = []byte{0x60, 0x00, 0x54, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3} // SLOAD 0, MSTORE, RETURN

// TestGovernanceNodeCount: the count is read from the state a block leaves,
// not from the chain, and reading it changes nothing in that state.
func TestGovernanceNodeCount(t *testing.T) {
	usePoAMode(t)
	registry, gov := common.Address{0x0e, 0x01}, common.Address{0x0e, 0x02}
	old := metaminer.BftRegistryFunc
	metaminer.BftRegistryFunc = func(*big.Int) (common.Address, error) { return registry, nil }
	t.Cleanup(func() { metaminer.BftRegistryFunc = old })

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatal(err)
	}
	statedb.SetCode(registry, registryCode(gov))
	statedb.SetCode(gov, govCode)

	_, _, chain, parent := newEngineChain(t)
	engine := NewEngine(ethash.NewFaker(), nil)
	header := &types.Header{ParentHash: parent.Hash(), Number: big.NewInt(engineBftBlock), Difficulty: big.NewInt(1),
		GasLimit: parent.GasLimit, Time: parent.Time, ExcessBlobGas: new(big.Int), BlobGasUsed: new(big.Int)}

	for _, n := range []int64{7, 4, 3, 0} {
		statedb.SetState(gov, common.Hash{}, common.BigToHash(big.NewInt(n)))
		before := statedb.IntermediateRoot(true)
		got, err := GovernanceNodeCount(chain, engine, header, statedb)
		if err != nil || got != uint64(n) {
			t.Fatalf("count %d: read %d, %v", n, got, err)
		}
		if after := statedb.IntermediateRoot(true); after != before {
			t.Fatalf("reading the count changed the state root")
		}
		err = engine.VerifyPostState(chain, header, statedb)
		if want := n < MinValidators; want != errors.Is(err, errTooFewValidators) {
			t.Errorf("%d nodes: %v", n, err)
		}
	}

	// Below bftBlock the rule does not apply.
	pre := types.CopyHeader(header)
	pre.Number = big.NewInt(engineBftBlock - 1)
	if err := engine.VerifyPostState(chain, pre, statedb); err != nil {
		t.Errorf("pre-fork block: %v", err)
	}
	// No readable governance is a failure, not a pass.
	metaminer.BftRegistryFunc = func(*big.Int) (common.Address, error) { return common.Address{}, metaminer.ErrNotInitialized }
	if err := engine.VerifyPostState(chain, header, statedb); !errors.Is(err, errNodeCountUnreadable) {
		t.Errorf("no registry: %v", err)
	}
	metaminer.BftRegistryFunc = func(*big.Int) (common.Address, error) { return common.Address{0xde, 0xad}, nil }
	if err := engine.VerifyPostState(chain, header, statedb); !errors.Is(err, errNodeCountUnreadable) {
		t.Errorf("registry without code: %v", err)
	}
}
