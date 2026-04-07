package eth

import (
	"math/rand"

	"github.com/ethereum/go-ethereum/common"
	metaapi "github.com/ethereum/go-ethereum/metadium/api"
	"github.com/ethereum/go-ethereum/p2p"
)

// SendStatusEx sends this node's miner status
func (p *Peer) SendStatusEx(status *metaapi.MetadiumMinerStatus) error {
	return p2p.Send(p.rw, StatusExMsg, status)
}

// SendEtcdCluster sends this node's etcd cluster
func (p *Peer) SendEtcdCluster(cluster string) error {
	return p2p.Send(p.rw, EtcdClusterMsg, cluster)
}

// RequestStatusEx sends a GetStatusEx request to the peer
func (p *Peer) RequestStatusEx() error {
	p.Log().Debug("Fetching extended status")
	_ = rand.Uint64()
	requestTracker.Track(p.id, p.version, GetStatusExMsg, StatusExMsg, rand.Uint64())
	return p2p.Send(p.rw, GetStatusExMsg, common.Big1)
}

// RequestEtcdAddMember requests the peer to add this node to the cluster
func (p *Peer) RequestEtcdAddMember() error {
	p.Log().Debug("Trying to join etcd network")
	requestTracker.Track(p.id, p.version, EtcdAddMemberMsg, EtcdClusterMsg, rand.Uint64())
	return p2p.Send(p.rw, EtcdAddMemberMsg, common.Big1)
}

// SendNodeData sends a batch of state trie node data (response to GetNodeData, eth/66).
func (p *Peer) SendNodeData(data [][]byte) error {
	return p2p.Send(p.rw, NodeDataMsg, NodeDataPacket(data))
}

// sendPooledTransactionHashesVersioned sends transaction hashes using the appropriate
// format for the negotiated protocol version. eth/68 includes type+size metadata;
// eth/66 sends hashes only.
func (p *Peer) sendPooledTransactionHashesVersioned(hashes []common.Hash, types []byte, sizes []uint32) error {
	p.knownTxs.Add(hashes...)
	if p.version >= ETH68 {
		return p2p.Send(p.rw, NewPooledTransactionHashesMsg, NewPooledTransactionHashesPacket{Types: types, Sizes: sizes, Hashes: hashes})
	}
	return p2p.Send(p.rw, NewPooledTransactionHashesMsg, NewPooledTransactionHashesPacket66(hashes))
}
