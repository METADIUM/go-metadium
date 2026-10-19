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
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/params"
)

// callerCode returns code that CALLs callee with no input, no value and all
// gas, and returns the first 32 bytes the callee returned.
func callerCode(callee common.Address) []byte {
	code := common.Hex2Bytes("6020600060006000600073") // PUSH1 32, PUSH1 0 ×4, PUSH20
	code = append(code, callee.Bytes()...)
	return append(code, common.Hex2Bytes("5af160206000f3")...) // GAS, CALL, PUSH1 32, PUSH1 0, RETURN
}

// TestDorajiDelegatedCall: CALL into an account whose code is a delegation
// designator runs the target's code on a Doraji chain, and costs one cold
// account access more than a CALL to the target itself; on a Camellia chain
// the designator is executed as code and the call fails.
func TestDorajiDelegatedCall(t *testing.T) {
	var (
		target    = common.HexToAddress("0x00000000000000000000000000000000000000cc")
		delegated = common.HexToAddress("0x00000000000000000000000000000000000000dd")
		viaTarget = common.HexToAddress("0x0000000000000000000000000000000000000d01")
		viaDeleg  = common.HexToAddress("0x0000000000000000000000000000000000000d02")
		returns42 = common.Hex2Bytes("602a60005260206000f3")
		word42    = common.LeftPadBytes([]byte{42}, 32)
	)
	newState := func() *state.StateDB {
		st, _ := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
		st.SetCode(target, returns42)
		st.SetCode(delegated, types.AddressToDelegation(target))
		st.SetCode(viaTarget, callerCode(target))
		st.SetCode(viaDeleg, callerCode(delegated))
		return st
	}
	call := func(cfg *params.ChainConfig, to common.Address) ([]byte, uint64) {
		c := &runtime.Config{ChainConfig: cfg, BlockNumber: big.NewInt(1), GasLimit: 1_000_000, State: newState()}
		ret, left, err := runtime.Call(to, nil, c)
		if err != nil {
			t.Fatalf("call %v: %v", to, err)
		}
		return ret, c.GasLimit - left
	}

	retDirect, gasDirect := call(dorajiChainConfig(), viaTarget)
	retDeleg, gasDeleg := call(dorajiChainConfig(), viaDeleg)
	if !bytes.Equal(retDirect, word42) || !bytes.Equal(retDeleg, word42) {
		t.Fatalf("doraji: direct %x, delegated %x, want 42", retDirect, retDeleg)
	}
	if diff := gasDeleg - gasDirect; diff != params.ColdAccountAccessCostEIP2929 {
		t.Fatalf("doraji: delegated call costs %d more than a direct one, want %d (cold target)", diff, params.ColdAccountAccessCostEIP2929)
	}

	retCam, _ := call(camelliaChainConfig(), viaDeleg)
	if !bytes.Equal(retCam, make([]byte, 32)) {
		t.Fatalf("camellia: a call into a designator returned %x, want the zero word of a failed call", retCam)
	}
}
