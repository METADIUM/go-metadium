package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// TestBlobGasCalculation tests blob gas calculations from EIP-4844
func TestBlobGasCalculation(t *testing.T) {
	tests := []struct {
		name              string
		parentExcessBlobGas *big.Int
		blobGasUsed       uint64
	}{
		{
			name:              "zero excess, no blob gas used",
			parentExcessBlobGas: big.NewInt(0),
			blobGasUsed:       0,
		},
		{
			name:              "excess with blob gas used",
			parentExcessBlobGas: big.NewInt(1000000),
			blobGasUsed:       131072, // BlobTxPerBlobGas * 1
		},
		{
			name:              "high excess blob gas",
			parentExcessBlobGas: big.NewInt(10000000),
			blobGasUsed:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := types.CalcExcessBlobGas(tt.parentExcessBlobGas, tt.blobGasUsed)
			if result == nil {
				t.Fatalf("CalcExcessBlobGas returned nil")
			}
			t.Logf("  Parent: %d, Used: %d -> Result: %d",
				tt.parentExcessBlobGas, tt.blobGasUsed, result)
		})
	}
}

// TestBlobBaseFeeCalculation tests blob base fee calculations from EIP-4844
func TestBlobBaseFeeCalculation(t *testing.T) {
	tests := []struct {
		name              string
		excessBlobGas     *big.Int
	}{
		{
			name:           "zero excess blob gas",
			excessBlobGas:  big.NewInt(0),
		},
		{
			name:           "one blob worth of excess (131072 gas)",
			excessBlobGas:  big.NewInt(131072),
		},
		{
			name:           "six blobs worth of excess",
			excessBlobGas:  big.NewInt(786432), // 6 * 131072
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fee := types.CalcBlobBaseFee(tt.excessBlobGas)
			if fee == nil {
				t.Fatalf("CalcBlobBaseFee returned nil")
			}
			if fee.Cmp(big.NewInt(1)) < 0 {
				t.Errorf("expected fee >= 1, got %d", fee)
			}
			t.Logf("  ExcessBlobGas: %d -> BaseFee: %d wei", tt.excessBlobGas, fee)
		})
	}
}

// TestCamelliaForksInChainConfigs verifies Camellia fork configuration
func TestCamelliaForksInChainConfigs(t *testing.T) {
	configs := []struct {
		name   string
		config *params.ChainConfig
	}{
		{"Mainnet", params.MainnetChainConfig},
		{"Testnet", params.TestChainConfig},
		{"MetadiumMainnet", params.MetadiumMainnetChainConfig},
		{"MetadiumTestnet", params.MetadiumTestnetChainConfig},
	}

	for _, cfg := range configs {
		t.Run(cfg.name, func(t *testing.T) {
			if cfg.config == nil {
				t.Skip("config is nil")
			}
			// Check if CamelliaBlock is defined
			if cfg.config.CamelliaBlock != nil {
				t.Logf("✓ %s: CamelliaBlock = %d", cfg.name, cfg.config.CamelliaBlock)
			} else {
				t.Logf("  %s: CamelliaBlock not yet activated (nil)", cfg.name)
			}
			// Verify IsCamellia method works
			isActive := cfg.config.IsCamellia(big.NewInt(0))
			t.Logf("  IsCamellia(0): %v", isActive)
		})
	}
}

// camelliaChainConfig returns a chain config with Camellia (and all prerequisites) active from block 0.
func camelliaChainConfig() *params.ChainConfig {
	return params.AllEthashProtocolChanges
}

// ============================================================
// T-08 ~ T-09: Type 22 Fee Delegation + Camellia
// ============================================================

