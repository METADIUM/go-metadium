// Copyright 2025 The go-metadium Authors

package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// The private-PoA idle seal flag is refused on the public networks, and that
// refusal is keyed on the genesis hash. Pin the mapping: a wrong or empty
// answer here would either let the flag through on mainnet/testnet or block a
// private chain that is entitled to it.
func TestPublicMetadiumNetwork(t *testing.T) {
	for _, tt := range []struct {
		name    string
		genesis common.Hash
		want    string
	}{
		{"mainnet", params.MetadiumMainnetGenesisHash, "mainnet"},
		{"testnet", params.MetadiumTestnetGenesisHash, "testnet"},
		{"private chain", common.HexToHash("0xdeadbeef"), ""},
		{"zero hash", common.Hash{}, ""},
		{"ethereum mainnet", params.MainnetGenesisHash, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := publicMetadiumNetwork(tt.genesis); got != tt.want {
				t.Fatalf("publicMetadiumNetwork(%s) = %q, want %q", tt.genesis.Hex(), got, tt.want)
			}
		})
	}
}
