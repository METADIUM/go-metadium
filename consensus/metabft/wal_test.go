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
	must(t, w.RecordVote(5, 1, MsgRoundChange, digestA))
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
	// A proposer's PRE-PREPARE is recorded like a vote and binds too.
	must(t, w.RecordVote(7, 0, MsgPreprepare, digestA))
	if err := w.RecordVote(7, 0, MsgPreprepare, digestB); !errors.Is(err, ErrConflictingVote) {
		t.Errorf("second PRE-PREPARE in a round: %v", err)
	}
	if err := w.RecordVote(7, 0, 9, digestA); err == nil {
		t.Error("unknown message type recorded")
	}
	if r, ok := w.MaxRound(5); !ok || r != 1 {
		t.Errorf("MaxRound(5) = %d, %v; want 1", r, ok)
	}
	if r, ok := w.MaxRound(7); !ok || r != 0 {
		t.Errorf("MaxRound(7) = %d, %v; want 0 (a round-0 vote counts)", r, ok)
	}
	if _, ok := w.MaxRound(8); ok {
		t.Error("MaxRound reports a height nothing was signed at")
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
