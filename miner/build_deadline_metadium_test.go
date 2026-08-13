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

package miner

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// TestCommitTransactionsHonoursBuildDeadline pins the per-transaction deadline
// guard. The deadline used to be consulted only between whole Pending()
// batches, so a batch could run past the block's slot and
// --miner.blockminbuildtxs had no effect at all (issue #65).
//
// The guard has two conditions and the cases below separate them: the slot has
// to have elapsed, and the block has to already carry BlockMinBuildTxs
// transactions. Stopping on time alone would produce empty blocks under load.
func TestCommitTransactionsHonoursBuildDeadline(t *testing.T) {
	oldMin := params.BlockMinBuildTxs
	params.BlockMinBuildTxs = 2
	defer func() { params.BlockMinBuildTxs = oldMin }()

	// The pending set has to be larger than the minimum, or reaching the
	// minimum and running out of transactions would be indistinguishable.
	const pending = 6

	commit := func(t *testing.T, till *time.Time) int {
		t.Helper()

		w, b := newTestWorker(t, ethashChainConfig, ethash.NewFaker(), rawdb.NewMemoryDatabase(), 0)
		defer w.close()

		// newTestWorker already seeded nonce 0 from this account (pendingTxs), so
		// start after it and count it in the expectations below.
		txs := make([]*types.Transaction, 0, pending)
		for i := len(pendingTxs); i < len(pendingTxs)+pending; i++ {
			tx, _ := types.SignTx(types.NewTransaction(uint64(i), common.Address{0x01},
				big.NewInt(1000), params.TxGas, big.NewInt(params.InitialBaseFee), nil),
				types.HomesteadSigner{}, testBankKey)
			txs = append(txs, tx)
		}
		if errs := b.txPool.Add(txs, true, true); errs[0] != nil {
			t.Fatalf("adding the test transactions: %v", errs[0])
		}

		env, err := w.prepareWork(&generateParams{timestamp: uint64(time.Now().Unix()), coinbase: testBankAddress})
		if err != nil {
			t.Fatalf("preparing the environment: %v", err)
		}
		defer env.discard()
		env.till = till

		filter := txpool.PendingFilter{BaseFee: nil, OnlyPlainTxs: true}
		plain := w.pool.Pending(filter)
		if len(plain) == 0 {
			t.Fatal("no pending transactions to commit")
		}
		plainTxs := newTransactionsByPriceAndNonce(env.signer, plain, env.header.BaseFee)
		blobTxs := newTransactionsByPriceAndNonce(env.signer, nil, env.header.BaseFee)
		if err := w.commitTransactions(env, plainTxs, blobTxs, nil); err != nil {
			t.Fatalf("committing: %v", err)
		}
		return env.tcount
	}

	t.Run("deadline passed", func(t *testing.T) {
		past := time.Now().Add(-time.Second)
		if got := commit(t, &past); got != int(params.BlockMinBuildTxs) {
			t.Errorf("filled %d transactions past the deadline, want the minimum %d",
				got, params.BlockMinBuildTxs)
		}
	})

	t.Run("deadline ahead", func(t *testing.T) {
		future := time.Now().Add(time.Minute)
		if got := commit(t, &future); got <= int(params.BlockMinBuildTxs) {
			t.Errorf("filled only %d transactions with time to spare, want more than %d",
				got, params.BlockMinBuildTxs)
		}
	})

	t.Run("no deadline set", func(t *testing.T) {
		if got := commit(t, nil); got <= int(params.BlockMinBuildTxs) {
			t.Errorf("filled only %d transactions with no deadline, want more than %d",
				got, params.BlockMinBuildTxs)
		}
	})
}
