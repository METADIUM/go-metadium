// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package kzg4844

import (
	"testing"

	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
)

// makeTestBlob returns a test blob (all-zero with first byte set).
func makeTestBlob(firstByte byte) gokzg4844.Blob {
	var blob gokzg4844.Blob
	blob[0] = firstByte
	return blob
}

// TestKZGToVersionedHash verifies the versioned hash computation.
func TestKZGToVersionedHash(t *testing.T) {
	commitment := make([]byte, 48)
	commitment[0] = 0xcd

	hash := KZGToVersionedHash(commitment)
	if hash[0] != BlobCommitmentVersionKZG {
		t.Errorf("versioned hash[0] = %x, want %x", hash[0], BlobCommitmentVersionKZG)
	}
}

// TestValidateBlobSidecar_LengthMismatch tests that mismatched sidecar lengths are rejected.
func TestValidateBlobSidecar_LengthMismatch(t *testing.T) {
	hashes := make([][32]byte, 2)
	blobs := [][]byte{make([]byte, gokzg4844.ScalarsPerBlob*gokzg4844.SerializedScalarSize)}
	commitments := [][]byte{make([]byte, 48)}
	proofs := [][]byte{make([]byte, 48)}

	err := ValidateBlobSidecar(hashes, blobs, commitments, proofs)
	if err == nil {
		t.Error("expected error for length mismatch, got nil")
	}
}

// TestValidateBlobSidecar_InvalidBlobSize tests that wrong-size blobs are rejected.
func TestValidateBlobSidecar_InvalidBlobSize(t *testing.T) {
	hashes := make([][32]byte, 1)
	blobs := [][]byte{make([]byte, 100)} // wrong size
	commitments := [][]byte{make([]byte, 48)}
	proofs := [][]byte{make([]byte, 48)}

	err := ValidateBlobSidecar(hashes, blobs, commitments, proofs)
	if err == nil {
		t.Error("expected error for invalid blob size, got nil")
	}
}

// TestValidateBlobSidecar_HashMismatch tests that a wrong commitment is rejected.
func TestValidateBlobSidecar_HashMismatch(t *testing.T) {
	ctx, err := getContext()
	if err != nil {
		t.Skip("KZG context unavailable:", err)
	}

	blob := makeTestBlob(0x01)
	commitment, err := ctx.BlobToKZGCommitment(&blob, 0)
	if err != nil {
		t.Fatalf("BlobToKZGCommitment: %v", err)
	}

	// Use a wrong hash (all zeros).
	hashes := make([][32]byte, 1)

	blobs := [][]byte{blob[:]}
	commitments := [][]byte{commitment[:]}
	proofs := [][]byte{make([]byte, 48)} // dummy proof

	err = ValidateBlobSidecar(hashes, blobs, commitments, proofs)
	if err == nil {
		t.Error("expected error for hash mismatch, got nil")
	}
}

// TestValidateBlobSidecar_Valid tests that a correctly constructed sidecar passes validation.
func TestValidateBlobSidecar_Valid(t *testing.T) {
	ctx, err := getContext()
	if err != nil {
		t.Skip("KZG context unavailable:", err)
	}

	blob := makeTestBlob(0x00)
	commitment, err := ctx.BlobToKZGCommitment(&blob, 0)
	if err != nil {
		t.Fatalf("BlobToKZGCommitment: %v", err)
	}
	proof, err := ctx.ComputeBlobKZGProof(&blob, commitment, 0)
	if err != nil {
		t.Fatalf("ComputeBlobKZGProof: %v", err)
	}

	versionedHash := KZGToVersionedHash(commitment[:])
	hashes := [][32]byte{versionedHash}
	blobs := [][]byte{blob[:]}
	commitments := [][]byte{commitment[:]}
	proofs := [][]byte{proof[:]}

	if err := ValidateBlobSidecar(hashes, blobs, commitments, proofs); err != nil {
		t.Errorf("ValidateBlobSidecar failed for valid sidecar: %v", err)
	}
}
