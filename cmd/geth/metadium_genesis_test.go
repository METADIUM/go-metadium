// Copyright 2026 The go-metadium Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/params"
	"github.com/urfave/cli/v2"
)

// asPoA switches the process-wide consensus method for one test. Without it
// core.DefaultGenesisBlock returns Ethereum's genesis, because the default
// outside a running node is PoW.
func asPoA(t *testing.T) {
	t.Helper()
	old := params.ConsensusMethod
	params.ConsensusMethod = params.ConsensusPoA
	t.Cleanup(func() { params.ConsensusMethod = old })
}

// servedGenesis renders a genesis document the way a peer would, with the chain
// config replaced by the given one. Marshalling the canonical genesis and then
// swapping the config is what produces a document whose block hash is canonical
// while its config is not — the case this guards against.
func servedGenesis(t *testing.T, network string, config *params.ChainConfig) []byte {
	t.Helper()

	var genesis *core.Genesis
	switch network {
	case "mainnet":
		genesis = core.DefaultGenesisBlock()
	case "testnet":
		genesis = core.DefaultTestnetGenesisBlock()
	default:
		t.Fatalf("unknown network %q", network)
	}
	doc, err := json.Marshal(genesis)
	if err != nil {
		t.Fatalf("marshalling the %s genesis: %v", network, err)
	}
	if config == nil {
		return doc
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc, &raw); err != nil {
		t.Fatalf("re-reading the %s genesis: %v", network, err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshalling the replacement config: %v", err)
	}
	raw["config"] = encoded
	doc, err = json.Marshal(raw)
	if err != nil {
		t.Fatalf("re-writing the %s genesis: %v", network, err)
	}
	return doc
}

// TestCanonicalGenesisConfigReplacesStaleConfig is the case from issue #72: a
// peer serves a genesis whose block is canonical but whose config predates a
// fork. The block hash cannot detect that — the config is not part of the block
// — so the config has to be compared and replaced.
func TestCanonicalGenesisConfigReplacesStaleConfig(t *testing.T) {
	asPoA(t)

	for _, network := range []string{"mainnet", "testnet"} {
		stale := *params.MetadiumMainnetChainConfig
		if network == "testnet" {
			stale = *params.MetadiumTestnetChainConfig
		}
		stale.CamelliaBlock = nil // as served by a pre-Camellia peer

		out, got, replaced, err := canonicalGenesisConfig(servedGenesis(t, network, &stale))
		if err != nil {
			t.Fatalf("%s: %v", network, err)
		}
		if got != network {
			t.Fatalf("%s: recognized as %q", network, got)
		}
		if !replaced {
			t.Fatalf("%s: a stale config was passed through", network)
		}
		var result core.Genesis
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("%s: the rewritten document does not parse: %v", network, err)
		}
		if result.Config.CamelliaBlock == nil {
			t.Errorf("%s: the rewritten config still has no camelliaBlock", network)
		}
		// The block itself must be untouched, or the file would initialize a
		// different chain than the one it was checked against.
		want := servedGenesis(t, network, nil)
		var original core.Genesis
		if err := json.Unmarshal(want, &original); err != nil {
			t.Fatalf("%s: %v", network, err)
		}
		if result.ToBlock().Hash() != original.ToBlock().Hash() {
			t.Errorf("%s: the genesis block changed: have %x, want %x",
				network, result.ToBlock().Hash(), original.ToBlock().Hash())
		}
	}
}

// TestCanonicalGenesisConfigAcceptsMatching leaves a correct document alone.
func TestCanonicalGenesisConfigAcceptsMatching(t *testing.T) {
	asPoA(t)

	doc := servedGenesis(t, "mainnet", nil)

	out, network, replaced, err := canonicalGenesisConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if network != "mainnet" {
		t.Fatalf("recognized as %q", network)
	}
	if replaced {
		t.Fatal("a matching config was replaced")
	}
	if string(out) != string(doc) {
		t.Fatal("a matching document was rewritten")
	}
}

// TestCanonicalGenesisConfigPassesPrivateNet checks that the guard does not
// break the command's other use: private networks bootstrap from a peer exactly
// this way, and there is no canonical config to compare them against.
func TestCanonicalGenesisConfigPassesPrivateNet(t *testing.T) {
	asPoA(t)

	private := core.DefaultGenesisBlock()
	private.Nonce = 0x4242 // any change to a block field moves the hash
	doc, err := json.Marshal(private)
	if err != nil {
		t.Fatal(err)
	}
	out, network, replaced, err := canonicalGenesisConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if network != "" || replaced {
		t.Fatalf("a private genesis was treated as %q (replaced=%v)", network, replaced)
	}
	if string(out) != string(doc) {
		t.Fatal("a private genesis was rewritten")
	}
}

