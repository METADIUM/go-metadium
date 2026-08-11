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
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

// TestBlobTxType verifies BlobTx type identification.
func TestBlobTxType(t *testing.T) {
	tx := &BlobTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       21000,
		To:        nil,
		Value:     uint256.NewInt(0),
		Data:      []byte{},
	}

	if tx.txType() != BlobTxType {
		t.Errorf("BlobTx.txType() = %d, want %d", tx.txType(), BlobTxType)
	}
}

// TestBlobTxCopy verifies that BlobTx.copy() creates a deep copy.
func TestBlobTxCopy(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := &BlobTx{
		ChainID:          uint256.NewInt(1),
		Nonce:            1,
		GasTipCap:        uint256.NewInt(100),
		GasFeeCap:        uint256.NewInt(200),
		Gas:              21000,
		To:               &addr,
		Value:            uint256.NewInt(1000),
		Data:             []byte("test data"),
		MaxFeePerBlobGas: uint256.NewInt(50),
		BlobHashes:       []common.Hash{common.HexToHash("0xaaaa"), common.HexToHash("0xbbbb")},
		V:                uint256.NewInt(27),
		R:                uint256.NewInt(1),
		S:                uint256.NewInt(2),
	}

	copied := tx.copy().(*BlobTx)

	// Verify all fields are copied
	if copied.Nonce != tx.Nonce {
		t.Errorf("Nonce not copied")
	}
	if copied.Gas != tx.Gas {
		t.Errorf("Gas not copied")
	}
	if copied.MaxFeePerBlobGas.Cmp(tx.MaxFeePerBlobGas) != 0 {
		t.Errorf("MaxFeePerBlobGas not copied")
	}
	if len(copied.BlobHashes) != len(tx.BlobHashes) {
		t.Errorf("BlobHashes count mismatch")
	}

	// Verify deep copy (modifying copy shouldn't affect original)
	if copied.Data != nil {
		copied.Data[0] = 99
		if tx.Data[0] == 99 {
			t.Errorf("Data was not deep copied")
		}
	}
}

// TestBlobTxSignatureValues verifies signature value handling.
func TestBlobTxSignatureValues(t *testing.T) {
	tx := &BlobTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1),
		Gas:       21000,
	}

	chainID := big.NewInt(1)
	v := big.NewInt(27)
	r := big.NewInt(100)
	s := big.NewInt(200)

	tx.setSignatureValues(chainID, v, r, s)

	retV, retR, retS := tx.rawSignatureValues()
	if retV.Cmp(v) != 0 || retR.Cmp(r) != 0 || retS.Cmp(s) != 0 {
		t.Errorf("Signature values not set correctly")
	}
}

// TestBlobTxBlobGasCost verifies blob gas cost calculation.
func TestBlobTxBlobGasCost(t *testing.T) {
	tests := []struct {
		numBlobs      int
		maxFeePerBlob uint64
		expectNonZero bool
	}{
		{0, 100, false},
		{1, 100, true},
		{2, 50, true},
	}

	for i, test := range tests {
		tx := &BlobTx{
			MaxFeePerBlobGas: uint256.NewInt(test.maxFeePerBlob),
		}

		// Add blobs
		for j := 0; j < test.numBlobs; j++ {
			tx.BlobHashes = append(tx.BlobHashes, common.Hash{})
		}

		cost := tx.blobGasCost()
		if test.expectNonZero && cost == nil {
			t.Errorf("Test case %d: expected non-nil blob gas cost", i)
		}
		if !test.expectNonZero && cost != nil {
			t.Errorf("Test case %d: expected nil blob gas cost", i)
		}
	}
}

// TestBlobTxFeePayer verifies that BlobTx has no fee payer.
func TestBlobTxFeePayer(t *testing.T) {
	tx := &BlobTx{
		ChainID: uint256.NewInt(1),
	}

	feePayer := tx.feePayer()
	if feePayer != nil {
		t.Errorf("BlobTx.feePayer() should be nil, got %v", feePayer)
	}

	v, r, s := tx.rawFeePayerSignatureValues()
	if v != nil || r != nil || s != nil {
		t.Errorf("BlobTx fee payer signature values should be nil")
	}
}

// TestBlobTxAccessList verifies access list handling.
func TestBlobTxAccessList(t *testing.T) {
	addr1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := common.HexToAddress("0x2222222222222222222222222222222222222222")

	key1 := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")

	accessList := AccessList{
		AccessTuple{
			Address:     addr1,
			StorageKeys: []common.Hash{key1},
		},
		AccessTuple{
			Address:     addr2,
			StorageKeys: []common.Hash{},
		},
	}

	tx := &BlobTx{
		AccessList: accessList,
	}

	if len(tx.accessList()) != 2 {
		t.Errorf("Access list length mismatch")
	}
}

// TestBlobTxTypeName verifies BlobTx type identification in wrapped transaction.
func TestBlobTxTypeName(t *testing.T) {
	tx := &BlobTx{
		ChainID: uint256.NewInt(1),
	}

	wrappedTx := NewTx(tx)
	if wrappedTx.Type() != BlobTxType {
		t.Errorf("Transaction type = %d, want %d", wrappedTx.Type(), BlobTxType)
	}
}

// TestBlobTxChainID verifies chain ID handling.
func TestBlobTxChainID(t *testing.T) {
	chainID := uint256.NewInt(1337)
	tx := &BlobTx{
		ChainID: chainID,
	}

	result := tx.chainID()
	if result == nil {
		t.Errorf("chainID() returned nil")
	}
	if result.Cmp(big.NewInt(1337)) != 0 {
		t.Errorf("chainID() = %v, want 1337", result)
	}
}

