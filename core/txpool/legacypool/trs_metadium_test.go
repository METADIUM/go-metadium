// trs_metadium_test.go -- Metadium fork guard.
//
// The v1.13.14 rebase silently dropped the pool-side TRS (Transaction
// Restriction Service) enforcement while leaving the trsListMap fields in
// place, so nothing failed at compile time and no test caught it. These tests
// pin the restored 0.10.x behaviour: a TRS-subscribed node rejects restricted
// transactions at admission and purges any that slipped in earlier.

package legacypool

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// asPoA switches the process-wide consensus method to PoA for the duration of
// one test, since the TRS gates sit behind !metaminer.IsPoW() and the default
// is PoW outside a running node. Tests using it must NOT call t.Parallel() --
// serial tests finish (and restore the global) before the parallel batch runs.
func asPoA(t *testing.T) {
	t.Helper()
	old := params.ConsensusMethod
	params.ConsensusMethod = params.ConsensusPoA
	t.Cleanup(func() { params.ConsensusMethod = old })
}

func TestTRSRejectsRestrictedSender(t *testing.T) {
	asPoA(t)

	pool, key := setupPool()
	defer pool.Close()

	from := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, from, big.NewInt(1000000))

	// Not subscribed: the transaction is accepted even if listed.
	pool.trsListMap = map[common.Address]bool{from: true}
	pool.trsSubscribe = false
	if err := pool.addRemote(transaction(0, 100000, key)); err != nil {
		t.Fatalf("unsubscribed node rejected listed tx: %v", err)
	}

	// Subscribed: the next transaction from the listed sender is rejected.
	pool.trsSubscribe = true
	if err := pool.addRemote(transaction(1, 100000, key)); !errors.Is(err, txpool.ErrIncludedTRSList) {
		t.Fatalf("want ErrIncludedTRSList, got %v", err)
	}
}

func TestTRSRejectsRestrictedRecipient(t *testing.T) {
	asPoA(t)

	pool, key := setupPool()
	defer pool.Close()

	from := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, from, big.NewInt(1000000))

	// transaction() sends to the zero address; restrict that recipient.
	pool.trsListMap = map[common.Address]bool{{}: true}
	pool.trsSubscribe = true
	if err := pool.addRemote(transaction(0, 100000, key)); !errors.Is(err, txpool.ErrIncludedTRSList) {
		t.Fatalf("want ErrIncludedTRSList, got %v", err)
	}
}

func TestTRSSweepPurgesPending(t *testing.T) {
	asPoA(t)

	pool, key := setupPool()
	defer pool.Close()

	from := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, from, big.NewInt(1000000))

	if err := pool.addRemote(transaction(0, 100000, key)); err != nil {
		t.Fatalf("failed to add tx: %v", err)
	}
	pending, _ := pool.Stats()
	if pending != 1 {
		t.Fatalf("want 1 pending before sweep, got %d", pending)
	}

	// The sender lands on the list after admission (e.g. governance update at
	// the next head); the demotion sweep must purge the pending transaction.
	pool.trsListMap = map[common.Address]bool{from: true}
	pool.trsSubscribe = true

	pool.mu.Lock()
	pool.demoteUnexecutables()
	pool.mu.Unlock()

	pending, queued := pool.Stats()
	if pending != 0 || queued != 0 {
		t.Fatalf("want pool empty after sweep, got pending=%d queued=%d", pending, queued)
	}
}
