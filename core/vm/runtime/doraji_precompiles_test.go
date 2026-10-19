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
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/params"
)

// TestDorajiPrecompiles calls the precompiles the fork adds through the EVM:
// on a Doraji chain they answer, on a Camellia chain the same addresses are
// empty accounts and a call returns nothing. The VRF precompile stays.
func TestDorajiPrecompiles(t *testing.T) {
	var (
		p256   = common.BytesToAddress([]byte{0x01, 0x00})
		g1add  = common.BytesToAddress([]byte{0x0b})
		mapG2  = common.BytesToAddress([]byte{0x11})
		vrf    = common.BytesToAddress([]byte{0x13})
		draft  = common.BytesToAddress([]byte{0x12}) // the retired draft set's last slot
		doraji = &runtime.Config{ChainConfig: dorajiChainConfig(), BlockNumber: big.NewInt(1), GasLimit: 10_000_000}
		cam    = &runtime.Config{ChainConfig: camelliaChainConfig(), BlockNumber: big.NewInt(1), GasLimit: 10_000_000}
	)
	// Call, unlike Execute, needs a state.
	doraji.State, _ = state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	cam.State, _ = state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	// EIP-7951 sample vector: a valid signature, so the answer is 1.
	p256In := common.Hex2Bytes("4cee90eb86eaa050036147a12d49004b6b9c72bd725d39d4785011fe190f0b4da73bd4903f0ce3b639bbbf6e8e80d16931ff4bcf5993d58468e8fb19086e8cac36dbcd03009df8c59286b162af3bd7fcc0450c9aa81be5d10d312af6c66b1d604aebd3099c618202fcfe16ae7770b0c49ab5eadf74b754204a3bb6060e44eff37618b065f9832de4ca6ca971a7a1adc826d0f7c00181a5fb2ddf79ae00b4e10e")
	one := common.LeftPadBytes([]byte{1}, 32)

	ret, gas, err := runtime.Call(p256, p256In, doraji)
	if err != nil || !bytes.Equal(ret, one) {
		t.Fatalf("doraji P256VERIFY: ret %x err %v", ret, err)
	}
	if used := doraji.GasLimit - gas; used != params.P256VerifyGas {
		t.Fatalf("doraji P256VERIFY: gas used %d, want %d", used, params.P256VerifyGas)
	}
	ret, _, err = runtime.Call(p256, p256In, cam)
	if err != nil || len(ret) != 0 {
		t.Fatalf("camellia 0x100: ret %x err %v, want an empty account", ret, err)
	}

	// G1ADD of the point at infinity with itself is the point at infinity:
	// 256 zero bytes in, 128 zero bytes out, at the EIP-2537 price.
	ret, gas, err = runtime.Call(g1add, make([]byte, 256), doraji)
	if err != nil || !bytes.Equal(ret, make([]byte, 128)) {
		t.Fatalf("doraji G1ADD: ret %x err %v", ret, err)
	}
	if used := doraji.GasLimit - gas; used != params.Bls12381G1AddGas {
		t.Fatalf("doraji G1ADD: gas used %d, want %d", used, params.Bls12381G1AddGas)
	}
	ret, _, err = runtime.Call(g1add, make([]byte, 256), cam)
	if err != nil || len(ret) != 0 {
		t.Fatalf("camellia 0x0b: ret %x err %v, want an empty account", ret, err)
	}

	// The draft set's slot past the final range is not a precompile.
	ret, _, err = runtime.Call(draft, make([]byte, 128), doraji)
	if err != nil || len(ret) != 0 {
		t.Fatalf("doraji 0x12: ret %x err %v, want an empty account", ret, err)
	}

	// The active address lists agree with the calls above.
	active := vm.ActivePrecompiles(dorajiChainConfig().Rules(big.NewInt(1), false, 0))
	has := func(a common.Address) bool {
		for _, x := range active {
			if x == a {
				return true
			}
		}
		return false
	}
	for _, a := range []common.Address{p256, g1add, mapG2, vrf} {
		if !has(a) {
			t.Fatalf("doraji active precompiles lack %v", a)
		}
	}
	if has(draft) {
		t.Fatalf("doraji active precompiles include the draft slot %v", draft)
	}
	camActive := vm.ActivePrecompiles(camelliaChainConfig().Rules(big.NewInt(1), false, 0))
	for _, x := range camActive {
		if x == p256 || x == g1add {
			t.Fatalf("camellia active precompiles include %v", x)
		}
	}
}
