package metadium

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/metabft"
	"github.com/ethereum/go-ethereum/metadium/metclient"
)

// TestBftGovernanceSignatures: the PBFT validator floor reads governance by
// raw EVM calls (metabft.GovernanceNodeCount); their signatures must be the
// contracts' own, for both governance versions.
func TestBftGovernanceSignatures(t *testing.T) {
	for _, tt := range []struct {
		name, abi, method, want string
	}{
		{"registry", RegistryAbi, "getContractAddress", metabft.RegistryLookupSig},
		{"governance", GovAbi, "getNodeLength", metabft.NodeLengthSig},
		{"legacy governance", GovLegacyAbi, "getNodeLength", metabft.NodeLengthSig},
	} {
		c, err := metclient.LoadJsonContract(strings.NewReader(tt.abi))
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		m, ok := c.Abi.Methods[tt.method]
		if !ok {
			t.Fatalf("%s has no %s", tt.name, tt.method)
		}
		if m.Sig != tt.want {
			t.Errorf("%s: %s, metabft calls %s", tt.name, m.Sig, tt.want)
		}
	}
}
