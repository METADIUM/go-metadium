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

package discover

import (
	"crypto/ecdsa"
	"net"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/p2p/discover/v4wire"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

// TestUDPv4_pingEndpointStatementRequiresBond checks that pings from unbonded
// nodes do not feed the endpoint predictor. Anyone can send a ping, so without
// the bond check a handful of forged senders could move the node's advertised
// IP to an address of their choosing.
func TestUDPv4_pingEndpointStatementRequiresBond(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	var (
		claimed = v4wire.Endpoint{IP: net.ParseIP("6.6.6.6").To4(), UDP: 6666}
		keys    []*ecdsa.PrivateKey
		addrs   []*net.UDPAddr
	)
	for i := 0; i < 20; i++ { // 2x iptrackMinStatements
		keys = append(keys, newkey())
		addrs = append(addrs, &net.UDPAddr{IP: net.IP{10, 20, 0, byte(i)}, Port: 30303})
	}
	ping := &v4wire.Ping{From: testRemote, To: claimed, Version: 4, Expiration: futureExp}

	// Pings from unbonded nodes must not change the local endpoint.
	for i := range keys {
		test.packetInFrom(nil, keys[i], addrs[i], ping)
	}
	if ip := test.udp.localNode.Node().IP(); ip.Equal(net.IP{6, 6, 6, 6}) {
		t.Fatalf("local endpoint changed by unbonded pings: %v", ip)
	}
	// Once bonded, the statements are accepted.
	for i := range keys {
		id := enode.PubkeyToIDV4(&keys[i].PublicKey)
		test.db.UpdateLastPongReceived(id, addrs[i].IP, time.Now())
		test.packetInFrom(nil, keys[i], addrs[i], ping)
	}
	if ip := test.udp.localNode.Node().IP(); !ip.Equal(net.IP{6, 6, 6, 6}) {
		t.Fatalf("local endpoint not updated by bonded pings: %v", ip)
	}
}
