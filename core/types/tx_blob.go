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
	"io"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

// blobTxRLP is a helper struct for RLP encode/decode of BlobTx.
// It uses *big.Int which the RLP package handles correctly (unlike uint256.Int).
type blobTxRLP struct {
	ChainID          *big.Int
	Nonce            uint64
	GasTipCap        *big.Int
	GasFeeCap        *big.Int
	Gas              uint64
	To               *common.Address `rlp:"nil"`
	Value            *big.Int
	Data             []byte
	AccessList       AccessList
	MaxFeePerBlobGas *big.Int
	BlobHashes       []common.Hash
	V                *big.Int
	R                *big.Int
	S                *big.Int
}

// EncodeRLP implements rlp.Encoder for BlobTx using the big.Int-based helper.
func (tx *BlobTx) EncodeRLP(w io.Writer) error {
	enc := blobTxRLP{
		Nonce:      tx.Nonce,
		Gas:        tx.Gas,
		To:         tx.To,
		Data:       tx.Data,
		AccessList: tx.AccessList,
		BlobHashes: tx.BlobHashes,
	}
	if tx.ChainID != nil {
		enc.ChainID = tx.ChainID.ToBig()
	}
	if tx.GasTipCap != nil {
		enc.GasTipCap = tx.GasTipCap.ToBig()
	}
	if tx.GasFeeCap != nil {
		enc.GasFeeCap = tx.GasFeeCap.ToBig()
	}
	if tx.Value != nil {
		enc.Value = tx.Value.ToBig()
	}
	if tx.MaxFeePerBlobGas != nil {
		enc.MaxFeePerBlobGas = tx.MaxFeePerBlobGas.ToBig()
	}
	if tx.V != nil {
		enc.V = tx.V.ToBig()
	}
	if tx.R != nil {
		enc.R = tx.R.ToBig()
	}
	if tx.S != nil {
		enc.S = tx.S.ToBig()
	}
	return rlp.Encode(w, enc)
}

// DecodeRLP implements rlp.Decoder for BlobTx.
func (tx *BlobTx) DecodeRLP(s *rlp.Stream) error {
	var dec blobTxRLP
	if err := s.Decode(&dec); err != nil {
		return err
	}
	tx.Nonce = dec.Nonce
	tx.Gas = dec.Gas
	tx.To = dec.To
	tx.Data = dec.Data
	tx.AccessList = dec.AccessList
	tx.BlobHashes = dec.BlobHashes

	fromBig := func(b *big.Int) *uint256.Int {
		if b == nil {
			return new(uint256.Int)
		}
		v, _ := uint256.FromBig(b)
		return v
	}
	tx.ChainID = fromBig(dec.ChainID)
	tx.GasTipCap = fromBig(dec.GasTipCap)
	tx.GasFeeCap = fromBig(dec.GasFeeCap)
	tx.Value = fromBig(dec.Value)
	tx.MaxFeePerBlobGas = fromBig(dec.MaxFeePerBlobGas)
	tx.V = fromBig(dec.V)
	tx.R = fromBig(dec.R)
	tx.S = fromBig(dec.S)
	return nil
}

// BlobTx represents an EIP-4844 blob transaction.
type BlobTx struct {
	ChainID    *uint256.Int    // Chain ID
	Nonce      uint64          // Account nonce
	GasTipCap  *uint256.Int    // Max priority fee per gas (EIP-1559)
	GasFeeCap  *uint256.Int    // Max fee per gas (EIP-1559)
	Gas        uint64          // Gas limit
	To         *common.Address // Recipient
	Value      *uint256.Int    // Amount of Ether to send
	Data       []byte          // Transaction data
	AccessList AccessList      // EIP-2930 access list

	MaxFeePerBlobGas *uint256.Int  // Max fee per blob gas (EIP-4844)
	BlobHashes       []common.Hash // Versioned hashes of blobs (EIP-4844)

	// Sidecar is not included in transaction hash; excluded from RLP
	Sidecar *BlobTxSidecar `rlp:"-"`

	V *uint256.Int // Signature V
	R *uint256.Int // Signature R
	S *uint256.Int // Signature S
}

// withoutSidecar returns a shallow copy of tx with the sidecar removed.
func (tx *BlobTx) withoutSidecar() *BlobTx {
	cpy := *tx
	cpy.Sidecar = nil
	return &cpy
}

