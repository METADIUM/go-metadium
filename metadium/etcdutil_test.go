package metadium

import (
	"sync"
	"testing"
)

// TestEtcdFixClusterValidation verifies that etcdFixCluster only accepts
// cluster members whose advertised etcd peer endpoint (ip:port+1) belongs to a
// governance-registered node, preventing a compromised partner from injecting
// arbitrary endpoints into InitialCluster (audit finding H4/H5).
func TestEtcdFixClusterValidation(t *testing.T) {
	ma := &metaAdmin{
		lock: &sync.Mutex{},
		self: &metaNode{Name: "node1", Ip: "10.0.0.1", Port: 8589},
		nodes: map[string]*metaNode{
			"node1": {Name: "node1", Ip: "10.0.0.1", Port: 8589},
			"node2": {Name: "node2", Ip: "10.0.0.2", Port: 8589},
			"node3": {Name: "node3", Ip: "10.0.0.3", Port: 8589},
		},
	}

	// All members map to governance nodes -> accepted.
	good := "node1=https://10.0.0.1:8590,node2=https://10.0.0.2:8590,node3=https://10.0.0.3:8590"
	if _, err := ma.etcdFixCluster(good); err != nil {
		t.Fatalf("valid cluster rejected: %v", err)
	}

	// Forged endpoint that is not a known node -> rejected.
	bad := "node1=https://10.0.0.1:8590,evil=https://6.6.6.6:8590"
	if _, err := ma.etcdFixCluster(bad); err == nil {
		t.Fatal("forged endpoint accepted")
	}

	// Forged endpoint placed first (before self) -> still rejected.
	badFirst := "evil=https://6.6.6.6:8590,node1=https://10.0.0.1:8590"
	if _, err := ma.etcdFixCluster(badFirst); err == nil {
		t.Fatal("forged endpoint (leading) accepted")
	}

	// A trusted endpoint with an arbitrary name is harmless (connection still
	// only targets the known node address) -> accepted.
	nameSwap := "node1=https://10.0.0.1:8590,whatever=https://10.0.0.2:8590"
	if _, err := ma.etcdFixCluster(nameSwap); err != nil {
		t.Fatalf("trusted endpoint with arbitrary name rejected: %v", err)
	}
}
