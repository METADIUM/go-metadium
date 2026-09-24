package ethapi

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestRPCMarshalHeaderBftFields(t *testing.T) {
	h := &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1)}
	out := RPCMarshalHeader(h)
	if _, ok := out["bftRound"]; ok {
		t.Error("a non-PBFT header gained bftRound")
	}
	if _, ok := out["commitSeals"]; ok {
		t.Error("a non-PBFT header gained commitSeals")
	}

	h.BftRound = 2
	h.CommitSeals = [][]byte{make([]byte, types.CommitSealLength), make([]byte, types.CommitSealLength)}
	out = RPCMarshalHeader(h)
	if got := out["bftRound"]; got != hexutil.Uint64(2) {
		t.Errorf("bftRound = %v, want 2", got)
	}
	if seals, ok := out["commitSeals"].([]hexutil.Bytes); !ok || len(seals) != 2 {
		t.Errorf("commitSeals = %v, want 2 seals", out["commitSeals"])
	}
}
