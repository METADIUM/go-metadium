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
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/params"
)

// TestFeeDelegationOffWarning: a chain configuration without applepieBlock
// produces the startup warning, and one with it (at any height, 0 included)
// does not. The public chains and every bundled private configuration set
// the key, so none of them warns.
func TestFeeDelegationOffWarning(t *testing.T) {
	missing := *params.TestChainConfig
	missing.ApplepieBlock = nil
	msg := feeDelegationOffWarning(&missing)
	if msg == "" {
		t.Fatal("no warning for a config without applepieBlock")
	}
	if !strings.Contains(msg, "applepieBlock") || !strings.Contains(msg, "type 22") {
		t.Fatalf("warning does not name the key and the effect: %q", msg)
	}

	for name, cfg := range map[string]*params.ChainConfig{
		"zero":    func() *params.ChainConfig { c := *params.TestChainConfig; c.ApplepieBlock = big.NewInt(0); return &c }(),
		"later":   func() *params.ChainConfig { c := *params.TestChainConfig; c.ApplepieBlock = big.NewInt(100); return &c }(),
		"mainnet": params.MetadiumMainnetChainConfig,
		"testnet": params.MetadiumTestnetChainConfig,
	} {
		if msg := feeDelegationOffWarning(cfg); msg != "" {
			t.Fatalf("%s: unexpected warning %q", name, msg)
		}
	}
	if msg := feeDelegationOffWarning(nil); msg != "" {
		t.Fatalf("nil config: unexpected warning %q", msg)
	}
}
