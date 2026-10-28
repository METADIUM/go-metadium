// Copyright 2015 The go-ethereum Authors
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

package tests

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers/logger"
	"github.com/holiman/uint256"
)

func initMatcher(st *testMatcher) {
	// Long tests:
	st.slow(`^stAttackTest/ContractCreationSpam`)
	st.slow(`^stBadOpcode/badOpcodes`)
	st.slow(`^stPreCompiledContracts/modexp`)
	st.slow(`^stQuadraticComplexityTest/`)
	st.slow(`^stStaticCall/static_Call50000`)
	st.slow(`^stStaticCall/static_Return50000`)
	st.slow(`^stSystemOperationsTest/CallRecursiveBomb`)
	st.slow(`^stTransactionTest/Opcodes_TransactionInit`)
	// Very time consuming
	st.skipLoad(`^stTimeConsuming/`)
	st.skipLoad(`.*vmPerformance/loop.*`)
	// Uses 1GB RAM per tested fork
	st.skipLoad(`^stStaticCall/static_Call1MB`)

	// Broken tests:
	// EOF is not part of cancun
	st.skipLoad(`^stEOF/`)
}

func TestState(t *testing.T) {
	t.Parallel()

	st := new(testMatcher)
	initMatcher(st)
	for _, dir := range []string{
		filepath.Join(baseDir, "EIPTests", "StateTests"),
		stateTestDir,
		benchmarksDir,
	} {
		st.walk(t, dir, func(t *testing.T, name string, test *StateTest) {
			execStateTest(t, st, test, nil)
		})
	}
}

// TestLegacyState tests some older tests, which were moved to the folder
// 'LegacyTests' for the Istanbul fork.
func TestLegacyState(t *testing.T) {
	st := new(testMatcher)
	initMatcher(st)
	st.walk(t, legacyStateTestDir, func(t *testing.T, name string, test *StateTest) {
		execStateTest(t, st, test, nil)
	})
}

// TestExecutionSpecState runs the test fixtures from execution-spec-tests.
func TestExecutionSpecState(t *testing.T) {
	if !common.FileExist(executionSpecStateTestDir) {
		t.Skipf("directory %s does not exist", executionSpecStateTestDir)
	}
	st := new(testMatcher)

	// Metadium uses MaxInitCodeSize=507904 (2*253952) vs upstream 49152 (2*24576).
	// EIP-3860 execution spec tests have expected state roots computed with upstream values.
	st.skipLoad(`eip3860_initcode`)
	// Metadium header RLP has additional fields causing state root mismatch with
	// upstream execution spec fixtures for cancun-related tests.
	st.skipLoad(`eip4844_blobs`)

	// Vectors go-ethereum skips as well: the pre-state puts storage on an
	// account with no code and no nonce, which cannot arise on a live chain.
	st.skipLoad(`RevertInCreateInInit`)
	st.skipLoad(`InitCollisionParis`)
	st.skipLoad(`dynamicAccountOverwriteEmpty_Paris`)
	st.skipLoad(`create2collisionStorageParis`)
	// EIP-7610 (CREATE collides with an account that has storage) came to
	// go-ethereum in v1.14; this tree is v1.13.14.
	st.skipLoad(`eip7610_create_collision`)

	// Metadium policy, on every fork. These are expected to fail, so a change
	// that makes them pass is noticed.
	//   - The VRF precompile at 0x13 exists, so a call to it is warm and
	//     answers, where an Ethereum vector expects an empty account.
	st.fails(`test_precompile_absence\[fork_(Berlin|London|Paris|Shanghai|Cancun|Osaka)-`, "Metadium VRF precompile at 0x13")
	st.fails(`failed_tx_xcf416c53_Paris`, "Metadium VRF precompile at 0x13 is warm")
	//   - Code and initcode size limits are 253,952 and 507,904 bytes (the
	//     254 KB transaction policy), not 24,576 and 49,152.
	st.fails(`(create|create2|creationTx)InitCodeSizeLimit.*-invalid\]`, "Metadium initcode size limit")
	st.fails(`(create|create2)CodeSizeLimit.*-invalid\]`, "Metadium code size limit")
	st.fails(`codesizeOOGInvalidSize`, "Metadium code size limit")
	st.fails(`createLargeResult.*RETURN-(HUGE|TOOBIG)\]`, "Metadium code size limit")
	st.fails(`CreateAddressWarmAfterFail.*code-too-big`, "Metadium code size limit")
	st.fails(`CREATE_ContractRETURNBigOffset.*-d0\]`, "Metadium code size limit")
	st.fails(`randomStatetest646`, "Metadium initcode size limit (81,914-byte CREATE)")

	// Doraji (docs/doraji-scope.md) runs the upstream Prague and Osaka vectors
	// under one rule set (tests/init.go). The vectors below exercise EIPs that
	// Doraji leaves out, so they are skipped rather than expected to fail:
	//   7623 calldata floor, 7825 transaction gas cap -- excluded on purpose,
	//        they conflict with the 254 KB transaction policy;
	//   7594 PeerDAS -- the blob schedule of a proof-of-stake chain; the public
	//        chains keep the Cancun blob parameters.
	// Other vectors that carry the 7623 floor in their expectation, and Prague
	// vectors that Osaka changes, are recognised per subtest (dorajiSkipReason).
	st.skipLoad(`eip7623_increase_calldata_cost`)
	st.skipLoad(`eip7825_transaction_gas_limit_cap`)
	st.skipLoad(`eip7594_peerdas`)

	st.walk(t, executionSpecStateTestDir, func(t *testing.T, name string, test *StateTest) {
		execStateTest(t, st, test, func(subtest StateSubtest) string {
			return dorajiSkipReason(name, test, subtest)
		})
	})
}

