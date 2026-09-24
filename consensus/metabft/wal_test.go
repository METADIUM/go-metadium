package metabft

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

var (
	digestA = common.HexToHash("0xaa")
	digestB = common.HexToHash("0xbb")
)

func openTestWAL(t *testing.T, path string, create bool) (*WAL, *OpenReport) {
	t.Helper()
	w, rep, err := OpenWAL(path, create)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, rep
}

func TestWALMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metabft", "wal")
	if _, _, err := OpenWAL(path, false); !errors.Is(err, ErrWALMissing) {
		t.Fatalf("missing WAL: %v", err)
	}
	_, rep := openTestWAL(t, path, true)
	if !rep.Created {
		t.Error("report does not say the WAL was created")
	}
}

// TestWALSurvivesRestart is the property the WAL exists for (design §6.1):
// after a restart the node still refuses to contradict what it sent.
func TestWALSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	w, _ := openTestWAL(t, path, true)
	if err := w.RecordVote(10, 0, MsgPrepare, digestA); err != nil {
		t.Fatal(err)
	}
	if err := w.RecordLock(10, 0, digestA, [][]byte{{1}, {2}}, []byte{9, 9}); err != nil {
		t.Fatal(err)
	}
	if err := w.RecordVote(10, 0, MsgCommit, digestA); err != nil {
		t.Fatal(err)
	}
	w.Close() // "crash"

	w2, rep := openTestWAL(t, path, false)
	if rep.Records != 3 || rep.TornTail {
		t.Fatalf("reopen report %+v", rep)
	}
	if err := w2.RecordVote(10, 0, MsgPrepare, digestB); !errors.Is(err, ErrConflictingVote) {
		t.Errorf("conflicting PREPARE after restart: %v", err)
	}
	if err := w2.RecordVote(10, 0, MsgCommit, digestB); !errors.Is(err, ErrConflictingVote) {
		t.Errorf("conflicting COMMIT after restart: %v", err)
	}
	if err := w2.RecordVote(10, 0, MsgCommit, digestA); err != nil {
		t.Errorf("resending the same COMMIT: %v", err)
	}
	lock, ok := w2.Lock(10)
	if !ok || lock.Digest != digestA || lock.Round != 0 || len(lock.Certificate) != 2 || !bytes.Equal(lock.Block, []byte{9, 9}) {
		t.Errorf("lock after restart: %+v, %v", lock, ok)
	}
	if d, ok := w2.Vote(10, 0, MsgPrepare); !ok || d != digestA {
		t.Errorf("recorded PREPARE: %x, %v", d, ok)
	}
}

func TestWALVoteRules(t *testing.T) {
	w, _ := openTestWAL(t, filepath.Join(t.TempDir(), "wal"), true)

	// Moving to a higher round with a different digest is allowed.
	must(t, w.RecordVote(5, 0, MsgPrepare, digestA))
	must(t, w.RecordRoundChange(&Message{Type: MsgRoundChange, Height: 5, Round: 1, ChainID: testChainID, Digest: digestA}))
	must(t, w.RecordVote(5, 1, MsgPrepare, digestB))
	// Going back is not.
	if err := w.RecordVote(5, 0, MsgCommit, digestA); !errors.Is(err, ErrPastRound) {
		t.Errorf("vote in an earlier round: %v", err)
	}
	// Within a round, COMMIT must match PREPARE.
	if err := w.RecordVote(5, 1, MsgCommit, digestA); !errors.Is(err, ErrConflictingVote) {
		t.Errorf("COMMIT of another digest than PREPAREd: %v", err)
	}
	must(t, w.RecordVote(5, 1, MsgCommit, digestB))
	// Other heights are independent.
	must(t, w.RecordVote(6, 0, MsgPrepare, digestA))
	// PRE-PREPARE is not a vote.
	if err := w.RecordVote(7, 0, MsgPreprepare, digestA); err == nil {
		t.Error("PRE-PREPARE recorded as a vote")
	}
	// Resending writes nothing.
	size := fileSize(t, w.path)
	must(t, w.RecordVote(6, 0, MsgPrepare, digestA))
	if fileSize(t, w.path) != size {
		t.Error("a resend was written again")
	}
}

// TestWALTornTail simulates a crash in the middle of an append: the frame
// never finished, so the vote was never sent and may be discarded.
func TestWALTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	w, _ := openTestWAL(t, path, true)
	must(t, w.RecordVote(10, 0, MsgPrepare, digestA))
	must(t, w.RecordVote(10, 0, MsgCommit, digestA))
	w.Close()
	good := fileSize(t, path)

	frame, err := encodeFrame(&Record{Kind: RecordVote, Height: 11, Type: MsgPrepare, Digest: digestB})
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{1, frameHeaderLen - 1, frameHeaderLen, frameHeaderLen + 1, len(frame) - 1} {
		writeFile(t, path, append(readFile(t, path)[:good], frame[:cut]...))

		w2, rep, err := OpenWAL(path, false)
		if err != nil {
			t.Fatalf("cut at %d: %v", cut, err)
		}
		if !rep.TornTail || rep.Records != 2 || rep.TailBytes != int64(cut) {
			t.Errorf("cut at %d: report %+v", cut, rep)
		}
		if fileSize(t, path) != good {
			t.Errorf("cut at %d: torn tail not truncated", cut)
		}
		// The discarded vote was never sent, so height 11 is free.
		must(t, w2.RecordVote(11, 0, MsgPrepare, digestA))
		// And the earlier votes still hold.
		if err := w2.RecordVote(10, 0, MsgPrepare, digestB); !errors.Is(err, ErrConflictingVote) {
			t.Errorf("cut at %d: earlier vote lost: %v", cut, err)
		}
		w2.Close()
		writeFile(t, path, readFile(t, path)[:good])
	}
}

