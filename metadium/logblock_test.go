// Copyright 2026 The go-metadium Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package metadium

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// logBlockHarness drives logBlock with an etcd stub: put fails while down is
// set, and yield only records that it was asked.
type logBlockHarness struct {
	ma     *metaAdmin
	down   bool
	puts   int
	stored metaWork
	yields []int64
}

func newLogBlockHarness(blocksPer int64) *logBlockHarness {
	return &logBlockHarness{ma: &metaAdmin{
		lock:      &sync.Mutex{},
		self:      &metaNode{Name: "node1", Id: "1"},
		blocksPer: blocksPer,
	}}
}

func (h *logBlockHarness) put(key, value string) (int64, error) {
	h.puts++
	if h.down {
		return 0, ErrNotRunning
	}
	if key != metaWorkKey {
		panic("unexpected key " + key)
	}
	if err := json.Unmarshal([]byte(value), &h.stored); err != nil {
		panic(err)
	}
	return int64(h.puts), nil
}

func (h *logBlockHarness) yield(height int64) {
	h.yields = append(h.yields, height)
	h.ma.blocksMined = 0
}

func (h *logBlockHarness) seal(height int64) {
	h.ma.logBlock(height, common.BigToHash(common.Big1), h.put, h.yield)
}

// TestLogBlockDoesNotRotateOnUnrecordedBlock: a block etcd refused to record
// (ErrNotRunning, the 2026-05-19 shape) neither advances blocksMined nor
// yields the lead, however many such blocks are sealed; once etcd takes the
// record again the rotation resumes at the next boundary (issue #139,
// post-mortem 7.1).
func TestLogBlockDoesNotRotateOnUnrecordedBlock(t *testing.T) {
	h := newLogBlockHarness(5)
	h.ma.blocksMined = 4 // one recorded block short of a rotation

	// etcd down across the boundary at height 9 (next height 10 % 5 == 0).
	h.down = true
	for height := int64(7); height <= 14; height++ {
		h.seal(height)
	}
	if h.puts != 8 {
		t.Fatalf("puts = %d, want 8 (every block is still offered to etcd)", h.puts)
	}
	if h.ma.blocksMined != 4 {
		t.Fatalf("blocksMined = %d after unrecorded blocks, want 4 (unchanged)", h.ma.blocksMined)
	}
	if len(h.yields) != 0 {
		t.Fatalf("yielded at %v on blocks etcd never recorded", h.yields)
	}

	// etcd back: height 15 is recorded, blocksMined reaches 5, but the next
	// height (16) is not a boundary, so the lead holds until 19.
	h.down = false
	h.seal(15)
	if h.stored.Height != 15 {
		t.Fatalf("recorded height = %d, want 15", h.stored.Height)
	}
	if h.ma.blocksMined != 5 || len(h.yields) != 0 {
		t.Fatalf("after recovery: blocksMined = %d, yields = %v; want 5 and none yet",
			h.ma.blocksMined, h.yields)
	}
	for height := int64(16); height <= 19; height++ {
		h.seal(height)
	}
	if len(h.yields) != 1 || h.yields[0] != 20 {
		t.Fatalf("yields = %v, want one at next height 20", h.yields)
	}
	if h.ma.blocksMined != 0 {
		t.Fatalf("blocksMined = %d after the yield, want 0", h.ma.blocksMined)
	}
}

// TestLogBlockRotatesOnRecordedBlocks: the rotation itself is unchanged. With
// etcd recording every block, the lead is yielded at the first blocksPer
// boundary reached with blocksPer blocks recorded, then at every boundary,
// and the counter restarts. With blocksPer 3 from a zero counter that is next
// height 6 (at height 3 only two blocks are recorded), then 9 and 12.
func TestLogBlockRotatesOnRecordedBlocks(t *testing.T) {
	h := newLogBlockHarness(3)
	for height := int64(1); height <= 12; height++ {
		h.seal(height)
	}
	want := []int64{6, 9, 12}
	if len(h.yields) != len(want) {
		t.Fatalf("yields = %v, want %v", h.yields, want)
	}
	for i, y := range want {
		if h.yields[i] != y {
			t.Fatalf("yields = %v, want %v", h.yields, want)
		}
	}
	if h.stored.Height != 12 || h.puts != 12 {
		t.Fatalf("recorded height = %d after %d puts, want 12 and 12", h.stored.Height, h.puts)
	}
}
