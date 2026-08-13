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
