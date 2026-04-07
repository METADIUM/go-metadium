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

// Package hashdb implements the hash-based trie node database (go-metadium
// only supports hash-scheme; this package provides the type declarations that
// upstream go-ethereum exposes from trie/triedb/hashdb).
package hashdb

// Config contains the configuration options for the hash-based trie database.
type Config struct {
	CleanCacheSize int // Maximum memory allowance (bytes) for caching clean nodes
}

// Defaults holds the default settings for the hash-based trie database.
var Defaults = &Config{
	CleanCacheSize: 256 * 1024 * 1024,
}
