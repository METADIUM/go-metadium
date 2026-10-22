// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package txpool

import (
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// initcodeTestConfig returns a chain config with every block-number fork at
// genesis and no timestamp forks. With camellia set, Camellia is active at
// genesis the way Metadium Mainnet and Testnet run it: IsShanghai (by time) is
// false, IsCamellia (by number) is true. With camellia unset the chain is
// pre-Shanghai in both senses.
func initcodeTestConfig(camellia bool) *params.ChainConfig {
	cfg := &params.ChainConfig{
		ChainID:                       big.NewInt(1337),
		HomesteadBlock:                big.NewInt(0),
		EIP150Block:                   big.NewInt(0),
		EIP155Block:                   big.NewInt(0),
		EIP158Block:                   big.NewInt(0),
		ByzantiumBlock:                big.NewInt(0),
		ConstantinopleBlock:           big.NewInt(0),
		PetersburgBlock:               big.NewInt(0),
		IstanbulBlock:                 big.NewInt(0),
		MuirGlacierBlock:              big.NewInt(0),
		BerlinBlock:                   big.NewInt(0),
		LondonBlock:                   big.NewInt(0),
		AvocadoBlock:                  big.NewInt(0),
		PangyoBlock:                   big.NewInt(0),
		ApplepieBlock:                 big.NewInt(0),
		BokbunjaBlock:                 big.NewInt(0),
		TerminalTotalDifficultyPassed: true,
		Ethash:                        new(params.EthashConfig),
	}
	if camellia {
		cfg.CamelliaBlock = big.NewInt(0)
	}
	return cfg
}

// validateCreation signs a legacy contract-creation transaction carrying data
// with the given gas and runs it through ValidateTransaction against a head at
// block 1 of cfg. maxSize is the pool's own size cap.
func validateCreation(t *testing.T, cfg *params.ChainConfig, data []byte, gas uint64, maxSize uint64) error {
	t.Helper()
	key, _ := crypto.GenerateKey()
	signer := types.LatestSigner(cfg)
	tx := types.MustSignNewTx(key, signer, &types.LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(params.InitialBaseFee),
		Gas:      gas,
		To:       nil,
		Value:    new(big.Int),
		Data:     data,
	})
	head := &types.Header{
		Number:   big.NewInt(1),
		GasLimit: 30_000_000,
		BaseFee:  big.NewInt(params.InitialBaseFee),
	}
	opts := &ValidationOptions{
		Config:  cfg,
		Accept:  1 << types.LegacyTxType,
		MaxSize: maxSize,
		MinTip:  new(big.Int),
	}
	return ValidateTransaction(tx, head, signer, opts)
}

// TestValidateTransactionCamelliaInitcodeGas checks that the pool's intrinsic
// gas floor for a contract creation includes the EIP-3860 initcode word cost on
// a Camellia chain, as execution's does. Before the fix the pool computed the
// pre-Shanghai minimum, so a creation with gas between the two minimums was
// admitted and propagated but could never execute.
func TestValidateTransactionCamelliaInitcodeGas(t *testing.T) {
	data := bytes.Repeat([]byte{0x01}, 64) // 2 initcode words, all non-zero bytes
	preShanghai, err := core.IntrinsicGas(data, nil, nil, true, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	withInitcode, err := core.IntrinsicGas(data, nil, nil, true, true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if withInitcode != preShanghai+2*params.InitCodeWordGas {
		t.Fatalf("test premise: initcode cost = %d, want %d", withInitcode-preShanghai, 2*params.InitCodeWordGas)
	}
	const maxSize = 2 * 1024 * 1024

	// Camellia: the pre-Shanghai minimum is no longer enough.
	camellia := initcodeTestConfig(true)
	if err := validateCreation(t, camellia, data, preShanghai, maxSize); !errors.Is(err, core.ErrIntrinsicGas) {
		t.Errorf("Camellia chain, gas %d (pre-Shanghai minimum): err = %v, want ErrIntrinsicGas", preShanghai, err)
	}
	if err := validateCreation(t, camellia, data, withInitcode, maxSize); errors.Is(err, core.ErrIntrinsicGas) {
		t.Errorf("Camellia chain, gas %d (with initcode words): unexpected %v", withInitcode, err)
	}

	// Pre-fork chain: the old minimum still stands.
	if err := validateCreation(t, initcodeTestConfig(false), data, preShanghai, maxSize); errors.Is(err, core.ErrIntrinsicGas) {
		t.Errorf("pre-Camellia chain, gas %d: unexpected %v", preShanghai, err)
	}
}

// TestValidateTransactionCamelliaInitcodeSize checks that the pool's EIP-3860
// initcode size cap applies on a Camellia chain. The legacypool's own size cap
// (params.MaxTransactionSize) is smaller than MaxInitCodeSize, so on Metadium
// this gate fires only for a pool configured with a larger MaxSize; it still
// has to agree with execution when it does.
func TestValidateTransactionCamelliaInitcodeSize(t *testing.T) {
	data := make([]byte, params.MaxInitCodeSize+1)
	const maxSize = 2 * params.MaxInitCodeSize
	const gas = 10_000_000

	if err := validateCreation(t, initcodeTestConfig(true), data, gas, maxSize); !errors.Is(err, core.ErrMaxInitCodeSizeExceeded) {
		t.Errorf("Camellia chain: err = %v, want ErrMaxInitCodeSizeExceeded", err)
	}
	if err := validateCreation(t, initcodeTestConfig(false), data, gas, maxSize); errors.Is(err, core.ErrMaxInitCodeSizeExceeded) {
		t.Errorf("pre-Camellia chain: unexpected %v", err)
	}
}
