// Copyright 2024 The go-ethereum Authors
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

package types

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/params"
)

// TestCalcExcessBlobGas tests the excess blob gas calculation (EIP-4844).
func TestCalcExcessBlobGas(t *testing.T) {
	tests := []struct {
		name              string
		prevExcessBlobGas *big.Int
		prevBlobGasUsed   uint64
		expectedExcessGas *big.Int
	}{
		{
			name:              "nil prevExcessBlobGas",
			prevExcessBlobGas: nil,
			prevBlobGasUsed:   0,
			expectedExcessGas: big.NewInt(0),
		},
		{
			name:              "no excess, below target",
			prevExcessBlobGas: big.NewInt(0),
			prevBlobGasUsed:   params.BlobTxTargetBlobGasPerBlock / 2,
			expectedExcessGas: big.NewInt(0),
		},
		{
			name:              "excess at target",
			prevExcessBlobGas: big.NewInt(0),
			prevBlobGasUsed:   params.BlobTxTargetBlobGasPerBlock,
			expectedExcessGas: big.NewInt(0),
		},
		{
			name:              "excess above target",
			prevExcessBlobGas: big.NewInt(0),
			prevBlobGasUsed:   params.BlobTxTargetBlobGasPerBlock + 100000,
			expectedExcessGas: big.NewInt(100000),
		},
		{
			name:              "accumulated excess",
			prevExcessBlobGas: big.NewInt(50000),
			prevBlobGasUsed:   params.BlobTxTargetBlobGasPerBlock + 50000,
			expectedExcessGas: big.NewInt(100000),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := CalcExcessBlobGas(test.prevExcessBlobGas, test.prevBlobGasUsed)
			if result.Cmp(test.expectedExcessGas) != 0 {
				t.Errorf("CalcExcessBlobGas(%v, %d) = %v, want %v",
					test.prevExcessBlobGas, test.prevBlobGasUsed, result, test.expectedExcessGas)
			}
		})
	}
}

// TestCalcBlobBaseFee tests the blob base fee calculation (EIP-4844).
func TestCalcBlobBaseFee(t *testing.T) {
	tests := []struct {
		name            string
		excessBlobGas   *big.Int
		expectedMinimum uint64 // minimum blob base fee should be at least this
	}{
		{
			name:            "nil excess blob gas",
			excessBlobGas:   nil,
			expectedMinimum: params.BlobTxMinBlobGasprice,
		},
		{
			name:            "zero excess blob gas",
			excessBlobGas:   big.NewInt(0),
			expectedMinimum: params.BlobTxMinBlobGasprice,
		},
		{
			name:            "small excess",
			excessBlobGas:   big.NewInt(100000),
			expectedMinimum: params.BlobTxMinBlobGasprice,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := CalcBlobBaseFee(test.excessBlobGas)
			if result == nil {
				t.Errorf("CalcBlobBaseFee(%v) returned nil", test.excessBlobGas)
			}
			if result.Cmp(big.NewInt(int64(test.expectedMinimum))) < 0 {
				t.Errorf("CalcBlobBaseFee(%v) = %v, expected >= %d",
					test.excessBlobGas, result, test.expectedMinimum)
			}
		})
	}
}

// TestBlobBaseFeeIncreasesWithExcess tests that blob base fee increases with excess blob gas.
func TestBlobBaseFeeIncreasesWithExcess(t *testing.T) {
	baseFee := CalcBlobBaseFee(big.NewInt(0))
	fee10M := CalcBlobBaseFee(big.NewInt(10_000_000))
	fee100M := CalcBlobBaseFee(big.NewInt(100_000_000))

	if fee10M.Cmp(baseFee) <= 0 {
		t.Errorf("blob base fee should increase with excess: 0 gas -> %v, 10M gas -> %v",
			baseFee, fee10M)
	}
	if fee100M.Cmp(fee10M) <= 0 {
		t.Errorf("blob base fee should increase with excess: 10M gas -> %v, 100M gas -> %v",
			fee10M, fee100M)
	}
}

// TestGetBlobGasUsed tests blob gas calculation.
func TestGetBlobGasUsed(t *testing.T) {
	tests := []struct {
		numBlobs    uint64
		expectedGas uint64
	}{
		{0, 0},
		{1, params.BlobTxBlobGasPerBlob},
		{2, 2 * params.BlobTxBlobGasPerBlob},
		{6, 6 * params.BlobTxBlobGasPerBlob},
	}

	for _, test := range tests {
		result := GetBlobGasUsed(test.numBlobs)
		if result != test.expectedGas {
			t.Errorf("GetBlobGasUsed(%d) = %d, want %d", test.numBlobs, result, test.expectedGas)
		}
	}
}
