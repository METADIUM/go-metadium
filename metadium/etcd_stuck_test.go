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

import "testing"

// fixedGuard returns a guard whose jitter draw is always offset, so bound is
// etcdStuckMinTicks+offset at backoff 0.
func fixedGuard(offset int) *etcdStuckGuard {
	g := &etcdStuckGuard{intn: func(n int) int { return offset }}
	g.redraw()
	return g
}

// tick feeds n identical observations and returns how many asked for a restart.
func tick(g *etcdStuckGuard, running, ready bool, n int) int {
	restarts := 0
	for i := 0; i < n; i++ {
		if g.observe(running, ready) {
			restarts++
		}
	}
	return restarts
}

// TestEtcdStuckGuardRestartsOnlyWhenRunningNotReady: the stuck state is
// running && !ready, and only that state, held for bound consecutive ticks,
// asks for a restart (#140). Ready and not-running ticks never do, and a
// ready tick in between resets the count.
func TestEtcdStuckGuardRestartsOnlyWhenRunningNotReady(t *testing.T) {
	g := fixedGuard(0) // bound 6
	if n := tick(g, true, true, 100); n != 0 {
		t.Fatalf("%d restarts while ready", n)
	}
	if n := tick(g, false, false, 100); n != 0 {
		t.Fatalf("%d restarts while not running", n)
	}
	// Five stuck ticks, a ready one, five more: never six in a row.
	if n := tick(g, true, false, 5); n != 0 {
		t.Fatalf("restarted after %d stuck ticks, bound is 6", 5)
	}
	tick(g, true, true, 1)
	if n := tick(g, true, false, 5); n != 0 {
		t.Fatal("restarted although a ready tick reset the count")
	}
	// The sixth consecutive stuck tick restarts, exactly once.
	if !g.observe(true, false) {
		t.Fatal("no restart on the 6th consecutive stuck tick")
	}
	if g.notReady != 0 || g.restarts != 1 {
		t.Fatalf("after restart: notReady=%d restarts=%d, want 0 and 1", g.notReady, g.restarts)
	}
}

// TestEtcdStuckGuardBacksOffUntilReady: each restart that does not reach
// ready doubles the bound, up to 2^etcdStuckMaxBackoff, so nodes that all
// went not-ready at once do not restart in lockstep forever; the first ready
// observation resets it.
func TestEtcdStuckGuardBacksOffUntilReady(t *testing.T) {
	g := fixedGuard(0) // base bound 6
	wantBounds := []int{6, 12, 24, 48, 96, 192, 192, 192}
	for i, want := range wantBounds {
		if g.bound != want {
			t.Fatalf("before restart %d: bound = %d, want %d", i+1, g.bound, want)
		}
		if n := tick(g, true, false, want-1); n != 0 {
			t.Fatalf("restart %d came early", i+1)
		}
		if !g.observe(true, false) {
			t.Fatalf("restart %d did not come at tick %d", i+1, want)
		}
	}
	// Not running (the restarted server died) keeps the backoff.
	tick(g, false, false, 3)
	if g.restarts != len(wantBounds) || g.bound != 192 {
		t.Fatalf("not-running ticks changed the backoff: restarts=%d bound=%d", g.restarts, g.bound)
	}
	// Ready resets it to the base bound.
	g.observe(true, true)
	if g.restarts != 0 || g.bound != 6 || g.notReady != 0 {
		t.Fatalf("ready did not reset: restarts=%d bound=%d notReady=%d", g.restarts, g.bound, g.notReady)
	}
}

// TestEtcdStuckGuardJitterPerNode: with the real draw, the base bound lies in
// [etcdStuckMinTicks, etcdStuckMaxTicks] (30-60 s on the 5 s loop) and
// varies between nodes, which is what keeps a partition from restarting
// every node on the same tick.
func TestEtcdStuckGuardJitterPerNode(t *testing.T) {
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		g := newEtcdStuckGuard()
		if g.bound < etcdStuckMinTicks || g.bound > etcdStuckMaxTicks {
			t.Fatalf("bound %d outside [%d, %d]", g.bound, etcdStuckMinTicks, etcdStuckMaxTicks)
		}
		seen[g.bound] = true
	}
	if len(seen) < 2 {
		t.Fatalf("200 guards drew the same bound %v; no jitter", seen)
	}
}
