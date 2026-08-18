// Package logrot writes a stream to a size-rotated log file.
//
// It replaces github.com/charlanxcc/logrot, which ended its process on every
// error path. That made log rotation part of the node's availability: gmet.sh
// runs the node as `gmet 2>&1 | logrot ...`, so when the reader exited the node
// took SIGPIPE on fd 1/2 and — because Go handles SIGPIPE on those two
// descriptors with the default disposition — died without printing anything.
//
// The rule here is therefore: keep draining the input until it ends, whatever
// happens to the file. A write or rename failure degrades logging, reports
// itself, and is retried; it never stops the reader and so never takes the
// writer down with it.
//
// The repo already vendors lumberjack for --log.rotate, but it cannot be used
// here: it has no drain invariant (a write error propagates to the caller,
// which on this path is the node's own log writer), it truncates rather than
// retries on failure, and its backup naming (timestamped) breaks operators'
// shippers that follow the numbered `.1`…`.N` scheme this replaces.
package logrot

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// readChunk is the read granularity. Input is copied through in chunks rather
// than scanned line by line, so a log record of any length passes through
// intact — the previous implementation's bufio.Scanner reported a record over
// 64KB as end-of-input, which is indistinguishable from a closed pipe.
const readChunk = 64 * 1024

// retryInterval is how long the writer waits before reopening the log file
// after an error. It bounds the damage of a permanent failure (a full disk, a
// deleted directory) to one report per interval instead of one per record.
const retryInterval = 30 * time.Second

// ParseSize parses a positive byte count with an optional k/m/g suffix. The
// result is an int64 whatever the platform: "2g" is a valid rotation size on a
// 386 build, where int arithmetic would wrap it negative.
func ParseSize(size string) (int64, error) {
	var m int64 = 1
	size = strings.TrimSpace(size)
	if size == "" {
		return 0, fmt.Errorf("empty size")
	}

	switch size[len(size)-1:] {
	case "k", "K":
		m = 1024
		size = strings.TrimSpace(size[:len(size)-1])
	case "m", "M":
		m = 1024 * 1024
		size = strings.TrimSpace(size[:len(size)-1])
	case "g", "G":
		m = 1024 * 1024 * 1024
		size = strings.TrimSpace(size[:len(size)-1])
	}

	i, err := strconv.ParseInt(size, 10, 64)
	if err != nil {
		return 0, err
	}
	if i <= 0 || i > math.MaxInt64/m {
		return 0, fmt.Errorf("size out of range: %q", size)
	}
	return i * m, nil
}

// rotate renames filename to filename.1, shifting the existing generations up
// and dropping filename.count. It is a no-op while the file is below size.
func rotate(filename string, size int64, count int) error {
	fi, err := os.Stat(filename)
	if err != nil {
		// Nothing to rotate yet, or the file is unreadable; the writer will
		// report the problem when it next opens it.
		return nil
	}
	if fi.Size() < size {
		return nil
	}

	// Find the highest existing generation walking up from 1, so the work
	// scales with the files that exist rather than with count, which is
	// configuration and can be absurd. Generations above a gap are stale
	// (a previous shift did not reach them) and age out as later shifts
	// rename over them.
	top := 0
	for i := 1; i <= count; i++ {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", filename, i)); err != nil {
			break
		}
		top = i
	}
	if top == count {
		os.Remove(fmt.Sprintf("%s.%d", filename, count))
		top--
	}
	for i := top; i >= 0; i-- {
		src := filename
		if i != 0 {
			src = fmt.Sprintf("%s.%d", filename, i)
		}
		if err := os.Rename(src, fmt.Sprintf("%s.%d", filename, i+1)); err != nil {
			return err
		}
	}
	return nil
}

// writer appends to the log file and rotates it, keeping every failure to
// itself.
type writer struct {
	filename string
	size     int64
	count    int
	diag     io.Writer

	out      *os.File
	off      int64 // bytes in the current file
	rotateAt int64 // offset at which to attempt the next rotation
	broken   bool  // the file is unusable; output is being dropped
	retry    time.Time
}

func (w *writer) reportf(format string, args ...interface{}) {
	if w.diag == nil {
		return
	}
	fmt.Fprintf(w.diag, "logrot: %s: %s\n",
		time.Now().Format("2006-01-02T15:04:05Z0700"), fmt.Sprintf(format, args...))
}

