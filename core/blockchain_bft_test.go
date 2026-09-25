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

package core

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// bftTestChain is a chain whose config switches to PBFT at bftBlock. The
// rules under test read only the config, so the blocks themselves are
// plain ones from the fake engine.
func bftTestChain(t *testing.T, bftBlock int64) (*BlockChain, *Genesis) {
	t.Helper()
	config := &params.ChainConfig{
		ChainID:        big.NewInt(1337),
		HomesteadBlock: big.NewInt(0), EIP150Block: big.NewInt(0), EIP155Block: big.NewInt(0), EIP158Block: big.NewInt(0),
		ByzantiumBlock: big.NewInt(0), ConstantinopleBlock: big.NewInt(0), PetersburgBlock: big.NewInt(0), IstanbulBlock: big.NewInt(0),
		CamelliaBlock: big.NewInt(0),
		BftBlock:      big.NewInt(bftBlock),
		Bft:           &params.BftConfig{EmptyBlockInterval: 5, BaseTimeout: 2, MaxBackoffExp: 5, TimeDrift: 2},
		Ethash:        new(params.EthashConfig),
	}
	genesis := &Genesis{Config: config, Difficulty: big.NewInt(1), GasLimit: 10_000_000, BaseFee: nil}
	bc, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, genesis, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bc.Stop)
	return bc, genesis
}

// fork builds n blocks on the block at number from; salt makes them
// differ from any other fork's.
func fork(bc *BlockChain, genesis *Genesis, from uint64, n int, salt byte) []*types.Block {
	parent := bc.GetBlockByNumber(from)
	blocks, _ := GenerateChain(genesis.Config, parent, bc.engine, bc.db, n, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{salt})
	})
	return blocks
}

// TestBftNoReorgBelowFinal: from bftBlock on every committed block is final
// (design §5.4). A heavier fork that would drop one is refused; below
// bftBlock the PoA fork choice is unchanged.
func TestBftNoReorgBelowFinal(t *testing.T) {
	bc, genesis := bftTestChain(t, 3)
	if _, err := bc.InsertChain(fork(bc, genesis, 0, 2, 0xa)); err != nil {
		t.Fatal(err)
	}
	// Still PoA: a longer fork from block 1 replaces block 2.
	pow := fork(bc, genesis, 1, 3, 0xb)
	if _, err := bc.InsertChain(pow); err != nil {
		t.Fatalf("PoA reorg below bftBlock: %v", err)
	}
	if head := bc.CurrentBlock(); head.Hash() != pow[2].Hash() {
		t.Fatalf("head %d %x, want the longer PoA fork", head.Number, head.Hash())
	}
	final := bc.CurrentBlock() // block 4, a PBFT height
	if got := bc.CurrentFinalBlock(); got == nil || got.Hash() != final.Hash() {
		t.Errorf("final block %v, want the head at a PBFT height", got)
	}

	// A longer fork from block 2 would drop blocks 3 and 4.
	heavier := fork(bc, genesis, 2, 5, 0xc)
	if _, err := bc.InsertChain(heavier); !errors.Is(err, errReorgBelowFinal) {
		t.Fatalf("reorg dropping PBFT blocks: %v, want %v", err, errReorgBelowFinal)
	}
	if head := bc.CurrentBlock(); head.Hash() != final.Hash() {
		t.Errorf("head moved to %d %x", head.Number, head.Hash())
	}
	// Extending the chain is no reorg.
	if _, err := bc.InsertChain(fork(bc, genesis, final.Number.Uint64(), 2, 0xd)); err != nil {
		t.Errorf("extending the final chain: %v", err)
	}
}

// TestBftNoReorgFromBelowSwitch: a fork rooted in the PoA segment, long
// enough to reach past bftBlock, drops PoA and PBFT blocks together; its
// lowest dropped block is a PoA one, but it is refused all the same. This
// is the fork the rule exists for (review on #154).
func TestBftNoReorgFromBelowSwitch(t *testing.T) {
	bc, genesis := bftTestChain(t, 3)
	if _, err := bc.InsertChain(fork(bc, genesis, 0, 4, 0xa)); err != nil { // 1..4; 3 and 4 are PBFT heights
		t.Fatal(err)
	}
	final := bc.CurrentBlock()
	heavier := fork(bc, genesis, 1, 6, 0xc) // 2..7: would drop 2 (PoA), 3 and 4 (PBFT)
	if _, err := bc.InsertChain(heavier); !errors.Is(err, errReorgBelowFinal) {
		t.Errorf("fork from below bftBlock: %v, want %v", err, errReorgBelowFinal)
	}
	if head := bc.CurrentBlock(); head.Hash() != final.Hash() {
		t.Errorf("head moved to %d %x", head.Number, head.Hash())
	}
}
