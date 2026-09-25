// Copyright 2026 The go-metadium Authors
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

package miner

import (
	"crypto/ecdsa"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/consensus/metabft"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
	"github.com/ethereum/go-ethereum/params"
)

// fakeProposer stands in for the consensus node.
type fakeProposer struct {
	wanted    atomic.Bool
	submitted chan *types.Block
}

func (p *fakeProposer) ProposalWanted(height uint64, pendingTxs bool) bool {
	return height == 1 && p.wanted.Load()
}

func (p *fakeProposer) SubmitBlock(b *types.Block) error {
	p.submitted <- b
	return nil
}

func bftWorkerConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:        big.NewInt(1337),
		HomesteadBlock: big.NewInt(0), EIP150Block: big.NewInt(0), EIP155Block: big.NewInt(0), EIP158Block: big.NewInt(0),
		ByzantiumBlock: big.NewInt(0), ConstantinopleBlock: big.NewInt(0), PetersburgBlock: big.NewInt(0), IstanbulBlock: big.NewInt(0),
		CamelliaBlock: big.NewInt(0),
		BftBlock:      big.NewInt(1),
		Bft:           &params.BftConfig{EmptyBlockInterval: 5, BaseTimeout: 2, MaxBackoffExp: 5, TimeDrift: 2},
	}
}

// newBftWorkerEnv sets up a PBFT engine with metadium's hooks stubbed:
// validator 1 builds, with a governance coinbase unlike its etherbase, and
// the PoA hooks count their calls in poaGates.
func newBftWorkerEnv(t *testing.T, poaGates *atomic.Int32) (*metabft.Engine, *fakeProposer) {
	t.Helper()
	oldMethod := params.ConsensusMethod
	params.ConsensusMethod = params.ConsensusPoA
	t.Cleanup(func() { params.ConsensusMethod = oldMethod })

	keys := make([]*ecdsa.PrivateKey, 4)
	pubs := make([][]byte, 4)
	coinbases := make([]common.Address, 4)
	for i := range keys {
		keys[i], _ = crypto.GenerateKey()
		pubs[i] = crypto.FromECDSAPub(&keys[i].PublicKey)[1:]
		coinbases[i] = common.Address{0xc0 + byte(i)}
	}
	set, err := metabft.NewValidatorSet(pubs)
	if err != nil {
		t.Fatal(err)
	}
	if set, err = set.WithCoinbases(coinbases); err != nil {
		t.Fatal(err)
	}

	oldSign, oldLog, oldToken, oldIsMiner, oldCoinbase := metaminer.SignBlockFunc, metaminer.LogBlockFunc,
		metaminer.AcquireMiningTokenFunc, metaminer.IsMinerFunc, metaminer.GetCoinbaseFunc
	t.Cleanup(func() {
		metaminer.SignBlockFunc, metaminer.LogBlockFunc, metaminer.AcquireMiningTokenFunc = oldSign, oldLog, oldToken
		metaminer.IsMinerFunc, metaminer.GetCoinbaseFunc = oldIsMiner, oldCoinbase
	})
	// The node's governance coinbase differs from its etherbase, as it may
	// in production; the transactions must still run against the former.
	metaminer.GetCoinbaseFunc = func(*big.Int) (common.Address, error) { return coinbases[1], nil }
	metaminer.SignBlockFunc = func(height *big.Int, hash common.Hash, isPangyo bool) (common.Address, []byte, []byte, error) {
		sig, err := crypto.Sign(ethash.BftBuilderSigHash(height, hash), keys[1])
		return coinbases[1], nil, sig, err
	}
	metaminer.LogBlockFunc = func(int64, common.Hash) { poaGates.Add(1) }
	metaminer.AcquireMiningTokenFunc = func(*big.Int, common.Hash) (bool, error) { poaGates.Add(1); return true, nil }
	metaminer.IsMinerFunc = func() bool { poaGates.Add(1); return true }

	engine := metabft.NewEngine(ethash.NewFaker(), func(uint64) (*metabft.ValidatorSet, error) { return set, nil })
	proposer := &fakeProposer{submitted: make(chan *types.Block, 16)}
	engine.SetProposer(proposer)

	engine.SetNodeCount(func(consensus.ChainHeaderReader, consensus.Engine, *types.Header, *state.StateDB) (uint64, error) {
		return 4, nil
	})
	return engine, proposer
}

