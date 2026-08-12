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

// asPoA / asPoW flip the global consensus method for one test, restoring the
// previous value afterwards. params.ConsensusMethod is PoW outside a running
// node. Tests using these must NOT call t.Parallel() -- serial tests finish
// (and restore the global) before the parallel batch runs.
func asPoA(t *testing.T) {
	t.Helper()
	old := params.ConsensusMethod
	params.ConsensusMethod = params.ConsensusPoA
	t.Cleanup(func() { params.ConsensusMethod = old })
}

func asPoW(t *testing.T) {
	t.Helper()
	old := params.ConsensusMethod
	params.ConsensusMethod = params.ConsensusPoW
	t.Cleanup(func() { params.ConsensusMethod = old })
}

func header(n uint64) *types.Header {
	return &types.Header{Number: new(big.Int).SetUint64(n)}
}

// TestLimboFinalPoASurrogate pins the PoA finality surrogate: when chain-level
// finality is unavailable, limbo eviction must key on head-limboFinalityDepth
// instead of silently never running (issue #76).
func TestLimboFinalPoASurrogate(t *testing.T) {
	asPoA(t)

	// Deep enough head: surrogate is head-depth.
	if got := limboFinal(nil, header(1000)); got == nil {
		t.Fatal("want surrogate final header under PoA, got nil")
	} else if want := uint64(1000 - limboFinalityDepth); got.Number.Uint64() != want {
		t.Fatalf("surrogate final number: got %d, want %d", got.Number.Uint64(), want)
	}
	// Boundary: the first head that can evict group 0.
	if got := limboFinal(nil, header(limboFinalityDepth)); got.Number.Uint64() != 0 {
		t.Fatalf("boundary head %d: got %d, want 0", limboFinalityDepth, got.Number.Uint64())
	}
	// Young chain: clamp to zero rather than underflow or return nil.
	if got := limboFinal(nil, header(limboFinalityDepth-1)); got == nil {
		t.Fatal("want zero-height surrogate on young chain, got nil")
	} else if got.Number.Uint64() != 0 {
		t.Fatalf("young-chain surrogate: got %d, want 0", got.Number.Uint64())
	}
	// A resolved chain-level boundary (however it appeared) always wins.
	if got := limboFinal(header(7), header(1000)); got.Number.Uint64() != 7 {
		t.Fatalf("explicit final must pass through, got %d", got.Number.Uint64())
	}
	// A nil head must not panic and must not fabricate a boundary.
	if got := limboFinal(nil, nil); got != nil {
		t.Fatalf("nil head must pass nil through, got %v", got.Number)
	}
}

// TestLimboFinalNonPoAPassthrough pins that non-PoA consensus keeps the
// upstream behavior bit-for-bit: nil stays nil (with upstream's error log
// downstream), and a real final header passes through. asPoW forces the
// non-PoA condition so this guard always runs.
func TestLimboFinalNonPoAPassthrough(t *testing.T) {
	asPoW(t)

	if got := limboFinal(nil, header(1000)); got != nil {
		t.Fatalf("non-PoA nil final must stay nil, got %v", got.Number)
	}
	if got := limboFinal(header(7), header(1000)); got.Number.Uint64() != 7 {
		t.Fatalf("non-PoA explicit final must pass through, got %d", got.Number.Uint64())
	}
}

// testBlockChainNilFinal is testBlockChainCamellia with chain-level finality
// unavailable, as on a governance-less private net or during the start-up /
// governance-read-failure windows (#76).
type testBlockChainNilFinal struct {
	testBlockChainCamellia
	head uint64
}

func (bc *testBlockChainNilFinal) CurrentFinalBlock() *types.Header { return nil }

func (bc *testBlockChainNilFinal) CurrentBlock() *types.Header {
	h := bc.testBlockChainCamellia.CurrentBlock()
	h.Number = new(big.Int).SetUint64(bc.head)
	return h
}

// TestLimboSurrogateEndToEnd exercises the real Reset call site with a
// nil-finality chain under PoA: a limbo entry deeper than the surrogate
// boundary must be evicted. This is the guard that fails if the call-site
// wiring regresses (e.g. swapped limboFinal arguments, which would compile).
func TestLimboSurrogateEndToEnd(t *testing.T) {
	asPoA(t)

	cfg := camelliaOnlyChainConfig()
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	chain := &testBlockChainNilFinal{
		testBlockChainCamellia: testBlockChainCamellia{config: cfg, statedb: statedb},
		head:                   200,
	}
	poolDir, err := os.MkdirTemp("", "blobpool-surrogate-test-*")
	if err != nil {
		t.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(poolDir)

	pool := New(DefaultConfig, chain)
	if err := pool.Init(params.InitialBaseFee, chain.CurrentBlock(), makeAddressReserver()); err != nil {
		t.Fatalf("pool.Init: %v", err)
	}
	defer pool.Close()

	// Park a sidecar in limbo at a block deeper than head-limboFinalityDepth.
	to := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	tx := types.NewTx(&types.BlobTx{
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
	})
	if err := pool.limbo.push(tx, 100); err != nil { // 100 <= 200-64
		t.Fatalf("limbo.push: %v", err)
	}
	if len(pool.limbo.index) != 1 {
		t.Fatalf("expected 1 limbo entry before reset, got %d", len(pool.limbo.index))
	}

	// Advance the head through the real Reset path; the surrogate boundary
	// (head-64 = 137) must evict the block-100 sidecar despite nil finality.
	old := chain.CurrentBlock()
	chain.head = 201
	pool.Reset(old, chain.CurrentBlock())

	if len(pool.limbo.index) != 0 {
		t.Fatalf("expected limbo drained via surrogate finality, still %d entries", len(pool.limbo.index))
	}
	if len(pool.limbo.groups) != 0 {
		t.Fatalf("expected 0 limbo groups after reset, got %d", len(pool.limbo.groups))
	}
}
