// trs_metadium_test.go -- Metadium fork guard.
//
// dropRestricted evicts stored blob transactions whose sender or recipient is
// on the TRS restriction list, using the SetGasTip eviction shape: blob
// nonces must stay gapless, so the first restricted transaction in an account
// takes everything after it down too. This test seeds a store, initializes
// the pool from it, and pins the three account shapes: tail-drop on a
// restricted recipient, whole-account drop on a restricted sender, and an
// untouched bystander.

package blobpool

import (
	"crypto/ecdsa"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/rlp"
	billy "github.com/holiman/billy"
	"github.com/holiman/uint256"
)

// makeTxTo is makeTx with an explicit recipient.
func makeTxTo(nonce uint64, to common.Address, key *ecdsa.PrivateKey) *types.Transaction {
	blobtx := makeUnsignedTx(nonce, 1, 1000, 100)
	blobtx.To = &to
	return types.MustSignNewTx(key, types.LatestSigner(testChainConfig), blobtx)
}

func TestBlobPoolDropRestricted(t *testing.T) {
	storage, _ := os.MkdirTemp("", "blobpool-trs-")
	defer os.RemoveAll(storage)

	os.MkdirAll(filepath.Join(storage, pendingTransactionStore), 0700)
	store, _ := billy.Open(billy.Options{Path: filepath.Join(storage, pendingTransactionStore)}, newSlotter(), nil)

	var (
		key1, _ = crypto.GenerateKey()
		key2, _ = crypto.GenerateKey()
		key3, _ = crypto.GenerateKey()

		addr1 = crypto.PubkeyToAddress(key1.PublicKey)
		addr2 = crypto.PubkeyToAddress(key2.PublicKey)
		addr3 = crypto.PubkeyToAddress(key3.PublicKey)

		cleanTo      = common.Address{0xcc}
		restrictedTo = common.Address{0xbb}
	)
	// addr1: clean tx at nonce 0, restricted recipient at nonce 1 -> tail drop.
	// addr2: restricted sender -> whole account drop.
	// addr3: untouched bystander.
	txs := []*types.Transaction{
		makeTxTo(0, cleanTo, key1),
		makeTxTo(1, restrictedTo, key1),
		makeTxTo(0, cleanTo, key2),
		makeTxTo(0, cleanTo, key3),
	}
	for _, tx := range txs {
		blob, _ := rlp.EncodeToBytes(tx)
		store.Put(blob)
	}
	store.Close()

	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewDatabase(memorydb.New())), nil)
	for _, addr := range []common.Address{addr1, addr2, addr3} {
		statedb.AddBalance(addr, uint256.NewInt(1_000_000_000))
	}
	statedb.Commit(0, true)

	chain := &testBlockChain{
		config:  testChainConfig,
		basefee: uint256.NewInt(1050),
		blobfee: uint256.NewInt(105),
		statedb: statedb,
	}
	pool := New(Config{Datadir: storage}, chain)
	if err := pool.Init(1, chain.CurrentBlock(), makeAddressReserver()); err != nil {
		t.Fatalf("failed to create blob pool: %v", err)
	}
	defer pool.Close()

	if have := len(pool.index); have != 3 {
		t.Fatalf("want 3 accounts before purge, have %d", have)
	}

	pool.lock.Lock()
	pool.trsListMap = map[common.Address]bool{restrictedTo: true, addr2: true}
	pool.trsSubscribe = true
	pool.dropRestricted()
	pool.lock.Unlock()

	// addr1 keeps nonce 0, loses the restricted-recipient tail.
	if have := len(pool.index[addr1]); have != 1 {
		t.Errorf("addr1: want 1 tx after purge, have %d", have)
	}
	if pool.Has(txs[1].Hash()) {
		t.Errorf("addr1 restricted tx still tracked after purge")
	}
	if !pool.Has(txs[0].Hash()) {
		t.Errorf("addr1 clean tx lost by purge")
	}
	// addr2 (restricted sender) is gone entirely.
	if _, ok := pool.index[addr2]; ok {
		t.Errorf("addr2 (restricted sender) still indexed after purge")
	}
	if pool.Has(txs[2].Hash()) {
		t.Errorf("addr2 tx still tracked after purge")
	}
	// addr3 untouched.
	if !pool.Has(txs[3].Hash()) {
		t.Errorf("addr3 bystander tx lost by purge")
	}
	// Heap/index/store bookkeeping stays consistent.
	verifyPoolInternals(t, pool)

	// The freed account must be claimable again (reservation released).
	if err := pool.reserve(addr2, true); err != nil {
		t.Errorf("addr2 reservation not released by whole-account purge: %v", err)
	}
	pool.reserve(addr2, false)
}
