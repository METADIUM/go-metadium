// Copyright 2026 The go-ethereum Authors
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

package core

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
)

// TestStopWaitsForPrefetchers checks that Stop does not return while a block
// prefetcher is still running, and that no prefetcher can start once the chain
// is stopping. The prefetcher reads the state database, which the node closes
// right after Stop returns.
func TestStopWaitsForPrefetchers(t *testing.T) {
	_, _, chain, err := newCanonical(ethash.NewFaker(), 0, true, rawdb.HashScheme)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if !chain.trackPrefetch() {
		t.Fatal("trackPrefetch refused a prefetcher on a running chain")
	}
	finished := make(chan struct{})
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(finished)
		chain.prefetchWg.Done()
	}()
	chain.Stop()
	select {
	case <-finished:
	default:
		t.Fatal("Stop returned while the prefetcher was still running")
	}
	if chain.trackPrefetch() {
		t.Fatal("trackPrefetch accepted a prefetcher after Stop")
	}
}
