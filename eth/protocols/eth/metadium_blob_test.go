package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

// TestBlobSidecarsPacketRoundtrip checks that the meta/69 blob-sidecar request
// and reply packets (M5) survive an RLP encode/decode roundtrip, including the
// positional, possibly-nil per-block sidecar slices.
func TestBlobSidecarsPacketRoundtrip(t *testing.T) {
	req := &GetBlobSidecarsPacket{
		RequestId: 42,
		Hashes:    []common.Hash{{0x01}, {0x02}},
	}
	enc, err := rlp.EncodeToBytes(req)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var gotReq GetBlobSidecarsPacket
	if err := rlp.DecodeBytes(enc, &gotReq); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if gotReq.RequestId != req.RequestId || len(gotReq.Hashes) != 2 || gotReq.Hashes[1] != (common.Hash{0x02}) {
		t.Fatalf("request roundtrip mismatch: %+v", gotReq)
	}

	res := &BlobSidecarsPacket{
		RequestId: 42,
		Sidecars: [][]*types.BlobTxSidecar{
			{
				{
					Blobs:       [][]byte{{0xaa}},
					Commitments: [][]byte{{0xbb}},
					Proofs:      [][]byte{{0xcc}},
					BlobHashes:  []common.Hash{{0xdd}},
				},
			},
			nil, // server did not have the second block
		},
	}
	enc, err = rlp.EncodeToBytes(res)
	if err != nil {
		t.Fatalf("encode reply: %v", err)
	}
	var gotRes BlobSidecarsPacket
	if err := rlp.DecodeBytes(enc, &gotRes); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if gotRes.RequestId != res.RequestId {
		t.Fatalf("reply request id mismatch: %d", gotRes.RequestId)
	}
	if len(gotRes.Sidecars) != 2 {
		t.Fatalf("reply block count mismatch: %d", len(gotRes.Sidecars))
	}
	if len(gotRes.Sidecars[0]) != 1 || gotRes.Sidecars[0][0].BlobHashes[0] != (common.Hash{0xdd}) {
		t.Fatalf("reply first block sidecar mismatch: %+v", gotRes.Sidecars[0])
	}
	if len(gotRes.Sidecars[1]) != 0 {
		t.Fatalf("reply second block should be empty, got: %+v", gotRes.Sidecars[1])
	}
}

// TestBlobSidecarPacketKind verifies the message codes are wired correctly.
func TestBlobSidecarPacketKind(t *testing.T) {
	if (&GetBlobSidecarsPacket{}).Kind() != GetBlobSidecarsMsg {
		t.Fatal("GetBlobSidecarsPacket kind mismatch")
	}
	if (&BlobSidecarsPacket{}).Kind() != BlobSidecarsMsg {
		t.Fatal("BlobSidecarsPacket kind mismatch")
	}
}
