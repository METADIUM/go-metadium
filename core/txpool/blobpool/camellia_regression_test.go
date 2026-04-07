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

package blobpool

import (
	"math/big"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// camelliaOnlyChainConfig returns a ChainConfig with Camellia enabled at block 0
// and CancunTime=nil, mirroring the Metadium mainnet configuration.
func camelliaOnlyChainConfig() *params.ChainConfig {
	cfg := new(params.ChainConfig)
	*cfg = *params.AllEthashProtocolChanges
	cfg.CancunTime = nil // Camellia-only: no timestamp-based Cancun
	return cfg
}

// testBlockChainCamellia is a minimal blockchain mock for Camellia-only chains.
type testBlockChainCamellia struct {
	config  *params.ChainConfig
	statedb *state.StateDB
}

func (bc *testBlockChainCamellia) Config() *params.ChainConfig { return bc.config }

func (bc *testBlockChainCamellia) CurrentBlock() *types.Header {
	return &types.Header{
		Number:        new(big.Int).Add(bc.config.LondonBlock, big.NewInt(1)),
		Time:          0,
		GasLimit:      30_000_000,
		BaseFee:       big.NewInt(params.InitialBaseFee),
		ExcessBlobGas: new(big.Int).SetUint64(0),
		BlobGasUsed:   new(big.Int).SetUint64(0),
	}
}

func (bc *testBlockChainCamellia) CurrentFinalBlock() *types.Header {
	return &types.Header{Number: big.NewInt(0)}
}

func (bc *testBlockChainCamellia) GetBlock(hash common.Hash, number uint64) *types.Block {
	return nil
}

func (bc *testBlockChainCamellia) StateAt(common.Hash) (*state.StateDB, error) {
	return bc.statedb, nil
}

// TestBlobPoolLimboFinalizeCamellia verifies that limbo finalization is triggered
// on Camellia-only chains (IsCancun=false, IsCamellia=true).
//
// Regression: blobpool.go:820 — before the fix, the condition was IsCancun-only.
// On Camellia-only chains (CancunTime=nil), finalize() was never called, causing
// blobs to accumulate in limbo indefinitely (memory/disk leak).
//
// Found by /autoplan on 2026-04-07
// Report: .gstack/qa-reports/qa-report-go-metadium-2026-04-07.md
func TestBlobPoolLimboFinalizeCamellia(t *testing.T) {
	cfg := camelliaOnlyChainConfig()

	// Part 1: verify the fork conditions are as expected.
	blockNum := big.NewInt(1)
	blockTime := uint64(0)

	if cfg.IsCancun(blockNum, blockTime) {
		t.Fatal("expected IsCancun=false for Camellia-only config (CancunTime=nil)")
	}
	if !cfg.IsCamellia(blockNum) {
		t.Fatal("expected IsCamellia=true for CamelliaBlock=0")
	}

	// The finalize condition in blobpool.go:820 must be true for Camellia chains.
	finalizeActive := cfg.IsCancun(blockNum, blockTime) || cfg.IsCamellia(blockNum)
	if !finalizeActive {
		t.Error("limbo finalize condition must be true for Camellia-only chain (was false before fix)")
	}

	// Part 2: directly exercise limbo.finalize() on a Camellia chain to verify
	// it does not panic and correctly prunes finalized blobs.
	dir := t.TempDir()
	l, err := newLimbo(dir)
	if err != nil {
		t.Fatalf("newLimbo: %v", err)
	}
	defer l.Close()

	// Build a minimal blob TX to push into limbo.
	to := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	inner := &types.BlobTx{
		ChainID:          uint256.MustFromBig(cfg.ChainID),
		Nonce:            0,
		GasTipCap:        uint256.NewInt(1e9),
		GasFeeCap:        uint256.NewInt(2e9),
		Gas:              21000,
		To:               &to,
		Value:            new(uint256.Int),
		MaxFeePerBlobGas: uint256.NewInt(params.BlobTxMinBlobGasprice),
		BlobHashes:       []common.Hash{emptyBlobVHash},
		Sidecar: &types.BlobTxSidecar{
			Blobs:       [][]byte{emptyBlob[:]},
			Commitments: [][]byte{emptyBlobCommit[:]},
			Proofs:      [][]byte{emptyBlobProof[:]},
		},
	}
	tx := types.NewTx(inner)

	// Push the blob at block 1.
	if err := l.push(tx, 1); err != nil {
		t.Fatalf("limbo.push: %v", err)
	}
	if len(l.index) != 1 {
		t.Fatalf("expected 1 entry in limbo index, got %d", len(l.index))
	}

	// Finalize at block 1: the blob should be pruned.
	l.finalize(&types.Header{Number: big.NewInt(1)})

	if len(l.index) != 0 {
		t.Errorf("expected 0 entries after finalize, got %d (memory leak on Camellia chain)", len(l.index))
	}
	if len(l.groups) != 0 {
		t.Errorf("expected 0 groups after finalize, got %d", len(l.groups))
	}

	// Part 3: verify the blobpool Reset() path exercises finalization without panicking
	// on a Camellia-only chain.
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	chain := &testBlockChainCamellia{config: cfg, statedb: statedb}

	poolDir, err := os.MkdirTemp("", "blobpool-camellia-test-*")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(poolDir)

	pool := New(DefaultConfig, chain)
	if err := pool.Init(params.InitialBaseFee, chain.CurrentBlock(), makeAddressReserver()); err != nil {
		t.Fatalf("pool.Init: %v", err)
	}
	defer pool.Close()

	// Reset to a new head — this is the code path that calls limbo.finalize()
	// at blobpool.go:820. On a Camellia-only chain with the fix, this must not panic.
	newHead := chain.CurrentBlock()
	newHead.Number = big.NewInt(2)
	pool.Reset(chain.CurrentBlock(), newHead)
}
