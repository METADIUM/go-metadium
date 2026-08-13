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

package ethash

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// TestVerifyCamelliaHeaderFields covers the header rules Camellia brings in.
// Before them, the fields were permitted but never value-checked, so a sealer
// could put an arbitrary blob fee market into a block and every node would
// accept it (issue #70).
func TestVerifyCamelliaHeaderFields(t *testing.T) {
	empty := types.EmptyWithdrawalsHash
	other := common.HexToHash("0xdead")

	// A parent that used one blob, so the expected excessBlobGas is not zero and
	// a wrong value cannot pass by accident.
	parentBlobGasUsed := uint64(params.BlobTxBlobGasPerBlob)
	parent := &types.Header{
		Number:        big.NewInt(100),
		ExcessBlobGas: new(big.Int).SetUint64(params.BlobTxTargetBlobGasPerBlock),
		BlobGasUsed:   new(big.Int).SetUint64(parentBlobGasUsed),
	}
	wantExcess := types.CalcExcessBlobGas(parent.ExcessBlobGas, parentBlobGasUsed)
	if wantExcess.Sign() == 0 {
		t.Fatal("the fixture produces a zero expectation, which would not catch a wrong value")
	}

	header := func(mutate func(*types.Header)) *types.Header {
		h := &types.Header{
			Number:          big.NewInt(101),
			WithdrawalsHash: &empty,
			ExcessBlobGas:   new(big.Int).Set(wantExcess),
			BlobGasUsed:     new(big.Int),
		}
		if mutate != nil {
			mutate(h)
		}
		return h
	}

	for _, tt := range []struct {
		name   string
		header *types.Header
		want   string // substring of the expected error, empty for success
	}{
		{"well formed", header(nil), ""},
		{"withdrawals hash missing", header(func(h *types.Header) { h.WithdrawalsHash = nil }), "missing withdrawalsHash"},
		{"withdrawals hash not empty", header(func(h *types.Header) { h.WithdrawalsHash = &other }), "invalid withdrawalsHash"},
		{"excess blob gas missing", header(func(h *types.Header) { h.ExcessBlobGas = nil }), "missing excessBlobGas"},
		{"blob gas used missing", header(func(h *types.Header) { h.BlobGasUsed = nil }), "missing blobGasUsed"},
		{
			"excess blob gas inflated",
			header(func(h *types.Header) { h.ExcessBlobGas = new(big.Int).Mul(wantExcess, big.NewInt(1000)) }),
			"invalid excessBlobGas",
		},
		{
			"excess blob gas zeroed",
			header(func(h *types.Header) { h.ExcessBlobGas = new(big.Int) }),
			"invalid excessBlobGas",
		},
	} {
		err := verifyCamelliaHeaderFields(tt.header, parent)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s: rejected a valid header: %v", tt.name, err)
		case tt.want != "" && err == nil:
			t.Errorf("%s: accepted, want an error containing %q", tt.name, tt.want)
		case tt.want != "" && !strings.Contains(err.Error(), tt.want):
			t.Errorf("%s: want an error containing %q, got %v", tt.name, tt.want, err)
		}
	}
}

// TestVerifyCamelliaExcessBlobGasAtFork covers the fork block itself, whose
// parent predates Camellia and therefore carries neither field. The builder
// treats both as zero (miner.initExcessBlobGas), so the first Camellia block
// must carry exactly zero.
func TestVerifyCamelliaExcessBlobGasAtFork(t *testing.T) {
	preFork := &types.Header{Number: big.NewInt(199)}
	empty := types.EmptyWithdrawalsHash

	first := &types.Header{
		Number:          big.NewInt(200),
		WithdrawalsHash: &empty,
		ExcessBlobGas:   new(big.Int),
		BlobGasUsed:     new(big.Int),
	}
	if err := verifyCamelliaHeaderFields(first, preFork); err != nil {
		t.Fatalf("the fork block was rejected: %v", err)
	}

	first.ExcessBlobGas = big.NewInt(1)
	if err := verifyCamelliaHeaderFields(first, preFork); err == nil {
		t.Fatal("a non-zero excessBlobGas was accepted at the fork block")
	}
}
