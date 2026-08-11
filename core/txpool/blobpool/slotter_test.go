// Copyright 2023 The go-ethereum Authors
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

package blobpool

import "testing"

// Tests that the slotter creates the expected database shelves.
func TestNewSlotter(t *testing.T) {
	// Generate the database shelve sizes
	slotter := newSlotter()

	var shelves []uint32
	for {
		shelf, done := slotter()
		shelves = append(shelves, shelf)
		if done {
			break
		}
	}
	// Compare the database shelves to the expected ones.
	// Metadium uses MaxBlobGasPerBlock = 2 * blobSize (max 2 blobs/tx), producing
	// maxBlobsPerTransaction+9 = 11 shelves (k=0..10). Upstream uses 6 blobs → 15 shelves.
	want := []uint32{
		0*blobSize + txAvgSize,  // 0 blob + some expected tx infos
		1*blobSize + txAvgSize,  // 1 blob + some expected tx infos
		2*blobSize + txAvgSize,  // 2 blobs + some expected tx infos (could be fewer blobs + more tx data)
		3*blobSize + txAvgSize,  // 1-2 blobs + unexpectedly large tx infos
		4*blobSize + txAvgSize,  // 1-2 blobs + unexpectedly large tx infos
		5*blobSize + txAvgSize,  // 1-2 blobs + unexpectedly large tx infos
		6*blobSize + txAvgSize,  // 1-2 blobs + unexpectedly large tx infos
		7*blobSize + txAvgSize,  // 1-2 blobs + unexpectedly large tx infos
		8*blobSize + txAvgSize,  // 1-2 blobs + unexpectedly large tx infos
		9*blobSize + txAvgSize,  // 1-2 blobs + unexpectedly large tx infos
		10*blobSize + txAvgSize, // 1-2 blobs + unexpectedly large tx infos >= maxBlobsPerTransaction*blobSize + txMaxSize
	}
	if len(shelves) != len(want) {
		t.Errorf("shelves count mismatch: have %d, want %d", len(shelves), len(want))
	}
	for i := 0; i < len(shelves) && i < len(want); i++ {
		if shelves[i] != want[i] {
			t.Errorf("shelf %d mismatch: have %d, want %d", i, shelves[i], want[i])
		}
	}
}
