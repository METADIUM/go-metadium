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

package vm

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// modExpInput builds a ModExp call with a base of baseLen bytes, a one-byte
// exponent and a one-byte modulus.
func modExpInput(baseLen int) []byte {
	in := common.LeftPadBytes(big.NewInt(int64(baseLen)).Bytes(), 32)
	in = append(in, common.LeftPadBytes([]byte{1}, 32)...)
	in = append(in, common.LeftPadBytes([]byte{1}, 32)...)
	base := make([]byte, baseLen)
	base[baseLen-1] = 9
	in = append(in, base...)
	return append(in, 1, 7)
}

// TestDorajiModExpSet pins what 0x05 is on each precompile set: the Doraji
// set prices ModExp under EIP-7883 and refuses inputs over 1024 bytes
// (EIP-7823); the Cancun set keeps EIP-2565 pricing and no limit.
func TestDorajiModExpSet(t *testing.T) {
	addr := common.BytesToAddress([]byte{5})
	doraji, ok := PrecompiledContractsDoraji[addr].(*bigModExp)
	if !ok || !doraji.eip2565 || !doraji.eip7883 || !doraji.eip7823 {
		t.Fatalf("doraji 0x05 = %#v, want bigModExp with eip2565, eip7883 and eip7823", PrecompiledContractsDoraji[addr])
	}
	cancun, ok := PrecompiledContractsCancun[addr].(*bigModExp)
	if !ok || !cancun.eip2565 || cancun.eip7883 || cancun.eip7823 {
		t.Fatalf("cancun 0x05 = %#v, want bigModExp with eip2565 only", PrecompiledContractsCancun[addr])
	}
	// The sets that price pre-Camellia history keep their flags: Berlin
	// (EIP-2565) and Istanbul (EIP-198 pricing).
	berlin, ok := PrecompiledContractsBerlin[addr].(*bigModExp)
	if !ok || !berlin.eip2565 || berlin.eip7883 || berlin.eip7823 {
		t.Fatalf("berlin 0x05 = %#v, want bigModExp with eip2565 only", PrecompiledContractsBerlin[addr])
	}
	istanbul, ok := PrecompiledContractsIstanbul[addr].(*bigModExp)
	if !ok || istanbul.eip2565 || istanbul.eip7883 || istanbul.eip7823 {
		t.Fatalf("istanbul 0x05 = %#v, want bigModExp with no flags", PrecompiledContractsIstanbul[addr])
	}

	// EIP-7883 raises the minimum from 200 to 500 and reprices the rest.
	small := modExpInput(32)
	if g := cancun.RequiredGas(small); g != 200 {
		t.Fatalf("cancun gas for a small input %d, want 200 (EIP-2565 minimum)", g)
	}
	if g := berlin.RequiredGas(small); g != 200 {
		t.Fatalf("berlin gas for a small input %d, want 200 (EIP-2565 minimum)", g)
	}
	if g := doraji.RequiredGas(small); g != 500 {
		t.Fatalf("doraji gas for a small input %d, want 500 (EIP-7883 minimum)", g)
	}

	// EIP-7823: a 1025-byte base is refused on Doraji and accepted on Cancun;
	// 1024 bytes is accepted on both.
	for _, tc := range []struct {
		name    string
		c       *bigModExp
		baseLen int
		wantErr bool
	}{
		{"doraji 1024", doraji, 1024, false},
		{"doraji 1025", doraji, 1025, true},
		{"cancun 1024", cancun, 1024, false},
		{"cancun 1025", cancun, 1025, false},
	} {
		out, err := tc.c.Run(modExpInput(tc.baseLen))
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err %v, want error %v", tc.name, err, tc.wantErr)
		}
		if !tc.wantErr && (len(out) != 1 || out[0] != 9%7) {
			t.Fatalf("%s: output %x, want 9 mod 7 = 2", tc.name, out)
		}
	}
}
