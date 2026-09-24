package metabft

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// Evidence is two messages a validator signed for the same
// (height, round, type) that cannot both be honest (design §7.1). Both carry
// their signatures and chain ID, so anyone can verify it independently.
type Evidence struct {
	First  Message
	Second Message
}

var errNotEquivocation = errors.New("messages are not an equivocation")

// Verify checks that the evidence is a genuine equivocation by a member of
// set on chainID and returns the offender's index.
func (e *Evidence) Verify(chainID uint64, set *ValidatorSet) (int, error) {
	a, b := &e.First, &e.Second
	ia, err := a.Verify(chainID, set)
	if err != nil {
		return 0, fmt.Errorf("first message: %w", err)
	}
	ib, err := b.Verify(chainID, set)
	if err != nil {
		return 0, fmt.Errorf("second message: %w", err)
	}
	switch {
	case ia != ib:
		return 0, fmt.Errorf("%w: signed by different validators", errNotEquivocation)
	case a.Type != b.Type || a.Height != b.Height || a.Round != b.Round:
		return 0, fmt.Errorf("%w: different height, round or type", errNotEquivocation)
	case a.Type == MsgRoundChange:
		// One ROUND-CHANGE per node per round: any difference in content is one.
		if a.SigningHash() == b.SigningHash() {
			return 0, fmt.Errorf("%w: identical ROUND-CHANGE", errNotEquivocation)
		}
	case a.Digest == b.Digest:
		return 0, fmt.Errorf("%w: same digest", errNotEquivocation)
	}
	return ia, nil
}

// id is independent of message order, so the same pair is stored once.
func (e *Evidence) id() string {
	x, y := e.First.SigningHash(), e.Second.SigningHash()
	if bytes.Compare(x[:], y[:]) > 0 {
		x, y = y, x
	}
	return fmt.Sprintf("%d-%d-%d-%x", e.First.Height, e.First.Round, e.First.Type,
		crypto.Keccak256(x[:], y[:])[:16])
}

// EvidenceStore keeps verified evidence on disk, one file per pair, so it
// survives restarts and can be handed to governance (design §7.1, §12).
type EvidenceStore struct {
	mu  sync.Mutex
	dir string
}

const evidenceExt = ".rlp"

// OpenEvidenceStore opens (creating if needed) the store in dir.
func OpenEvidenceStore(dir string) (*EvidenceStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &EvidenceStore{dir: dir}, nil
}

// Add stores evidence after checking it. It reports whether the pair was new.
func (s *EvidenceStore) Add(e *Evidence, chainID uint64, set *ValidatorSet) (bool, error) {
	if _, err := e.Verify(chainID, set); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dir, e.id()+evidenceExt)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	enc, err := rlp.EncodeToBytes(e)
	if err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := writeSynced(tmp, enc); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, syncDir(s.dir)
}

// List returns every stored piece of evidence, ordered by height, round and type.
func (s *EvidenceStore) List() ([]*Evidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []*Evidence
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), evidenceExt) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		e := new(Evidence)
		if err := rlp.DecodeBytes(data, e); err != nil {
			return nil, fmt.Errorf("evidence %s: %w", entry.Name(), err)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].First, out[j].First
		if a.Height != b.Height {
			return a.Height < b.Height
		}
		if a.Round != b.Round {
			return a.Round < b.Round
		}
		return a.Type < b.Type
	})
	return out, nil
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}
