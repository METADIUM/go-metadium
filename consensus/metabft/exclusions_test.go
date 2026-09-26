package metabft

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
)

// TestExclusions: a transaction left out by the validator floor is tried
// again after ExcludeFor, a time rather than a height count (P5-27).
func TestExclusions(t *testing.T) {
	e := NewEngine(ethash.NewFaker(), nil)
	h := common.Hash{0xe1}
	if e.IsExcluded(h) || len(e.ExcludedTxs()) != 0 {
		t.Fatal("excluded before anything was recorded")
	}
	until := e.ExcludeTx(h, common.Address{0xa1}, 7, "leaves 3 governance nodes")
	if d := time.Until(until); d <= ExcludeFor-time.Second || d > ExcludeFor {
		t.Errorf("retry in %v, want %v", d, ExcludeFor)
	}
	if !e.IsExcluded(h) {
		t.Fatal("recorded transaction not excluded")
	}
	list := e.ExcludedTxs()
	if len(list) != 1 || list[0].Sender != (common.Address{0xa1}) || list[0].Nonce != 7 || list[0].Reason == "" {
		t.Fatalf("listed %+v", list)
	}
	// Once its time is up it is tried again, and no longer listed.
	x := e.excluded[h]
	x.Until = time.Now().Add(-time.Millisecond)
	e.excluded[h] = x
	if e.IsExcluded(h) || len(e.ExcludedTxs()) != 0 {
		t.Error("still excluded after its retry time")
	}
}
