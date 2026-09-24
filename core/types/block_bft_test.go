package types

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

// usePoA runs the test with the header encoding Metadium nodes use. The
// package default is ConsensusPoW, under which Hash and EncodeRLP take the
// legacy header path and ignore the Metadium and PBFT fields entirely.
func usePoA(t *testing.T) {
	t.Helper()
	old := params.ConsensusMethod
	params.ConsensusMethod = params.ConsensusPoA
	t.Cleanup(func() { params.ConsensusMethod = old })
}

// bftTestHeader returns a Metadium PoA header. With camellia set it carries
// every optional field before the PBFT ones, as a PBFT header must
// (docs/pbft-consensus-design.md §5.1).
func bftTestHeader(baseFee, camellia bool) *Header {
	h := &Header{
		ParentHash: common.HexToHash("0x1111"), Coinbase: common.HexToAddress("0x2222"),
		Root: common.HexToHash("0x3333"), TxHash: EmptyTxsHash, ReceiptHash: EmptyReceiptsHash,
		Difficulty: big.NewInt(1), Number: big.NewInt(117_764_001), GasLimit: 105_000_000, GasUsed: 21000,
		Fees: big.NewInt(21000), Time: 1_788_000_000, Extra: []byte("metadium"),
		Rewards: []byte(`[{"addr":"0x2222","reward":1}]`), MinerNodeId: []byte{1, 2, 3}, MinerNodeSig: []byte{4, 5, 6},
	}
	if baseFee || camellia {
		h.BaseFee = big.NewInt(80_000_000_000)
	}
	if camellia {
		h.WithdrawalsHash = &EmptyWithdrawalsHash
		h.ExcessBlobGas = big.NewInt(0)
		h.BlobGasUsed = big.NewInt(131072)
	}
	return h
}

func testSeals(n int) [][]byte {
	seals := make([][]byte, n)
	for i := range seals {
		seals[i] = bytes.Repeat([]byte{byte(i + 1)}, CommitSealLength)
	}
	return seals
}

