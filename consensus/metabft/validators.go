package metabft

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// PubKeyLength is the length of a validator identity: an uncompressed
// secp256k1 public key without its 0x04 prefix, as in an enode ID.
const PubKeyLength = 64

// Validator is one member of the validator set, identified by its node key.
type Validator struct {
	PubKey [PubKeyLength]byte
	// Coinbase is the node's address in governance, which a block it builds
	// names as its coinbase. Zero in a set built from keys alone.
	Coinbase common.Address
}

// Address returns the account address derived from the validator's key.
func (v Validator) Address() common.Address {
	return common.BytesToAddress(crypto.Keccak256(v.PubKey[:])[12:])
}

func (v Validator) String() string {
	return fmt.Sprintf("%x…", v.PubKey[:8])
}

// ValidatorSet is the ordered validator set for one height. The order is
// the governance order (getMetaNodes sorts by name), which every node sees
// identically; proposer selection depends on it (design §4.2).
type ValidatorSet struct {
	list  []Validator
	index map[[PubKeyLength]byte]int
}

var (
	errEmptyValidatorSet = errors.New("empty validator set")
	errBadPubKeyLength   = errors.New("validator key must be 64 bytes")
)

// NewValidatorSet builds a set from enode public keys in governance order.
func NewValidatorSet(pubKeys [][]byte) (*ValidatorSet, error) {
	if len(pubKeys) == 0 {
		return nil, errEmptyValidatorSet
	}
	set := &ValidatorSet{
		list:  make([]Validator, len(pubKeys)),
		index: make(map[[PubKeyLength]byte]int, len(pubKeys)),
	}
	for i, key := range pubKeys {
		if len(key) != PubKeyLength {
			return nil, fmt.Errorf("validator %d: %w, have %d", i, errBadPubKeyLength, len(key))
		}
		var v Validator
		copy(v.PubKey[:], key)
		if j, dup := set.index[v.PubKey]; dup {
			return nil, fmt.Errorf("validator %d duplicates validator %d (%v)", i, j, v)
		}
		set.list[i] = v
		set.index[v.PubKey] = i
	}
	return set, nil
}

// WithCoinbases returns a copy of the set with each validator's coinbase,
// in set order.
func (s *ValidatorSet) WithCoinbases(coinbases []common.Address) (*ValidatorSet, error) {
	if len(coinbases) != len(s.list) {
		return nil, fmt.Errorf("%d coinbases for %d validators", len(coinbases), len(s.list))
	}
	cpy := &ValidatorSet{list: make([]Validator, len(s.list)), index: s.index}
	for i, v := range s.list {
		v.Coinbase = coinbases[i]
		cpy.list[i] = v
	}
	return cpy, nil
}

// Size returns N.
func (s *ValidatorSet) Size() int { return len(s.list) }

// F returns the number of faulty validators tolerated, floor((N-1)/3).
func (s *ValidatorSet) F() int { return (len(s.list) - 1) / 3 }

// Quorum returns ceil(2N/3), the smallest quorum for which any two quorums
// share at least F()+1 validators (design §4.1). It equals 2F()+1 only
// when N = 3F()+1; for N = 5 or 6, 2F()+1 would be unsafe.
func (s *ValidatorSet) Quorum() int { return (2*len(s.list) + 2) / 3 }

// Validators returns the set in order. The slice must not be modified.
func (s *ValidatorSet) Validators() []Validator { return s.list }

// At returns the validator at index i.
func (s *ValidatorSet) At(i int) Validator { return s.list[i] }

// IndexOf returns the index of the validator with the given public key.
func (s *ValidatorSet) IndexOf(pubKey []byte) (int, bool) {
	if len(pubKey) != PubKeyLength {
		return 0, false
	}
	var k [PubKeyLength]byte
	copy(k[:], pubKey)
	i, ok := s.index[k]
	return i, ok
}

// Proposer returns proposer(height, round) = validators[(height+round) % N]
// (design §4.3), computed without overflowing.
func (s *ValidatorSet) Proposer(height, round uint64) Validator {
	n := uint64(len(s.list))
	return s.list[(height%n+round%n)%n]
}

// Equal reports whether two sets hold the same validators in the same order.
func (s *ValidatorSet) Equal(o *ValidatorSet) bool {
	if len(s.list) != len(o.list) {
		return false
	}
	for i := range s.list {
		if !bytes.Equal(s.list[i].PubKey[:], o.list[i].PubKey[:]) {
			return false
		}
	}
	return true
}
