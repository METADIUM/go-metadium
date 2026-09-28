//go:build pbftfault

package metabft

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

// TestEquivocate: the second PRE-PREPARE is for another block, carries the
// same justification, verifies as the same signer's, and is what the dedup
// cache reports as a conflict.
func TestEquivocate(t *testing.T) {
	net := newTestNet(t, 4)
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(9), Difficulty: big.NewInt(1)})
	enc, _ := rlp.EncodeToBytes(block)
	m := &Message{Type: MsgPreprepare, Height: 9, Round: 1, ChainID: testChainID, Digest: block.Hash(),
		Payload: encodePayload(&preprepareBody{Block: enc})}
	if err := m.Sign(net.keys[1]); err != nil {
		t.Fatal(err)
	}
	alt, err := Equivocate(m, net.keys[1], ethash.NewFaker().SealPoA)
	if err != nil {
		t.Fatal(err)
	}
	if alt.Digest == m.Digest || alt.Height != m.Height || alt.Round != m.Round {
		t.Fatalf("alt %d/%d %x vs %d/%d %x", alt.Height, alt.Round, alt.Digest, m.Height, m.Round, m.Digest)
	}
	i, err := alt.Verify(testChainID, net.set)
	j, _ := m.Verify(testChainID, net.set)
	if err != nil || i != j {
		t.Fatalf("alt verifies as %d (%v), m as %d", i, err, j)
	}
	body := new(preprepareBody)
	if err := rlp.DecodeBytes(alt.Payload, body); err != nil {
		t.Fatal(err)
	}
	b := new(types.Block)
	if err := rlp.DecodeBytes(body.Block, b); err != nil || b.Hash() != alt.Digest {
		t.Errorf("alt payload block %x, digest %x (%v)", b.Hash(), alt.Digest, err)
	}
}
