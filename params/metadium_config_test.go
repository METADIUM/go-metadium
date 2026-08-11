// metadium_config_test.go -- Metadium fork guard.
//
// Every fork block in the built-in Metadium chain configs is consensus- or
// upgrade-critical state that a go-ethereum rebase can silently revert (the
// v1.13.14 rebase nil'd testnet PetersburgBlock, which rewinds upgrading
// 0.10.x nodes by ~80M blocks, and CheckConfigForkOrder is stubbed out in
// this fork). This table pins every field to its historical value so the
// next such drift is a CI failure instead of a production discovery.

package params

import (
	"math/big"
	"testing"
)

func TestMetadiumChainConfigsPinned(t *testing.T) {
	for _, network := range []struct {
		name    string
		config  *ChainConfig
		forks   map[string]*big.Int
		daoFork bool
	}{
		{
			name:   "mainnet",
			config: MetadiumMainnetChainConfig,
			forks: map[string]*big.Int{
				"ChainID":             big.NewInt(11),
				"HomesteadBlock":      big.NewInt(0),
				"DAOForkBlock":        big.NewInt(0),
				"EIP150Block":         big.NewInt(11_441_000),
				"EIP155Block":         big.NewInt(0),
				"EIP158Block":         big.NewInt(0),
				"ByzantiumBlock":      big.NewInt(0),
				"ConstantinopleBlock": big.NewInt(11_441_000),
				"PetersburgBlock":     big.NewInt(11_441_000),
				"IstanbulBlock":       big.NewInt(11_441_000),
				"MuirGlacierBlock":    big.NewInt(11_441_000),
				"BerlinBlock":         big.NewInt(51_960_000),
				"LondonBlock":         big.NewInt(51_960_000),
				"AvocadoBlock":        big.NewInt(59_860_000),
				"PangyoBlock":         big.NewInt(73_225_410),
				"ApplepieBlock":       big.NewInt(73_225_410),
				"BokbunjaBlock":       big.NewInt(73_225_410),
				"CamelliaBlock":       big.NewInt(117_764_000),
			},
			daoFork: true,
		},
		{
			name:   "testnet",
			config: MetadiumTestnetChainConfig,
			forks: map[string]*big.Int{
				"ChainID":             big.NewInt(12),
				"HomesteadBlock":      big.NewInt(0),
				"DAOForkBlock":        big.NewInt(0),
				"EIP150Block":         big.NewInt(5_623_000),
				"EIP155Block":         big.NewInt(0),
				"EIP158Block":         big.NewInt(0),
				"ByzantiumBlock":      big.NewInt(0),
				"ConstantinopleBlock": big.NewInt(5_623_000),
				"PetersburgBlock":     big.NewInt(5_623_000),
				"IstanbulBlock":       big.NewInt(5_623_000),
				"MuirGlacierBlock":    big.NewInt(5_623_000),
				"BerlinBlock":         big.NewInt(38_067_000),
				"LondonBlock":         big.NewInt(38_067_000),
				"AvocadoBlock":        big.NewInt(40_759_810),
				"PangyoBlock":         big.NewInt(44_671_396),
				"ApplepieBlock":       big.NewInt(44_671_396),
				"BokbunjaBlock":       big.NewInt(44_671_396),
				"CamelliaBlock":       big.NewInt(86_449_000),
			},
			daoFork: true,
		},
	} {
		c := network.config
		got := map[string]*big.Int{
			"ChainID":             c.ChainID,
			"HomesteadBlock":      c.HomesteadBlock,
			"DAOForkBlock":        c.DAOForkBlock,
			"EIP150Block":         c.EIP150Block,
			"EIP155Block":         c.EIP155Block,
			"EIP158Block":         c.EIP158Block,
			"ByzantiumBlock":      c.ByzantiumBlock,
			"ConstantinopleBlock": c.ConstantinopleBlock,
			"PetersburgBlock":     c.PetersburgBlock,
			"IstanbulBlock":       c.IstanbulBlock,
			"MuirGlacierBlock":    c.MuirGlacierBlock,
			"BerlinBlock":         c.BerlinBlock,
			"LondonBlock":         c.LondonBlock,
			"AvocadoBlock":        c.AvocadoBlock,
			"PangyoBlock":         c.PangyoBlock,
			"ApplepieBlock":       c.ApplepieBlock,
			"BokbunjaBlock":       c.BokbunjaBlock,
			"CamelliaBlock":       c.CamelliaBlock,
		}
		for field, want := range network.forks {
			g := got[field]
			if g == nil || g.Cmp(want) != 0 {
				t.Errorf("%s %s = %v, want %v: a rebase likely dropped or changed a "+
					"Metadium fork value. If this change is intentional, update this "+
					"pin alongside it.", network.name, field, g, want)
			}
		}
		if c.DAOForkSupport != network.daoFork {
			t.Errorf("%s DAOForkSupport = %v, want %v (0.10.x stored-config parity; "+
				"a mismatch masks later fork compatibility checks on upgrade, since "+
				"checkCompatible returns on the first failing check)",
				network.name, c.DAOForkSupport, network.daoFork)
		}
	}
}