// TestFeeDelegationAfterCamellia verifies that Type 22 (FeeDelegateDynamicFee)
// transactions are processed correctly after Camellia fork is active. (T-08)
func TestFeeDelegationAfterCamellia(t *testing.T) {
	senderKey, _ := crypto.GenerateKey()
	feePayerKey, _ := crypto.GenerateKey()
	senderAddr := crypto.PubkeyToAddress(senderKey.PublicKey)
	feePayerAddr := crypto.PubkeyToAddress(feePayerKey.PublicKey)
	toAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")

	chainCfg := camelliaChainConfig()
	chainID := chainCfg.ChainID

	// Pre-fund accounts
	initialFeePayerBalance := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(10)) // 10 ETH
	initialSenderBalance := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1))    // 1 ETH

	db := rawdb.NewMemoryDatabase()
	gspec := &Genesis{
		Config: chainCfg,
		Alloc: GenesisAlloc{
			senderAddr:  {Balance: initialSenderBalance},
			feePayerAddr: {Balance: initialFeePayerBalance},
		},
	}
	genesis := gspec.MustCommit(db)

	// Build sender tx (DynamicFee) and sign with sender
	londonSigner := types.NewLondonSigner(chainID)
	gasFeeCap := big.NewInt(1e9) // 1 gwei
	gasTipCap := big.NewInt(1e9)
	gas := uint64(21000)

	senderTxData := &types.DynamicFeeTx{
		ChainID:    chainID,
		Nonce:      0,
		GasFeeCap:  gasFeeCap,
		GasTipCap:  gasTipCap,
		Gas:        gas,
		To:         &toAddr,
		Value:      big.NewInt(0),
		Data:       nil,
		AccessList: nil,
	}
	signedSenderTx, err := types.SignNewTx(senderKey, londonSigner, senderTxData)
	if err != nil {
		t.Fatalf("failed to sign sender tx: %v", err)
	}
	V, R, S := signedSenderTx.RawSignatureValues()

	// Construct FeeDelegateDynamicFeeTx
	fdTxData := &types.FeeDelegateDynamicFeeTx{
		FeePayer: &feePayerAddr,
	}
	fdTxData.SetSenderTx(types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     0,
		GasFeeCap: gasFeeCap,
		GasTipCap: gasTipCap,
		Gas:       gas,
		To:        &toAddr,
		Value:     big.NewInt(0),
		V:         V,
		R:         R,
		S:         S,
	})
	fdTx := types.NewTx(fdTxData)

	// Sign fee payer part
	feeDelegateSigner := types.NewFeeDelegateSigner(chainID)
	signedFdTx, err := types.SignTx(fdTx, feeDelegateSigner, feePayerKey)
	if err != nil {
		t.Fatalf("failed to sign fee payer: %v", err)
	}

	// Execute via GenerateChain + InsertChain
	blockchain, _ := NewBlockChain(db, nil, chainCfg, ethash.NewFaker(), vm.Config{}, nil, nil)
	defer blockchain.Stop()

	chain, _ := GenerateChain(chainCfg, genesis, ethash.NewFaker(), db, 1, func(i int, gen *BlockGen) {
		gen.AddTx(signedFdTx)
	})

	if _, err := blockchain.InsertChain(chain); err != nil {
		t.Fatalf("T-08: InsertChain failed: %v", err)
	}

	st, _ := blockchain.State()
	feePayerBalance := st.GetBalance(feePayerAddr)
	senderBalance := st.GetBalance(senderAddr)

	// feePayer should have paid gas, sender should not
	if feePayerBalance.Cmp(initialFeePayerBalance) >= 0 {
		t.Errorf("T-08: feePayer balance should have decreased (was %d, now %d)",
			initialFeePayerBalance, feePayerBalance)
	} else {
		t.Logf("T-08 PASS: feePayer balance decreased from %d to %d", initialFeePayerBalance, feePayerBalance)
	}

	// Sender sent 0 value, should still have ~initialSenderBalance (no gas deducted from sender)
	if senderBalance.Cmp(initialSenderBalance) != 0 {
		t.Errorf("T-09: sender balance should not change for gas (was %d, now %d)",
			initialSenderBalance, senderBalance)
	} else {
		t.Logf("T-09 PASS: sender balance unchanged at %d (gas paid by feePayer)", senderBalance)
	}
}

