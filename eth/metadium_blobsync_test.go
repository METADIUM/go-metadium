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

package eth

import (
	"crypto/sha256"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/holiman/uint256"
)

var (
	testBlob          = kzg4844.Blob{}
	testBlobCommit, _ = kzg4844.BlobToCommitment(testBlob)
	testBlobProof, _  = kzg4844.ComputeBlobProof(testBlob, testBlobCommit)
	testBlobVHash     = kzg4844.CalcBlobHashV1(sha256.New(), &testBlobCommit)
)

// newTestBlobTx builds a blob transaction carrying the given versioned hashes.
func newTestBlobTx(hashes []common.Hash) *types.Transaction {
	return types.NewTx(&types.BlobTx{
		ChainID:    uint256.NewInt(1),
		BlobHashes: hashes,
	})
}

// newValidSidecar builds a sidecar that matches a single empty blob.
func newValidSidecar() *types.BlobTxSidecar {
	return &types.BlobTxSidecar{
		Blobs:       [][]byte{testBlob[:]},
		Commitments: [][]byte{testBlobCommit[:]},
		Proofs:      [][]byte{testBlobProof[:]},
		BlobHashes:  []common.Hash{testBlobVHash},
	}
}

func TestValidateBlobSidecarsForBlock(t *testing.T) {
	blobTx := newTestBlobTx([]common.Hash{testBlobVHash})

	// Happy path: one blob tx, one matching valid sidecar.
	if err := validateBlobSidecarsForBlock([]*types.Transaction{blobTx}, []*types.BlobTxSidecar{newValidSidecar()}); err != nil {
		t.Fatalf("valid sidecar rejected: %v", err)
	}

	// Count mismatch: more txs than sidecars.
	if err := validateBlobSidecarsForBlock([]*types.Transaction{blobTx, blobTx}, []*types.BlobTxSidecar{newValidSidecar()}); err == nil {
		t.Fatal("expected error on sidecar count mismatch")
	}

	// Nil sidecar entry.
	if err := validateBlobSidecarsForBlock([]*types.Transaction{blobTx}, []*types.BlobTxSidecar{nil}); err == nil {
		t.Fatal("expected error on nil sidecar")
	}

	// Versioned-hash mismatch: sidecar advertises a different hash than the tx.
	badHash := newValidSidecar()
	badHash.BlobHashes = []common.Hash{{0xde, 0xad}}
	if err := validateBlobSidecarsForBlock([]*types.Transaction{blobTx}, []*types.BlobTxSidecar{badHash}); err == nil {
		t.Fatal("expected error on versioned-hash mismatch")
	}

	// Tampered proof: KZG verification must fail (ValidateBlobSidecar).
	badProof := newValidSidecar()
	tampered := make([]byte, len(badProof.Proofs[0]))
	copy(tampered, badProof.Proofs[0])
	tampered[0] ^= 0xff
	badProof.Proofs[0] = tampered
	if err := validateBlobSidecarsForBlock([]*types.Transaction{blobTx}, []*types.BlobTxSidecar{badProof}); err == nil {
		t.Fatal("expected error on tampered KZG proof")
	}
}
