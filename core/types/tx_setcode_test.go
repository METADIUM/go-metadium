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

package types

import (
	"bytes"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

func TestParseDelegation(t *testing.T) {
	addr := common.Address{0x42}
	for _, tt := range []struct {
		val  []byte
		want *common.Address
	}{
		{
			// simple correct delegation
			val:  append(DelegationPrefix, addr.Bytes()...),
			want: &addr,
		},
		{
			// wrong address size
			val: append(DelegationPrefix, addr.Bytes()[0:19]...),
		},
		{
			// short address
			val: append(DelegationPrefix, 0x42),
		},
		{
			// long address
			val: append(append(DelegationPrefix, addr.Bytes()...), 0x42),
		},
		{
			// bad prefix
			val: append([]byte{0xef, 0x01, 0x01}, addr.Bytes()...),
		},
		{
			// bad prefix
			val: append([]byte{0xef, 0x00, 0x00}, addr.Bytes()...),
		},
		{
			// empty
			val: []byte{},
		},
	} {
		got, ok := ParseDelegation(tt.val)
		if ok && tt.want == nil {
			t.Fatalf("expected fail, got %s", got.Hex())
		}
		if !ok && tt.want != nil {
			t.Fatalf("failed to parse, want %s", tt.want.Hex())
		}
		if ok && got != *tt.want {
			t.Fatalf("parsed address %s, want %s", got.Hex(), tt.want.Hex())
		}
	}
}

// TestSetCodeAuthorization: a signed authorization recovers its authority,
// a tampered one does not recover it, and the JSON form round-trips.
func TestSetCodeAuthorization(t *testing.T) {
	key, _ := crypto.GenerateKey()
	authority := crypto.PubkeyToAddress(key.PublicKey)
	auth, err := SignSetCode(key, SetCodeAuthorization{
		ChainID: *uint256.NewInt(12),
		Address: common.Address{0xaa},
		Nonce:   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := auth.Authority(); err != nil || got != authority {
		t.Fatalf("authority = %v, %v; want %v", got, err, authority)
	}
	tampered := auth
	tampered.Nonce++
	if got, err := tampered.Authority(); err == nil && got == authority {
		t.Fatal("a changed nonce still recovers the authority")
	}
	js, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	var back SetCodeAuthorization
	if err := json.Unmarshal(js, &back); err != nil {
		t.Fatal(err)
	}
	if back != auth {
		t.Fatalf("json round trip: %+v != %+v", back, auth)
	}
}

// TestSetCodeTxCoding: a signed set-code transaction survives the binary and
// JSON encodings with its authorization list, keeps its hash, recovers its
// sender under the Doraji signer, and is refused by the London signer.
func TestSetCodeTxCoding(t *testing.T) {
	var (
		key, _     = crypto.GenerateKey()
		authKey, _ = crypto.GenerateKey()
		sender     = crypto.PubkeyToAddress(key.PublicKey)
		chainID    = big.NewInt(12)
		signer     = NewDorajiSigner(chainID)
		recipient  = common.HexToAddress("095e7baea6a6c7c4c2dfeb977efac326af552d87")
	)
	auth, err := SignSetCode(authKey, SetCodeAuthorization{ChainID: *uint256.NewInt(12), Address: common.Address{0xcc}, Nonce: 0})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := SignNewTx(key, signer, &SetCodeTx{
		ChainID:    uint256.NewInt(12),
		Nonce:      3,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(10),
		Gas:        100_000,
		To:         recipient,
		Value:      uint256.NewInt(5),
		Data:       []byte("abcdef"),
		AccessList: AccessList{{Address: common.Address{1}, StorageKeys: []common.Hash{{0}}}},
		AuthList:   []SetCodeAuthorization{auth},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Type() != SetCodeTxType {
		t.Fatalf("type %d", tx.Type())
	}
	if from, err := Sender(signer, tx); err != nil || from != sender {
		t.Fatalf("sender = %v, %v; want %v", from, err, sender)
	}
	if _, err := Sender(NewLondonSigner(chainID), tx); err == nil {
		t.Fatal("the London signer accepted a set-code transaction")
	}
	if got := tx.SetCodeAuthorities(); len(got) != 1 || got[0] != crypto.PubkeyToAddress(authKey.PublicKey) {
		t.Fatalf("authorities = %v", got)
	}

	bin, err := tx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if bin[0] != SetCodeTxType {
		t.Fatalf("binary type byte %x", bin[0])
	}
	var fromBin Transaction
	if err := fromBin.UnmarshalBinary(bin); err != nil {
		t.Fatal(err)
	}
	assertSetCodeEqual(t, tx, &fromBin)

	js, err := tx.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(js, []byte(`"authorizationList"`)) || !bytes.Contains(js, []byte(`"type":"0x4"`)) {
		t.Fatalf("json lacks the type or the authorization list: %s", js)
	}
	var fromJSON Transaction
	if err := fromJSON.UnmarshalJSON(js); err != nil {
		t.Fatal(err)
	}
	assertSetCodeEqual(t, tx, &fromJSON)
}

func assertSetCodeEqual(t *testing.T, want, got *Transaction) {
	t.Helper()
	if got.Hash() != want.Hash() {
		t.Fatalf("hash %v, want %v", got.Hash(), want.Hash())
	}
	w, g := want.inner.(*SetCodeTx), got.inner.(*SetCodeTx)
	if len(g.AuthList) != 1 || g.AuthList[0] != w.AuthList[0] {
		t.Fatalf("auth list %+v, want %+v", g.AuthList, w.AuthList)
	}
	if g.To != w.To || g.Nonce != w.Nonce || g.Gas != w.Gas || !bytes.Equal(g.Data, w.Data) || len(g.AccessList) != 1 {
		t.Fatalf("fields differ: %+v vs %+v", g, w)
	}
}
