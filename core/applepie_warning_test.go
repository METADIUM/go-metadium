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
// produces the startup warning at any head, one whose applepieBlock is still
// ahead of the head produces it too, and one the head has reached (0
// included) does not. The public chains and every bundled private
// configuration set the key in their past, so none of them warns.
func TestFeeDelegationOffWarning(t *testing.T) {
	missing := *params.TestChainConfig
	missing.ApplepieBlock = nil
	for _, head := range []*big.Int{nil, big.NewInt(0), big.NewInt(1_000_000)} {
		msg := feeDelegationOffWarning(&missing, head)
		if msg == "" {
			t.Fatalf("head %v: no warning for a config without applepieBlock", head)
		}
		if !strings.Contains(msg, "applepieBlock") || !strings.Contains(msg, "type 22") {
			t.Fatalf("warning does not name the key and the effect: %q", msg)
		}
	}

	later := *params.TestChainConfig
	later.ApplepieBlock = big.NewInt(100)
	msg := feeDelegationOffWarning(&later, big.NewInt(99))
	if msg == "" {
		t.Fatal("no warning for a head below applepieBlock")
	}
	if !strings.Contains(msg, "until applepieBlock 100") || !strings.Contains(msg, "head 99") {
		t.Fatalf("warning does not name the fork height and the head: %q", msg)
	}
	if msg := feeDelegationOffWarning(&later, nil); msg != "" {
		t.Fatalf("unknown head: unexpected warning %q", msg)
	}

	type at struct {
		cfg  *params.ChainConfig
		head *big.Int
	}
	for name, c := range map[string]at{
		"zero":           {func() *params.ChainConfig { c := *params.TestChainConfig; c.ApplepieBlock = big.NewInt(0); return &c }(), big.NewInt(0)},
		"later, reached": {&later, big.NewInt(100)},
		"later, past":    {&later, big.NewInt(101)},
		"mainnet":        {params.MetadiumMainnetChainConfig, params.MetadiumMainnetChainConfig.CamelliaBlock},
		"testnet":        {params.MetadiumTestnetChainConfig, params.MetadiumTestnetChainConfig.CamelliaBlock},
	} {
		if msg := feeDelegationOffWarning(c.cfg, c.head); msg != "" {
			t.Fatalf("%s: unexpected warning %q", name, msg)
		}
	}
	if msg := feeDelegationOffWarning(nil, big.NewInt(0)); msg != "" {
		t.Fatalf("nil config: unexpected warning %q", msg)
	}
}
