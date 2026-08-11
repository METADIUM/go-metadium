// Copyright 2019 The go-ethereum Authors
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
//go:build rocksdb
// +build rocksdb

package rocksdb

import (
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/dbtest"
)

type EphemeralRDB struct {
	rdb  *RDBDatabase
	file string
}

func newEphemeralRDB(file string, cache int, handles int, namespace string, readonly bool) (*EphemeralRDB, error) {
	rdb, err := New(file, cache, handles, namespace, readonly)
	if err != nil {
		return nil, err
	}
	return &EphemeralRDB{
		rdb:  rdb,
		file: file,
	}, nil
}

func (db *EphemeralRDB) Path() string {
	return db.rdb.Path()
}

func (db *EphemeralRDB) Put(key []byte, value []byte) error {
	return db.rdb.Put(key, value)
}

func (db *EphemeralRDB) Has(key []byte) (bool, error) {
	return db.rdb.Has(key)
}

func (db *EphemeralRDB) Get(key []byte) ([]byte, error) {
	return db.rdb.Get(key)
}

func (db *EphemeralRDB) Delete(key []byte) error {
	return db.rdb.Delete(key)
}

func (db *EphemeralRDB) NewIterator(prefix, start []byte) ethdb.Iterator {
	return db.rdb.NewIterator(prefix, start)
}

func (db *EphemeralRDB) NewIteratorWithStart(start []byte) ethdb.Iterator {
	return db.rdb.NewIteratorWithStart(start)
}

func (db *EphemeralRDB) NewIteratorWithPrefix(prefix []byte) ethdb.Iterator {
	return db.rdb.NewIteratorWithPrefix(prefix)
}

func (db *EphemeralRDB) Close() error {
	err := db.rdb.Close()
	if len(db.file) > 0 {
		os.RemoveAll(db.file)
	}
	return err
}

func (db *EphemeralRDB) Stat(property string) (string, error) {
	return db.rdb.Stat(property)
}

func (db *EphemeralRDB) Compact(start []byte, limit []byte) error {
	return db.rdb.Compact(start, limit)
}

func (db *EphemeralRDB) Meter(prefix string) {
	return
}

func (db *EphemeralRDB) NewBatch() ethdb.Batch {
	return db.rdb.NewBatch()
}

func (db *EphemeralRDB) NewBatchWithSize(size int) ethdb.Batch {
	return db.rdb.NewBatchWithSize(size)
}

func (db *EphemeralRDB) NewSnapshot() (ethdb.Snapshot, error) {
	return db.rdb.NewSnapshot()
}

func TestRocksDB(t *testing.T) {
	t.Run("DatabaseSuite", func(t *testing.T) {
		dbtest.TestDatabaseSuite(t, func() ethdb.KeyValueStore {
			db, err := newEphemeralRDB("test", 1024, 1024, "test", false)
			if err != nil {
				t.Fatal(err)
			}
			return db
		})
	})
}

// TestSnapshotHasMissingKey verifies that snapshot.Has() returns (false, nil)
// for a key that does not exist, not (false, error).
func TestSnapshotHasMissingKey(t *testing.T) {
	dir := t.TempDir()
	db, err := New(dir, 16, 16, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.Put([]byte("exists"), []byte("value")); err != nil {
		t.Fatal(err)
	}

	snap, err := db.NewSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Release()

	// Key that exists: must return (true, nil)
	has, err := snap.Has([]byte("exists"))
	if err != nil {
		t.Fatalf("Has(existing key) returned error: %v", err)
	}
	if !has {
		t.Fatal("Has(existing key) returned false")
	}

	// Key that does NOT exist: must return (false, nil), not (false, error)
	has, err = snap.Has([]byte("missing"))
	if err != nil {
		t.Fatalf("Has(missing key) returned non-nil error: %v (want nil)", err)
	}
	if has {
		t.Fatal("Has(missing key) returned true")
	}
}

// TestSnapshotDoubleRelease verifies that calling Release() twice does not panic.
func TestSnapshotDoubleRelease(t *testing.T) {
	dir := t.TempDir()
	db, err := New(dir, 16, 16, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	snap, err := db.NewSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	snap.Release()
	snap.Release() // must not panic
}

// TestStatKnownProperty verifies that Stat() returns a non-empty result for a
// well-known RocksDB property.
func TestStatKnownProperty(t *testing.T) {
	dir := t.TempDir()
	db, err := New(dir, 16, 16, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	val, err := db.Stat("rocksdb.stats")
	if err != nil {
		t.Fatalf("Stat(rocksdb.stats) returned error: %v", err)
	}
	if len(val) == 0 {
		t.Fatal("Stat(rocksdb.stats) returned empty string")
	}

	// Unknown property must return an error
	_, err = db.Stat("rocksdb.unknown-property-xyz")
	if err == nil {
		t.Fatal("Stat(unknown property) should return error")
	}
}

// TestCloseIdempotent verifies that the database pointers are nil after Close.
func TestCloseNilAfterClose(t *testing.T) {
	dir := t.TempDir()
	db, err := New(dir, 16, 16, "test", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if db.db != nil || db.opts != nil || db.wopts != nil || db.ropts != nil {
		t.Fatal("Close() did not nil out internal pointers")
	}
}
