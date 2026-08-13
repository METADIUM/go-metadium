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
package logrot

import (
	"fmt"
	"io"
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

// ParseSize parses a byte count with an optional k/m/g suffix.
func ParseSize(size string) (int, error) {
	m := 1
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

	i, err := strconv.Atoi(size)
	if err != nil {
		return 0, err
	}
	return i * m, nil
}

// rotate renames filename to filename.1, shifting the existing generations up
// and dropping filename.count. It is a no-op while the file is below size.
func rotate(filename string, size, count int) error {
	fi, err := os.Stat(filename)
	if err != nil {
		// Nothing to rotate yet, or the file is unreadable; the writer will
		// report the problem when it next opens it.
		return nil
	}
	if fi.Size() < int64(size) {
		return nil
	}

	for i := count; i >= 0; i-- {
		if i == count {
			os.Remove(fmt.Sprintf("%s.%d", filename, i))
			continue
		}

		src := filename
		if i != 0 {
			src = fmt.Sprintf("%s.%d", filename, i)
		}
		if _, err := os.Stat(src); err != nil && os.IsNotExist(err) {
			continue
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
	size     int
	count    int
	diag     io.Writer

	out      *os.File
	off      int  // bytes in the current file
	rotateAt int  // offset at which to attempt the next rotation
	broken   bool // the file is unusable; output is being dropped
	retry    time.Time
	atBOL    bool // the last byte written was a newline
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
	w.out, w.off, w.broken = out, int(off), false
	w.atBOL = true
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

// write appends p, rotating once the file is over size. Output is dropped
// while the file is unusable so that the reader can keep draining.
func (w *writer) write(p []byte) {
	if !w.open() {
		return
	}

	n, err := w.out.Write(p)
	if err != nil {
		w.out.Close()
		w.out = nil
		w.fail("cannot write %s: %v", w.filename, err)
		return
	}
	w.off += n
	w.atBOL = p[len(p)-1] == '\n'

	// Rotate on a record boundary only, so a record is never split across two
	// files. A record longer than the rotation size therefore overshoots it,
	// which is the same behaviour as before.
	if w.off >= w.rotateAt && w.atBOL {
		w.rotate()
	}
}

func (w *writer) rotate() {
	w.out.Close()
	w.out = nil

	if err := rotate(w.filename, w.size, w.count); err != nil {
		// Keep appending to the current file: an oversized log is better than
		// a stopped one. Hold off the next attempt for a full size, so a
		// permanently failing rename reports once per size rather than once
		// per record.
		w.reportf("cannot rotate %s: %v (continuing in place)", w.filename, err)
		w.rotateAt = w.off + w.size
		return
	}
	w.rotateAt = w.size
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
// An error is returned only for invalid parameters or a failed read.
func Run(r io.Reader, filename string, size, count int, diag io.Writer) error {
	if size <= 0 || count <= 0 {
		return fmt.Errorf("invalid parameters: size=%d count=%d", size, count)
	}

	w := &writer{filename: filename, size: size, count: count, diag: diag}
	defer w.close()

	// Rotate once up front so a restart does not append to an already
	// oversized file.
	if err := rotate(filename, size, count); err != nil {
		w.reportf("cannot rotate %s at startup: %v", filename, err)
	}

	buf := make([]byte, readChunk)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			w.write(buf[:n])
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
