package metabft

import (
	"crypto/ecdsa"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const testChainID = 638200003

type testNet struct {
	keys []*ecdsa.PrivateKey
	set  *ValidatorSet
}

func newTestNet(t *testing.T, n int) *testNet {
	t.Helper()
	net := &testNet{keys: make([]*ecdsa.PrivateKey, n)}
	pubs := make([][]byte, n)
	for i := range net.keys {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		net.keys[i], pubs[i] = key, pubKeyOf(key)
	}
	set, err := NewValidatorSet(pubs)
	if err != nil {
		t.Fatal(err)
	}
	net.set = set
	return net
}

func pubKeyOf(key *ecdsa.PrivateKey) []byte {
	return crypto.FromECDSAPub(&key.PublicKey)[1:]
}

// signed builds and signs a message from validator i. COMMITs get a seal by
// the same key.
func (net *testNet) signed(t *testing.T, i int, typ MsgType, height, round uint64, digest common.Hash) *Message {
	t.Helper()
	m := &Message{Type: typ, Height: height, Round: round, ChainID: testChainID, Digest: digest}
	if typ == MsgCommit {
		seal, err := SignCommitSeal(CommitDigest(digest, round, testChainID), net.keys[i])
		if err != nil {
			t.Fatal(err)
		}
		m.CommitSeal = seal
	}
	if err := m.Sign(net.keys[i]); err != nil {
		t.Fatal(err)
	}
	return m
}
