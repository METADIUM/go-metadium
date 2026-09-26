// Command tamper rewrites one block of an exported chain file (§11.2 S-10,
// S-17), for import.sh: blocks up to and including the tampered one are
// written out, so an import stops there if the block is refused.
//
//	go run ./tests/private-net-pbft/tamper IN OUT NUMBER CHAINID MODE
//
// MODE is one of
//
//	drop     remove seals until one short of the quorum's worth
//	forge    replace one seal with one signed by a key outside the set
//	dup      replace one seal with a copy of another
//	round    change BftRound, so the seals are over another round
//	preseal  add commit seals to a block below bftBlock (S-17)
//
// CommitSeals and BftRound are outside the block hash, so the next block
// still links to the tampered one; only the seals decide.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/consensus/metabft"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

func main() {
	// The block codec follows the node's consensus method; the network's
	// nodes run with --consensusmethod 2 (PoA), not the PoW default.
	params.ConsensusMethod = params.ConsensusPoA
	if len(os.Args) != 6 {
		fmt.Fprintln(os.Stderr, "usage: tamper IN OUT NUMBER CHAINID drop|forge|dup|round|preseal")
		os.Exit(2)
	}
	number, err1 := strconv.ParseUint(os.Args[3], 10, 64)
	chainID, err2 := strconv.ParseUint(os.Args[4], 10, 64)
	if err := errors.Join(err1, err2); err != nil {
		fail(err)
	}
	if err := run(os.Args[1], os.Args[2], number, chainID, os.Args[5]); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tamper:", err)
	os.Exit(1)
}

func run(in, out string, number, chainID uint64, mode string) error {
	r, err := os.Open(in)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.Create(out)
	if err != nil {
		return err
	}
	defer w.Close()
	stream := rlp.NewStream(r, 0)
	for {
		var b types.Block
		if err := stream.Decode(&b); errors.Is(err, io.EOF) {
			return fmt.Errorf("block %d is not in %s", number, in)
		} else if err != nil {
			return err
		}
		if b.NumberU64() == number {
			h, err := tamper(b.Header(), chainID, mode)
			if err != nil {
				return err
			}
			fmt.Printf("block %d %s: %d seals, round %d\n", number, mode, len(h.CommitSeals), h.BftRound)
			return rlp.Encode(w, b.WithSeal(h))
		}
		if err := rlp.Encode(w, &b); err != nil {
			return err
		}
	}
}

func tamper(h *types.Header, chainID uint64, mode string) (*types.Header, error) {
	seals := h.CommitSeals
	if mode != "preseal" && len(seals) < 2 {
		return nil, fmt.Errorf("block %d has %d seals; not a PBFT block", h.Number, len(seals))
	}
	switch mode {
	case "drop":
		h.CommitSeals = seals[:len(seals)-1] // blocks carry exactly a quorum
	case "forge":
		outsider, _ := crypto.GenerateKey()
		seal, err := metabft.SignCommitSeal(metabft.CommitDigest(h.Hash(), h.BftRound, chainID), outsider)
		if err != nil {
			return nil, err
		}
		h.CommitSeals = append(append([][]byte{}, seals[:len(seals)-1]...), seal)
	case "dup":
		h.CommitSeals = append(append([][]byte{}, seals[:len(seals)-1]...), seals[0])
	case "round":
		h.BftRound++
	case "preseal":
		if len(seals) != 0 {
			return nil, fmt.Errorf("block %d already has seals; preseal is for a block below bftBlock", h.Number)
		}
		for i := 0; i < 5; i++ {
			k, _ := crypto.GenerateKey()
			seal, err := metabft.SignCommitSeal(metabft.CommitDigest(h.Hash(), 0, chainID), k)
			if err != nil {
				return nil, err
			}
			h.CommitSeals = append(h.CommitSeals, seal)
		}
	default:
		return nil, fmt.Errorf("unknown mode %q", mode)
	}
	return h, nil
}
