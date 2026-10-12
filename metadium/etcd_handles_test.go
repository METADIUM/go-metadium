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
	"sync"
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
)

// TestEtcdServerReadyIgnoresReplacedServer: the ready branch of
// etcdEventHandler acts only while the admin still holds the server that
// reported ready. A handler left over from a server that etcdRestart has
// since replaced must not mark the successor ready (#201 review, item 1).
func TestEtcdServerReadyIgnoresReplacedServer(t *testing.T) {
	old, replacement := &embed.Etcd{}, &embed.Etcd{}
	ma := &metaAdmin{lock: &sync.Mutex{}}
	etcdReady.Store(false)
	t.Cleanup(func() { etcdReady.Store(false) })

	// The successor is installed; the old server's handler fires late.
	ma.etcdSet(replacement, &clientv3.Client{})
	if ma.etcdServerReady(old) {
		t.Fatalf("etcdServerReady(old) = true, want false: old server was replaced")
	}
	if etcdReady.Load() {
		t.Fatalf("etcdReady set by a replaced server's handler")
	}
	// The successor's own handler is the one allowed to set the flag.
	if !ma.etcdServerReady(replacement) {
		t.Fatalf("etcdServerReady(replacement) = false, want true")
	}
	if !etcdReady.Load() {
		t.Fatalf("etcdReady not set by the current server's handler")
	}
	// And a nil or cleared admin owns nothing.
	ma.etcdClear()
	if ma.etcdOwns(replacement) || ma.etcdOwns(nil) {
		t.Fatalf("etcdOwns reports ownership after etcdClear")
	}
}

// TestEtcdHandlesSurviveClear: callers take the server and client as one
// snapshot and use the copies, so a concurrent etcdStop/etcdRestart that
// clears the admin's fields cannot turn a passed readiness check into a nil
// dereference (#201 review, item 2). The snapshot stays usable; only the
// next check sees the server gone.
func TestEtcdHandlesSurviveClear(t *testing.T) {
	ma := &metaAdmin{lock: &sync.Mutex{}}
	srv, cli := &embed.Etcd{}, &clientv3.Client{}
	etcdReady.Store(false)
	t.Cleanup(func() { etcdReady.Store(false) })

	if e, c, ready := ma.etcdHandles(); e != nil || c != nil || ready {
		t.Fatalf("empty admin: handles = (%v, %v, %v), want (nil, nil, false)", e, c, ready)
	}
	ma.etcdSet(srv, cli)
	if _, _, ready := ma.etcdHandles(); ready {
		t.Fatalf("ready before the server reported ready")
	}
	etcdReady.Store(true)
	e, c, ready := ma.etcdHandles()
	if e != srv || c != cli || !ready {
		t.Fatalf("handles = (%p, %p, %v), want (%p, %p, true)", e, c, ready, srv, cli)
	}

	// A stop lands between the caller's check and its use.
	ma.etcdClear()
	if e == nil || c == nil {
		t.Fatalf("snapshot cleared under the caller")
	}
	if ma.etcdIsRunning() || ma.etcdIsReady() {
		t.Fatalf("admin still reports running/ready after etcdClear")
	}
	if _, _, ready := ma.etcdHandles(); ready {
		t.Fatalf("fresh handles report ready after etcdClear")
	}
}
