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

package tracers

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// returnValueOf digs the EVM return value out of a trace result produced by
// the default (struct logger) tracer.
func returnValueOf(t *testing.T, res interface{}) string {
	t.Helper()
	blob, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ReturnValue string `json:"returnValue"`
	}
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatal(err)
	}
	return out.ReturnValue
}

// TestDorajiTraceReplay: every path that re-executes a block applies the
// block's pre-transaction system calls, so on a Doraji block a traced
// transaction that reads the history storage contract sees the parent hash
// the imported block recorded, and the intermediate roots match the state the
// transaction-level replay reaches. Before the fork the contract is absent and
// the same call returns nothing.
//
// Each block carries two transactions: the first calls the history contract
// for the parent's number, the second is a plain transfer, so that the roots
// after the first transaction can be compared across paths.
func TestDorajiTraceReplay(t *testing.T) {
	t.Parallel()

	const (
		forkBlock = 3
		genBlocks = 6
	)
	cfg := *params.AllEthashProtocolChanges
	cfg.DorajiBlock = big.NewInt(forkBlock)
	accounts := newAccounts(2)
	genesis := &core.Genesis{
		Config: &cfg,
		Alloc: types.GenesisAlloc{
			accounts[0].addr: {Balance: big.NewInt(params.Ether)},
			accounts[1].addr: {Balance: big.NewInt(params.Ether)},
		},
	}
	signer := types.HomesteadSigner{}
	historyTx := make(map[uint64]common.Hash) // block number -> first tx
	backend := newTestBackend(t, genBlocks, genesis, func(i int, b *core.BlockGen) {
		number := uint64(i + 1)
		// Ask the history contract for the parent's hash.
		call, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(2 * i),
			To:       &params.HistoryStorageAddress,
			Gas:      100_000,
			GasPrice: b.BaseFee(),
			Data:     common.BigToHash(new(big.Int).SetUint64(number - 1)).Bytes(),
		}), signer, accounts[0].key)
		b.AddTx(call)
		historyTx[number] = call.Hash()
		transfer, _ := types.SignTx(types.NewTx(&types.LegacyTx{
			Nonce:    uint64(2*i + 1),
			To:       &accounts[1].addr,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas,
			GasPrice: b.BaseFee(),
		}), signer, accounts[0].key)
		b.AddTx(transfer)
	})
	defer backend.teardown()
	api := NewAPI(backend)
	ctx := context.Background()

	// What the call returns on each block: nothing before the fork (no code
	// at the address), the parent's hash from the fork on.
	want := func(number uint64) string {
		if number < forkBlock {
			return ""
		}
		return common.Bytes2Hex(backend.chain.GetHeaderByNumber(number - 1).Hash().Bytes())
	}

	for number := uint64(1); number <= genBlocks; number++ {
		block := backend.chain.GetBlockByNumber(number)

		// debug_traceBlockByNumber
		results, err := api.TraceBlockByNumber(ctx, rpc.BlockNumber(number), nil)
		if err != nil {
			t.Fatalf("block %d: TraceBlockByNumber: %v", number, err)
		}
		if got := returnValueOf(t, results[0].Result); got != want(number) {
			t.Errorf("block %d: TraceBlockByNumber returned %q, want %q", number, got, want(number))
		}

		// debug_traceTransaction, through the state at the transaction.
		res, err := api.TraceTransaction(ctx, historyTx[number], nil)
		if err != nil {
			t.Fatalf("block %d: TraceTransaction: %v", number, err)
		}
		if got := returnValueOf(t, res); got != want(number) {
			t.Errorf("block %d: TraceTransaction returned %q, want %q", number, got, want(number))
		}

		// debug_intermediateRoots agrees with the state the transaction-level
		// replay reaches after the first transaction.
		roots, err := api.IntermediateRoots(ctx, block.Hash(), nil)
		if err != nil {
			t.Fatalf("block %d: IntermediateRoots: %v", number, err)
		}
		if len(roots) != 2 {
			t.Fatalf("block %d: %d intermediate roots, want 2", number, len(roots))
		}
		_, _, statedb, release, err := backend.StateAtTransaction(ctx, block, 1, 0)
		if err != nil {
			t.Fatalf("block %d: StateAtTransaction: %v", number, err)
		}
		if got := statedb.IntermediateRoot(cfg.IsEIP158(block.Number())); got != roots[0] {
			t.Errorf("block %d: intermediate root after tx 0 = %v, state at tx 1 = %v", number, roots[0], got)
		}
		release()
	}

	// debug_traceChain across the fork.
	from, _ := api.blockByNumber(ctx, rpc.BlockNumber(0))
	to, _ := api.blockByNumber(ctx, rpc.BlockNumber(genBlocks))
	next := uint64(1)
	for result := range api.traceChain(from, to, nil, nil) {
		if uint64(result.Block) != next {
			t.Fatalf("traceChain: block %d, want %d", result.Block, next)
		}
		if got := returnValueOf(t, result.Traces[0].Result); got != want(next) {
			t.Errorf("block %d: traceChain returned %q, want %q", next, got, want(next))
		}
		next++
	}
	if next != genBlocks+1 {
		t.Fatalf("traceChain stopped at block %d", next-1)
	}
}