// unsortedGenesis renders a genesis document with its top-level keys in
// reverse-sorted order, the way no Go map marshal would ever produce it. A
// rewrite that round-trips the document through a map re-sorts these keys, so
// byte-exact assertions against this fixture pin the splice behaviour (#102).
func unsortedGenesis(t *testing.T, doc []byte, config json.RawMessage) []byte {
	t.Helper()

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc, &raw); err != nil {
		t.Fatal(err)
	}
	if config != nil {
		raw["config"] = config
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	var out bytes.Buffer
	out.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			out.WriteString(",")
		}
		fmt.Fprintf(&out, "%q: %s", k, raw[k])
	}
	out.WriteString("}")
	return out.Bytes()
}

// TestCanonicalGenesisConfigPreservesServedBytes is item 3 of #102: the
// replacement branch must leave every byte it does not replace exactly as
// served -- same key order, same spacing -- so that all three of the command's
// paths stay diffable against the peer's document.
func TestCanonicalGenesisConfigPreservesServedBytes(t *testing.T) {
	asPoA(t)

	stale := *params.MetadiumMainnetChainConfig
	stale.CamelliaBlock = nil
	staleRaw, err := json.Marshal(&stale)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(params.MetadiumMainnetChainConfig)
	if err != nil {
		t.Fatal(err)
	}
	base := servedGenesis(t, "mainnet", nil)
	doc := unsortedGenesis(t, base, staleRaw)

	out, network, replaced, err := canonicalGenesisConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if network != "mainnet" || !replaced {
		t.Fatalf("network=%q replaced=%v", network, replaced)
	}
	expected := unsortedGenesis(t, base, want)
	if !bytes.Equal(out, expected) {
		t.Fatalf("the rewritten document does not match the served bytes outside config:\nhave %s\nwant %s",
			out, expected)
	}
}

// TestSpliceConfigAddsMissingKey covers the served document that carries no
// config at all: the compiled config has to be added, and the rest of the
// document still passes through untouched.
func TestSpliceConfigAddsMissingKey(t *testing.T) {
	doc := []byte(`{"nonce": "0x42","alloc": {}}`)
	out, err := spliceConfig(doc, []byte(`{"chainId":11}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"nonce": "0x42","alloc": {},"config": {"chainId":11}}`
	if string(out) != want {
		t.Fatalf("have %s\nwant %s", out, want)
	}
}

// TestReadGenesisEnvelopeDrainsBody is item 1 of #102: one Read is not one
// body. The one-byte reader forces the transport-may-return-less case that a
// single Read into a large buffer silently truncates.
func TestReadGenesisEnvelopeDrainsBody(t *testing.T) {
	inner := strings.Repeat("x", 64*1024)
	envelope, err := json.Marshal(map[string]string{"result": inner})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := readGenesisEnvelope(iotest.OneByteReader(bytes.NewReader(envelope)))
	if err != nil {
		t.Fatal(err)
	}
	if string(doc) != inner {
		t.Fatalf("the envelope was not drained: got %d bytes, want %d", len(doc), len(inner))
	}
}

// TestReadGenesisEnvelopeRejectsOversize pins the explicit ceiling: a response
// larger than the old implicit 1MB bound errors out loudly instead of being
// cut short.
func TestReadGenesisEnvelopeRejectsOversize(t *testing.T) {
	huge := strings.NewReader("[" + strings.Repeat("0,", maxGenesisDocument/2) + "0]")
	if _, err := readGenesisEnvelope(huge); err == nil {
		t.Fatal("an oversized response was accepted")
	}
}

// TestEmitGenesisReportsWriteFailure is item 4 of #102: a write the disk
// refuses must fail the command, not leave a truncated genesis behind an
// exit status of 0. /dev/full is the kernel's deterministic full disk.
func TestEmitGenesisReportsWriteFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/dev/full is a Linux device")
	}
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full is not available")
	}
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	set.String(outFlag.Name, "", "")
	if err := set.Set(outFlag.Name, "/dev/full"); err != nil {
		t.Fatal(err)
	}
	ctx := cli.NewContext(nil, set, nil)
	if err := emitGenesis(ctx, []byte(`{"nonce":"0x42"}`)); err == nil {
		t.Fatal("a failed write went unreported")
	}
}
