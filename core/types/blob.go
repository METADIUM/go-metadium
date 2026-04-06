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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	BlobSize = 4096 * 31 // 131072 bytes per blob
)

// BlobTxSidecar contains the blobs and proofs for a blob transaction.
type BlobTxSidecar struct {
	Blobs       [][]byte       // Blob data (4096 * 31 bytes each)
	Commitments [][]byte       // Blob commitments (KZG commitments)
	Proofs      [][]byte       // Blob proofs
	BlobHashes  []common.Hash  // Versioned hashes of blobs
}

// blobTxNetworkWrapper is used to decode the EIP-4844 network encoding for blob transactions.
// Network encoding format: 0x03 || rlp([[tx_fields...], blobs, commitments, proofs])
// This is distinct from the canonical encoding that only includes tx fields.
type blobTxNetworkWrapper struct {
	Tx          BlobTx
	Blobs       [][]byte
	Commitments [][]byte
	Proofs      [][]byte
}

// DecodeBlobTxNetworkEncoding decodes a blob transaction from the EIP-4844 network encoding,
// which includes the BlobTxSidecar (blobs, commitments, proofs) appended after the tx body.
//
// Network encoding: 0x03 || rlp([[tx_fields], blobs, commitments, proofs])
// Canonical encoding: 0x03 || rlp([tx_fields])
//
// Returns the transaction and sidecar. If the input is not in network format (no sidecar),
// both return values are nil and err is non-nil.
func DecodeBlobTxNetworkEncoding(data []byte) (*Transaction, *BlobTxSidecar, error) {
	if len(data) < 2 || data[0] != BlobTxType {
		return nil, nil, ErrTxTypeNotSupported
	}
	var wrapper blobTxNetworkWrapper
	if err := rlp.DecodeBytes(data[1:], &wrapper); err != nil {
		return nil, nil, err
	}
	tx := NewTx(&wrapper.Tx)
	sidecar := &BlobTxSidecar{
		Blobs:       wrapper.Blobs,
		Commitments: wrapper.Commitments,
		Proofs:      wrapper.Proofs,
		BlobHashes:  wrapper.Tx.BlobHashes,
	}
	return tx, sidecar, nil
}

// BlobTxSidecarSize returns the total size of the sidecars in bytes.
func (s *BlobTxSidecar) Size() int {
	size := 0
	for _, blob := range s.Blobs {
		size += len(blob)
	}
	for _, commitment := range s.Commitments {
		size += len(commitment)
	}
	for _, proof := range s.Proofs {
		size += len(proof)
	}
	size += len(s.BlobHashes) * 32
	return size
}
