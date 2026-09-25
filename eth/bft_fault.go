//go:build pbftfault

package eth

// Fault injection for the private network, compiled in only with the
// pbftfault build tag (tests/private-net-pbft, byzantine.sh). The node
// misbehaves as METABFT_FAULT says; unset, it behaves.
//
//	bad-rewards      its proposals carry a rewards field that is not the
//	                 distribution (§11.2 S-05)
//	time-past        its proposals are stamped before their parent (S-14)
//	time-future      its proposals are stamped 10 s ahead (S-14)
//	equivocate       it sends two different PRE-PREPAREs for its rounds:
//	                 B first to half its peers, then A to all (S-04)
//	withhold-commit  it sends no round-0 COMMIT at heights divisible by 10,
//	                 so, run on every validator, those heights are decided
//	                 by a re-proposal of the prepared block (S-16)

import (
	"bytes"
	"os"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/consensus/metabft"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

var bftFault = os.Getenv("METABFT_FAULT")

func init() {
	if bftFault != "" {
		log.Warn("PBFT fault injection is on", "fault", bftFault)
	}
}

// resealer redoes the PoA seal fields after a header change.
var resealer = ethash.NewFaker()

type faultProposer struct {
	metabft.Proposer
	s *bftService
}

func bftFaultProposer(p metabft.Proposer, s *bftService) metabft.Proposer {
	switch bftFault {
	case "bad-rewards", "time-past", "time-future":
		return &faultProposer{p, s}
	}
	return p
}

func (f *faultProposer) SubmitBlock(b *types.Block) error {
	h := b.Header()
	switch bftFault {
	case "bad-rewards":
		h.Rewards = []byte(`[{"addr":"0x0000000000000000000000000000000000000001","reward":1}]`)
	case "time-past":
		if parent := f.s.bc.GetHeaderByHash(h.ParentHash); parent != nil && parent.Time > 0 {
			h.Time = parent.Time - 1
		}
	case "time-future":
		h.Time = uint64(time.Now().Unix()) + 10
	}
	log.Warn("PBFT fault: proposing a tampered block", "fault", bftFault, "number", h.Number)
	return f.Proposer.SubmitBlock(resealer.SealPoA(b.WithSeal(h)))
}

func bftFaultBroadcast(s *bftService, m *metabft.Message) bool {
	switch bftFault {
	case "withhold-commit":
		if m.Type == metabft.MsgCommit && m.Round == 0 && m.Height%10 == 0 {
			log.Warn("PBFT fault: withholding a COMMIT", "height", m.Height)
			return true
		}
	case "equivocate":
		if m.Type != metabft.MsgPreprepare {
			return false
		}
		if signer, err := m.Signer(); err != nil || !bytes.Equal(signer, s.self) {
			return false
		}
		alt, err := metabft.Equivocate(m, s.key, resealer.SealPoA)
		if err != nil {
			log.Error("PBFT fault: cannot equivocate", "err", err)
			return false
		}
		log.Warn("PBFT fault: equivocating", "height", m.Height, "round", m.Round, "a", m.Digest, "b", alt.Digest)
		s.mu.RLock()
		ids := make([]string, 0, len(s.peers))
		for id := range s.peers {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for i, id := range ids {
			p := s.peers[id]
			if !p.admitted.Load() {
				continue
			}
			if i%2 == 0 {
				send(p, alt)
			}
			send(p, m)
		}
		s.mu.RUnlock()
		return true
	}
	return false
}

// send queues m for p, dropping it when the queue is full, as broadcast does,
// so a slow peer cannot stall the consensus loop.
func send(p *bftPeer, m *metabft.Message) {
	select {
	case p.queue <- m:
	default:
		p.Log().Debug("metabft send queue full; dropping", "type", m.Type, "height", m.Height, "round", m.Round)
	}
}
