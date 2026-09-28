//go:build pbftfault

package metabft

import (
	"crypto/ecdsa"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

// Equivocate returns a second PRE-PREPARE for m's height and round, for a
// different block: m's block resealed by reseal (a new nonce, so a new
// hash), with m's justification, signed by key. Fault injection only
// (build tag pbftfault).
func Equivocate(m *Message, key *ecdsa.PrivateKey, reseal func(*types.Block) *types.Block) (*Message, error) {
	body := new(preprepareBody)
	if err := rlp.DecodeBytes(m.Payload, body); err != nil {
		return nil, err
	}
	block := new(types.Block)
	if err := rlp.DecodeBytes(body.Block, block); err != nil {
		return nil, err
	}
	alt := reseal(block)
	if alt.Hash() == block.Hash() {
		return nil, fmt.Errorf("reseal did not change block %d", block.NumberU64())
	}
	enc, err := rlp.EncodeToBytes(alt)
	if err != nil {
		return nil, err
	}
	body.Block = enc
	out := &Message{Type: MsgPreprepare, Height: m.Height, Round: m.Round, ChainID: m.ChainID, Digest: alt.Hash(),
		Payload: encodePayload(body)}
	if err := out.Sign(key); err != nil {
		return nil, err
	}
	return out, nil
}

// SetClockOffset moves the clock c checks proposal timestamps against by d,
// as on a node whose clock is off (§11.2 S-07). Fault injection only (build
// tag pbftfault); call it before the node starts.
func (c *BlockChain) SetClockOffset(d time.Duration) {
	c.now = func() time.Time { return time.Now().Add(d) }
}

// IgnorePoolSidecars makes c fetch every proposal's blob sidecars from its
// peers, as a validator does whose pool has not received them yet (§11.3
// M-10). Fault injection only (build tag pbftfault); call it before the
// node starts.
func (c *BlockChain) IgnorePoolSidecars() { c.ignorePool = true }
