package eth

import (
	metaapi "github.com/ethereum/go-ethereum/metadium/api"
)

// GetStatusExPacket is the network packet for GetStatusEx
type GetStatusExPacket int

// StatusExPacket is the network packet for extended status of a node
type StatusExPacket metaapi.MetadiumMinerStatus

// EtcdAddMemberPacket is the network packet for EtcdAddMember
type EtcdAddMemberPacket int

// EtcdClusterPacket is the network packet for EtcdAddMember / EtcdCluster exchange
type EtcdClusterPacket string

func (*GetStatusExPacket) Name() string { return "GetStatusEx" }
func (*GetStatusExPacket) Kind() byte   { return GetStatusExMsg }

func (*StatusExPacket) Name() string { return "StatusEx" }
func (*StatusExPacket) Kind() byte   { return StatusExMsg }

func (*EtcdAddMemberPacket) Name() string { return "EtcdAddMember" }
func (*EtcdAddMemberPacket) Kind() byte   { return EtcdAddMemberMsg }

func (*EtcdClusterPacket) Name() string { return "EtcdCluster" }
func (*EtcdClusterPacket) Kind() byte   { return EtcdClusterMsg }

// --- meta/69 replay-protected packets (M12) ---
//
// These carry a per-connection monotonic Nonce and a unix-seconds Timestamp,
// checked on receipt to reject replayed/stale frames. They are used only on
// meta/69 links; meta/66 and meta/68 keep the unprotected packets above. The
// message codes are unchanged — the wire shape is disambiguated by the
// negotiated protocol version (version-keyed handler maps + send paths).

// GetStatusEx69Packet is the meta/69 GetStatusEx request.
type GetStatusEx69Packet struct {
	Nonce     uint64
	Timestamp uint64
}

// StatusEx69Packet is the meta/69 StatusEx response.
type StatusEx69Packet struct {
	Nonce     uint64
	Timestamp uint64
	Status    metaapi.MetadiumMinerStatus
}

// EtcdAddMember69Packet is the meta/69 EtcdAddMember request.
type EtcdAddMember69Packet struct {
	Nonce     uint64
	Timestamp uint64
}

// EtcdCluster69Packet is the meta/69 EtcdCluster message.
type EtcdCluster69Packet struct {
	Nonce     uint64
	Timestamp uint64
	Cluster   string
}
