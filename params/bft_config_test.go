package params

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

// validBftConfig is the smallest PBFT-enabled config checkBft accepts.
func validBftConfig() *ChainConfig {
	return &ChainConfig{
		ChainID:       big.NewInt(638200003),
		CamelliaBlock: big.NewInt(0),
		BftBlock:      big.NewInt(17280),
		Bft: &BftConfig{
			EmptyBlockInterval: 5,
			BaseTimeout:        2,
			MaxBackoffExp:      5,
			TimeDrift:          2,
		},
	}
}

func TestCheckBft(t *testing.T) {
	for _, tt := range []struct {
		name   string
		modify func(c *ChainConfig)
		want   string // substring of the error; empty means accepted
	}{
		{"valid", func(c *ChainConfig) {}, ""},
		{"no PBFT at all", func(c *ChainConfig) { c.BftBlock, c.Bft = nil, nil }, ""},
		{"camellia at the switch block", func(c *ChainConfig) { c.CamelliaBlock = new(big.Int).Set(c.BftBlock) }, ""},
		{"max backoff at the cap", func(c *ChainConfig) { c.Bft.MaxBackoffExp = maxBftBackoffExp }, ""},
		{"zero max backoff", func(c *ChainConfig) { c.Bft.MaxBackoffExp = 0 }, ""},

		{"parameters without switch block", func(c *ChainConfig) { c.BftBlock = nil }, "without bftBlock"},
		{"switch at genesis", func(c *ChainConfig) { c.BftBlock = big.NewInt(0) }, "greater than 0"},
		{"negative switch block", func(c *ChainConfig) { c.BftBlock = big.NewInt(-1) }, "greater than 0"},
		{"no camellia", func(c *ChainConfig) { c.CamelliaBlock = nil }, "camelliaBlock"},
		{"camellia after the switch", func(c *ChainConfig) { c.CamelliaBlock = big.NewInt(17281) }, "camelliaBlock"},
		{"switch block without parameters", func(c *ChainConfig) { c.Bft = nil }, "parameters are missing"},
		{"zero empty-block interval", func(c *ChainConfig) { c.Bft.EmptyBlockInterval = 0 }, "emptyBlockInterval"},
		{"zero base timeout", func(c *ChainConfig) { c.Bft.BaseTimeout = 0 }, "baseTimeout"},
		{"zero time drift", func(c *ChainConfig) { c.Bft.TimeDrift = 0 }, "timeDrift"},
		{"max backoff over the cap", func(c *ChainConfig) { c.Bft.MaxBackoffExp = maxBftBackoffExp + 1 }, "maxBackoffExp"},
	} {
		c := validBftConfig()
		tt.modify(c)
		// Both entry points: CheckConfigForkOrder is what init and startup call.
		for _, err := range []error{c.checkBft(), c.CheckConfigForkOrder()} {
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("%s: rejected: %v", tt.name, err)
			case tt.want != "" && err == nil:
				t.Errorf("%s: accepted, want an error mentioning %q", tt.name, tt.want)
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Errorf("%s: error %q does not mention %q", tt.name, err, tt.want)
			}
		}
	}
}

func TestIsBft(t *testing.T) {
	c := validBftConfig()
	for _, tt := range []struct {
		num  int64
		want bool
	}{{0, false}, {17279, false}, {17280, true}, {17281, true}} {
		if got := c.IsBft(big.NewInt(tt.num)); got != tt.want {
			t.Errorf("IsBft(%d) = %v, want %v", tt.num, got, tt.want)
		}
	}
	if (&ChainConfig{}).IsBft(big.NewInt(1 << 40)) {
		t.Error("a config without bftBlock reports PBFT")
	}
}

func TestCheckCompatibleBft(t *testing.T) {
	stored := validBftConfig()

	moved := validBftConfig()
	moved.BftBlock = big.NewInt(20000)

	retuned := validBftConfig()
	retuned.Bft.BaseTimeout = 3

	for _, tt := range []struct {
		name    string
		newcfg  *ChainConfig
		head    uint64
		wantErr bool
	}{
		{"same config past the switch", validBftConfig(), 50000, false},
		{"switch block moved before either is reached", moved, 100, false},
		{"switch block moved after the old one is reached", moved, 17280, true},
		{"parameters changed before the switch", retuned, 17279, false},
		{"parameters changed at the switch", retuned, 17280, true},
	} {
		err := stored.CheckCompatible(tt.newcfg, tt.head, 0)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: CheckCompatible = %v, want error %v", tt.name, err, tt.wantErr)
		}
		if err != nil && err.RewindToBlock >= stored.BftBlock.Uint64() {
			t.Errorf("%s: rewinds to %d, want below the switch block %v", tt.name, err.RewindToBlock, stored.BftBlock)
		}
	}
}

func TestBftConfigJSON(t *testing.T) {
	const doc = `{"chainId":638200421,"camelliaBlock":0,"bftBlock":17280,` +
		`"bft":{"emptyBlockInterval":5,"baseTimeout":2,"maxBackoffExp":5,"timeDrift":2}}`
	var c ChainConfig
	if err := json.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckConfigForkOrder(); err != nil {
		t.Fatalf("the design's example genesis config is rejected: %v", err)
	}
	if c.BftBlock.Int64() != 17280 || *c.Bft != *validBftConfig().Bft {
		t.Fatalf("decoded %v / %+v", c.BftBlock, c.Bft)
	}
	if !strings.Contains(c.Description(), "PBFT switch") {
		t.Error("the startup banner does not show the PBFT switch")
	}
	// An invalid config (switch block without parameters) must not panic
	// the banner (review on #144).
	broken := validBftConfig()
	broken.Bft = nil
	if !strings.Contains(broken.Description(), "<nil>") {
		t.Error("banner of a config without bft parameters")
	}

	// Configs without PBFT must encode exactly as before.
	out, err := json.Marshal(&ChainConfig{ChainID: big.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "bft") {
		t.Errorf("a non-PBFT config gained PBFT fields: %s", out)
	}
}
