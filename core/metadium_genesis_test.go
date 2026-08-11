// metadium_genesis_test.go -- Metadium fork guard.
//
// The embedded genesis JSONs are what a fresh --metadium-mainnet /
// --metadium-testnet node runs its FIRST session with: until a restart swaps
// in the compiled params config, a fork block missing from the JSON simply
// does not exist for that node. camelliaBlock was missing from both networks'
// JSONs, so any fresh sync whose first session crossed the fork would stall
// at the boundary (post-fork headers carry WithdrawalsHash, rejected when
// !IsCamellia). This test pins the genesis hashes -- proving config-only JSON
// edits are hash-neutral -- and requires EVERY fork field of the embedded
// configs to agree with the compiled params configs, so the next fork added
// to params but forgotten in the JSON literal fails CI instead of recreating
// this bug.
//
// Note DefaultGenesisBlock() returns the *Ethereum* genesis under PoW
// consensus (the unit-test default of params.ConsensusMethod), and ToBlock
// hashes differently under PoW -- hence the serial consensus-method switch,
// restored via t.Cleanup (precedent: eth/protocols/eth/protocol_test.go's
// ConsensusMethod save/restore; never combine with t.Parallel).

package core

import (
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

func TestMetadiumEmbeddedGenesisMatchesParams(t *testing.T) {
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
		if n.genesis.Config == nil {
			t.Errorf("%s embedded genesis has no config section at all", n.name)
			continue
		}
		if n.genesis.Config.ChainID == nil || n.genesis.Config.ChainID.Cmp(n.cfg.ChainID) != 0 {
			t.Errorf("%s embedded genesis chain id = %v, want %v",
				n.name, n.genesis.Config.ChainID, n.cfg.ChainID)
		}
		// Every *big.Int fork field must agree between the embedded JSON and
		// the compiled params config. A fork present in params but nil in the
		// JSON is exactly the camelliaBlock bug: a fresh node's first session
		// runs on the JSON verbatim and stalls at the fork boundary.
		var (
			gv = reflect.ValueOf(*n.genesis.Config)
			pv = reflect.ValueOf(*n.cfg)
			tt = gv.Type()
		)
		for i := 0; i < tt.NumField(); i++ {
			f := tt.Field(i)
			if f.Type != reflect.TypeOf((*big.Int)(nil)) || !strings.HasSuffix(f.Name, "Block") {
				continue
			}
			got, _ := gv.Field(i).Interface().(*big.Int)
			want, _ := pv.Field(i).Interface().(*big.Int)
			switch {
			case (got == nil) != (want == nil):
				t.Errorf("%s embedded genesis %s = %v, params say %v: a fork "+
					"added to params must be added to the embedded JSON too "+
					"(core/metadium_genesis.go), or a fresh node's first sync "+
					"session stalls at its boundary", n.name, f.Name, got, want)
			case got != nil && got.Cmp(want) != 0:
				t.Errorf("%s embedded genesis %s = %v, params say %v",
					n.name, f.Name, got, want)
			}
		}
		if n.genesis.Config.DAOForkSupport != n.cfg.DAOForkSupport {
			t.Errorf("%s embedded genesis DAOForkSupport = %v, params say %v",
				n.name, n.genesis.Config.DAOForkSupport, n.cfg.DAOForkSupport)
		}
	}
}
