// trs_metadium_test.go -- Metadium fork guard.
//
// The v1.13.14 rebase silently dropped the pool-side TRS (Transaction
// Restriction Service) enforcement while leaving the trsListMap fields in
// place, so nothing failed at compile time and no test caught it. These tests
// pin the restored 0.10.x behaviour: a TRS-subscribed node rejects restricted
// transactions at admission and purges any that slipped in earlier.

package legacypool

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
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

// setTRS updates the pool's TRS view the way a governance update would: under
// pool.mu. That is the lock the pool holds when it reads these fields (see
// trsAndFeePayerSweep, "the caller holds pool.mu"), so assigning them bare
// races with the reorg loop -- which the race detector reports on these tests.
func setTRS(pool *LegacyPool, list map[common.Address]bool, subscribe bool) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.trsListMap = list
	pool.trsSubscribe = subscribe
}

func TestTRSRejectsRestrictedSender(t *testing.T) {
	asPoA(t)

	pool, key := setupPool()
	defer pool.Close()

	from := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, from, big.NewInt(1000000))

	listed := map[common.Address]bool{from: true}

	// Not subscribed: the transaction is accepted even if listed.
	setTRS(pool, listed, false)
	if err := pool.addRemote(transaction(0, 100000, key)); err != nil {
		t.Fatalf("unsubscribed node rejected listed tx: %v", err)
	}

	// Subscribed: the next transaction from the listed sender is rejected.
	setTRS(pool, listed, true)
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
	setTRS(pool, map[common.Address]bool{{}: true}, true)
	if err := pool.addRemote(transaction(0, 100000, key)); !errors.Is(err, txpool.ErrIncludedTRSList) {
		t.Fatalf("want ErrIncludedTRSList, got %v", err)
	}
}

// toTransaction is transaction() with an explicit recipient.
func toTransaction(nonce uint64, to common.Address, key *ecdsa.PrivateKey) *types.Transaction {
	tx, _ := types.SignTx(types.NewTransaction(nonce, to, big.NewInt(100), 100000, big.NewInt(1), nil), types.HomesteadSigner{}, key)
	return tx
}

// TestTRSSweepCascade pins the strict-list cascade contract: pending lists
// are strict, so removing a restricted transaction at a low nonce also expels
// every higher-nonce transaction behind it. Those innocent cascade victims
// must be re-enqueued -- not leaked into a state where they occupy pool.all
// (blocking resubmission) while belonging to no list.
func TestTRSSweepCascade(t *testing.T) {
	asPoA(t)

	pool, key := setupPool()
	defer pool.Close()

	var (
		from       = crypto.PubkeyToAddress(key.PublicKey)
		restricted = common.Address{0xbb}
		clean      = common.Address{0xcc}
	)
	testAddBalance(pool, from, big.NewInt(1000000))

	// nonce 0 pays a soon-to-be-restricted recipient; 1 and 2 are innocent.
	txs := []*types.Transaction{
		toTransaction(0, restricted, key),
		toTransaction(1, clean, key),
		toTransaction(2, clean, key),
	}
	// Sync, because the assertion below reads the pending set. addRemote returns
	// as soon as the transaction is queued and leaves promotion to the reorg
	// loop, so with it this reads whatever the loop happens to have done.
	for i, tx := range txs {
		if err := pool.addRemoteSync(tx); err != nil {
			t.Fatalf("failed to add tx %d: %v", i, err)
		}
	}
	if pending, _ := pool.Stats(); pending != 3 {
		t.Fatalf("want 3 pending before sweep, got %d", pending)
	}

	// The recipient lands on the list; the demote sweep must drop nonce 0 and
	// re-queue (not leak) nonces 1 and 2.
	setTRS(pool, map[common.Address]bool{restricted: true}, true)

	pool.mu.Lock()
	pool.demoteUnexecutables()
	pool.mu.Unlock()

	pending, queued := pool.Stats()
	if pending != 0 || queued != 2 {
		t.Fatalf("want pending=0 queued=2 after sweep, got pending=%d queued=%d", pending, queued)
	}
	if pool.Has(txs[0].Hash()) {
		t.Errorf("restricted tx still tracked by the pool after sweep")
	}
	for i := 1; i <= 2; i++ {
		if !pool.Has(txs[i].Hash()) {
			t.Errorf("cascade victim %d leaked out of the pool entirely", i)
		}
	}
	// The freed nonce must be reusable: a ghost in pool.all would answer
	// ErrAlreadyKnown / nonce-gap here forever.
	if err := pool.addRemote(toTransaction(0, clean, key)); err != nil {
		t.Fatalf("resubmission at the freed nonce failed: %v", err)
	}
}

func TestTRSSweepPurgesPending(t *testing.T) {
	asPoA(t)

	pool, key := setupPool()
	defer pool.Close()

	from := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, from, big.NewInt(1000000))

	// Sync, so that the transaction has actually been promoted before the
	// pending count is read and before the sweep runs. See the note in
	// TestTRSSweepCascade: addRemote hands promotion to the reorg loop,
	// which is what made this test flake (issue #73).
	if err := pool.addRemoteSync(transaction(0, 100000, key)); err != nil {
		t.Fatalf("failed to add tx: %v", err)
	}
	pending, _ := pool.Stats()
	if pending != 1 {
		t.Fatalf("want 1 pending before sweep, got %d", pending)
	}

	// The sender lands on the list after admission (e.g. governance update at
	// the next head); the demotion sweep must purge the pending transaction.
	setTRS(pool, map[common.Address]bool{from: true}, true)

	pool.mu.Lock()
	pool.demoteUnexecutables()
	pool.mu.Unlock()

	pending, queued := pool.Stats()
	if pending != 0 || queued != 0 {
		t.Fatalf("want pool empty after sweep, got pending=%d queued=%d", pending, queued)
	}
}
