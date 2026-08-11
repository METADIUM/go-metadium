package eth

import (
	"testing"
	"time"
)

// TestAcceptMetaReplay covers the meta/69 replay-protection accept/reject rules
// (M12): monotonic nonce and bounded timestamp skew.
func TestAcceptMetaReplay(t *testing.T) {
	p := &Peer{}
	now := uint64(time.Now().Unix())

	if !p.acceptMetaReplay(1, now) {
		t.Fatal("fresh nonce 1 should be accepted")
	}
	if p.acceptMetaReplay(1, now) {
		t.Fatal("replayed nonce 1 should be rejected")
	}
	if p.acceptMetaReplay(0, now) {
		t.Fatal("lower nonce 0 should be rejected")
	}
	if !p.acceptMetaReplay(2, now) {
		t.Fatal("increasing nonce 2 should be accepted")
	}
	if p.acceptMetaReplay(3, now-metaReplaySkewSeconds-10) {
		t.Fatal("stale timestamp should be rejected")
	}
	if p.acceptMetaReplay(4, now+metaReplaySkewSeconds+10) {
		t.Fatal("future timestamp should be rejected")
	}
	// rejected-on-timestamp frames must not advance the accepted nonce
	if !p.acceptMetaReplay(3, now) {
		t.Fatal("nonce 3 with fresh timestamp should be accepted after stale/future rejects")
	}
	// within-skew timestamp is fine
	if !p.acceptMetaReplay(4, now-metaReplaySkewSeconds+5) {
		t.Fatal("in-skew nonce 4 should be accepted")
	}
}

// TestNextMetaNonce verifies outbound nonces are monotonic and start at 1.
func TestNextMetaNonce(t *testing.T) {
	p := &Peer{}
	if n := p.nextMetaNonce(); n != 1 {
		t.Fatalf("first nonce = %d, want 1", n)
	}
	if n := p.nextMetaNonce(); n != 2 {
		t.Fatalf("second nonce = %d, want 2", n)
	}
}
