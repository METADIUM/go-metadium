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

package rawdb

import (
	"testing"

	"github.com/ethereum/go-ethereum/ethdb"
)

// nilOnMissing answers a missing key with (nil, nil), as the RocksDB backend
// does, instead of the not-found error the other backends return.
type nilOnMissing struct {
	ethdb.KeyValueStore
}

func (db nilOnMissing) Get(key []byte) ([]byte, error) {
	if ok, _ := db.KeyValueStore.Has(key); !ok {
		return nil, nil
	}
	return db.KeyValueStore.Get(key)
}

// TestUncleanShutdownMarkerMissingKey: the marker list starts out, grows and
// shrinks the same way whether the backend reports a missing key as an error
// or as an empty value.
func TestUncleanShutdownMarkerMissingKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		db   ethdb.KeyValueStore
	}{
		{"not-found error", NewMemoryDatabase()},
		{"nil, nil", nilOnMissing{NewMemoryDatabase()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := tc.db
			if prev, discarded, err := PushUncleanShutdownMarker(db); err != nil || len(prev) != 0 || discarded != 0 {
				t.Fatalf("first push: prev %v, discarded %d, err %v", prev, discarded, err)
			}
			UpdateUncleanShutdownMarker(db)
			// A second start without a pop sees the first marker: that is the
			// unclean shutdown it reports.
			if prev, _, err := PushUncleanShutdownMarker(db); err != nil || len(prev) != 1 {
				t.Fatalf("second push: prev %v, err %v", prev, err)
			}
			PopUncleanShutdownMarker(db)
			PopUncleanShutdownMarker(db)
			if prev, _, err := PushUncleanShutdownMarker(db); err != nil || len(prev) != 0 {
				t.Fatalf("push after two pops: prev %v, err %v", prev, err)
			}
			PopUncleanShutdownMarker(db)
		})
	}
}
