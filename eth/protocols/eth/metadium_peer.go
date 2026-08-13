package eth

import (
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	metaapi "github.com/ethereum/go-ethereum/metadium/api"
	"github.com/ethereum/go-ethereum/p2p"
)

// SendStatusEx sends this node's miner status. On meta/69 it is wrapped with a
// replay-protection nonce/timestamp; on meta/66/68 the legacy packet is sent.
func (p *Peer) SendStatusEx(status *metaapi.MetadiumMinerStatus) error {
	if p.version >= ETH69 {
		return p2p.Send(p.rw, StatusExMsg, &StatusEx69Packet{
			Nonce:     p.nextMetaNonce(),
			Timestamp: uint64(time.Now().Unix()),
			Status:    *status,
		})
	}
	return p2p.Send(p.rw, StatusExMsg, status)
}

// SendEtcdCluster sends this node's etcd cluster.
func (p *Peer) SendEtcdCluster(cluster string) error {
	if p.version >= ETH69 {
		return p2p.Send(p.rw, EtcdClusterMsg, &EtcdCluster69Packet{
			Nonce:     p.nextMetaNonce(),
			Timestamp: uint64(time.Now().Unix()),
			Cluster:   cluster,
		})
	}
	return p2p.Send(p.rw, EtcdClusterMsg, cluster)
}

// RequestStatusEx sends a GetStatusEx request to the peer
func (p *Peer) RequestStatusEx() error {
	p.Log().Debug("Fetching extended status")
	requestTracker.Track(p.id, p.version, GetStatusExMsg, StatusExMsg, rand.Uint64())
	if p.version >= ETH69 {
		return p2p.Send(p.rw, GetStatusExMsg, &GetStatusEx69Packet{
			Nonce:     p.nextMetaNonce(),
			Timestamp: uint64(time.Now().Unix()),
		})
	}
	return p2p.Send(p.rw, GetStatusExMsg, common.Big1)
}

// RequestEtcdAddMember requests the peer to add this node to the cluster
func (p *Peer) RequestEtcdAddMember() error {
	p.Log().Debug("Trying to join etcd network")
	requestTracker.Track(p.id, p.version, EtcdAddMemberMsg, EtcdClusterMsg, rand.Uint64())
	if p.version >= ETH69 {
		return p2p.Send(p.rw, EtcdAddMemberMsg, &EtcdAddMember69Packet{
			Nonce:     p.nextMetaNonce(),
			Timestamp: uint64(time.Now().Unix()),
		})
	}
	return p2p.Send(p.rw, EtcdAddMemberMsg, common.Big1)
}

// SendNodeData sends a batch of state trie node data (response to GetNodeData, eth/66).
func (p *Peer) SendNodeData(data [][]byte) error {
	return p2p.Send(p.rw, NodeDataMsg, NodeDataPacket(data))
}

// RequestBlobSidecars fetches the blob sidecars for the given block hashes from
// a meta/69 peer (M5). The id correlates the BlobSidecars reply.
func (p *Peer) RequestBlobSidecars(id uint64, hashes []common.Hash) error {
	p.Log().Debug("Fetching blob sidecars", "count", len(hashes))
	requestTracker.Track(p.id, p.version, GetBlobSidecarsMsg, BlobSidecarsMsg, id)

	// Blob sidecars are the largest payload this protocol carries, and this
	// request also bypasses the dispatcher, so register the id for the response
	// gate here; handleBlobSidecars69 retires it. See response_gate.go.
	p.trackPending(id)

	return p2p.Send(p.rw, GetBlobSidecarsMsg, &GetBlobSidecarsPacket{
		RequestId: id,
		Hashes:    hashes,
	})
}

// ReplyBlobSidecars sends blob sidecars in response to a GetBlobSidecars request
// (M5). sidecars is positional with the request's hashes.
func (p *Peer) ReplyBlobSidecars(id uint64, sidecars [][]*types.BlobTxSidecar) error {
	return p2p.Send(p.rw, BlobSidecarsMsg, &BlobSidecarsPacket{
		RequestId: id,
		Sidecars:  sidecars,
	})
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
