package main

import (
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
)

func TestCheckGenesisChainID(t *testing.T) {
	for _, tt := range []struct {
		name     string
		id       *big.Int
		wantErr  bool
		wantWarn string
	}{
		{"scheme ID", big.NewInt(638200421), false, ""},
		{"missing", nil, false, "no chainId"},
		{"zero placeholder", big.NewInt(0), true, ""},
		{"negative", big.NewInt(-1), true, ""},
		{"Metadium mainnet", big.NewInt(11), false, "Metadium mainnet"},
		{"Metadium testnet", big.NewInt(12), false, "Metadium testnet"},
		{"dev default", big.NewInt(1337), false, "development default"},
		{"beyond uint64", new(big.Int).Lsh(big.NewInt(1), 70), false, ""},
	} {
		warn, err := checkGenesisChainID(tt.id)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: err = %v, want error %v", tt.name, err, tt.wantErr)
		}
		if tt.wantWarn == "" && warn != "" {
			t.Errorf("%s: unexpected warning %q", tt.name, warn)
		}
		if tt.wantWarn != "" && !strings.Contains(warn, tt.wantWarn) {
			t.Errorf("%s: warning %q does not mention %q", tt.name, warn, tt.wantWarn)
		}
	}
}

// loadTemplate decodes the shipped genesis template the way genGenesis does.
func loadTemplate(t *testing.T) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile("../../metadium/scripts/genesis-template.json")
	if err != nil {
		t.Fatal(err)
	}
	var genesis map[string]interface{}
	if err := json.Unmarshal(data, &genesis); err != nil {
		t.Fatal(err)
	}
	return genesis
}

func TestApplyGenesisChainID(t *testing.T) {
	// The shipped template carries a placeholder, so a data file without a
	// chain ID is refused rather than silently producing chain 11.
	if _, err := applyGenesisChainID(loadTemplate(t), nil); !errors.Is(err, errZeroChainID) {
		t.Fatalf("template without a chain ID in the data file: got %v, want %v", err, errZeroChainID)
	}

	// The data file's chain ID lands in the output, exactly.
	genesis := loadTemplate(t)
	if warn, err := applyGenesisChainID(genesis, big.NewInt(638200421)); err != nil || warn != "" {
		t.Fatalf("scheme chain ID: warn %q, err %v", warn, err)
	}
	out, err := json.Marshal(genesis)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"chainId":638200421`) {
		t.Errorf("generated genesis does not carry the chain ID: %s", out)
	}

	// A template that already has a real chain ID keeps working without one
	// in the data file.
	genesis = loadTemplate(t)
	genesis["config"].(map[string]interface{})["chainId"] = json.Number("638200003")
	if _, err := applyGenesisChainID(genesis, nil); err != nil {
		t.Errorf("template with its own chain ID: %v", err)
	}

	// A public chain ID is a warning, not an error.
	if warn, err := applyGenesisChainID(loadTemplate(t), big.NewInt(11)); err != nil || !strings.Contains(warn, "Metadium mainnet") {
		t.Errorf("mainnet chain ID: warn %q, err %v", warn, err)
	}

	if _, err := applyGenesisChainID(map[string]interface{}{}, big.NewInt(638200003)); err == nil {
		t.Error("a template without a config section was accepted")
	}
}

// TestConfigExampleChainID keeps the shipped example data file usable with
// the placeholder template.
func TestConfigExampleChainID(t *testing.T) {
	f, err := os.Open("../../metadium/scripts/config.json.example")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	config, err := loadGenesisConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	if warn, err := applyGenesisChainID(loadTemplate(t), config.ChainID); err != nil || warn != "" {
		t.Errorf("example data file with the template: warn %q, err %v", warn, err)
	}
}
