// Copyright 2026 The go-metadium Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/params"
)

// asPoA switches the process-wide consensus method for one test. Without it
// core.DefaultGenesisBlock returns Ethereum's genesis, because the default
// outside a running node is PoW.
func asPoA(t *testing.T) {
	t.Helper()
	old := params.ConsensusMethod
	params.ConsensusMethod = params.ConsensusPoA
	t.Cleanup(func() { params.ConsensusMethod = old })
}

// servedGenesis renders a genesis document the way a peer would, with the chain
// config replaced by the given one. Marshalling the canonical genesis and then
// swapping the config is what produces a document whose block hash is canonical
// while its config is not — the case this guards against.
func servedGenesis(t *testing.T, network string, config *params.ChainConfig) []byte {
	t.Helper()

	var genesis *core.Genesis
	switch network {
	case "mainnet":
		genesis = core.DefaultGenesisBlock()
	case "testnet":
		genesis = core.DefaultTestnetGenesisBlock()
	default:
		t.Fatalf("unknown network %q", network)
	}
	doc, err := json.Marshal(genesis)
	if err != nil {
		t.Fatalf("marshalling the %s genesis: %v", network, err)
	}
	if config == nil {
		return doc
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc, &raw); err != nil {
		t.Fatalf("re-reading the %s genesis: %v", network, err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshalling the replacement config: %v", err)
	}
	raw["config"] = encoded
	doc, err = json.Marshal(raw)
	if err != nil {
		t.Fatalf("re-writing the %s genesis: %v", network, err)
	}
	return doc
}

// TestCanonicalGenesisConfigReplacesStaleConfig is the case from issue #72: a
// peer serves a genesis whose block is canonical but whose config predates a
// fork. The block hash cannot detect that — the config is not part of the block
// — so the config has to be compared and replaced.
func TestCanonicalGenesisConfigReplacesStaleConfig(t *testing.T) {
	asPoA(t)

	for _, network := range []string{"mainnet", "testnet"} {
		stale := *params.MetadiumMainnetChainConfig
		if network == "testnet" {
			stale = *params.MetadiumTestnetChainConfig
		}
		stale.CamelliaBlock = nil // as served by a pre-Camellia peer

		out, got, replaced, err := canonicalGenesisConfig(servedGenesis(t, network, &stale))
		if err != nil {
			t.Fatalf("%s: %v", network, err)
		}
		if got != network {
			t.Fatalf("%s: recognized as %q", network, got)
		}
		if !replaced {
			t.Fatalf("%s: a stale config was passed through", network)
		}
		var result core.Genesis
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("%s: the rewritten document does not parse: %v", network, err)
		}
		if result.Config.CamelliaBlock == nil {
			t.Errorf("%s: the rewritten config still has no camelliaBlock", network)
		}
		// The block itself must be untouched, or the file would initialize a
		// different chain than the one it was checked against.
		want := servedGenesis(t, network, nil)
		var original core.Genesis
		if err := json.Unmarshal(want, &original); err != nil {
			t.Fatalf("%s: %v", network, err)
		}
		if result.ToBlock().Hash() != original.ToBlock().Hash() {
			t.Errorf("%s: the genesis block changed: have %x, want %x",
				network, result.ToBlock().Hash(), original.ToBlock().Hash())
		}
	}
}

// TestCanonicalGenesisConfigAcceptsMatching leaves a correct document alone.
func TestCanonicalGenesisConfigAcceptsMatching(t *testing.T) {
	asPoA(t)

	doc := servedGenesis(t, "mainnet", nil)

	out, network, replaced, err := canonicalGenesisConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if network != "mainnet" {
		t.Fatalf("recognized as %q", network)
	}
	if replaced {
		t.Fatal("a matching config was replaced")
	}
	if string(out) != string(doc) {
		t.Fatal("a matching document was rewritten")
	}
}

// TestCanonicalGenesisConfigPassesPrivateNet checks that the guard does not
// break the command's other use: private networks bootstrap from a peer exactly
// this way, and there is no canonical config to compare them against.
func TestCanonicalGenesisConfigPassesPrivateNet(t *testing.T) {
	asPoA(t)

	private := core.DefaultGenesisBlock()
	private.Nonce = 0x4242 // any change to a block field moves the hash
	doc, err := json.Marshal(private)
	if err != nil {
		t.Fatal(err)
	}
	out, network, replaced, err := canonicalGenesisConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if network != "" || replaced {
		t.Fatalf("a private genesis was treated as %q (replaced=%v)", network, replaced)
	}
	if string(out) != string(doc) {
		t.Fatal("a private genesis was rewritten")
	}
}
