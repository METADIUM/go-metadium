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
	"fmt"
	"math/rand"
	"net"
	"sync"
	"testing"
	"time"
)

// freePortPair finds a port p such that p+1 and p+2 (the etcd peer and
// client ports etcdNewConfig derives from a node's port) are both free.
func freePortPair(t *testing.T) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		p := 20000 + rand.Intn(20000)
		ok := true
		for _, q := range []int{p + 1, p + 2} {
			l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", q))
			if err != nil {
				ok = false
				break
			}
			l.Close()
		}
		if ok {
			return p
		}
	}
	t.Fatal("no free port pair")
	return 0
}

func waitReady(t *testing.T, ma *metaAdmin, want bool, what string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ma.etcdIsReady() == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: etcdIsReady() != %v after 30s", what, want)
}

// TestEtcdStopReleasesListenersForRestart: the restart path etcdRestart
// takes, stop then start on the same ports and data directory, works with a
// real embedded server. etcdStop must release the peer and client listeners
// (the old HardStop-only version left them bound) and clear etcdReady; the
// restarted server must come back ready from its data directory.
func TestEtcdStopReleasesListenersForRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an embedded etcd server")
	}
	port := freePortPair(t)
	ma := &metaAdmin{
		lock:    &sync.Mutex{},
		self:    &metaNode{Name: "node1", Ip: "127.0.0.1", Port: port},
		etcdDir: t.TempDir(),
	}
	etcdReady.Store(false)
	t.Cleanup(func() {
		if ma.etcdIsRunning() {
			ma.etcdStop()
		}
		etcdReady.Store(false)
	})

	if err := ma.etcdInit(); err != nil {
		t.Fatalf("etcdInit: %v", err)
	}
	waitReady(t, ma, true, "first start")

	// Stop: flag cleared, objects gone, ports free again.
	if err := ma.etcdStop(); err != nil {
		t.Fatalf("etcdStop: %v", err)
	}
	if etcdReady.Load() || ma.etcdIsRunning() {
		t.Fatalf("after stop: etcdReady=%v running=%v, want false and false", etcdReady.Load(), ma.etcdIsRunning())
	}
	for _, q := range []int{port + 1, port + 2} {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", q))
		if err != nil {
			t.Fatalf("port %d still bound after etcdStop: %v", q, err)
		}
		l.Close()
	}

	// Start again from the data directory, as etcdRestart does.
	if err := ma.etcdStart(); err != nil {
		t.Fatalf("etcdStart after stop: %v", err)
	}
	waitReady(t, ma, true, "restart")
	if _, err := ma.etcdPut(metaWorkKey, "restarted"); err != nil {
		t.Fatalf("etcdPut after restart: %v", err)
	}
	if v, err := ma.etcdGet(metaWorkKey); err != nil || v != "restarted" {
		t.Fatalf("etcdGet after restart: %q, %v", v, err)
	}
}