// TestWorkerProposesAtPBFTHeight: on a PBFT chain the worker builds only
// when the consensus node wants a block, without the PoA miner and token
// gates, hands the block to the node instead of writing it, and the block
// passes the validators' proposal checks.
func TestWorkerProposesAtPBFTHeight(t *testing.T) {
	var poaGates atomic.Int32
	engine, proposer := newBftWorkerEnv(t, &poaGates)

	w, b := newTestWorker(t, bftWorkerConfig(), engine, rawdb.NewMemoryDatabase(), 0)
	defer w.close()
	w.start()

	select {
	case blk := <-proposer.submitted:
		t.Fatalf("block %d built while the node wanted none", blk.NumberU64())
	case <-time.After(1500 * time.Millisecond):
	}

	tx, err := types.SignTx(types.NewTransaction(b.txPool.Nonce(testBankAddress), testUserAddress, big.NewInt(1000), params.TxGas,
		big.NewInt(params.InitialBaseFee), nil), types.LatestSigner(bftWorkerConfig()), testBankKey)
	if err != nil {
		t.Fatal(err)
	}
	if errs := b.txPool.Add([]*types.Transaction{tx}, true, true); errs[0] != nil {
		t.Fatalf("adding a transaction: %v", errs[0])
	}
	before := time.Now()
	proposer.wanted.Store(true)
	engine.WakeProposer()
	var blk *types.Block
	select {
	case blk = <-proposer.submitted:
	case <-time.After(5 * time.Second):
		t.Fatal("no block proposed once wanted")
	}
	genesis := b.chain.Genesis()
	switch {
	case blk.NumberU64() != 1 || blk.ParentHash() != genesis.Hash():
		t.Fatalf("proposed block %d on %x", blk.NumberU64(), blk.ParentHash())
	case blk.Time() < uint64(before.Unix()) || blk.Time() > uint64(time.Now().Unix()) || blk.Time() < genesis.Time():
		t.Errorf("timestamp %d, want max(parent, now) around %d", blk.Time(), before.Unix())
	case len(blk.Transactions()) == 0:
		t.Error("pending transactions not included")
	}
	if head := b.chain.CurrentBlock().Number.Uint64(); head != 0 {
		t.Errorf("the worker wrote the proposal itself: head %d", head)
	}
	if n := poaGates.Load(); n != 0 {
		t.Errorf("PoA miner, token or work-log hooks called %d times at a PBFT height", n)
	}
	chain := metabft.NewBlockChain(b.chain, engine, nil)
	if err := chain.VerifyBlock(blk, true); err != nil {
		t.Errorf("the proposal fails the validators' checks: %v", err)
	}
}

// TestWorkerLeavesOutValidatorFloorBreach: a transaction whose result would
// leave governance with fewer than four nodes is left out of the proposal
// (design §9.3.1); the block is proposed without it and still valid.
func TestWorkerLeavesOutValidatorFloorBreach(t *testing.T) {
	var poaGates atomic.Int32
	engine, proposer := newBftWorkerEnv(t, &poaGates)
	// Stand-in for a governance removal: once testUserAddress holds funds,
	// the state has three nodes.
	engine.SetNodeCount(func(_ consensus.ChainHeaderReader, _ consensus.Engine, _ *types.Header, statedb *state.StateDB) (uint64, error) {
		if statedb.GetBalance(testUserAddress).Sign() > 0 {
			return 3, nil
		}
		return 4, nil
	})
	w, b := newTestWorker(t, bftWorkerConfig(), engine, rawdb.NewMemoryDatabase(), 0)
	defer w.close()
	tx, err := types.SignTx(types.NewTransaction(b.txPool.Nonce(testBankAddress), testUserAddress, big.NewInt(1000), params.TxGas,
		big.NewInt(params.InitialBaseFee), nil), types.LatestSigner(bftWorkerConfig()), testBankKey)
	if err != nil {
		t.Fatal(err)
	}
	if errs := b.txPool.Add([]*types.Transaction{tx}, true, true); errs[0] != nil {
		t.Fatal(errs[0])
	}
	w.start()
	proposer.wanted.Store(true)
	engine.WakeProposer()
	var blk *types.Block
	select {
	case blk = <-proposer.submitted:
	case <-time.After(5 * time.Second):
		t.Fatal("no block proposed")
	}
	if len(blk.Transactions()) != 0 || blk.GasUsed() != 0 || blk.Fees().Sign() != 0 {
		t.Fatalf("proposal has %d txs, gas %d, fees %v; want the breaching transaction left out", len(blk.Transactions()), blk.GasUsed(), blk.Fees())
	}
	if err := metabft.NewBlockChain(b.chain, engine, nil).VerifyBlock(blk, true); err != nil {
		t.Errorf("the proposal without it: %v", err)
	}
}
