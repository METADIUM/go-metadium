// Copyright 2023 The go-ethereum Authors
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

// Package pathdb implements the path-based trie node database.
// go-metadium does NOT support path-scheme; this package is a stub only.
// Any attempt to instantiate a path-based database will panic.
package pathdb

// Config contains the configuration options for the path-based trie database.
// go-metadium stub — path-scheme is not supported.
type Config struct {
	StateHistory  uint64 // Number of recent blocks to maintain state history for
	CleanCacheSize int   // Maximum memory allowance (bytes) for caching clean nodes
	DirtyCacheSize int   // Maximum memory allowance (bytes) for caching dirty nodes
}

// Defaults holds the default settings for the path-based trie database.
var Defaults = &Config{
	StateHistory:   128,
	CleanCacheSize: 16 * 1024 * 1024,
	DirtyCacheSize: 256 * 1024 * 1024,
}
