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

package ethapi

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// TestSetCodeRPCTransaction: a set-code transaction serializes to the RPC
// form with its authorization list and type 0x4, and that form decodes back
// to the same transaction.
func TestSetCodeRPCTransaction(t *testing.T) {
	config := *params.AllEthashProtocolChanges
	config.DorajiBlock = big.NewInt(0)
	key, _ := crypto.GenerateKey()
	authKey, _ := crypto.GenerateKey()
	auth, err := types.SignSetCode(authKey, types.SetCodeAuthorization{ChainID: *uint256.NewInt(config.ChainID.Uint64()), Address: common.Address{0xcc}, Nonce: 3})
	if err != nil {
		t.Fatal(err)
	}
	to := common.Address{0xde, 0xad}
	chainID, _ := uint256.FromBig(config.ChainID)
	tx, err := types.SignNewTx(key, types.LatestSigner(&config), &types.SetCodeTx{
		ChainID: chainID, Nonce: 5, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(10), Gas: 7,
		To: to, Value: uint256.NewInt(8), Data: []byte{0, 1, 2}, AuthList: []types.SetCodeAuthorization{auth},
	})
	if err != nil {
		t.Fatal(err)
	}
	rpcTx := newRPCTransaction(tx, common.Hash{}, 0, 0, 0, nil, &config)
	data, err := json.Marshal(rpcTx)
	if err != nil {
		t.Fatal(err)
	}
	js := string(data)
	for _, want := range []string{`"type":"0x4"`, `"authorizationList":[{`, `"yParity":"0x`, `"address":"0x` + strings.TrimPrefix(common.Address{0xcc}.Hex(), "0x")} {
		if !strings.Contains(strings.ToLower(js), strings.ToLower(want)) {
			t.Fatalf("rpc json lacks %s: %s", want, js)
		}
	}
	var back types.Transaction
	if err := back.UnmarshalJSON(data); err != nil {
		t.Fatalf("decoding the rpc form: %v", err)
	}
	if back.Hash() != tx.Hash() {
		t.Fatalf("hash %v after the rpc round trip, want %v", back.Hash(), tx.Hash())
	}
	if got := back.SetCodeAuthorizations(); len(got) != 1 || got[0] != auth {
		t.Fatalf("authorization list after the round trip: %+v", got)
	}
}

// TestSetCodeTransactionArgs: arguments with an authorization list build a
// set-code transaction and a message carrying the list; a list with a fee
// payer, with blobs, with no recipient or with no entry is refused.
func TestSetCodeTransactionArgs(t *testing.T) {
	authKey, _ := crypto.GenerateKey()
	auth, _ := types.SignSetCode(authKey, types.SetCodeAuthorization{ChainID: *uint256.NewInt(1337), Address: common.Address{0xcc}, Nonce: 0})
	var (
		to    = common.Address{0xde, 0xad}
		nonce = hexutil.Uint64(1)
		gas   = hexutil.Uint64(100_000)
		args  = TransactionArgs{
			To: &to, Nonce: &nonce, Gas: &gas, ChainID: (*hexutil.Big)(big.NewInt(1337)),
			MaxFeePerGas: (*hexutil.Big)(big.NewInt(10)), MaxPriorityFeePerGas: (*hexutil.Big)(big.NewInt(1)),
			Value: (*hexutil.Big)(big.NewInt(0)), AuthorizationList: []types.SetCodeAuthorization{auth},
		}
	)
	tx := args.toTransaction()
	if tx.Type() != types.SetCodeTxType {
		t.Fatalf("built type %d, want set-code", tx.Type())
	}
	if got := tx.SetCodeAuthorizations(); len(got) != 1 || got[0] != auth {
		t.Fatalf("built authorization list: %+v", got)
	}
	msg, err := args.ToMessage(0, big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.SetCodeAuthorizations) != 1 || msg.SetCodeAuthorizations[0] != auth {
		t.Fatalf("message authorization list: %+v", msg.SetCodeAuthorizations)
	}

	// The combinations setDefaults refuses, checked on its guards directly.
	for name, bad := range map[string]TransactionArgs{
		"fee payer": {To: &to, AuthorizationList: []types.SetCodeAuthorization{auth}, FeePayer: &common.Address{1}},
		"blobs":     {To: &to, AuthorizationList: []types.SetCodeAuthorization{auth}, BlobHashes: []common.Hash{{1}}},
		"no to":     {AuthorizationList: []types.SetCodeAuthorization{auth}},
		"empty":     {To: &to, AuthorizationList: []types.SetCodeAuthorization{}},
	} {
		if err := bad.checkSetCode(); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if err := args.checkSetCode(); err != nil {
		t.Fatalf("valid arguments refused: %v", err)
	}
}
