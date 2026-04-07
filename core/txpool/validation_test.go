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
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// TestValidateTransactionCamelliaOnly verifies that blob transactions are accepted
// by the txpool fork gate on a Camellia-only chain (CancunTime=nil, CamelliaBlock=0).
//
// Regression: validation.go:72 — before the fix, Camellia-only chains had no Cancun
// timestamp and blob TXs were always rejected with "pool not yet in Cancun or Camellia".
// This was a mainnet blocker since Metadium mainnet uses CamelliaBlock, not CancunTime.
//
// Found by /autoplan on 2026-04-07
// Report: .gstack/qa-reports/qa-report-go-metadium-2026-04-07.md
func TestValidateTransactionCamelliaOnly(t *testing.T) {
	key, _ := crypto.GenerateKey()
	to := common.HexToAddress("0x000000000000000000000000000000000000dEaD")

	// Camellia-only config: CamelliaBlock=0, CancunTime=nil.
	// This mirrors the Metadium mainnet configuration.
	camelliaOnlyConfig := &params.ChainConfig{
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
		CamelliaBlock:                 big.NewInt(0), // Camellia active at genesis
		CancunTime:                    nil,           // No timestamp-based Cancun
		TerminalTotalDifficultyPassed: true,
		Ethash:                        new(params.EthashConfig),
	}

	head := &types.Header{
		Number:   big.NewInt(1),
		GasLimit: 30_000_000,
		BaseFee:  big.NewInt(params.InitialBaseFee),
	}
	signer := types.LatestSigner(camelliaOnlyConfig)

	blobTx := &types.BlobTx{
		ChainID:          uint256.MustFromBig(camelliaOnlyConfig.ChainID),
		Nonce:            0,
		GasTipCap:        uint256.NewInt(1e9),
		GasFeeCap:        uint256.NewInt(params.InitialBaseFee + 1e9),
		Gas:              21000,
		To:               &to,
		Value:            new(uint256.Int),
		MaxFeePerBlobGas: uint256.NewInt(params.BlobTxMinBlobGasprice),
		BlobHashes:       []common.Hash{{0x01}},
	}
	tx := types.MustSignNewTx(key, signer, blobTx)

	opts := &ValidationOptions{
		Config:  camelliaOnlyConfig,
		Accept:  1<<types.LegacyTxType | 1<<types.AccessListTxType | 1<<types.DynamicFeeTxType | 1<<types.BlobTxType,
		MaxSize: 2 * 1024 * 1024,
		MinTip:  new(big.Int),
	}

	err := ValidateTransaction(tx, head, signer, opts)

	// The Camellia fork gate (validation.go:72) must not reject the TX.
	// Further validation errors (e.g., "missing sidecar") are expected and acceptable.
	if err != nil && strings.Contains(err.Error(), "not yet in Cancun or Camellia") {
		t.Errorf("Camellia-only chain should pass blob TX fork gate: %v", err)
	}

	// Complementary: a chain with neither Cancun nor Camellia must reject blob TXs.
	neitherConfig := &params.ChainConfig{
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
		CamelliaBlock:                 nil, // No Camellia
		CancunTime:                    nil, // No Cancun
		TerminalTotalDifficultyPassed: true,
		Ethash:                        new(params.EthashConfig),
	}
	signer2 := types.LatestSigner(neitherConfig)
	tx2 := types.MustSignNewTx(key, signer2, blobTx)
	optsNeither := &ValidationOptions{
		Config:  neitherConfig,
		Accept:  1<<types.LegacyTxType | 1<<types.AccessListTxType | 1<<types.DynamicFeeTxType | 1<<types.BlobTxType,
		MaxSize: 2 * 1024 * 1024,
		MinTip:  new(big.Int),
	}
	err2 := ValidateTransaction(tx2, head, signer2, optsNeither)
	if err2 == nil || !strings.Contains(err2.Error(), "not yet in Cancun or Camellia") {
		t.Errorf("pre-fork chain should reject blob TX: got %v", err2)
	}
}