// TestWALCorrupt: damage inside the file is not a crash artefact; the node
// must distrust the whole WAL and start as an observer.
func TestWALCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	w, _ := openTestWAL(t, path, true)
	must(t, w.RecordVote(10, 0, MsgPrepare, digestA))
	must(t, w.RecordVote(10, 0, MsgCommit, digestA))
	w.Close()
	clean := readFile(t, path)

	for name, damage := range map[string]func(b []byte){
		"payload byte in first frame": func(b []byte) { b[frameHeaderLen+2] ^= 0xff },
		"checksum of last frame":      func(b []byte) { b[len(b)/2+5] ^= 0xff },
		"zero length":                 func(b []byte) { copy(b[:4], []byte{0, 0, 0, 0}) },
		"huge length":                 func(b []byte) { copy(b[:4], []byte{0xff, 0xff, 0xff, 0xff}) },
	} {
		b := append([]byte(nil), clean...)
		damage(b)
		writeFile(t, path, b)
		if _, _, err := OpenWAL(path, false); !errors.Is(err, ErrWALCorrupt) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestWALPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	w, _ := openTestWAL(t, path, true)
	for h := uint64(1); h <= 5; h++ {
		must(t, w.RecordVote(h, 0, MsgPrepare, digestA))
		must(t, w.RecordVote(h, 0, MsgCommit, digestA))
	}
	must(t, w.RecordLock(5, 0, digestA, nil, []byte{1}))
	must(t, w.Prune(4))

	// Pruned heights are forgotten; kept ones still bind.
	if _, ok := w.Vote(3, 0, MsgPrepare); ok {
		t.Error("height 3 survived pruning")
	}
	if err := w.RecordVote(5, 0, MsgCommit, digestB); !errors.Is(err, ErrConflictingVote) {
		t.Errorf("height 5 lost by pruning: %v", err)
	}
	must(t, w.RecordVote(6, 0, MsgPrepare, digestA)) // appends after a prune
	w.Close()

	// A leftover temp file from a crashed prune is ignored.
	writeFile(t, path+".tmp", []byte("junk"))
	w2, rep := openTestWAL(t, path, false)
	if rep.Records != 4 { // height 5: 2 votes + lock, height 6: 1 vote
		t.Errorf("after prune and reopen: %d records, want 4", rep.Records)
	}
	if _, ok := w2.Lock(5); !ok {
		t.Error("lock at height 5 lost")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestWALConcurrent records from many goroutines at once; every vote must
// land exactly once and the file must reopen cleanly.
func TestWALConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	w, _ := openTestWAL(t, path, true)
	const heights = 32
	errs := make(chan error, heights*2)
	for h := uint64(1); h <= heights; h++ {
		go func(h uint64) {
			errs <- w.RecordVote(h, 0, MsgPrepare, digestA)
		}(h)
		go func(h uint64) {
			errs <- w.RecordVote(h, 0, MsgPrepare, digestA) // resend race
		}(h)
	}
	for i := 0; i < heights*2; i++ {
		must(t, <-errs)
	}
	w.Close()
	_, rep := openTestWAL(t, path, false)
	if rep.Records != heights || rep.TornTail {
		t.Errorf("reopened with %d records (torn %v), want %d", rep.Records, rep.TornTail, heights)
	}
}

// TestWALRoundChangeContent: a second ROUND-CHANGE for one round with the
// same prepared digest but different content is refused, because the
// evidence rule would count the pair as an equivocation (review on #147).
func TestWALRoundChangeContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	w, _ := openTestWAL(t, path, true)
	rc := &Message{Type: MsgRoundChange, Height: 9, Round: 2, ChainID: testChainID, Digest: digestA, Payload: []byte{1}}
	must(t, w.RecordRoundChange(rc))
	must(t, w.RecordRoundChange(rc)) // resend of the same message

	bigger := *rc
	bigger.Payload = []byte{1, 2}
	if err := w.RecordRoundChange(&bigger); !errors.Is(err, ErrConflictingVote) {
		t.Errorf("same digest, different content: %v", err)
	}
	if err := w.RecordVote(9, 3, MsgRoundChange, digestA); err == nil {
		t.Error("RecordVote accepted a ROUND-CHANGE without its content")
	}
	w.Close()
	w2, _ := openTestWAL(t, path, false)
	if err := w2.RecordRoundChange(&bigger); !errors.Is(err, ErrConflictingVote) {
		t.Errorf("after restart: %v", err)
	}
	if err := w2.RecordRoundChange(rc); err != nil {
		t.Errorf("resend after restart: %v", err)
	}
}

// TestWALLockConflict: a lock contradicting this node's own PREPARE or COMMIT
// in that round is a caller bug and is refused (review on #147).
func TestWALLockConflict(t *testing.T) {
	w, _ := openTestWAL(t, filepath.Join(t.TempDir(), "wal"), true)
	must(t, w.RecordVote(4, 1, MsgPrepare, digestA))
	if err := w.RecordLock(4, 1, digestB, nil, nil); !errors.Is(err, ErrLockConflict) {
		t.Errorf("lock on another digest than PREPAREd: %v", err)
	}
	must(t, w.RecordLock(4, 1, digestA, nil, nil))
	must(t, w.RecordLock(4, 2, digestB, nil, nil)) // a later round may lock another block
	if _, ok := w.Lock(4); !ok {
		t.Error("lock missing")
	}
}
