package eth

import (
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/metabft"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

// BftAPI is the metabft RPC namespace (docs/pbft-consensus-design.md §6,
// §7.1, §9.3): what a validator's consensus is doing, whether a network is
// ready for its switch, and the equivocation evidence it holds.
type BftAPI struct{ s *bftService }

// BftValidator is one member of a validator set.
type BftValidator struct {
	Index    int            `json:"index"`
	NodeID   hexutil.Bytes  `json:"nodeId"`
	Coinbase common.Address `json:"coinbase"`
}

// GetValidators returns the validator set for height, the one that agrees
// on it, read from the state at height-1. Without a height, the one being
// agreed on now.
func (api *BftAPI) GetValidators(height *hexutil.Uint64) ([]BftValidator, error) {
	h := api.s.currentHeight()
	if height != nil {
		h = uint64(*height)
	}
	set, err := api.s.validatorSet(h)
	if err != nil {
		return nil, err
	}
	out := make([]BftValidator, set.Size())
	for i, v := range set.Validators() {
		out[i] = BftValidator{Index: i, NodeID: append([]byte{}, v.PubKey[:]...), Coinbase: v.Coinbase}
	}
	return out, nil
}

// BftRoundState is where consensus stands on this node.
type BftRoundState struct {
	Height   hexutil.Uint64 `json:"height"`
	Round    hexutil.Uint64 `json:"round"`
	Proposer hexutil.Bytes  `json:"proposer,omitempty"` // node ID of this round's proposer
	Observer bool           `json:"observer"`           // signing nothing until Height commits
}

// GetRoundState returns the height and round being agreed on and the
// round's proposer. Height is 0 before the switch.
func (api *BftAPI) GetRoundState() BftRoundState {
	st := api.s.node.FullStatus()
	rs := BftRoundState{Height: hexutil.Uint64(st.Height), Round: hexutil.Uint64(st.Round), Observer: st.Observer}
	if st.Height != 0 {
		if set, err := api.s.validatorSet(st.Height); err == nil {
			p := set.Proposer(st.Height, st.Round)
			rs.Proposer = append([]byte{}, p.PubKey[:]...)
		}
	}
	return rs
}

// BftRejection is the last proposal this node refused.
type BftRejection struct {
	Height hexutil.Uint64 `json:"height"`
	Hash   common.Hash    `json:"hash"`
	Reason string         `json:"reason"`
	At     time.Time      `json:"at"`
}

// BftStatus is the operator's view of a validator.
type BftStatus struct {
	BftRoundState
	Validator       bool          `json:"validator"` // advertises metabft/1
	Peers           int           `json:"peers"`     // metabft/1 peers
	AdmittedPeers   int           `json:"admittedPeers"`
	InsertFailures  uint64        `json:"insertFailures"`
	DroppedMessages uint64        `json:"droppedMessages"`
	LastRejection   *BftRejection `json:"lastRejection,omitempty"`
}

// Status returns the round state, peers, failure counters and the last
// proposal this node refused, with the reason (design §9.3.1).
func (api *BftAPI) Status() BftStatus {
	st := api.s.node.FullStatus()
	out := BftStatus{BftRoundState: api.GetRoundState(), Validator: api.s.validator,
		InsertFailures: st.InsertFailures, DroppedMessages: st.DroppedMessages}
	api.s.mu.RLock()
	for _, p := range api.s.peers {
		out.Peers++
		if p.admitted.Load() {
			out.AdmittedPeers++
		}
	}
	api.s.mu.RUnlock()
	if r := st.LastRejection; r != nil {
		out.LastRejection = &BftRejection{Height: hexutil.Uint64(r.Height), Hash: r.Hash, Reason: r.Reason, At: r.At}
	}
	return out
}

// BftReadiness is what the switch needs (design §9.3), checked from this
// node's view before bftBlock.
type BftReadiness struct {
	Head       hexutil.Uint64 `json:"head"`
	BftBlock   hexutil.Uint64 `json:"bftBlock"`
	BlocksLeft int64          `json:"blocksLeft"` // to bftBlock; 0 once past it
	Governance bool           `json:"governance"` // a validator set is readable
	Validators int            `json:"validators"`
	Minimum    int            `json:"minimum"`
	Advertises bool           `json:"advertises"` // this node offers metabft/1
	InSet      bool           `json:"inSet"`      // this node is a validator
	Ready      bool           `json:"ready"`
	Problems   []string       `json:"problems,omitempty"`
}

// Readiness reports whether this node and its network meet the switch
// conditions: governance deployed, at least MinValidators nodes, and this
// node advertising metabft/1 if it is one of them (a local check only).
func (api *BftAPI) Readiness() BftReadiness {
	head := api.s.bc.CurrentBlock().Number.Uint64()
	bftBlock := api.s.bc.Config().BftBlock.Uint64()
	r := BftReadiness{Head: hexutil.Uint64(head), BftBlock: hexutil.Uint64(bftBlock), Minimum: metabft.MinValidators,
		Advertises: api.s.validator}
	if bftBlock > head {
		r.BlocksLeft = int64(bftBlock - head)
	}
	set, err := api.s.validators(head + 1) // uncached: governance may be changing
	switch {
	case errors.Is(err, metabft.ErrTooFewValidators):
		r.Governance = true
		r.Problems = append(r.Problems, err.Error())
	case err != nil:
		r.Problems = append(r.Problems, "no validator set: "+err.Error())
	default:
		r.Governance, r.Validators = true, set.Size()
		_, r.InSet = set.IndexOf(api.s.self)
		if r.InSet && !r.Advertises {
			r.Problems = append(r.Problems, "this node is a validator but does not advertise metabft/1; restart it")
		}
	}
	r.Ready = len(r.Problems) == 0
	return r
}

// BftEvidence is one equivocation: two messages a validator signed for the
// same height, round and type. Raw is the RLP of both, which anyone can
// check against the validator set (design §7.1) and hand to governance.
type BftEvidence struct {
	Height hexutil.Uint64 `json:"height"`
	Round  hexutil.Uint64 `json:"round"`
	Type   string         `json:"type"`
	Signer hexutil.Bytes  `json:"signer"`
	First  common.Hash    `json:"firstDigest"`
	Second common.Hash    `json:"secondDigest"`
	Raw    hexutil.Bytes  `json:"raw"`
}

// GetEvidence returns the equivocation evidence this node has stored,
// ordered by height, round and type.
func (api *BftAPI) GetEvidence() ([]BftEvidence, error) {
	list, err := api.s.evidence.List()
	if err != nil {
		return nil, err
	}
	out := make([]BftEvidence, 0, len(list))
	for _, ev := range list {
		signer, _ := ev.First.Signer()
		raw, err := rlp.EncodeToBytes(ev)
		if err != nil {
			return nil, err
		}
		out = append(out, BftEvidence{Height: hexutil.Uint64(ev.First.Height), Round: hexutil.Uint64(ev.First.Round),
			Type: ev.First.Type.String(), Signer: signer, First: ev.First.Digest, Second: ev.Second.Digest, Raw: raw})
	}
	return out, nil
}

func (s *bftService) apis() []rpc.API {
	return []rpc.API{{Namespace: "metabft", Service: &BftAPI{s}}}
}
