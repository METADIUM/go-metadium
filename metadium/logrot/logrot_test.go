package logrot

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunPassesOversizedRecords is the regression test for the failure that
// took a mainnet node down: the previous implementation scanned with a
// bufio.Scanner, whose 64KB token limit made a longer record look like
// end-of-input. The reader exited, and the node writing into the pipe died of
// SIGPIPE without a message. A record of any length must pass through.
func TestRunPassesOversizedRecords(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "log")

	long := strings.Repeat("x", 256*1024)
	in := fmt.Sprintf("before\n%s\nafter\n", long)

	if err := Run(strings.NewReader(in), name, 1024*1024, 5, io.Discard); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != in {
		t.Errorf("log does not match input: got %d bytes, want %d", len(got), len(in))
	}
}

// TestRunDrainsWhenTheFileIsUnusable checks the property the node depends on:
// however badly the file behaves, Run keeps reading to the end of the input and
// reports success, so the writer on the other side of the pipe is never killed.
func TestRunDrainsWhenTheFileIsUnusable(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "no-such-dir", "log")

	in := strings.Repeat("a log line\n", 1000)
	r := strings.NewReader(in)
	var diag bytes.Buffer

	if err := Run(r, name, 1024, 5, &diag); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Len() != 0 {
		t.Errorf("input not drained: %d bytes left", r.Len())
	}
	if diag.Len() == 0 {
		t.Error("the failure was not reported")
	}
}

// TestRunRotates covers the ordinary path: the log is rotated once it passes
// size, and no more than count generations are kept.
func TestRunRotates(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "log")

	line := strings.Repeat("y", 99) + "\n"
	if err := Run(strings.NewReader(strings.Repeat(line, 100)), name, 1000, 3, io.Discard); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Stat(name + ".1"); err != nil {
		t.Errorf("log.1 missing: %v", err)
	}
	if _, err := os.Stat(name + ".4"); err == nil {
		t.Error("log.4 exists, but only 3 generations should be kept")
	}
}

// TestRunSurvivesRotationFailure makes the rename fail permanently: log.1 is a
// non-empty directory, which os.Remove cannot clear and os.Rename cannot
// replace. Rotation is expendable; the reader is not.
func TestRunSurvivesRotationFailure(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "log")

	if err := os.Mkdir(name+".1", 0700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(name+".1", "keep"), []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	line := strings.Repeat("z", 99) + "\n"
	in := strings.Repeat(line, 100)
	var diag bytes.Buffer

	if err := Run(strings.NewReader(in), name, 1000, 1, &diag); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(diag.String(), "cannot rotate") {
		t.Errorf("rotation failure not reported: %q", diag.String())
	}

	// Everything still landed in the log, just without rotation.
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != len(in) {
		t.Errorf("log has %d bytes, want %d", len(got), len(in))
	}
}

func TestParseSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		bad  bool
	}{
		{in: "10", want: 10},
		{in: "10k", want: 10 * 1024},
		{in: "10M", want: 10 * 1024 * 1024},
		{in: " 2 g ", want: 2 * 1024 * 1024 * 1024},
		{in: "", bad: true},
		{in: "  ", bad: true},
		{in: "k", bad: true},
		{in: "ten", bad: true},
	} {
		got, err := ParseSize(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("ParseSize(%q) = %d, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSize(%q): %v", tc.in, err)
		} else if got != tc.want {
			t.Errorf("ParseSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestRunRejectsInvalidParameters(t *testing.T) {
	name := filepath.Join(t.TempDir(), "log")
	if err := Run(strings.NewReader(""), name, 0, 5, io.Discard); err == nil {
		t.Error("size 0 accepted")
	}
	if err := Run(strings.NewReader(""), name, 1024, 0, io.Discard); err == nil {
		t.Error("count 0 accepted")
	}
}
