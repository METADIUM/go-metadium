package eth

import (
	"errors"

	protoeth "github.com/ethereum/go-ethereum/eth/protocols/eth"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
)

// peerSyncOp checks if the given peer is suitable for syncing with.
// Returns nil op if already in sync or not appropriate to sync.
func (cs *chainSyncer) peerSyncOp(p *protoeth.Peer) (*chainSyncOp, error) {
	if cs.doneCh != nil {
		return nil, errors.New("sync already in progress")
	} else if metaminer.AmPartner() && !metaminer.IsPartner(p.ID()) {
		return nil, errors.New("not a miner")
	}

	mode, ourTD := cs.modeAndLocalHead()
	op := peerToSyncOp(mode, p)
	if op.td.Cmp(ourTD) <= 0 {
		return nil, nil
	}
	return op, nil
}
