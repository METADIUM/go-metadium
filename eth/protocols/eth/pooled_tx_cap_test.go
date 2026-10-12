// Copyright 2026 The go-ethereum Authors
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

package eth

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// TestGetPooledTransactionsLookupCap checks that pooled transaction retrievals
// stop after maxPooledTxServe lookups, even if the requested hashes are unknown
// and the response stays empty. Unknown hashes cost a pool lookup each but add
// nothing to the response, so without the cap a request stuffed with junk
// hashes is bounded only by the message size.
func TestGetPooledTransactionsLookupCap(t *testing.T) {
	backend := newTestBackend(0)
	defer backend.close()

	signer := types.LatestSigner(backend.chain.Config())
	tx := types.MustSignNewTx(testKey, signer, &types.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(params.InitialBaseFee),
		Gas:      params.TxGas,
		To:       &testAddr,
		Value:    big.NewInt(10_000),
	})
	if errs := backend.txpool.Add([]*types.Transaction{tx}, true, true); errs[0] != nil {
		t.Fatal(errs[0])
	}
	// Pad the request with unknown hashes so the known one lands at the given index.
	request := func(index int) GetPooledTransactionsRequest {
		query := make(GetPooledTransactionsRequest, index+1)
		for i := 0; i < index; i++ {
			query[i] = common.BigToHash(big.NewInt(int64(i + 1)))
		}
		query[index] = tx.Hash()
		return query
	}
	if hashes, _ := answerGetPooledTransactions(backend, request(maxPooledTxServe-1)); len(hashes) != 1 {
		t.Errorf("transaction within lookup cap not served: have %d, want 1", len(hashes))
	}
	if hashes, _ := answerGetPooledTransactions(backend, request(maxPooledTxServe)); len(hashes) != 0 {
		t.Errorf("transaction beyond lookup cap served: have %d, want 0", len(hashes))
	}
}