// open makes the log file ready for appending. A failure marks the writer
// broken and schedules a retry; it is never fatal.
func (w *writer) open() bool {
	if w.out != nil {
		return true
	}
	if w.broken && time.Now().Before(w.retry) {
		return false
	}

	out, err := os.OpenFile(w.filename, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		w.fail("cannot open %s: %v", w.filename, err)
		return false
	}
	off, err := out.Seek(0, io.SeekEnd)
	if err != nil {
		out.Close()
		w.fail("cannot seek %s: %v", w.filename, err)
		return false
	}

	if w.broken {
		w.reportf("resumed writing to %s", w.filename)
	}
	w.out, w.off, w.broken = out, off, false
	if w.rotateAt < w.size {
		w.rotateAt = w.size
	}
	return true
}

// fail records a failure and holds off further attempts for retryInterval.
func (w *writer) fail(format string, args ...interface{}) {
	if !w.broken {
		w.reportf("%s (dropping output until it recovers)", fmt.Sprintf(format, args...))
	}
	w.broken = true
	w.retry = time.Now().Add(retryInterval)
}

// emit appends p to the file as it is. Output is dropped while the file is
// unusable so that the reader can keep draining.
func (w *writer) emit(p []byte) bool {
	if len(p) == 0 {
		return true
	}
	if !w.open() {
		return false
	}
	n, err := w.out.Write(p)
	if err != nil {
		w.out.Close()
		w.out = nil
		w.fail("cannot write %s: %v", w.filename, err)
		return false
	}
	w.off += int64(n)
	return true
}

// write appends p, rotating once the file passes size.
//
// A chunk is not a record: under load the pipe coalesces many records into one
// read. When a chunk would carry the file past the rotation point it is split
// at its last newline — the head completes the current file on a record
// boundary, the tail opens the next one — so the overshoot is bounded by one
// read (readChunk) rather than by how long the reader stays saturated. A
// stream with no record boundaries at all is cut mid-record once the overshoot
// reaches a full extra size: an imprecise boundary beats an unbounded file.
func (w *writer) write(p []byte) {
	if w.off+int64(len(p)) >= w.rotateAt {
		if ix := bytes.LastIndexByte(p, '\n'); ix >= 0 {
			if !w.emit(p[:ix+1]) {
				return
			}
			if w.off >= w.rotateAt {
				w.rotate()
			}
			p = p[ix+1:]
		}
	}
	if !w.emit(p) {
		return
	}
	if w.off >= w.rotateAt+w.size {
		w.rotate()
	}
}

func (w *writer) rotate() {
	if w.out != nil {
		w.out.Close()
		w.out = nil
	}

	if err := rotate(w.filename, w.size, w.count); err != nil {
		// Keep appending to the current file: an oversized log is better than
		// a stopped one. Hold off the next attempt for a full size, so a
		// permanently failing rename reports once per size rather than once
		// per record.
		w.reportf("cannot rotate %s: %v (continuing in place)", w.filename, err)
		w.rotateAt = w.off + w.size
		return
	}
	// The current file was renamed away; the next emit starts a fresh one.
	w.off, w.rotateAt = 0, w.size
}

func (w *writer) close() {
	if w.out != nil {
		w.out.Close()
		w.out = nil
	}
}

// Run copies r into filename, rotating it every size bytes and keeping count
// generations. Diagnostics go to diag, which may be nil.
//
// Run returns only when r ends: while r is open it always keeps reading, so the
// process writing into r never blocks on a full pipe and never sees it close.
// An error is returned only for invalid parameters or a persistent read
// failure — a transient one is reported and retried.
func Run(r io.Reader, filename string, size int64, count int, diag io.Writer) error {
	if size <= 0 || count <= 0 {
		return fmt.Errorf("invalid parameters: size=%d count=%d", size, count)
	}

	w := &writer{filename: filename, size: size, count: count, diag: diag, rotateAt: size}
	defer w.close()

	// Rotate once up front so a restart does not append to an already
	// oversized file.
	if err := rotate(filename, size, count); err != nil {
		w.reportf("cannot rotate %s at startup: %v", filename, err)
	}

	buf := make([]byte, readChunk)
	fails := 0
	for {
		n, err := r.Read(buf)
		if n > 0 {
			fails = 0
			w.write(buf[:n])
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			// A read error on the pipe is close to impossible; retry rather
			// than abandoning the drain over a transient one.
			if fails++; fails >= 10 {
				return err
			}
			w.reportf("cannot read input: %v (retrying)", err)
			time.Sleep(time.Second)
		}
	}
}
