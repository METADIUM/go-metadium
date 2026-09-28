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
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
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

// TestProposalSidecars: a PBFT proposal's blob sidecars are served by hash
// before the block is written (design §12).
func TestProposalSidecars(t *testing.T) {
	bc, _ := bftTestChain(t, 3)
	hash := common.Hash{0xb1}
	if got := bc.GetBlobSidecars(hash); got != nil {
		t.Fatalf("sidecars for an unknown block: %v", got)
	}
	bc.AddProposalSidecars(hash, nil)
	if got := bc.GetBlobSidecars(hash); got != nil {
		t.Fatalf("an empty set was recorded: %v", got)
	}
	sc := []*types.BlobTxSidecar{{}}
	bc.AddProposalSidecars(hash, sc)
	if got := bc.GetBlobSidecars(hash); len(got) != 1 {
		t.Errorf("proposal sidecars: %v", got)
	}
}

// TestCompleteSidecarsOnImport: a validator that fetched a proposal's full
// sidecar set before voting keeps it when the block is written, even if its
// pool holds only some of them (review on #163).
func TestCompleteSidecarsOnImport(t *testing.T) {
	bc, _ := bftTestChain(t, 3)
	hash := common.Hash{0xb2}
	one, two := &types.BlobTxSidecar{}, &types.BlobTxSidecar{}
	partial := []*types.BlobTxSidecar{one}
	if got := bc.completeSidecars(hash, partial, 2); len(got) != 1 {
		t.Fatalf("nothing recorded: %d sidecars, want the pool's 1", len(got))
	}
	bc.AddProposalSidecars(hash, []*types.BlobTxSidecar{one, two})
	if got := bc.completeSidecars(hash, partial, 2); len(got) != 2 {
		t.Errorf("recorded full set: %d sidecars, want 2", len(got))
	}
	if got := bc.completeSidecars(hash, []*types.BlobTxSidecar{two, one}, 2); got[0] != two {
		t.Error("a complete pool set was replaced")
	}
	if got := bc.completeSidecars(common.Hash{0xb3}, nil, 0); got != nil {
		t.Errorf("a block without blobs: %v", got)
	}
}

// TestImportKeepsRecordedSidecars drives the same case through block import:
// a block with two blob transactions, the pool holding the sidecar of only
// the first, and the full set recorded for the block before it is written.
func TestImportKeepsRecordedSidecars(t *testing.T) {
	key, _ := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	sender := crypto.PubkeyToAddress(key.PublicKey)
	config := &params.ChainConfig{
		ChainID:        big.NewInt(1337),
		HomesteadBlock: big.NewInt(0), EIP150Block: big.NewInt(0), EIP155Block: big.NewInt(0), EIP158Block: big.NewInt(0),
		ByzantiumBlock: big.NewInt(0), ConstantinopleBlock: big.NewInt(0), PetersburgBlock: big.NewInt(0), IstanbulBlock: big.NewInt(0),
		BerlinBlock: big.NewInt(0), LondonBlock: big.NewInt(0), CamelliaBlock: big.NewInt(0),
		Ethash: new(params.EthashConfig),
	}
	genesis := &Genesis{Config: config, Difficulty: big.NewInt(1), GasLimit: 30_000_000, BaseFee: big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{sender: {Balance: big.NewInt(1e18)}}}
	signer := types.LatestSigner(config)
	var txs []*types.Transaction
	_, blocks, _ := GenerateChainWithGenesis(genesis, ethash.NewFaker(), 1, func(i int, b *BlockGen) {
		for n := uint64(0); n < 2; n++ {
			tx := types.MustSignNewTx(key, signer, &types.BlobTx{
				ChainID: uint256.NewInt(1337), Nonce: n, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(1e12),
				Gas: 21000, To: &common.Address{0x01}, MaxFeePerBlobGas: uint256.NewInt(1e12), Value: new(uint256.Int),
				BlobHashes: []common.Hash{{0x01, byte(n)}},
			})
			b.AddTx(tx)
			txs = append(txs, tx)
		}
	})
	bc, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, genesis, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Stop()
	first, second := &types.BlobTxSidecar{Commitments: [][]byte{{1}}}, &types.BlobTxSidecar{Commitments: [][]byte{{2}}}
	bc.BlobSidecarFn = func(h common.Hash) *types.BlobTxSidecar {
		if h == txs[0].Hash() {
			return first
		}
		return nil
	}
	bc.AddProposalSidecars(blocks[0].Hash(), []*types.BlobTxSidecar{first, second})
	if _, err := bc.InsertChain(blocks); err != nil {
		t.Fatal(err)
	}
	if got := rawdb.ReadBlobSidecars(bc.db, blocks[0].Hash(), 1); len(got) != 2 {
		t.Errorf("stored %d sidecars after import, want the recorded 2", len(got))
	}
}
