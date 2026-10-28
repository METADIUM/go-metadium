// Copyright 2017 The go-ethereum Authors
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

package params

import (
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/math"
)

func TestCheckCompatible(t *testing.T) {
	type test struct {
		stored, new   *ChainConfig
		headBlock     uint64
		headTimestamp uint64
		wantErr       *ConfigCompatError
	}
	tests := []test{
		{stored: AllEthashProtocolChanges, new: AllEthashProtocolChanges, headBlock: 0, headTimestamp: 0, wantErr: nil},
		{stored: AllEthashProtocolChanges, new: AllEthashProtocolChanges, headBlock: 0, headTimestamp: uint64(time.Now().Unix()), wantErr: nil},
		{stored: AllEthashProtocolChanges, new: AllEthashProtocolChanges, headBlock: 100, wantErr: nil},
		{
			stored:    &ChainConfig{EIP150Block: big.NewInt(10)},
			new:       &ChainConfig{EIP150Block: big.NewInt(20)},
			headBlock: 9,
			wantErr:   nil,
		},
		{
			stored:    AllEthashProtocolChanges,
			new:       &ChainConfig{HomesteadBlock: nil},
			headBlock: 3,
			wantErr: &ConfigCompatError{
				What:          "Homestead fork block",
				StoredBlock:   big.NewInt(0),
				NewBlock:      nil,
				RewindToBlock: 0,
			},
		},
		{
			stored:    AllEthashProtocolChanges,
			new:       &ChainConfig{HomesteadBlock: big.NewInt(1)},
			headBlock: 3,
			wantErr: &ConfigCompatError{
				What:          "Homestead fork block",
				StoredBlock:   big.NewInt(0),
				NewBlock:      big.NewInt(1),
				RewindToBlock: 0,
			},
		},
		{
			stored:    &ChainConfig{HomesteadBlock: big.NewInt(30), EIP150Block: big.NewInt(10)},
			new:       &ChainConfig{HomesteadBlock: big.NewInt(25), EIP150Block: big.NewInt(20)},
			headBlock: 25,
			wantErr: &ConfigCompatError{
				What:          "EIP150 fork block",
				StoredBlock:   big.NewInt(10),
				NewBlock:      big.NewInt(20),
				RewindToBlock: 9,
			},
		},
		{
			stored:    &ChainConfig{ConstantinopleBlock: big.NewInt(30)},
			new:       &ChainConfig{ConstantinopleBlock: big.NewInt(30), PetersburgBlock: big.NewInt(30)},
			headBlock: 40,
			wantErr:   nil,
		},
		{
			stored:    &ChainConfig{ConstantinopleBlock: big.NewInt(30)},
			new:       &ChainConfig{ConstantinopleBlock: big.NewInt(30), PetersburgBlock: big.NewInt(31)},
			headBlock: 40,
			wantErr: &ConfigCompatError{
				What:          "Petersburg fork block",
				StoredBlock:   nil,
				NewBlock:      big.NewInt(31),
				RewindToBlock: 30,
			},
		},
		{
			stored:    &ChainConfig{CamelliaBlock: big.NewInt(30), DorajiBlock: big.NewInt(30)},
			new:       &ChainConfig{CamelliaBlock: big.NewInt(30), DorajiBlock: big.NewInt(30)},
			headBlock: 40,
			wantErr:   nil,
		},
		{
			stored:    &ChainConfig{CamelliaBlock: big.NewInt(30)},
			new:       &ChainConfig{CamelliaBlock: big.NewInt(30), DorajiBlock: big.NewInt(50)},
			headBlock: 40,
			wantErr:   nil,
		},
		{
			stored:    &ChainConfig{CamelliaBlock: big.NewInt(30)},
			new:       &ChainConfig{CamelliaBlock: big.NewInt(30), DorajiBlock: big.NewInt(31)},
			headBlock: 40,
			wantErr: &ConfigCompatError{
				What:          "Doraji fork block",
				StoredBlock:   nil,
				NewBlock:      big.NewInt(31),
				RewindToBlock: 30,
			},
		},
		{
			stored:    &ChainConfig{CamelliaBlock: big.NewInt(30), DorajiBlock: big.NewInt(35)},
			new:       &ChainConfig{CamelliaBlock: big.NewInt(30), DorajiBlock: big.NewInt(36)},
			headBlock: 40,
			wantErr: &ConfigCompatError{
				What:          "Doraji fork block",
				StoredBlock:   big.NewInt(35),
				NewBlock:      big.NewInt(36),
				RewindToBlock: 34,
			},
		},
		{
			stored:        &ChainConfig{ShanghaiTime: newUint64(10)},
			new:           &ChainConfig{ShanghaiTime: newUint64(20)},
			headTimestamp: 9,
			wantErr:       nil,
		},
		{
			stored:        &ChainConfig{ShanghaiTime: newUint64(10)},
			new:           &ChainConfig{ShanghaiTime: newUint64(20)},
			headTimestamp: 25,
			wantErr: &ConfigCompatError{
				What:         "Shanghai fork timestamp",
				StoredTime:   newUint64(10),
				NewTime:      newUint64(20),
				RewindToTime: 9,
			},
		},
	}

	for _, test := range tests {
		err := test.stored.CheckCompatible(test.new, test.headBlock, test.headTimestamp)
		if !reflect.DeepEqual(err, test.wantErr) {
			t.Errorf("error mismatch:\nstored: %v\nnew: %v\nheadBlock: %v\nheadTimestamp: %v\nerr: %v\nwant: %v", test.stored, test.new, test.headBlock, test.headTimestamp, err, test.wantErr)
		}
	}
}