// dorajiSkipReason returns why an upstream Prague or Osaka subtest does not
// apply to the Doraji rule set it runs under, or "" if it applies.
//
//   - A Prague subtest whose vector also has an Osaka variant, or that sits
//     under tests/osaka/, checks the behaviour before an Osaka change (ModExp
//     pricing and limits, CLZ, P256VERIFY). Doraji takes Prague and Osaka in
//     one step, so the Osaka variant is the expectation that applies. The
//     static ModExp vectors were retired at Osaka for the EIP-7883 repricing
//     and have no Osaka variant; they are named here.
//     A Prague vector without an Osaka variant otherwise runs: most were
//     retired at Osaka for the EIP-7825 gas cap, which Doraji leaves out, so
//     they check behaviour Doraji keeps.
//   - A subtest whose execution uses less gas than the EIP-7623 calldata floor
//     has the floor in its expected gas and balances. Doraji leaves 7623 out
//     (docs/doraji-scope.md), so that expectation does not hold here. The test
//     runs the subtest once to find out, and skips it if the floor binds.
func dorajiSkipReason(name string, test *StateTest, subtest StateSubtest) string {
	if subtest.Fork != "Prague" && subtest.Fork != "Osaka" {
		return ""
	}
	if subtest.Fork == "Prague" {
		if strings.HasPrefix(name, "osaka/") || dorajiRetiredModExp.MatchString(name) {
			return "Prague behaviour before an Osaka change; Doraji has no Prague-only rule set"
		}
		if file, key, ok := strings.Cut(name, ".json/"); ok {
			osaka := strings.Replace(key, "[fork_Prague-", "[fork_Osaka-", 1)
			if osaka != key && specFileHasKey(file+".json", osaka) {
				return "Prague expectation; the Osaka variant of this vector is the one Doraji follows"
			}
		}
	}
	state, _, err := test.RunNoVerify(subtest, vm.Config{}, false, rawdb.HashScheme)
	gasUsed := state.GasUsed
	state.Close()
	if err != nil || gasUsed == 0 {
		return ""
	}
	if floor := test.floorDataGas(subtest); gasUsed < floor {
		return fmt.Sprintf("expectation includes the EIP-7623 calldata floor (%d > %d gas used), not part of Doraji", floor, gasUsed)
	}
	return ""
}

// dorajiRetiredModExp names the static ModExp vectors filled up to Prague only.
var dorajiRetiredModExp = regexp.MustCompile(`^static/state_tests/stPreCompiledContracts/modexp(Tests)?\.json/`)

// specKeys caches the vector names in each execution-spec-tests fixture file.
var specKeys sync.Map // file name -> map[string]bool

