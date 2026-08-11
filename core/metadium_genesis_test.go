// metadium_genesis_test.go -- Metadium fork guard.
//
// The embedded genesis JSONs are what a fresh --metadium-mainnet /
// --metadium-testnet node runs its FIRST session with: until a restart swaps
// in the compiled params config, a fork block missing from the JSON simply
// does not exist for that node. camelliaBlock was missing from both networks'
// JSONs, so any fresh sync whose first session crossed the fork would stall
// at the boundary (post-fork headers carry WithdrawalsHash, rejected when
// !IsCamellia). This test requires the embedded configs to carry the fork
// the release activates, and to agree with the compiled params configs.
//
// Note DefaultGenesisBlock() returns the *Ethereum* genesis under PoW
// consensus (test default) -- the Metadium branch is PoA-gated, hence the
// consensus-method switch below (same pattern as the legacypool TRS tests:
// serial, restored via t.Cleanup, never t.Parallel).

package core

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

func TestMetadiumEmbeddedGenesisCarriesCamellia(t *testing.T) {
	old := params.ConsensusMethod
	params.ConsensusMethod = params.ConsensusPoA
	t.Cleanup(func() { params.ConsensusMethod = old })

	for _, n := range []struct {
		name    string
		genesis *Genesis
		hash    common.Hash
		cfg     *params.ChainConfig
	}{
		{"mainnet", DefaultGenesisBlock(), params.MetadiumMainnetGenesisHash, params.MetadiumMainnetChainConfig},
		{"testnet", DefaultTestnetGenesisBlock(), params.MetadiumTestnetGenesisHash, params.MetadiumTestnetChainConfig},
	} {
		// The genesis hash must not move: the config section is not part of
		// the genesis block, so fork-field edits to the JSON must be
		// hash-neutral. (Under PoA, ToBlock reproduces the canonical hashes
		// exactly; under PoW it does not, hence the switch above.)
		if h := n.genesis.ToBlock().Hash(); h != n.hash {
			t.Errorf("%s embedded genesis hash = %s, want %s: the genesis JSON "+
				"edit changed the genesis block itself", n.name, h.Hex(), n.hash.Hex())
		}
		if n.genesis.Config == nil || n.genesis.Config.ChainID == nil ||
			n.genesis.Config.ChainID.Cmp(n.cfg.ChainID) != 0 {
			t.Errorf("%s embedded genesis has wrong chain id: got %v, want %v",
				n.name, n.genesis.Config.ChainID, n.cfg.ChainID)
			continue
		}
		if n.genesis.Config.CamelliaBlock == nil {
			t.Errorf("%s embedded genesis config lacks camelliaBlock: a fresh "+
				"node's first sync session would stall at the fork boundary",
				n.name)
			continue
		}
		if n.genesis.Config.CamelliaBlock.Cmp(n.cfg.CamelliaBlock) != 0 {
			t.Errorf("%s embedded genesis camelliaBlock = %v, params say %v",
				n.name, n.genesis.Config.CamelliaBlock, n.cfg.CamelliaBlock)
		}
	}
}
