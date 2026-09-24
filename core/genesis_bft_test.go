package core

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

// TestGenesisCommitChecksBft covers the `init` path: a PBFT genesis is written
// only if its PBFT settings are valid (docs/pbft-consensus-design.md §8.1).
func TestGenesisCommitChecksBft(t *testing.T) {
	config := func(bftBlock int64, bft *params.BftConfig) *params.ChainConfig {
		c := *params.TestChainConfig
		c.CamelliaBlock = big.NewInt(0)
		c.BftBlock = big.NewInt(bftBlock)
		c.Bft = bft
		return &c
	}
	valid := &params.BftConfig{EmptyBlockInterval: 5, BaseTimeout: 2, MaxBackoffExp: 5, TimeDrift: 2}

	for _, tt := range []struct {
		name   string
		config *params.ChainConfig
		want   string
	}{
		{"valid PBFT genesis", config(17280, valid), ""},
		{"switch at genesis", config(0, valid), "greater than 0"},
		{"no parameters", config(17280, nil), "parameters are missing"},
	} {
		db := rawdb.NewMemoryDatabase()
		g := &Genesis{Config: tt.config, BaseFee: big.NewInt(params.InitialBaseFee)}
		_, err := g.Commit(db, triedb.NewDatabase(db, nil))
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s: rejected: %v", tt.name, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%s: got %v, want an error mentioning %q", tt.name, err, tt.want)
		}
	}
}
