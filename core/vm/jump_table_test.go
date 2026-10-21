// Copyright 2022 The go-ethereum Authors
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
	"testing"

	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// TestJumpTableCopy tests that deep copy is necessary to prevent modify shared jump table
func TestJumpTableCopy(t *testing.T) {
	tbl := newMergeInstructionSet()
	require.Equal(t, uint64(0), tbl[SLOAD].constantGas)

	// a deep copy won't modify the shared jump table
	deepCopy := copyJumpTable(&tbl)
	deepCopy[SLOAD].constantGas = 100
	require.Equal(t, uint64(100), deepCopy[SLOAD].constantGas)
	require.Equal(t, uint64(0), tbl[SLOAD].constantGas)
}

// TestDorajiInstructionSet: the Doraji set is the Cancun set plus CLZ, and a
// Doraji rule set selects it without the "not defined" error that a Prague
// rule set got before the fork existed.
func TestDorajiInstructionSet(t *testing.T) {
	cancun, doraji := newCancunInstructionSet(), newDorajiInstructionSet()
	// Undefined slots hold opUndefined, so defined-ness is read as HasCost.
	require.False(t, cancun[CLZ].HasCost(), "CLZ defined before Doraji")
	require.True(t, doraji[CLZ].HasCost(), "CLZ not defined in Doraji")
	require.Equal(t, GasFastStep, doraji[CLZ].constantGas)
	for op := range doraji {
		if OpCode(op) == CLZ {
			continue
		}
		require.Equal(t, cancun[op].constantGas, doraji[op].constantGas, "%v: constant gas differs from Cancun", OpCode(op))
		require.Equal(t, cancun[op].minStack, doraji[op].minStack, "%v: min stack differs from Cancun", OpCode(op))
		require.Equal(t, cancun[op].HasCost(), doraji[op].HasCost(), "%v: defined-ness differs from Cancun", OpCode(op))
	}

	rules := params.Rules{IsCancun: true, IsPrague: true, IsOsaka: true, IsDoraji: true}
	jt, err := LookupInstructionSet(rules)
	require.NoError(t, err)
	require.True(t, jt[CLZ].HasCost())
	rules.IsDoraji = false
	_, err = LookupInstructionSet(rules)
	require.Error(t, err, "a Prague rule set without Doraji is still undefined")
}