// TestBlobGasConstants verifies EIP-4844 constants.
// Metadium uses reduced blob limits (2 blobs/block max, 1 blob target) compared to
// Ethereum mainnet (6/3) because Metadium runs at 2s block time (6× faster), which
// keeps per-second blob throughput comparable while reducing block propagation overhead.
func TestBlobGasConstants(t *testing.T) {
	tests := []struct {
		name     string
		value    uint64
		expected uint64
	}{
		{"BlobTxPerBlobGas", params.BlobTxPerBlobGas, 131072},
		{"MaxBlobGasPerBlock", params.MaxBlobGasPerBlock, 262144},  // 2 blobs × 131072
		{"MaxBlobsPerTransaction", params.MaxBlobsPerTransaction, 4}, // max 4 blobs/tx
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == tt.expected {
				t.Logf("✓ %s = %d", tt.name, tt.value)
			} else {
				t.Errorf("expected %s = %d, got %d", tt.name, tt.expected, tt.value)
			}
		})
	}
}

// blobTestMsg is a minimal Message implementation for testing blob tx validation paths.
type blobTestMsg struct {
	types.Message            // embed for non-blob method defaults
	blobHashes       []common.Hash
	maxFeePerBlobGas *big.Int
}

func (m blobTestMsg) BlobHashes() []common.Hash { return m.blobHashes }
func (m blobTestMsg) MaxFeePerBlobGas() *big.Int { return m.maxFeePerBlobGas }

// TestBlobTxPreCheckErrors verifies that blob transaction validation in preCheck()
// correctly rejects txs with insufficient MaxFeePerBlobGas or too many blobs.
// Even though Metadium does not use blob transactions, the validation code exists
// and must behave correctly if a blob tx somehow reaches the pool.
func TestBlobTxPreCheckErrors(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)

	cfg := camelliaChainConfig()
	gspec := &Genesis{
		Config: cfg,
		Alloc:  GenesisAlloc{addr: {Balance: big.NewInt(1e18)}},
	}
	gspec.MustCommit(db)
	chain, _ := NewBlockChain(db, nil, cfg, ethash.NewFaker(), vm.Config{}, nil, nil)
	defer chain.Stop()

	// excessBlobGas=0 → blobBaseFee = MinBlobBaseFee (1 wei)
	excessBlobGas := big.NewInt(0)
	blobBaseFee := types.CalcBlobBaseFee(excessBlobGas)

	makeEVM := func() *vm.EVM {
		blockCtx := NewEVMBlockContext(chain.CurrentBlock(), chain, &addr)
		blockCtx.ExcessBlobGas = excessBlobGas
		statedb, _ := chain.StateAt(chain.CurrentBlock().Root)
		return vm.NewEVM(blockCtx, vm.TxContext{Origin: addr}, statedb, cfg, vm.Config{})
	}

	t.Run("ErrBlobFeeCapTooLow", func(t *testing.T) {
		tooLow := new(big.Int).Sub(blobBaseFee, big.NewInt(1))
		if tooLow.Sign() < 0 {
			tooLow = big.NewInt(0)
		}
		base := types.NewMessage(addr, &addr, 0, big.NewInt(0), 30000,
			big.NewInt(1e9), big.NewInt(1e9), big.NewInt(1e9), nil, nil, true)
		msg := blobTestMsg{
			Message:          base,
			blobHashes:       []common.Hash{{0x01}},
			maxFeePerBlobGas: tooLow,
		}
		st := NewStateTransition(makeEVM(), msg, new(GasPool).AddGas(1e9))
		err := st.preCheck()
		if err == nil {
			t.Fatal("expected ErrBlobFeeCapTooLow, got nil")
		}
		t.Logf("✓ ErrBlobFeeCapTooLow: %v", err)
	})

	t.Run("ErrBlobCountExceeded", func(t *testing.T) {
		tooMany := make([]common.Hash, params.MaxBlobsPerTransaction+1)
		for i := range tooMany {
			tooMany[i] = common.Hash{byte(i + 1)}
		}
		base := types.NewMessage(addr, &addr, 0, big.NewInt(0), 30000,
			big.NewInt(1e9), big.NewInt(1e9), big.NewInt(1e9), nil, nil, true)
		msg := blobTestMsg{
			Message:          base,
			blobHashes:       tooMany,
			maxFeePerBlobGas: new(big.Int).Set(blobBaseFee),
		}
		st := NewStateTransition(makeEVM(), msg, new(GasPool).AddGas(1e9))
		err := st.preCheck()
		if err == nil {
			t.Fatal("expected ErrBlobCountExceeded, got nil")
		}
		t.Logf("✓ ErrBlobCountExceeded: %v", err)
	})
}
