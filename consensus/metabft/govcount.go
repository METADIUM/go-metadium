package metabft

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
)

// MinValidators is the smallest validator set PBFT tolerates a fault in
// (f >= 1, design §4.1). A block that leaves governance with fewer nodes is
// invalid (design §9.3.1): the chain keeps going without it, where a halt
// would leave no way to add nodes back.
const MinValidators = 4

// The governance calls NodeCount makes, by signature. metadium's contract
// ABIs name the same methods (checked in metadium's tests).
const (
	RegistryLookupSig = "getContractAddress(bytes32)"
	NodeLengthSig     = "getNodeLength()"
)

var (
	errTooFewValidators = errors.New("metabft: block leaves fewer governance nodes than PBFT needs")
	// errNodeCountUnreadable is a failure too, but a different one for an
	// operator: governance is broken or unreachable, not a node removed.
	errNodeCountUnreadable = errors.New("metabft: governance node count cannot be read")

	registryLookupSelector = crypto.Keccak256([]byte(RegistryLookupSig))[:4]
	nodeLengthSelector     = crypto.Keccak256([]byte(NodeLengthSig))[:4]
	governanceContractName = func() (b [32]byte) { copy(b[:], "GovernanceContract"); return }()
)

// NodeCountFunc returns the number of governance nodes in statedb, the
// state after executing header's block.
type NodeCountFunc func(chain consensus.ChainHeaderReader, engine consensus.Engine, header *types.Header, statedb *state.StateDB) (uint64, error)

// GovernanceNodeCount reads getNodeLength from the governance contract in
// statedb, finding the contract through the registry in the same state, so
// a block that replaces the contract is judged by the one it installs.
// metadium's own reads go through committed blocks; this one has to see
// the state of a block that is not written yet.
func GovernanceNodeCount(chain consensus.ChainHeaderReader, engine consensus.Engine, header *types.Header, statedb *state.StateDB) (uint64, error) {
	registry, err := metaminer.BftRegistry(new(big.Int).Sub(header.Number, common.Big1))
	if err != nil {
		return 0, fmt.Errorf("no governance registry: %w", err)
	}
	word, err := staticCall(chain, engine, header, statedb, registry, append(append([]byte{}, registryLookupSelector...), governanceContractName[:]...))
	if err != nil {
		return 0, fmt.Errorf("registry lookup: %w", err)
	}
	gov := common.BytesToAddress(word[12:])
	if gov == (common.Address{}) {
		return 0, errors.New("registry names no governance contract")
	}
	word, err = staticCall(chain, engine, header, statedb, gov, nodeLengthSelector)
	if err != nil {
		return 0, fmt.Errorf("getNodeLength: %w", err)
	}
	n := new(big.Int).SetBytes(word)
	if !n.IsUint64() {
		return 0, fmt.Errorf("getNodeLength returned %v", n)
	}
	return n.Uint64(), nil
}

// evmChain gives the EVM the chain context it asks for.
type evmChain struct {
	consensus.ChainHeaderReader
	engine consensus.Engine
}

func (c evmChain) Engine() consensus.Engine { return c.engine }

// staticCallGas bounds a governance read; the calls are views.
const staticCallGas = 10_000_000

// staticCall runs a view call on statedb and returns the first 32-byte word
// of the result. The state is left as it was: even a static call touches the
// accounts it reaches, so it runs inside a snapshot that is reverted.
func staticCall(chain consensus.ChainHeaderReader, engine consensus.Engine, header *types.Header, statedb *state.StateDB, to common.Address, input []byte) ([]byte, error) {
	snap := statedb.Snapshot()
	defer statedb.RevertToSnapshot(snap)

	evm := vm.NewEVM(core.NewEVMBlockContext(header, evmChain{chain, engine}, &header.Coinbase), vm.TxContext{}, statedb, chain.Config(), vm.Config{})
	out, _, err := evm.StaticCall(vm.AccountRef(common.Address{}), to, input, staticCallGas)
	if err != nil {
		return nil, err
	}
	if len(out) < 32 {
		return nil, fmt.Errorf("%x returned %d bytes", to, len(out))
	}
	return out[:32], nil
}