// specFileHasKey reports whether the fixture file (relative to
// executionSpecStateTestDir) contains a vector named key.
func specFileHasKey(file, key string) bool {
	v, ok := specKeys.Load(file)
	if !ok {
		keys := make(map[string]bool)
		var m map[string]json.RawMessage
		if data, err := os.ReadFile(filepath.Join(executionSpecStateTestDir, file)); err == nil && json.Unmarshal(data, &m) == nil {
			for k := range m {
				keys[k] = true
			}
		}
		v, _ = specKeys.LoadOrStore(file, keys)
	}
	return v.(map[string]bool)[key]
}

// execStateTest runs every subtest of test in the four trie/snapshot
// variants. skip, if not nil, names subtests that do not apply.
func execStateTest(t *testing.T, st *testMatcher, test *StateTest, skip func(StateSubtest) string) {
	if runtime.GOARCH == "386" && runtime.GOOS == "windows" && rand.Int63()%2 == 0 {
		t.Skip("test (randomly) skipped on 32-bit windows")
		return
	}
	for _, subtest := range test.Subtests() {
		subtest := subtest
		key := fmt.Sprintf("%s/%d", subtest.Fork, subtest.Index)
		if skip != nil {
			if reason := skip(subtest); reason != "" {
				t.Run(key, func(t *testing.T) { t.Skip(reason) })
				continue
			}
		}

		t.Run(key+"/hash/trie", func(t *testing.T) {
			withTrace(t, test.gasLimit(subtest), func(vmconfig vm.Config) error {
				var result error
				test.Run(subtest, vmconfig, false, rawdb.HashScheme, func(err error, state *StateTestState) {
					result = st.checkFailure(t, err)
				})
				return result
			})
		})
		t.Run(key+"/hash/snap", func(t *testing.T) {
			withTrace(t, test.gasLimit(subtest), func(vmconfig vm.Config) error {
				var result error
				test.Run(subtest, vmconfig, true, rawdb.HashScheme, func(err error, state *StateTestState) {
					if state.Snapshots != nil && state.StateDB != nil {
						if _, err := state.Snapshots.Journal(state.StateDB.IntermediateRoot(false)); err != nil {
							result = err
							return
						}
					}
					result = st.checkFailure(t, err)
				})
				return result
			})
		})
		t.Run(key+"/path/trie", func(t *testing.T) {
			withTrace(t, test.gasLimit(subtest), func(vmconfig vm.Config) error {
				var result error
				test.Run(subtest, vmconfig, false, rawdb.PathScheme, func(err error, state *StateTestState) {
					result = st.checkFailure(t, err)
				})
				return result
			})
		})
		t.Run(key+"/path/snap", func(t *testing.T) {
			withTrace(t, test.gasLimit(subtest), func(vmconfig vm.Config) error {
				var result error
				test.Run(subtest, vmconfig, true, rawdb.PathScheme, func(err error, state *StateTestState) {
					if state.Snapshots != nil && state.StateDB != nil {
						if _, err := state.Snapshots.Journal(state.StateDB.IntermediateRoot(false)); err != nil {
							result = err
							return
						}
					}
					result = st.checkFailure(t, err)
				})
				return result
			})
		})
	}
}

// Transactions with gasLimit above this value will not get a VM trace on failure.
const traceErrorLimit = 400000

func withTrace(t *testing.T, gasLimit uint64, test func(vm.Config) error) {
	// Use config from command line arguments.
	config := vm.Config{}
	err := test(config)
	if err == nil {
		return
	}

	// Test failed, re-run with tracing enabled.
	t.Error(err)
	if gasLimit > traceErrorLimit {
		t.Log("gas limit too high for EVM trace")
		return
	}
	buf := new(bytes.Buffer)
	w := bufio.NewWriter(buf)
	config.Tracer = logger.NewJSONLogger(&logger.Config{}, w)
	err2 := test(config)
	if !reflect.DeepEqual(err, err2) {
		t.Errorf("different error for second run: %v", err2)
	}
	w.Flush()
	if buf.Len() == 0 {
		t.Log("no EVM operation logs generated")
	} else {
		t.Log("EVM operation log:\n" + buf.String())
	}
	// t.Logf("EVM output: 0x%x", tracer.Output())
	// t.Logf("EVM error: %v", tracer.Error())
}

