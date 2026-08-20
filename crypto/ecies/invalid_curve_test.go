package ecies

import (
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// TestGenerateSharedRejectsOffCurve pins the CVE-2026-26315 guard: a point that
// is not on the curve must be refused before the ECDH multiplication, and with
// ErrInvalidPublicKey rather than whatever the multiplication happens to
// produce. (0, 0) is a well-formed encoding of such a point.
func TestGenerateSharedRejectsOffCurve(t *testing.T) {
	prv, err := GenerateKey(rand.Reader, crypto.S256(), nil)
	if err != nil {
		t.Fatalf("key generation failed: %v", err)
	}
	offCurve := &PublicKey{
		X:      new(big.Int),
		Y:      new(big.Int),
		Curve:  prv.PublicKey.Curve,
		Params: prv.PublicKey.Params,
	}
	if prv.PublicKey.Curve.IsOnCurve(offCurve.X, offCurve.Y) {
		t.Fatal("the test point is on the curve, so it proves nothing")
	}
	sk, err := prv.GenerateShared(offCurve, 16, 16)
	t.Logf("GenerateShared returned err=%v", err)
	if err != ErrInvalidPublicKey {
		t.Fatalf("want ErrInvalidPublicKey, got %v", err)
	}
	if sk != nil {
		t.Fatalf("a shared key was derived from an off-curve point")
	}
}

// TestGenerateSharedRejectsUnreducedCoordinates pins the range half of the same
// guard, which the two build modes would otherwise treat differently: the cgo
// curve's IsOnCurve rejects a coordinate at or above P, while btcec converts to
// Jacobian coordinates first and so tests it as if it had been reduced. Adding
// P to a valid coordinate produces exactly that input.
func TestGenerateSharedRejectsUnreducedCoordinates(t *testing.T) {
	prv, err := GenerateKey(rand.Reader, crypto.S256(), nil)
	if err != nil {
		t.Fatalf("key generation failed: %v", err)
	}
	p := prv.PublicKey.Curve.Params().P

	for _, tc := range []struct {
		name string
		x, y *big.Int
	}{
		{"x above P", new(big.Int).Add(prv.PublicKey.X, p), prv.PublicKey.Y},
		{"y above P", prv.PublicKey.X, new(big.Int).Add(prv.PublicKey.Y, p)},
		{"x negative", new(big.Int).Neg(prv.PublicKey.X), prv.PublicKey.Y},
	} {
		pub := &PublicKey{X: tc.x, Y: tc.y, Curve: prv.PublicKey.Curve, Params: prv.PublicKey.Params}
		sk, err := prv.GenerateShared(pub, 16, 16)
		if err != ErrInvalidPublicKey {
			t.Errorf("%s: want ErrInvalidPublicKey, got %v", tc.name, err)
		}
		if sk != nil {
			t.Errorf("%s: a shared key was derived", tc.name)
		}
	}
}
