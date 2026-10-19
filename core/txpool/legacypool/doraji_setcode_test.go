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

package legacypool

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

func dorajiPoolConfig() *params.ChainConfig {
	c := *params.TestChainConfig
	c.CamelliaBlock = big.NewInt(0)
	c.DorajiBlock = big.NewInt(0)
	return &c
}

// setCodeTx returns a signed set-code transaction from key with the given
// authorizations, to the given recipient.
func setCodeTx(nonce uint64, key *ecdsa.PrivateKey, to common.Address, auths []types.SetCodeAuthorization) *types.Transaction {
	chainID, _ := uint256.FromBig(params.TestChainConfig.ChainID)
	return types.MustSignNewTx(key, types.NewDorajiSigner(params.TestChainConfig.ChainID), &types.SetCodeTx{
		ChainID: chainID, Nonce: nonce, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(100),
		Gas: 200_000, To: to, Value: uint256.NewInt(0), AuthList: auths,
	})
}

func signAuth(t *testing.T, key *ecdsa.PrivateKey, nonce uint64) types.SetCodeAuthorization {
	t.Helper()
	auth, err := types.SignSetCode(key, types.SetCodeAuthorization{ChainID: *uint256.NewInt(params.TestChainConfig.ChainID.Uint64()), Address: common.Address{0xcc}, Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

// TestDorajiSetCodeAdmission: the pool admits set-code transactions from
// Doraji and refuses them before it; a set-code transaction with no
// authorization is refused.
func TestDorajiSetCodeAdmission(t *testing.T) {
	t.Parallel()

	pre, key := setupPoolWithConfig(params.TestChainConfig)
	defer pre.Close()
	authKey, _ := crypto.GenerateKey()
	testAddBalance(pre, crypto.PubkeyToAddress(key.PublicKey), big.NewInt(1e18))
	if err := pre.addRemoteSync(setCodeTx(0, key, common.Address{1}, []types.SetCodeAuthorization{signAuth(t, authKey, 0)})); !errors.Is(err, core.ErrTxTypeNotSupported) {
		t.Fatalf("before Doraji: err = %v, want ErrTxTypeNotSupported", err)
	}

	pool, key := setupPoolWithConfig(dorajiPoolConfig())
	defer pool.Close()
	testAddBalance(pool, crypto.PubkeyToAddress(key.PublicKey), big.NewInt(1e18))
	if err := pool.addRemoteSync(setCodeTx(0, key, common.Address{1}, []types.SetCodeAuthorization{})); !errors.Is(err, core.ErrEmptyAuthList) {
		t.Fatalf("empty authorization list: err = %v, want ErrEmptyAuthList", err)
	}
	tx := setCodeTx(0, key, common.Address{1}, []types.SetCodeAuthorization{signAuth(t, authKey, 0)})
	if err := pool.addRemoteSync(tx); err != nil {
		t.Fatalf("set-code tx: %v", err)
	}
	pending, _ := pool.Stats()
	if pending != 1 {
		t.Fatalf("pending %d, want 1", pending)
	}
	authority := crypto.PubkeyToAddress(authKey.PublicKey)
	if !pool.HasPendingAuth(authority) {
		t.Fatal("authority not tracked while its authorization is pooled")
	}
	// Dropping the transaction drops the tracking.
	pool.removeTx(tx.Hash(), true, true)
	if pool.HasPendingAuth(authority) {
		t.Fatal("authority still tracked after its authorization left the pool")
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatal(err)
	}
}

// TestDorajiDelegationLimits: an account with a pooled authorization, or
// with a delegation in the state, gets one in-flight transaction — a second
// is refused, a gapped nonce is refused, a replacement is allowed; and an
// authority with two pooled transactions cannot be delegated.
func TestDorajiDelegationLimits(t *testing.T) {
	t.Parallel()
	pool, key := setupPoolWithConfig(dorajiPoolConfig())
	defer pool.Close()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, sender, big.NewInt(1e18))

	// Pending authorization: the authority is limited to one in-flight tx.
	authKey, _ := crypto.GenerateKey()
	authority := crypto.PubkeyToAddress(authKey.PublicKey)
	testAddBalance(pool, authority, big.NewInt(1e18))
	if err := pool.addRemoteSync(setCodeTx(0, key, common.Address{1}, []types.SetCodeAuthorization{signAuth(t, authKey, 0)})); err != nil {
		t.Fatalf("set-code tx: %v", err)
	}
	if err := pool.addRemoteSync(pricedTransaction(0, 100_000, big.NewInt(100), authKey)); err != nil {
		t.Fatalf("first tx from the authority: %v", err)
	}
	if err := pool.addRemoteSync(pricedTransaction(1, 100_000, big.NewInt(100), authKey)); !errors.Is(err, txpool.ErrInflightTxLimitReached) {
		t.Fatalf("second tx from the authority: err = %v, want ErrInflightTxLimitReached", err)
	}
	// A replacement of the one in-flight tx is fine.
	if err := pool.addRemoteSync(pricedTransaction(0, 100_000, big.NewInt(200), authKey)); err != nil {
		t.Fatalf("replacement from the authority: %v", err)
	}

	// Delegation in the state: the same limit, from the code alone.
	delegatedKey, _ := crypto.GenerateKey()
	delegated := crypto.PubkeyToAddress(delegatedKey.PublicKey)
	testAddBalance(pool, delegated, big.NewInt(1e18))
	pool.currentState.SetCode(delegated, types.AddressToDelegation(common.Address{0xcc}))
	if err := pool.addRemoteSync(pricedTransaction(1, 100_000, big.NewInt(100), delegatedKey)); !errors.Is(err, ErrOutOfOrderTxFromDelegated) {
		t.Fatalf("gapped nonce from a delegated account: err = %v, want ErrOutOfOrderTxFromDelegated", err)
	}
	if err := pool.addRemoteSync(pricedTransaction(0, 100_000, big.NewInt(100), delegatedKey)); err != nil {
		t.Fatalf("first tx from a delegated account: %v", err)
	}
	if err := pool.addRemoteSync(pricedTransaction(1, 100_000, big.NewInt(100), delegatedKey)); !errors.Is(err, txpool.ErrInflightTxLimitReached) {
		t.Fatalf("second tx from a delegated account: err = %v, want ErrInflightTxLimitReached", err)
	}

	// An authority with two pooled transactions is reserved.
	busyKey, _ := crypto.GenerateKey()
	busy := crypto.PubkeyToAddress(busyKey.PublicKey)
	testAddBalance(pool, busy, big.NewInt(1e18))
	for n := uint64(0); n < 2; n++ {
		if err := pool.addRemoteSync(pricedTransaction(n, 100_000, big.NewInt(100), busyKey)); err != nil {
			t.Fatalf("tx %d from the busy account: %v", n, err)
		}
	}
	if err := pool.addRemoteSync(setCodeTx(1, key, common.Address{1}, []types.SetCodeAuthorization{signAuth(t, busyKey, 2)})); !errors.Is(err, ErrAuthorityReserved) {
		t.Fatalf("delegating a busy authority: err = %v, want ErrAuthorityReserved", err)
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatal(err)
	}
}
