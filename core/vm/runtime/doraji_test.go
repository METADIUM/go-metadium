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

package runtime_test

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/params"
)

// dorajiChainConfig is the Camellia configuration with Doraji active from
// genesis.
func dorajiChainConfig() *params.ChainConfig {
	c := *camelliaChainConfig()
	c.DorajiBlock = big.NewInt(0)
	return &c
}

// TestDorajiCLZ runs CLZ through the interpreter: on a Doraji chain it
// returns the leading zero count, on a Camellia chain the opcode is undefined.
func TestDorajiCLZ(t *testing.T) {
	// PUSH1 0x01; CLZ; PUSH1 0; MSTORE; PUSH1 32; PUSH1 0; RETURN
	code := common.Hex2Bytes("60011e60005260206000f3")

	ret, _, err := runtime.Execute(code, nil, &runtime.Config{ChainConfig: dorajiChainConfig(), BlockNumber: big.NewInt(1)})
	if err != nil {
		t.Fatalf("doraji: %v", err)
	}
	if got := new(big.Int).SetBytes(ret); got.Cmp(big.NewInt(255)) != 0 {
		t.Fatalf("doraji: clz(1) = %v, want 255", got)
	}

	_, _, err = runtime.Execute(code, nil, &runtime.Config{ChainConfig: camelliaChainConfig(), BlockNumber: big.NewInt(1)})
	if err == nil || !strings.Contains(err.Error(), "invalid opcode") {
		t.Fatalf("camellia: want an invalid opcode error, got %v", err)
	}
}
