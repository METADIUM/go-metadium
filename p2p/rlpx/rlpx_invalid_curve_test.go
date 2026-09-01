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

package rlpx

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
)

// TestHandshakeECIESInvalidCurve is upstream go-ethereum's proof for
// CVE-2026-26315 (46bee92f9), ported as a regression test.
//
// The handshake decrypts unauthenticated network input. An ephemeral public key
// that is not on the curve must be refused outright; if it reaches ECDH and
// fails later at MAC verification, the difference in error and in work done is
// an oracle.
//
// This tree already satisfies that: Decrypt decodes the point with
// elliptic.Unmarshal, which validates it here (see crypto/secp256k1/curve.go on
// the unmarshaler interface), so the packet is rejected with ErrInvalidPublicKey
// with or without the guard added to GenerateShared. The test therefore pins the
// handshake's behaviour rather than proving that guard — the direct proof for it
// is TestGenerateSharedRejectsOffCurve in crypto/ecies.
func TestHandshakeECIESInvalidCurve(t *testing.T) {
	initKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	respKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	init := handshakeState{
		initiator: true,
		remote:    ecies.ImportECDSAPublic(&respKey.PublicKey),
	}
	authMsg, err := init.makeAuthMsg(initKey)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := init.sealEIP8(authMsg)
	if err != nil {
		t.Fatal(err)
	}

	// The untampered packet has to decrypt, or the test below would pass for
	// the wrong reason.
	var recv handshakeState
	if _, err := recv.readMsg(new(authMsgV4), respKey, bytes.NewReader(packet)); err != nil {
		t.Fatalf("valid handshake packet failed to decrypt: %v", err)
	}

	// Replace the ephemeral point with (0, 0), which is a well-formed
	// uncompressed encoding of a point that is not on secp256k1.
	tampered := append([]byte(nil), packet...)
	if len(tampered) < 2+65 {
		t.Fatalf("unexpected packet length %d", len(tampered))
	}
	tampered[2] = 0x04
	for i := 1; i < 65; i++ {
		tampered[2+i] = 0x00
	}

	var recv2 handshakeState
	_, err = recv2.readMsg(new(authMsgV4), respKey, bytes.NewReader(tampered))
	if err == nil {
		t.Fatal("handshake accepted a point that is not on the curve")
	}
	if !errors.Is(err, ecies.ErrInvalidPublicKey) {
		t.Fatalf("want ErrInvalidPublicKey, got %v", err)
	}
}
