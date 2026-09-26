package metabft

import (
	"bytes"
	"errors"
	"math"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func setOfSize(t *testing.T, n int) *ValidatorSet {
	t.Helper()
	keys := make([][]byte, n)
	for i := range keys {
		keys[i] = bytes.Repeat([]byte{byte(i + 1)}, PubKeyLength)
	}
	set, err := NewValidatorSet(keys)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// TestQuorum pins the design §4.1 table and checks, for every N up to 100,
// that two quorums always share an honest validator (safety) and that the
// honest validators alone can form a quorum (liveness).
func TestQuorum(t *testing.T) {
	want := map[int]int{1: 1, 2: 2, 3: 2, 4: 3, 5: 4, 6: 4, 7: 5, 8: 6, 9: 6, 10: 7, 11: 8, 12: 8, 13: 9}
	for n, q := range want {
		if got := setOfSize(t, n).Quorum(); got != q {
			t.Errorf("N=%d: quorum %d, want %d", n, got, q)
		}
	}
	for n := 1; n <= 100; n++ {
		s := setOfSize(t, n)
		q, f := s.Quorum(), s.F()
		if 2*q-n < f+1 {
			t.Errorf("N=%d: two quorums of %d may share only %d validators, need f+1=%d", n, q, 2*q-n, f+1)
		}
		if n-f < q {
			t.Errorf("N=%d: %d honest validators cannot reach quorum %d", n, n-f, q)
		}
		if q > 1 && 2*(q-1)-n >= f+1 {
			t.Errorf("N=%d: quorum %d is not minimal", n, q)
		}
	}
	// Why not 2f+1 (design §4.1): for N=6 two such quorums can be disjoint.
	if s := setOfSize(t, 6); 2*(2*s.F()+1)-6 >= s.F()+1 {
		t.Error("2f+1 turned out safe for N=6; the design note is wrong")
	}
}

func TestProposerRotation(t *testing.T) {
	s := setOfSize(t, 4)
	for _, tt := range []struct {
		height, round uint64
		want          int
	}{
		{0, 0, 0}, {1, 0, 1}, {3, 0, 3}, {4, 0, 0}, {5, 2, 3}, {17280, 0, 0}, {17280, 1, 1},
		{math.MaxUint64, 0, int(math.MaxUint64 % 4)},
		{math.MaxUint64, math.MaxUint64, int((math.MaxUint64%4 + math.MaxUint64%4) % 4)},
	} {
		if got := s.Proposer(tt.height, tt.round); got != s.At(tt.want) {
			t.Errorf("proposer(%d, %d) = %v, want index %d", tt.height, tt.round, got, tt.want)
		}
	}
	// Each round moves to the next validator, so N consecutive rounds cover everyone.
	seen := map[Validator]bool{}
	for r := uint64(0); r < 4; r++ {
		seen[s.Proposer(100, r)] = true
	}
	if len(seen) != 4 {
		t.Errorf("4 rounds reached %d proposers", len(seen))
	}
}

func TestNewValidatorSetRejects(t *testing.T) {
	if _, err := NewValidatorSet(nil); !errors.Is(err, errEmptyValidatorSet) {
		t.Errorf("empty set: %v", err)
	}
	if _, err := NewValidatorSet([][]byte{make([]byte, 65)}); !errors.Is(err, errBadPubKeyLength) {
		t.Errorf("65-byte key: %v", err)
	}
	k := bytes.Repeat([]byte{7}, PubKeyLength)
	if _, err := NewValidatorSet([][]byte{k, bytes.Repeat([]byte{8}, PubKeyLength), k}); err == nil {
		t.Error("duplicate validator accepted")
	}
}

func TestValidatorIdentity(t *testing.T) {
	key, _ := crypto.GenerateKey()
	s, err := NewValidatorSet([][]byte{pubKeyOf(key)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.At(0).Address(), crypto.PubkeyToAddress(key.PublicKey); got != want {
		t.Errorf("address %v, want %v", got, want)
	}
	if i, ok := s.IndexOf(pubKeyOf(key)); !ok || i != 0 {
		t.Error("IndexOf does not find the validator")
	}
	if _, ok := s.IndexOf(pubKeyOf(key)[:63]); ok {
		t.Error("IndexOf accepted a short key")
	}
	if !s.Equal(s) || s.Equal(setOfSize(t, 1)) {
		t.Error("Equal is wrong")
	}
}
