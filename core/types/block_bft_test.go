package types

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
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
		t.Skipf("encoding refused (%v); a refusal enforces the rule just as well", err)
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

// mainnetHeader118924592 is Metadium mainnet block 118,924,592 as served by
// eth_getBlockByHash: a Camellia header with withdrawalsRoot, blobGasUsed and
// excessBlobGas set and baseFeePerGas 0. A real block catches field-ordering
// mistakes that a hand-built header would share with the code under test.
func mainnetHeader118924592() *Header {
	return &Header{
		ParentHash:      common.HexToHash("0xfbdc7e5eec7d45e319c0f42d885aafad7abfc28729d385ed020be19cd42f4f78"),
		UncleHash:       common.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:        common.HexToAddress("0x880a74d68b09418136c4442d1ea0f5cc72e5325a"),
		Root:            common.HexToHash("0xc752357ab46af832007bfe01dca517d094bfc9ddd16b7b483941a218596ccf64"),
		TxHash:          common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		ReceiptHash:     common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		Bloom:           BytesToBloom(hexutil.MustDecode("0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000")),
		Difficulty:      hexBig("0x1"),
		Number:          hexBig("0x716a530"),
		GasLimit:        105000000,
		GasUsed:         0,
		Fees:            hexBig("0x0"),
		Time:            1790121719,
		Extra:           hexutil.MustDecode("0x676d65742f76312e312e332f6c696e75782f676f312e32322e35"),
		Rewards:         hexutil.MustDecode("0x5b7b2261646472223a22307838383061373464363862303934313831333663343434326431656130663563633732653533323561222c22726577617264223a307d2c7b2261646472223a22307831343737396635316337373230333534313262313134383866653338646535643331383135323437222c22726577617264223a307d2c7b2261646472223a22307864386635323732656632316135306335616462306663656633316562396539316163346562326635222c22726577617264223a307d2c7b2261646472223a22307865373038303338346332326161396232333239393365323031316238633136353835353730353234222c22726577617264223a307d2c7b2261646472223a22307861346133613562333038643233313961663634646566313536386363383061383266343065613836222c22726577617264223a307d2c7b2261646472223a22307836636538333238613634363064366665346331376664626266366661613838313230646436356238222c22726577617264223a307d2c7b2261646472223a22307866623061376432353336623965343262363237333135633562623933386631613635323466346533222c22726577617264223a307d2c7b2261646472223a22307832646331353266376339636163646666626665666535373734616631666466313464663063366636222c22726577617264223a307d2c7b2261646472223a22307861393936656566666532643964336366666463653562376663353163333537646137616265613664222c22726577617264223a307d2c7b2261646472223a22307866633361373564666431373262343631316439633532623065346336366332613931323534353263222c22726577617264223a307d2c7b2261646472223a22307866633361373564666431373262343631316439633532623065346336366332613931323534353263222c22726577617264223a307d2c7b2261646472223a22307836643436383536326561363765616163366162626339363932386437306233363563326436363461222c22726577617264223a307d2c7b2261646472223a22307839666133393661636264646264366261313030313532363866333333353531333437666562323538222c22726577617264223a307d5d"),
		MixDigest:       common.HexToHash("0xd7894761b9e3d5d6581cc62fa6a897ea99c65c327bdd593544718d8be13bbf8e"),
		Nonce:           EncodeNonce(6643627331274317656),
		MinerNodeId:     hexutil.MustDecode("0x"),
		MinerNodeSig:    hexutil.MustDecode("0xf16b3605ca3b65be6e5a4cdbe93ec84eb92dfe2fd993f33370e2ffc0ea4b102e56ea5325b88d0148a39b1828bb089ea7df931485dab6fae800f457dbc223027f00"),
		BaseFee:         hexBig("0x0"),
		WithdrawalsHash: hashPtr("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		ExcessBlobGas:   hexBig("0x0"),
		BlobGasUsed:     hexBig("0x0"),
	}
}

const mainnetHash118924592 = "0xd033ad5b01f9c1c00d1e577a961750288ad6dbbfd1103586e6ec7628a7aeba59"

func hexBig(s string) *big.Int { return hexutil.MustDecodeBig(s) }

func hashPtr(s string) *common.Hash {
	h := common.HexToHash(s)
	return &h
}

// TestMainnetHeaderHash: a real mainnet header still hashes to its on-chain
// hash, and appending zero-valued PBFT fields does not change it.
func TestMainnetHeaderHash(t *testing.T) {
	usePoA(t)
	h := mainnetHeader118924592()
	want := common.HexToHash(mainnetHash118924592)
	if got := h.Hash(); got != want {
		t.Fatalf("mainnet block 118924592 hashes to %v, want %v", got, want)
	}
	enc, err := rlp.EncodeToBytes(h)
	if err != nil {
		t.Fatal(err)
	}
	var dec Header
	if err := rlp.DecodeBytes(enc, &dec); err != nil {
		t.Fatal(err)
	}
	if dec.Hash() != want {
		t.Errorf("hash changed across an RLP round trip")
	}
}

// TestTrailingSealsDecode documents what the PoA engine's
// verifyNoPbftFields rule exists for: bytes appended to a pre-PBFT header,
// which releases before the fields existed fail to decode, now decode into
// CommitSeals without changing the hash.
func TestTrailingSealsDecode(t *testing.T) {
	usePoA(t)
	plain := mainnetHeader118924592()
	enc, err := rlp.EncodeToBytes(plain)
	if err != nil {
		t.Fatal(err)
	}
	// Re-encode with seals set, as a peer could send it.
	withSeals := CopyHeader(plain)
	withSeals.CommitSeals = testSeals(3)
	junk, err := rlp.EncodeToBytes(withSeals)
	if err != nil {
		t.Fatal(err)
	}
	var dec Header
	if err := rlp.DecodeBytes(junk, &dec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dec.CommitSeals) != 3 || dec.Hash() != plain.Hash() {
		t.Fatalf("seals %d, hash equal %v", len(dec.CommitSeals), dec.Hash() == plain.Hash())
	}
	if len(junk) <= len(enc) {
		t.Fatal("the seals did not add bytes")
	}
}
