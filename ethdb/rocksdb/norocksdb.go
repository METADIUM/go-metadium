// rocksdb_database.go
//go:build !rocksdb
// +build !rocksdb

package rocksdb

import "github.com/ethereum/go-ethereum/ethdb/leveldb"

func New(file string, cache int, handles int, namespace string, readonly bool) (*leveldb.Database, error) {
	return leveldb.New(file, cache, handles, namespace, readonly)
}

func EnableStats(b bool) {}

func Stats(device string) (disk_r_count, disk_r_bytes, disk_w_count, disk_w_bytes, r_count, r_bytes, w_count, w_bytes, l_count, d_count uint64) {
	return 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
}
