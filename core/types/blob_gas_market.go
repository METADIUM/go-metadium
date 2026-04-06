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

	"github.com/ethereum/go-ethereum/params"
)

// CalcExcessBlobGas calculates the excess blob gas for a block based on the previous block's
// excess blob gas and blob gas used (EIP-4844).
//
// Formula: excessBlobGas = max(0, prevExcessBlobGas + prevBlobGasUsed - TARGET_BLOB_GAS_PER_BLOCK)
func CalcExcessBlobGas(prevExcessBlobGas *big.Int, prevBlobGasUsed uint64) *big.Int {
	if prevExcessBlobGas == nil {
		prevExcessBlobGas = big.NewInt(0)
	}

	// prevExcessBlobGas + prevBlobGasUsed
	sum := new(big.Int).Add(prevExcessBlobGas, new(big.Int).SetUint64(prevBlobGasUsed))

	// subtract TARGET_BLOB_GAS_PER_BLOCK
	excess := new(big.Int).Sub(sum, new(big.Int).SetUint64(params.TargetBlobGasPerBlock))

	// max(0, excess)
	if excess.Sign() < 0 {
		return big.NewInt(0)
	}
	return excess
}

// CalcBlobBaseFee calculates the blob base fee from the excess blob gas (EIP-4844).
//
// Formula: blobBaseFee = fakeexponential(MIN_BLOB_BASE_FEE, excessBlobGas, BLOB_BASE_FEE_UPDATE_FRACTION)
// where fakeexponential is defined as:
//
//	def fakeexponential(factor, numerator, denominator):
//	    i = 1
//	    output = 0
//	    numerator_accum = factor * denominator
//	    while numerator_accum > 0:
//	        output += numerator_accum
//	        numerator_accum = (numerator_accum * numerator) // (denominator * i)
//	        i += 1
//	    return output // denominator
func CalcBlobBaseFee(excessBlobGas *big.Int) *big.Int {
	if excessBlobGas == nil {
		excessBlobGas = big.NewInt(0)
	}

	// Convert constants to big.Int
	minBlobBaseFee := big.NewInt(int64(params.MinBlobBaseFee))
	denominator := big.NewInt(int64(params.BlobBaseFeeUpdateFraction))

	// i = 1, output = 0, numerator_accum = MIN_BLOB_BASE_FEE * BLOB_BASE_FEE_UPDATE_FRACTION
	i := big.NewInt(1)
	output := big.NewInt(0)
	numeratorAccum := new(big.Int).Mul(minBlobBaseFee, denominator)

	// Perform fakeexponential calculation
	for numeratorAccum.Sign() > 0 {
		// output += numerator_accum
		output.Add(output, numeratorAccum)

		// numerator_accum = (numerator_accum * excessBlobGas) // (BLOB_BASE_FEE_UPDATE_FRACTION * i)
		numeratorAccum.Mul(numeratorAccum, excessBlobGas)
		denomAccum := new(big.Int).Mul(denominator, i)
		numeratorAccum.Div(numeratorAccum, denomAccum)

		// i += 1
		i.Add(i, big.NewInt(1))

		// Prevent infinite loops for very large excess blob gas
		// After reasonable iterations, the numeratorAccum becomes negligible
		if i.Cmp(big.NewInt(100)) > 0 {
			break
		}
	}

	// return output // denominator
	return new(big.Int).Div(output, denominator)
}

// GetBlobGasUsed calculates the total blob gas used in a block based on the number of blobs.
// Each blob costs BlobTxPerBlobGas gas.
func GetBlobGasUsed(numBlobs uint64) uint64 {
	return numBlobs * params.BlobTxPerBlobGas
}
