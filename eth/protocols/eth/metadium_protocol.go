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
