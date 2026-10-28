// Copyright 2026 The go-metadium Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"bytes"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"golang.org/x/exp/slog"
)

// TestFeeDelegationOffWarning: a chain configuration without applepieBlock
// produces the startup warning at any head, one whose applepieBlock is still
// ahead of the head produces it too, and one the head has reached (0
// included) does not. Every bundled private configuration sets the key at 0,
// so none of them warns. Mainnet and Testnet set it in their past but are
// exempt by genesis hash at any head, since a node syncing either from
// genesis starts below the fork; a private genesis with the same fork heights
// is not exempt.
func TestFeeDelegationOffWarning(t *testing.T) {
	missing := *params.TestChainConfig
	missing.ApplepieBlock = nil
	for _, head := range []*big.Int{nil, big.NewInt(0), big.NewInt(1_000_000)} {
		msg := feeDelegationOffWarning(&missing, common.Hash{}, head)
		if msg == "" {
			t.Fatalf("head %v: no warning for a config without applepieBlock", head)
		}
		if !strings.Contains(msg, "applepieBlock") || !strings.Contains(msg, "type 22") {
			t.Fatalf("warning does not name the key and the effect: %q", msg)
		}
	}

	later := *params.TestChainConfig
	later.ApplepieBlock = big.NewInt(100)
	msg := feeDelegationOffWarning(&later, common.Hash{}, big.NewInt(99))
	if msg == "" {
		t.Fatal("no warning for a head below applepieBlock")
	}
	if !strings.Contains(msg, "until applepieBlock 100") || !strings.Contains(msg, "head 99") {
		t.Fatalf("warning does not name the fork height and the head: %q", msg)
	}
	if msg := feeDelegationOffWarning(&later, common.Hash{}, nil); msg != "" {
		t.Fatalf("unknown head: unexpected warning %q", msg)
	}

	type at struct {
		cfg     *params.ChainConfig
		genesis common.Hash
		head    *big.Int
	}
	for name, c := range map[string]at{
		"zero":           {func() *params.ChainConfig { c := *params.TestChainConfig; c.ApplepieBlock = big.NewInt(0); return &c }(), common.Hash{}, big.NewInt(0)},
		"later, reached": {&later, common.Hash{}, big.NewInt(100)},
		"later, past":    {&later, common.Hash{}, big.NewInt(101)},
		"mainnet":        {params.MetadiumMainnetChainConfig, params.MetadiumMainnetGenesisHash, params.MetadiumMainnetChainConfig.CamelliaBlock},
		"testnet":        {params.MetadiumTestnetChainConfig, params.MetadiumTestnetGenesisHash, params.MetadiumTestnetChainConfig.CamelliaBlock},
		// A node syncing a public chain from genesis starts below applepieBlock.
		"mainnet, genesis sync": {params.MetadiumMainnetChainConfig, params.MetadiumMainnetGenesisHash, big.NewInt(0)},
		"testnet, genesis sync": {params.MetadiumTestnetChainConfig, params.MetadiumTestnetGenesisHash, big.NewInt(0)},
	} {
		if msg := feeDelegationOffWarning(c.cfg, c.genesis, c.head); msg != "" {
			t.Fatalf("%s: unexpected warning %q", name, msg)
		}
	}
	if msg := feeDelegationOffWarning(nil, common.Hash{}, big.NewInt(0)); msg != "" {
		t.Fatalf("nil config: unexpected warning %q", msg)
	}

	// The exemption is by genesis, not by configuration: a private network
	// that copied a public chain's fork heights still warns below them.
	if msg := feeDelegationOffWarning(params.MetadiumMainnetChainConfig, common.Hash{1}, big.NewInt(0)); msg == "" {
		t.Fatal("private genesis with a future applepieBlock: no warning")
	}
}

// TestFeeDelegationOffWarningWiring opens a chain through NewBlockChain and
// checks what reaches the log, so the call site is covered as well as the
// helper: a fresh Mainnet or Testnet node at head 0 must not print the
// warning, and the same genesis with any other hash must.
func TestFeeDelegationOffWarningWiring(t *testing.T) {
	old := params.ConsensusMethod
	params.ConsensusMethod = params.ConsensusPoA
	t.Cleanup(func() { params.ConsensusMethod = old })

	logged := func(g *Genesis) string {
		var buf bytes.Buffer
		prev := log.Root()
		log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(&buf, slog.LevelWarn, false)))
		defer log.SetDefault(prev)

		bc, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, g, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
		if err != nil {
			t.Fatalf("NewBlockChain: %v", err)
		}
		bc.Stop()
		return buf.String()
	}
	for name, g := range map[string]*Genesis{
		"mainnet": DefaultGenesisBlock(),
		"testnet": DefaultTestnetGenesisBlock(),
	} {
		if out := logged(g); strings.Contains(out, "type 22") {
			t.Fatalf("%s from genesis: unexpected warning\n%s", name, out)
		}
		private := *g
		private.ExtraData = append([]byte("private"), g.ExtraData...)
		if out := logged(&private); !strings.Contains(out, "type 22") {
			t.Fatalf("%s fork heights on a private genesis: no warning\n%s", name, out)
		}
	}
}