func TestConfigRules(t *testing.T) {
	c := &ChainConfig{
		LondonBlock:  new(big.Int),
		ShanghaiTime: newUint64(500),
	}
	var stamp uint64
	if r := c.Rules(big.NewInt(0), true, stamp); r.IsShanghai {
		t.Errorf("expected %v to not be shanghai", stamp)
	}
	stamp = 500
	if r := c.Rules(big.NewInt(0), true, stamp); !r.IsShanghai {
		t.Errorf("expected %v to be shanghai", stamp)
	}
	stamp = math.MaxInt64
	if r := c.Rules(big.NewInt(0), true, stamp); !r.IsShanghai {
		t.Errorf("expected %v to be shanghai", stamp)
	}
}

// TestDorajiScheduling: Doraji must come at or after Camellia, cannot be set
// without it, is reported by the rules and the banner, and is not scheduled
// on either public chain yet.
func TestDorajiScheduling(t *testing.T) {
	base := func() *ChainConfig {
		c := *TestChainConfig
		c.CamelliaBlock = big.NewInt(100)
		return &c
	}
	c := base()
	c.DorajiBlock = big.NewInt(100)
	if err := c.CheckConfigForkOrder(); err != nil {
		t.Fatalf("doraji at camellia: unexpected error %v", err)
	}
	c.DorajiBlock = big.NewInt(99)
	if err := c.CheckConfigForkOrder(); err == nil || !strings.Contains(err.Error(), "before camelliaBlock") {
		t.Fatalf("doraji before camellia: want an ordering error, got %v", err)
	}
	if err := c.checkForkOrder(); err == nil || !strings.Contains(err.Error(), "dorajiBlock") {
		t.Fatalf("doraji before camellia: upstream ordering check did not name dorajiBlock: %v", err)
	}
	c = base()
	c.CamelliaBlock = nil
	c.DorajiBlock = big.NewInt(100)
	if err := c.CheckConfigForkOrder(); err == nil || !strings.Contains(err.Error(), "without camelliaBlock") {
		t.Fatalf("doraji without camellia: want an error, got %v", err)
	}

	c = base()
	c.DorajiBlock = big.NewInt(100)
	if r := c.Rules(big.NewInt(99), false, 0); r.IsDoraji {
		t.Fatal("block 99: doraji before the fork block")
	}
	if r := c.Rules(big.NewInt(100), false, 0); !r.IsDoraji || !r.IsCamellia {
		t.Fatalf("block 100: rules = doraji %v camellia %v", r.IsDoraji, r.IsCamellia)
	}
	// Doraji stands in for the upstream forks it takes its rules from, the
	// way Camellia stands in for Shanghai and Cancun.
	if r := c.Rules(big.NewInt(99), false, 0); r.IsPrague || r.IsOsaka {
		t.Fatalf("block 99: rules = prague %v osaka %v", r.IsPrague, r.IsOsaka)
	}
	if r := c.Rules(big.NewInt(100), false, 0); !r.IsPrague || !r.IsOsaka {
		t.Fatalf("block 100: rules = prague %v osaka %v", r.IsPrague, r.IsOsaka)
	}
	if !strings.Contains(c.Description(), "Doraji Fork:                 #100") {
		t.Fatalf("banner does not show the fork block:\n%s", c.Description())
	}
	c.DorajiBlock = nil
	if strings.Contains(c.Description(), "Doraji") {
		t.Fatal("banner shows an unscheduled fork")
	}

	// The public chains schedule the fork in a later change, once the rules
	// it activates are in the tree.
	for name, pc := range map[string]*ChainConfig{"mainnet": MetadiumMainnetChainConfig, "testnet": MetadiumTestnetChainConfig} {
		if pc.DorajiBlock != nil {
			t.Fatalf("%s: dorajiBlock scheduled at %v", name, pc.DorajiBlock)
		}
		if pc.IsDoraji(new(big.Int).Lsh(big.NewInt(1), 62)) {
			t.Fatalf("%s: doraji active with no fork block", name)
		}
	}
}
