package miner

import (
	"math/big"
	"testing"
)

func TestIsBft(t *testing.T) {
	t.Cleanup(func() { SetBftBlock(nil) })
	if IsBft(big.NewInt(1 << 40)) {
		t.Error("a PBFT height with no switch block")
	}
	SetBftBlock(big.NewInt(120))
	for n, want := range map[int64]bool{0: false, 119: false, 120: true, 1 << 40: true} {
		if IsBft(big.NewInt(n)) != want {
			t.Errorf("height %d: want %v", n, want)
		}
	}
	if IsBft(nil) {
		t.Error("nil height")
	}
}