func BenchmarkEVM(b *testing.B) {
	// Walk the directory.
	dir := benchmarksDir
	dirinfo, err := os.Stat(dir)
	if os.IsNotExist(err) || !dirinfo.IsDir() {
		fmt.Fprintf(os.Stderr, "can't find test files in %s, did you clone the evm-benchmarks submodule?\n", dir)
		b.Skip("missing test files")
	}
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext == ".json" {
			name := filepath.ToSlash(strings.TrimPrefix(strings.TrimSuffix(path, ext), dir+string(filepath.Separator)))
			b.Run(name, func(b *testing.B) { runBenchmarkFile(b, path) })
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
}

func runBenchmarkFile(b *testing.B, path string) {
	m := make(map[string]StateTest)
	if err := readJSONFile(path, &m); err != nil {
		b.Fatal(err)
		return
	}
	if len(m) != 1 {
		b.Fatal("expected single benchmark in a file")
		return
	}
	for _, t := range m {
		t := t
		runBenchmark(b, &t)
	}
}

func runBenchmark(b *testing.B, t *StateTest) {
	for _, subtest := range t.Subtests() {
		subtest := subtest
		key := fmt.Sprintf("%s/%d", subtest.Fork, subtest.Index)

		b.Run(key, func(b *testing.B) {
			vmconfig := vm.Config{}

			config, eips, err := GetChainConfig(subtest.Fork)
			if err != nil {
				b.Error(err)
				return
			}
			var rules = config.Rules(new(big.Int), false, 0)

			vmconfig.ExtraEips = eips
			block := t.genesis(config).ToBlock()
			state := MakePreState(rawdb.NewMemoryDatabase(), t.json.Pre, false, rawdb.HashScheme)
			defer state.Close()

			var baseFee *big.Int
			if rules.IsLondon {
				baseFee = t.json.Env.BaseFee
				if baseFee == nil {
					// Retesteth uses `0x10` for genesis baseFee. Therefore, it defaults to
					// parent - 2 : 0xa as the basefee for 'this' context.
					baseFee = big.NewInt(0x0a)
				}
			}
			post := t.json.Post[subtest.Fork][subtest.Index]
			msg, err := t.json.Tx.toMessage(post, baseFee)
			if err != nil {
				b.Error(err)
				return
			}

			// Try to recover tx with current signer
			if len(post.TxBytes) != 0 {
				var ttx types.Transaction
				err := ttx.UnmarshalBinary(post.TxBytes)
				if err != nil {
					b.Error(err)
					return
				}

				if _, err := types.Sender(types.LatestSigner(config), &ttx); err != nil {
					b.Error(err)
					return
				}
			}

			// Prepare the EVM.
			txContext := core.NewEVMTxContext(msg)
			context := core.NewEVMBlockContext(block.Header(), nil, &t.json.Env.Coinbase)
			context.GetHash = vmTestBlockHash
			context.BaseFee = baseFee
			evm := vm.NewEVM(context, txContext, state.StateDB, config, vmconfig)

			// Create "contract" for sender to cache code analysis.
			sender := vm.NewContract(vm.AccountRef(msg.From), vm.AccountRef(msg.From),
				nil, 0)

			var (
				gasUsed uint64
				elapsed uint64
				refund  uint64
			)
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				snapshot := state.StateDB.Snapshot()
				state.StateDB.Prepare(rules, msg.From, context.Coinbase, msg.To, vm.ActivePrecompiles(rules), msg.AccessList)
				b.StartTimer()
				start := time.Now()

				// Execute the message.
				_, leftOverGas, err := evm.Call(sender, *msg.To, msg.Data, msg.GasLimit, uint256.MustFromBig(msg.Value))
				if err != nil {
					b.Error(err)
					return
				}

				b.StopTimer()
				elapsed += uint64(time.Since(start))
				refund += state.StateDB.GetRefund()
				gasUsed += msg.GasLimit - leftOverGas

				state.StateDB.RevertToSnapshot(snapshot)
			}
			if elapsed < 1 {
				elapsed = 1
			}
			// Keep it as uint64, multiply 100 to get two digit float later
			mgasps := (100 * 1000 * (gasUsed - refund)) / elapsed
			b.ReportMetric(float64(mgasps)/100, "mgas/s")
		})
	}
}
