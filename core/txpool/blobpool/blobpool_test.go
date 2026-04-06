// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package blobpool

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
)

// mockChain implements blobpool.BlockChain for testing.
type mockChain struct {
	block   *types.Block
	statedb *state.StateDB
	config  *params.ChainConfig
}

func (m *mockChain) CurrentBlock() *types.Block                  { return m.block }
func (m *mockChain) GetBlock(common.Hash, uint64) *types.Block   { return m.block }
func (m *mockChain) StateAt(common.Hash) (*state.StateDB, error) { return m.statedb, nil }
func (m *mockChain) Config() *params.ChainConfig                 { return m.config }
func (m *mockChain) SubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) event.Subscription {
	return event.NewSubscription(func(quit <-chan struct{}) error {
		<-quit
		return nil
	})
}

// newTestPool returns a BlobPool backed by an in-memory state with a funded account.
func newTestPool(t *testing.T, balance *big.Int) (*BlobPool, *ecdsa.PrivateKey, func()) {
	t.Helper()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)

	db := rawdb.NewMemoryDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db), nil)
	statedb.SetBalance(addr, balance)
	statedb.Commit(false)

	chainCfg := params.AllEthashProtocolChanges

	// ExcessBlobGas=0 → blobBaseFee = 1 wei.
	header := &types.Header{
		Number:        big.NewInt(1),
		GasLimit:      10_000_000,
		BaseFee:       big.NewInt(1e9),
		ExcessBlobGas: new(big.Int),
	}
	block := types.NewBlockWithHeader(header)

	chain := &mockChain{
		block:   block,
		statedb: statedb,
		config:  chainCfg,
	}

	pool := New(chainCfg, chain)
	pool.Init(big.NewInt(0), header)

	return pool, key, func() { pool.Close() }
}

// makeBlobTx builds and signs a minimal Type 3 blob transaction.
func makeBlobTx(t *testing.T, key *ecdsa.PrivateKey, chainID *big.Int, nonce uint64, blobHash common.Hash) *types.Transaction {
	t.Helper()

	toAddr := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	toU256 := func(b *big.Int) *uint256.Int { v, _ := uint256.FromBig(b); return v }

	inner := &types.BlobTx{
		ChainID:          toU256(chainID),
		Nonce:            nonce,
		GasTipCap:        toU256(big.NewInt(1e9)),
		GasFeeCap:        toU256(big.NewInt(2e9)),
		Gas:              21000,
		To:               &toAddr,
		Value:            toU256(big.NewInt(0)),
		MaxFeePerBlobGas: toU256(big.NewInt(1e9)),
		BlobHashes:       []common.Hash{blobHash},
	}
	signer := types.NewLondonSigner(chainID)
	tx, err := types.SignNewTx(key, signer, inner)
	if err != nil {
		t.Fatalf("SignNewTx: %v", err)
	}
	return tx
}

// makeValidSidecar returns a BlobTxSidecar with a real KZG commitment+proof,
// and its versioned hash. Uses gokzg4844 directly to avoid unexported kzg4844.getContext().
func makeValidSidecar(t *testing.T) (*types.BlobTxSidecar, [32]byte) {
	t.Helper()

	ctx, err := gokzg4844.NewContext4096Secure()
	if err != nil {
		t.Skip("KZG context unavailable:", err)
	}

	var blob gokzg4844.Blob
	blob[0] = 0x01 // non-zero to exercise real field-element encoding

	commitment, err := ctx.BlobToKZGCommitment(&blob, 0)
	if err != nil {
		t.Fatalf("BlobToKZGCommitment: %v", err)
	}
	proof, err := ctx.ComputeBlobKZGProof(&blob, commitment, 0)
	if err != nil {
		t.Fatalf("ComputeBlobKZGProof: %v", err)
	}

	versionedHash := kzg4844.KZGToVersionedHash(commitment[:])

	sidecar := &types.BlobTxSidecar{
		Blobs:       [][]byte{blob[:]},
		Commitments: [][]byte{commitment[:]},
		Proofs:      [][]byte{proof[:]},
	}
	return sidecar, versionedHash
}

// TestAddWithSidecar_ValidKZG verifies that a correctly constructed blob sidecar
// passes KZG validation and is stored so GetSidecar returns it.
func TestAddWithSidecar_ValidKZG(t *testing.T) {
	balance := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(100))
	pool, key, cleanup := newTestPool(t, balance)
	defer cleanup()

	sidecar, versionedHash := makeValidSidecar(t)

	chainID := params.AllEthashProtocolChanges.ChainID
	tx := makeBlobTx(t, key, chainID, 0, common.Hash(versionedHash))

	if err := pool.AddWithSidecar(tx, sidecar); err != nil {
		t.Fatalf("AddWithSidecar failed: %v", err)
	}

	stored := pool.GetSidecar(tx.Hash())
	if stored == nil {
		t.Fatal("GetSidecar returned nil after AddWithSidecar")
	}
	if len(stored.Blobs) != 1 {
		t.Errorf("expected 1 blob, got %d", len(stored.Blobs))
	}
	t.Logf("PASS: sidecar stored and retrieved (blobs=%d)", len(stored.Blobs))
}

// TestAddWithSidecar_InvalidKZG verifies that a sidecar with a tampered commitment
// is rejected and the tx is not added to the pool.
func TestAddWithSidecar_InvalidKZG(t *testing.T) {
	balance := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(100))
	pool, key, cleanup := newTestPool(t, balance)
	defer cleanup()

	sidecar, versionedHash := makeValidSidecar(t)

	// Tamper: replace commitment with all-zero bytes.
	sidecar.Commitments = [][]byte{make([]byte, 48)}

	chainID := params.AllEthashProtocolChanges.ChainID
	tx := makeBlobTx(t, key, chainID, 0, common.Hash(versionedHash))

	err := pool.AddWithSidecar(tx, sidecar)
	if err == nil {
		t.Fatal("expected error for invalid KZG sidecar, got nil")
	}
	t.Logf("PASS: invalid sidecar rejected: %v", err)

	if pool.Has(tx.Hash()) {
		t.Error("tx should not be in pool after KZG validation failure")
	}
}

// TestAddWithSidecar_NilSidecar verifies that nil sidecar skips KZG validation
// and the tx is added without a stored sidecar.
func TestAddWithSidecar_NilSidecar(t *testing.T) {
	balance := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(100))
	pool, key, cleanup := newTestPool(t, balance)
	defer cleanup()

	// KZG validation is skipped for nil sidecar; any dummy hash works.
	dummyHash := common.Hash{0x01}
	chainID := params.AllEthashProtocolChanges.ChainID
	tx := makeBlobTx(t, key, chainID, 0, dummyHash)

	if err := pool.AddWithSidecar(tx, nil); err != nil {
		t.Fatalf("AddWithSidecar(nil sidecar) failed: %v", err)
	}
	if sc := pool.GetSidecar(tx.Hash()); sc != nil {
		t.Errorf("expected nil sidecar, got non-nil")
	}
	t.Log("PASS: nil sidecar — tx added, GetSidecar returns nil")
}

// TestGetSidecar_UnknownHash verifies that GetSidecar returns nil for an unknown hash.
func TestGetSidecar_UnknownHash(t *testing.T) {
	pool, _, cleanup := newTestPool(t, big.NewInt(1e18))
	defer cleanup()

	unknown := common.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if sc := pool.GetSidecar(unknown); sc != nil {
		t.Errorf("expected nil for unknown hash, got non-nil")
	}
	t.Log("PASS: GetSidecar returns nil for unknown hash")
}
