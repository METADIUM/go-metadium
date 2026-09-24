package metabft

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
)

// The WAL keeps every vote and lock this node signed, so a restart cannot make
// it sign something that conflicts with what it already sent
// (docs/pbft-consensus-design.md §6.1). A record is written and fsynced
// before the corresponding message is sent.
//
// File format: a sequence of frames,
//
//	[4 bytes big-endian payload length][4 bytes CRC-32C of payload][payload = RLP(Record)]

// RecordKind is the kind of a WAL record.
type RecordKind uint8

const (
	RecordVote RecordKind = 1 // a PREPARE, COMMIT or ROUND-CHANGE about to be sent
	RecordLock RecordKind = 2 // entered PREPARED
)

// Record is one WAL entry.
type Record struct {
	Kind   RecordKind
	Height uint64
	Round  uint64 // vote round; for a lock, the prepared round
	Type   MsgType
	Digest common.Hash
	// Lock only: the PREPARE quorum certificate (RLP-encoded messages) and
	// the block RLP, needed to justify and re-propose after a restart.
	Certificate [][]byte
	Block       []byte
}

var (
	// ErrWALMissing means there is no WAL file. Whether that is a fresh node
	// or a lost WAL depends on the chain head (design §6.1): the caller decides.
	ErrWALMissing = errors.New("metabft: WAL does not exist")

	// ErrWALCorrupt means a frame inside the WAL does not check out. The
	// node must not trust any of it and has to start as an observer.
	ErrWALCorrupt = errors.New("metabft: WAL is corrupt")

	// ErrConflictingVote refuses a vote that contradicts one already recorded.
	ErrConflictingVote = errors.New("metabft: conflicts with a recorded vote")

	// ErrPastRound refuses a vote below the highest round already voted in at that height.
	ErrPastRound = errors.New("metabft: round is below one already voted in")
)

const (
	frameHeaderLen = 8
	maxFrameLen    = 64 << 20 // a lock carries a block; far above any block size
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// WAL is an append-only vote log. It is safe for concurrent use.
type WAL struct {
	mu    sync.Mutex
	path  string
	f     *os.File
	state *VoteState
}

// OpenReport tells the caller what OpenWAL found.
type OpenReport struct {
	Created   bool // the file did not exist and was created
	TornTail  bool // an incomplete final frame was discarded
	Records   int
	TailBytes int64 // bytes discarded with a torn tail
}

// OpenWAL opens the WAL at path. With create unset, a missing file is
// ErrWALMissing; the caller creates the WAL only while no PBFT vote can have
// been cast yet (head+1 < bftBlock, design §6.1).
//
// An incomplete final frame is discarded: records are fsynced before the
// message goes out, so a frame cut short by a crash was never sent. Any other
// damage is ErrWALCorrupt.
func OpenWAL(path string, create bool) (*WAL, *OpenReport, error) {
	report := new(OpenReport)
	flags := os.O_RDWR
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, nil, ErrWALMissing
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, nil, err
		}
		flags |= os.O_CREATE | os.O_EXCL
		report.Created = true
	} else if err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if report.Created {
		if err := syncDir(filepath.Dir(path)); err != nil {
			f.Close()
			return nil, nil, err
		}
	}
	records, good, torn, err := readFrames(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if torn {
		size, _ := f.Seek(0, io.SeekEnd)
		report.TornTail, report.TailBytes = true, size-good
		if err := f.Truncate(good); err != nil {
			f.Close()
			return nil, nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, nil, err
		}
	}
	if _, err := f.Seek(good, io.SeekStart); err != nil {
		f.Close()
		return nil, nil, err
	}
	state := newVoteState()
	for _, r := range records {
		state.apply(r)
	}
	report.Records = len(records)
	return &WAL{path: path, f: f, state: state}, report, nil
}

// readFrames reads every frame. It returns the offset just past the last
// good frame and whether the file ends with an incomplete frame.
func readFrames(r io.Reader) (records []Record, good int64, torn bool, err error) {
	br := bufio.NewReader(r)
	var header [frameHeaderLen]byte
	for {
		n, err := io.ReadFull(br, header[:])
		if err == io.EOF {
			return records, good, false, nil
		}
		if err == io.ErrUnexpectedEOF {
			return records, good, n > 0, nil
		}
		if err != nil {
			return nil, 0, false, err
		}
		length := binary.BigEndian.Uint32(header[:4])
		if length == 0 || length > maxFrameLen {
			return nil, 0, false, fmt.Errorf("%w: frame at %d has length %d", ErrWALCorrupt, good, length)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(br, payload); err == io.ErrUnexpectedEOF || err == io.EOF {
			return records, good, true, nil
		} else if err != nil {
			return nil, 0, false, err
		}
		if crc32.Checksum(payload, crcTable) != binary.BigEndian.Uint32(header[4:]) {
			return nil, 0, false, fmt.Errorf("%w: checksum mismatch in frame at %d", ErrWALCorrupt, good)
		}
		var rec Record
		if err := rlp.DecodeBytes(payload, &rec); err != nil {
			return nil, 0, false, fmt.Errorf("%w: frame at %d: %v", ErrWALCorrupt, good, err)
		}
		records = append(records, rec)
		good += frameHeaderLen + int64(length)
	}
}

func encodeFrame(rec *Record) ([]byte, error) {
	payload, err := rlp.EncodeToBytes(rec)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, frameHeaderLen+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(frame[4:8], crc32.Checksum(payload, crcTable))
	copy(frame[frameHeaderLen:], payload)
	return frame, nil
}

// RecordVote records a vote durably. It must return nil before the vote is
// signed and sent; a conflicting vote is refused and nothing is written.
// Recording the same vote again is allowed (a resend).
func (w *WAL) RecordVote(height, round uint64, typ MsgType, digest common.Hash) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch typ {
	case MsgPrepare, MsgCommit, MsgRoundChange:
	default:
		return fmt.Errorf("%w: %v is not a vote", errUnknownMsgType, typ)
	}
	dup, err := w.state.checkVote(height, round, typ, digest)
	if err != nil || dup {
		return err
	}
	return w.append(&Record{Kind: RecordVote, Height: height, Round: round, Type: typ, Digest: digest})
}