// TestHeaderHashUnchangedByBftFields pins header hashes computed, in PoA mode,
// on the tree before the PBFT fields existed (feature/pbft-consensus at
// 8d4ca746b); the encodings matched byte for byte too. A change here means existing blocks would get new
// hashes, i.e. a chain split (P1-T2).
func TestHeaderHashUnchangedByBftFields(t *testing.T) {
	usePoA(t)
	for _, tt := range []struct {
		name   string
		header *Header
		want   common.Hash
	}{
		{"pre-London", bftTestHeader(false, false), common.HexToHash("0x0cc714349796131205cccf6a13b212de5fdaf3c55b1ed085d74dfe27f941fcd8")},
		{"London", bftTestHeader(true, false), common.HexToHash("0x073f5f022774597aa254ca0932dedb857c3ed2d5edffc81ecf6893fc720099ca")},
		{"Camellia", bftTestHeader(true, true), common.HexToHash("0x5e16431281fb58ddb0ca6ae4bc3f1a230e3d5c7fc4cbc10b556382250fd1f800")},
	} {
		if got := tt.header.Hash(); got != tt.want {
			t.Errorf("%s: hash %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestBftFieldsExcludedFromHash: the seal set and round never change the hash
// (P1-T3), and every other field still does.
func TestBftFieldsExcludedFromHash(t *testing.T) {
	usePoA(t)
	plain := bftTestHeader(true, true)
	want := plain.Hash()

	for _, tt := range []struct {
		name  string
		round uint64
		seals [][]byte
	}{
		{"round only", 3, nil},
		{"empty non-nil seals", 0, [][]byte{}},
		{"three seals", 0, testSeals(3)},
		{"five seals, round 2", 2, testSeals(5)},
	} {
		h := CopyHeader(plain)
		h.BftRound, h.CommitSeals = tt.round, tt.seals
		if got := h.Hash(); got != want {
			t.Errorf("%s: hash %v, want %v", tt.name, got, want)
		}
		if h.BftRound != tt.round || !reflect.DeepEqual(h.CommitSeals, tt.seals) {
			t.Errorf("%s: Hash() modified the header", tt.name)
		}
	}

	// Fields outside SealHash stay in the hash: PBFT signs the block hash, so
	// these must not be malleable (design §5.2).
	for name, mutate := range map[string]func(h *Header){
		"Rewards":      func(h *Header) { h.Rewards = []byte("other") },
		"MinerNodeSig": func(h *Header) { h.MinerNodeSig = []byte{9} },
		"MinerNodeId":  func(h *Header) { h.MinerNodeId = []byte{9} },
		"Coinbase":     func(h *Header) { h.Coinbase = common.HexToAddress("0x9999") },
		"Time":         func(h *Header) { h.Time++ },
	} {
		h := CopyHeader(plain)
		h.CommitSeals = testSeals(3)
		mutate(h)
		if h.Hash() == want {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
}

// TestBftHeaderRLP round-trips PBFT headers (P1-T1) and checks that zero-valued
// PBFT fields encode exactly like a header without them (P1-T4).
func TestBftHeaderRLP(t *testing.T) {
	usePoA(t)
	plain := bftTestHeader(true, true)
	plainEnc, err := rlp.EncodeToBytes(plain)
	if err != nil {
		t.Fatal(err)
	}
	zero := CopyHeader(plain)
	zero.BftRound, zero.CommitSeals = 0, nil
	if enc, _ := rlp.EncodeToBytes(zero); !bytes.Equal(enc, plainEnc) {
		t.Fatal("round 0 without seals does not encode like a header without PBFT fields")
	}

	for _, tt := range []struct {
		name  string
		round uint64
		seals [][]byte
	}{
		{"round 0 with seals", 0, testSeals(3)},
		{"round 4 with seals", 4, testSeals(7)},
	} {
		h := CopyHeader(plain)
		h.BftRound, h.CommitSeals = tt.round, tt.seals
		enc, err := rlp.EncodeToBytes(h)
		if err != nil {
			t.Fatalf("%s: encode: %v", tt.name, err)
		}
		if bytes.Equal(enc, plainEnc) {
			t.Fatalf("%s: PBFT fields were not encoded", tt.name)
		}
		var dec Header
		if err := rlp.DecodeBytes(enc, &dec); err != nil {
			t.Fatalf("%s: decode: %v", tt.name, err)
		}
		if dec.BftRound != tt.round || !reflect.DeepEqual(dec.CommitSeals, tt.seals) {
			t.Errorf("%s: decoded round %d seals %d, want %d / %d", tt.name,
				dec.BftRound, len(dec.CommitSeals), tt.round, len(tt.seals))
		}
		if dec.Hash() != h.Hash() || dec.Hash() != plain.Hash() {
			t.Errorf("%s: hash changed across the round trip", tt.name)
		}
		if dec.ParentBeaconRoot != nil {
			t.Errorf("%s: ParentBeaconRoot appeared after decoding", tt.name)
		}
	}
}

// TestBftFieldsNeedCamelliaFields documents why IsBft requires IsCamellia
// (params.checkBft): ahead of set PBFT fields, unset optional fields cannot
// round-trip.
func TestBftFieldsNeedCamelliaFields(t *testing.T) {
	usePoA(t)
	h := bftTestHeader(true, false) // London: BaseFee set, Camellia fields nil
	h.CommitSeals = testSeals(3)
	enc, err := rlp.EncodeToBytes(h)
	if err != nil {
		return // refusing to encode is an acceptable outcome too
	}
	var dec Header
	if err := rlp.DecodeBytes(enc, &dec); err == nil && reflect.DeepEqual(dec.WithdrawalsHash, h.WithdrawalsHash) &&
		reflect.DeepEqual(dec.ExcessBlobGas, h.ExcessBlobGas) {
		t.Fatal("a PBFT header without Camellia fields round-tripped; the IsBft => IsCamellia rule may no longer be needed")
	}
}

func TestBftHeaderSanityCheck(t *testing.T) {
	usePoA(t)
	h := bftTestHeader(true, true)
	h.CommitSeals = testSeals(MaxCommitSeals)
	if err := h.SanityCheck(); err != nil {
		t.Fatalf("MaxCommitSeals seals rejected: %v", err)
	}
	h.CommitSeals = testSeals(MaxCommitSeals + 1)
	if err := h.SanityCheck(); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("too many seals: %v", err)
	}
	h.CommitSeals = [][]byte{make([]byte, CommitSealLength-1)}
	if err := h.SanityCheck(); err == nil || !strings.Contains(err.Error(), "length") {
		t.Errorf("short seal: %v", err)
	}
}

func TestCopyHeaderCopiesSeals(t *testing.T) {
	usePoA(t)
	h := bftTestHeader(true, true)
	h.BftRound, h.CommitSeals = 2, testSeals(3)
	cpy := CopyHeader(h)
	cpy.CommitSeals[0][0] ^= 0xff
	cpy.CommitSeals[1] = nil
	if h.CommitSeals[0][0] != 1 || h.CommitSeals[1] == nil {
		t.Error("CopyHeader shares the seal slices")
	}
	if cpy.BftRound != 2 {
		t.Error("CopyHeader dropped BftRound")
	}
	if CopyHeader(bftTestHeader(true, true)).CommitSeals != nil {
		t.Error("CopyHeader turned nil seals into a non-nil slice, which would change nothing but is a needless difference")
	}
	if h.Size() <= bftTestHeader(true, true).Size() {
		t.Error("Size does not count the seals")
	}
}

func TestBftHeaderJSON(t *testing.T) {
	usePoA(t)
	// Non-PBFT headers keep their JSON unchanged.
	out, err := json.Marshal(bftTestHeader(true, true))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "bftRound") || strings.Contains(string(out), "commitSeals") {
		t.Fatalf("a non-PBFT header gained PBFT fields: %s", out)
	}

	h := bftTestHeader(true, true)
	h.BftRound, h.CommitSeals = 3, testSeals(2)
	out, err = json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var dec Header
	if err := json.Unmarshal(out, &dec); err != nil {
		t.Fatal(err)
	}
	if dec.BftRound != 3 || !reflect.DeepEqual(dec.CommitSeals, h.CommitSeals) || dec.Hash() != h.Hash() {
		t.Errorf("JSON round trip lost PBFT fields: %s", out)
	}
}