// TestBlobTxAccessors verifies all accessor methods.
func TestBlobTxAccessors(t *testing.T) {
	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := &BlobTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     5,
		GasTipCap: uint256.NewInt(10),
		GasFeeCap: uint256.NewInt(20),
		Gas:       100000,
		To:        &addr,
		Value:     uint256.NewInt(1000),
		Data:      []byte("hello"),
	}

	if tx.nonce() != 5 {
		t.Errorf("nonce() = %d, want 5", tx.nonce())
	}
	if tx.gas() != 100000 {
		t.Errorf("gas() = %d, want 100000", tx.gas())
	}
	if tx.to() != &addr {
		t.Errorf("to() mismatch")
	}
	if string(tx.data()) != "hello" {
		t.Errorf("data() = %s, want hello", string(tx.data()))
	}
}

// TestDecodeBlobTxNetworkEncoding verifies round-trip encode/decode of the
// EIP-4844 network encoding that includes the BlobTxSidecar.
func TestDecodeBlobTxNetworkEncoding(t *testing.T) {
	addr := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	blobHash := common.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000001")
	blob := make([]byte, BlobSize)
	blob[0] = 0xab
	commitment := make([]byte, 48)
	commitment[0] = 0xcd
	proof := make([]byte, 48)
	proof[0] = 0xef

	inner := &BlobTx{
		ChainID:          uint256.NewInt(1337),
		Nonce:            3,
		GasTipCap:        uint256.NewInt(1e9),
		GasFeeCap:        uint256.NewInt(2e9),
		Gas:              100000,
		To:               &addr,
		Value:            uint256.NewInt(0),
		Data:             []byte{},
		MaxFeePerBlobGas: uint256.NewInt(1e6),
		BlobHashes:       []common.Hash{blobHash},
		V:                uint256.NewInt(1),
		R:                uint256.NewInt(2),
		S:                uint256.NewInt(3),
	}

	// Build the network encoding: 0x03 || rlp([[tx_fields], blobs, commitments, proofs])
	wrapper := blobTxNetworkWrapper{
		Tx:          *inner,
		Blobs:       [][]byte{blob},
		Commitments: [][]byte{commitment},
		Proofs:      [][]byte{proof},
	}
	encoded, err := rlp.EncodeToBytes(&wrapper)
	if err != nil {
		t.Fatalf("failed to RLP-encode wrapper: %v", err)
	}
	data := append([]byte{BlobTxType}, encoded...)

	tx, sidecar, err := DecodeBlobTxNetworkEncoding(data)
	if err != nil {
		t.Fatalf("DecodeBlobTxNetworkEncoding failed: %v", err)
	}
	if tx.Type() != BlobTxType {
		t.Errorf("tx type = %d, want %d", tx.Type(), BlobTxType)
	}
	if len(tx.BlobHashes()) != 1 || tx.BlobHashes()[0] != blobHash {
		t.Errorf("blob hashes mismatch: %v", tx.BlobHashes())
	}
	if len(sidecar.Blobs) != 1 || !bytes.Equal(sidecar.Blobs[0], blob) {
		t.Errorf("sidecar blob mismatch")
	}
	if len(sidecar.Commitments) != 1 || !bytes.Equal(sidecar.Commitments[0], commitment) {
		t.Errorf("sidecar commitment mismatch")
	}
	if len(sidecar.Proofs) != 1 || !bytes.Equal(sidecar.Proofs[0], proof) {
		t.Errorf("sidecar proof mismatch")
	}
	if len(sidecar.BlobHashes) != 1 || sidecar.BlobHashes[0] != blobHash {
		t.Errorf("sidecar blob hash mismatch")
	}

	// Canonical encoding (without sidecar) must fail DecodeBlobTxNetworkEncoding.
	canonical, err := NewTx(inner).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	_, _, err = DecodeBlobTxNetworkEncoding(canonical)
	if err == nil {
		t.Error("expected error decoding canonical (no-sidecar) encoding, got nil")
	}
}

// Ensure unused import is referenced.
var _ = big.NewInt

// createEmptyBlobTxInner creates a minimal BlobTx for testing.
// Used by transaction_signing_test.go.
func createEmptyBlobTxInner(withSidecar bool) *BlobTx {
	addr := common.HexToAddress("0x0304050000000000000000000000000000000000")
	blobHash := common.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000001")
	inner := &BlobTx{
		ChainID:          uint256.NewInt(1),
		Nonce:            5,
		GasTipCap:        uint256.NewInt(22),
		GasFeeCap:        uint256.NewInt(5),
		Gas:              25000,
		To:               &addr,
		Value:            uint256.NewInt(99),
		Data:             make([]byte, 50),
		MaxFeePerBlobGas: uint256.NewInt(15),
		BlobHashes:       []common.Hash{blobHash},
		V:                uint256.NewInt(0),
		R:                uint256.NewInt(1),
		S:                uint256.NewInt(1),
	}
	if withSidecar {
		blob := make([]byte, BlobSize)
		commitment := make([]byte, 48)
		proof := make([]byte, 48)
		inner.Sidecar = &BlobTxSidecar{
			Blobs:       [][]byte{blob},
			Commitments: [][]byte{commitment},
			Proofs:      [][]byte{proof},
			BlobHashes:  []common.Hash{blobHash},
		}
	}
	return inner
}