// copy creates a deep copy of the transaction data and initializes all fields.
func (tx *BlobTx) copy() TxData {
	cpy := &BlobTx{
		ChainID:          new(uint256.Int),
		Nonce:            tx.Nonce,
		GasTipCap:        new(uint256.Int),
		GasFeeCap:        new(uint256.Int),
		Gas:              tx.Gas,
		Value:            new(uint256.Int),
		Data:             common.CopyBytes(tx.Data),
		MaxFeePerBlobGas: new(uint256.Int),
		AccessList:       make(AccessList, len(tx.AccessList)),
		BlobHashes:       make([]common.Hash, len(tx.BlobHashes)),
		V:                new(uint256.Int),
		R:                new(uint256.Int),
		S:                new(uint256.Int),
	}
	copy(cpy.AccessList, tx.AccessList)
	copy(cpy.BlobHashes, tx.BlobHashes)
	if tx.To != nil {
		cpy.To = new(common.Address)
		*cpy.To = *tx.To
	}
	if tx.ChainID != nil {
		cpy.ChainID.Set(tx.ChainID)
	}
	if tx.GasTipCap != nil {
		cpy.GasTipCap.Set(tx.GasTipCap)
	}
	if tx.GasFeeCap != nil {
		cpy.GasFeeCap.Set(tx.GasFeeCap)
	}
	if tx.Value != nil {
		cpy.Value.Set(tx.Value)
	}
	if tx.MaxFeePerBlobGas != nil {
		cpy.MaxFeePerBlobGas.Set(tx.MaxFeePerBlobGas)
	}
	if tx.V != nil {
		cpy.V.Set(tx.V)
	}
	if tx.R != nil {
		cpy.R.Set(tx.R)
	}
	if tx.S != nil {
		cpy.S.Set(tx.S)
	}
	return cpy
}

func (tx *BlobTx) txType() byte {
	return BlobTxType
}

func (tx *BlobTx) chainID() *big.Int {
	if tx.ChainID == nil {
		return nil
	}
	return tx.ChainID.ToBig()
}

func (tx *BlobTx) accessList() AccessList {
	return tx.AccessList
}

func (tx *BlobTx) data() []byte {
	return tx.Data
}

func (tx *BlobTx) gas() uint64 {
	return tx.Gas
}

func (tx *BlobTx) gasPrice() *big.Int {
	return tx.GasFeeCap.ToBig()
}

func (tx *BlobTx) gasTipCap() *big.Int {
	return tx.GasTipCap.ToBig()
}

func (tx *BlobTx) gasFeeCap() *big.Int {
	return tx.GasFeeCap.ToBig()
}

func (tx *BlobTx) value() *big.Int {
	return tx.Value.ToBig()
}

func (tx *BlobTx) nonce() uint64 {
	return tx.Nonce
}

func (tx *BlobTx) to() *common.Address {
	return tx.To
}

// blobHashes returns the versioned hashes of the blobs committed to by the transaction.
func (tx *BlobTx) blobHashes() []common.Hash {
	return tx.BlobHashes
}

// feePayer always returns nil for blob transactions (no fee delegation in EIP-4844)
func (tx *BlobTx) feePayer() *common.Address {
	return nil
}

// rawFeePayerSignatureValues returns nil for blob transactions (no fee delegation)
func (tx *BlobTx) rawFeePayerSignatureValues() (*big.Int, *big.Int, *big.Int) {
	return nil, nil, nil
}

// BlobGasCost returns the gas cost of the blobs committed to by the transaction.
func (tx *BlobTx) blobGasCost() *big.Int {
	if tx.MaxFeePerBlobGas == nil || len(tx.BlobHashes) == 0 {
		return nil
	}
	return new(big.Int).Mul(tx.MaxFeePerBlobGas.ToBig(), new(big.Int).SetUint64(uint64(len(tx.BlobHashes)*131072)))
}

func (tx *BlobTx) rawSignatureValues() (*big.Int, *big.Int, *big.Int) {
	return tx.V.ToBig(), tx.R.ToBig(), tx.S.ToBig()
}

func (tx *BlobTx) setSignatureValues(chainID, v, r, s *big.Int) {
	tx.ChainID, _ = uint256.FromBig(chainID)
	tx.V, _ = uint256.FromBig(v)
	tx.R, _ = uint256.FromBig(r)
	tx.S, _ = uint256.FromBig(s)
}
