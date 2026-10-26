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
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// historySlot is where the EIP-2935 contract keeps the hash of block n.
func historySlot(n uint64) common.Hash {
	return common.BigToHash(new(big.Int).SetUint64(n % params.HistoryServeWindow))
}

// TestDorajiHistoryStorage: the history contract is written into the state
// at the Doraji fork block and nowhere before, each block from the fork
// records its parent's hash, the generated blocks import through the state
// processor (the chain maker and the processor agree on the state), and the
// contract answers a call with the hash for a block in its window and
// reverts for one outside.
func TestDorajiHistoryStorage(t *testing.T) {
	cfg := *params.AllEthashProtocolChanges
	cfg.DorajiBlock = big.NewInt(3)
	gspec := &Genesis{Config: &cfg, Alloc: GenesisAlloc{}}
	const n = 6
	_, blocks, _ := GenerateChainWithGenesis(gspec, ethash.NewFaker(), n, nil)

	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, gspec, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	if i, err := chain.InsertChain(blocks); err != nil {
		t.Fatalf("import of block %d: %v", i, err)
	}
	hashOf := func(num uint64) common.Hash { return chain.GetHeaderByNumber(num).Hash() }

	// Before the fork: no contract, nothing recorded.
	pre, _ := chain.StateAt(blocks[1].Root()) // block 2
	if pre.GetCodeSize(params.HistoryStorageAddress) != 0 {
		t.Fatal("history contract present before the fork")
	}
	// At the fork block: the contract, with the parent's hash and nothing older.
	at, _ := chain.StateAt(blocks[2].Root()) // block 3
	if !bytes.Equal(at.GetCode(params.HistoryStorageAddress), params.HistoryStorageCode) {
		t.Fatal("history contract not written at the fork block")
	}
	if at.GetNonce(params.HistoryStorageAddress) != 1 {
		t.Fatalf("history contract nonce %d, want 1", at.GetNonce(params.HistoryStorageAddress))
	}
	if got := at.GetState(params.HistoryStorageAddress, historySlot(2)); got != hashOf(2) {
		t.Fatalf("at the fork block, slot for block 2 = %v, want %v", got, hashOf(2))
	}
	if got := at.GetState(params.HistoryStorageAddress, historySlot(1)); got != (common.Hash{}) {
		t.Fatalf("at the fork block, slot for block 1 = %v, want empty", got)
	}
	// After: every parent since the fork.
	head, _ := chain.StateAt(blocks[n-1].Root()) // block 6
	for num := uint64(2); num < n; num++ {
		if got := head.GetState(params.HistoryStorageAddress, historySlot(num)); got != hashOf(num) {
			t.Fatalf("slot for block %d = %v, want %v", num, got, hashOf(num))
		}
	}

	// The contract, called: a block in the window answers, one outside reverts.
	header := blocks[n-1].Header()
	evm := vm.NewEVM(NewEVMBlockContext(header, chain, nil), vm.TxContext{}, head, &cfg, vm.Config{})
	call := func(num uint64) ([]byte, error) {
		ret, _, err := evm.Call(vm.AccountRef(common.Address{0x11}), params.HistoryStorageAddress, common.BigToHash(new(big.Int).SetUint64(num)).Bytes(), 100_000, common.U2560)
		return ret, err
	}
	if ret, err := call(4); err != nil || !bytes.Equal(ret, hashOf(4).Bytes()) {
		t.Fatalf("call for block 4: ret %x, err %v; want %v", ret, err, hashOf(4))
	}
	if ret, err := call(n); err == nil {
		t.Fatalf("call for the current block %d answered %x, want a revert", n, ret)
	}
	if ret, err := call(1); err != nil || !bytes.Equal(ret, make([]byte, 32)) {
		// Within the window but never recorded: the contract returns the empty slot.
		t.Fatalf("call for block 1: ret %x, err %v; want the empty slot", ret, err)
	}
}

// TestDorajiHistoryFromGenesis: with Doraji at genesis, block 1 installs the
// contract and records the genesis hash.
func TestDorajiHistoryFromGenesis(t *testing.T) {
	cfg := *params.AllEthashProtocolChanges
	cfg.DorajiBlock = big.NewInt(0)
	gspec := &Genesis{Config: &cfg, Alloc: GenesisAlloc{}}
	_, blocks, _ := GenerateChainWithGenesis(gspec, ethash.NewFaker(), 2, nil)

	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, gspec, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	if i, err := chain.InsertChain(blocks); err != nil {
		t.Fatalf("import of block %d: %v", i, err)
	}
	st, _ := chain.StateAt(blocks[0].Root())
	if !bytes.Equal(st.GetCode(params.HistoryStorageAddress), params.HistoryStorageCode) {
		t.Fatal("history contract not written at block 1")
	}
	if got := st.GetState(params.HistoryStorageAddress, historySlot(0)); got != chain.Genesis().Hash() {
		t.Fatalf("slot for the genesis = %v, want %v", got, chain.Genesis().Hash())
	}
	// And the genesis state itself carries nothing: the contract is installed
	// by block processing, not by the genesis.
	g, _ := chain.StateAt(chain.Genesis().Root())
	if g.GetCodeSize(params.HistoryStorageAddress) != 0 {
		t.Fatal("history contract in the genesis state")
	}
}

// TestDorajiHistoryServeWindowInCode: the contract's ring buffer size is baked
// into its bytecode as PUSH2 operands; params.HistoryServeWindow must be that
// value, or a node answering a serve-window question from the constant would
// disagree with the contract.
func TestDorajiHistoryServeWindowInCode(t *testing.T) {
	code := params.HistoryStorageCode
	var push2 []uint64
	for pc := 0; pc < len(code); pc++ {
		op := vm.OpCode(code[pc])
		if !op.IsPush() {
			continue
		}
		size := int(op - vm.PUSH1 + 1)
		if pc+size >= len(code) {
			t.Fatalf("push at %d runs past the end of the code", pc)
		}
		if op == vm.PUSH2 {
			push2 = append(push2, uint64(code[pc+1])<<8|uint64(code[pc+2]))
		}
		pc += size
	}
	if len(push2) == 0 {
		t.Fatal("no PUSH2 in the history contract code")
	}
	for i, v := range push2 {
		if v != params.HistoryServeWindow {
			t.Fatalf("PUSH2 operand %d = %d, want HistoryServeWindow %d", i, v, params.HistoryServeWindow)
		}
	}
}
