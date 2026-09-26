package ethash

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
)

// TestVerifyNoPbftFields: the PoA engine rejects any header carrying the PBFT
// fields, including an explicitly empty seal list, so a node accepts exactly
// the headers the current release accepts (review on #146).
func TestVerifyNoPbftFields(t *testing.T) {
	seal := make([]byte, types.CommitSealLength)
	for _, tt := range []struct {
		name  string
		round uint64
		seals [][]byte
		ok    bool
	}{
		{"plain header", 0, nil, true},
		{"three seals", 0, [][]byte{seal, seal, seal}, false},
		{"round only", 1, nil, false},
		{"empty, non-nil seal list", 0, [][]byte{}, false},
	} {
		h := &types.Header{Number: big.NewInt(118_924_592), BftRound: tt.round, CommitSeals: tt.seals}
		err := verifyNoPbftFields(h)
		if tt.ok != (err == nil) || (!tt.ok && !errors.Is(err, errPbftFields)) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
}
