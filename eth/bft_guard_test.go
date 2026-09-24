package eth

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/params"
)

func TestCheckBftNode(t *testing.T) {
	bft := &params.ChainConfig{
		ChainID:       big.NewInt(638200003),
		CamelliaBlock: big.NewInt(0),
		BftBlock:      big.NewInt(17280),
		Bft:           &params.BftConfig{EmptyBlockInterval: 5, BaseTimeout: 2, MaxBackoffExp: 5, TimeDrift: 2},
	}
	broken := *bft
	broken.Bft = nil

	for _, tt := range []struct {
		name   string
		config *params.ChainConfig
		method int
		is     error  // errors.Is target
		want   string // substring otherwise; empty with nil is means accepted
	}{
		{"mainnet", params.MetadiumMainnetChainConfig, params.ConsensusPoA, nil, ""},
		{"testnet", params.MetadiumTestnetChainConfig, params.ConsensusPoA, nil, ""},
		{"PoW dev chain", params.AllEthashProtocolChanges, params.ConsensusPoW, nil, ""},
		{"PBFT chain on PoA", bft, params.ConsensusPoA, errBftNotImplemented, ""},
		{"PBFT chain on PoW", bft, params.ConsensusPoW, nil, "--consensusmethod 2"},
		{"PBFT chain with invalid parameters", &broken, params.ConsensusPoA, nil, "parameters are missing"},
	} {
		err := checkBftNode(tt.config, tt.method)
		switch {
		case tt.is != nil:
			if !errors.Is(err, tt.is) {
				t.Errorf("%s: got %v, want %v", tt.name, err, tt.is)
			}
		case tt.want == "":
			if err != nil {
				t.Errorf("%s: rejected: %v", tt.name, err)
			}
		case err == nil || !strings.Contains(err.Error(), tt.want):
			t.Errorf("%s: got %v, want an error mentioning %q", tt.name, err, tt.want)
		}
	}
}