// RecordLock records entering PREPARED at (height, round) for digest.
func (w *WAL) RecordLock(height, round uint64, digest common.Hash, certificate [][]byte, block []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.append(&Record{Kind: RecordLock, Height: height, Round: round, Digest: digest, Certificate: certificate, Block: block})
}

func (w *WAL) append(rec *Record) error {
	frame, err := encodeFrame(rec)
	if err != nil {
		return err
	}
	if _, err := w.f.Write(frame); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	w.state.apply(*rec)
	return nil
}

// Lock returns the latest lock at height, if any.
func (w *WAL) Lock(height uint64) (Record, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l, ok := w.state.locks[height]
	return l, ok
}

// Vote returns the digest recorded for (height, round, typ), if any.
func (w *WAL) Vote(height, round uint64, typ MsgType) (common.Hash, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	d, ok := w.state.votes[voteKey{height, round, typ}]
	return d, ok
}

// Prune drops every record at or below committed. It rewrites the file
// atomically: a crash leaves either the old or the new WAL.
func (w *WAL) Prune(committed uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	records, _, _, err := readFrames(w.f)
	if err != nil {
		return err
	}
	tmp := w.path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	state := newVoteState()
	for i := range records {
		if records[i].Height <= committed {
			continue
		}
		frame, err := encodeFrame(&records[i])
		if err == nil {
			_, err = out.Write(frame)
		}
		if err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
		state.apply(records[i])
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, w.path); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := syncDir(filepath.Dir(w.path)); err != nil {
		out.Close()
		return err
	}
	w.f.Close()
	if _, err := out.Seek(0, io.SeekEnd); err != nil {
		out.Close()
		return err
	}
	w.f, w.state = out, state
	return nil
}

// Close closes the WAL file.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

type voteKey struct {
	height, round uint64
	typ           MsgType
}

// VoteState is what the WAL records imply about what may still be signed.
type VoteState struct {
	votes    map[voteKey]common.Hash
	maxRound map[uint64]uint64
	locks    map[uint64]Record // latest lock per height
}

func newVoteState() *VoteState {
	return &VoteState{
		votes:    make(map[voteKey]common.Hash),
		maxRound: make(map[uint64]uint64),
		locks:    make(map[uint64]Record),
	}
}

func (s *VoteState) apply(r Record) {
	switch r.Kind {
	case RecordVote:
		s.votes[voteKey{r.Height, r.Round, r.Type}] = r.Digest
		if r.Round > s.maxRound[r.Height] {
			s.maxRound[r.Height] = r.Round
		}
	case RecordLock:
		if cur, ok := s.locks[r.Height]; !ok || r.Round >= cur.Round {
			s.locks[r.Height] = r
		}
	}
}

// checkVote reports whether (height, round, typ, digest) may be signed. dup
// means exactly this vote is already recorded.
func (s *VoteState) checkVote(height, round uint64, typ MsgType, digest common.Hash) (dup bool, err error) {
	if d, ok := s.votes[voteKey{height, round, typ}]; ok {
		if d != digest {
			return false, fmt.Errorf("%w: %v at height %d round %d was %x, now %x", ErrConflictingVote, typ, height, round, d, digest)
		}
		return true, nil
	}
	if max, ok := s.maxRound[height]; ok && round < max {
		return false, fmt.Errorf("%w: height %d round %d, already voted in round %d", ErrPastRound, height, round, max)
	}
	// An honest node PREPAREs and COMMITs the same digest within a round.
	if typ == MsgPrepare || typ == MsgCommit {
		other := MsgCommit
		if typ == MsgCommit {
			other = MsgPrepare
		}
		if d, ok := s.votes[voteKey{height, round, other}]; ok && d != digest {
			return false, fmt.Errorf("%w: %v at height %d round %d was %x, now %v %x", ErrConflictingVote, other, height, round, d, typ, digest)
		}
	}
	return false, nil
}
