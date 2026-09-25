package eth

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/params"
)

// errBftNotImplemented refuses PBFT chains until the engine exists. Without
// it, a node would keep sealing PoA blocks past bftBlock, which is exactly the
// silent continuation docs/pbft-consensus-design.md §9.3 forbids. Remove it
// together with P5-01 in docs/pbft-implementation-checklist.md.
var errBftNotImplemented = errors.New("chain config sets bftBlock, but this build has no PBFT engine yet")

// checkBftNode enforces the node-side rules for PBFT chains
// (docs/pbft-consensus-design.md §8.1, §8.2).
func checkBftNode(config *params.ChainConfig, consensusMethod int, syncMode downloader.SyncMode) error {
	// A stored config is not re-validated anywhere else on this path.
	if err := config.CheckConfigForkOrder(); err != nil {
		return err
	}
	if config.BftBlock == nil {
		return nil
	}
	// The PoA bootstrap segment runs the Metadium PoA engine; PBFT switches
	// inside that engine family at bftBlock rather than by flag.
	if consensusMethod != params.ConsensusPoA {
		return fmt.Errorf("chain config sets bftBlock %v, which needs --consensusmethod %d (PoA), have %d",
			config.BftBlock, params.ConsensusPoA, consensusMethod)
	}
	// A PBFT header is verified against the validator set in its parent's
	// state, and snap sync has no state for the headers it takes (§7.7), so
	// it would fail on the first one; say so here instead. Checked before
	// errBftNotImplemented on purpose: it is the more useful message.
	if syncMode == downloader.SnapSync {
		return fmt.Errorf("chain config sets bftBlock %v, which needs --syncmode full; snap sync cannot verify PBFT headers",
			config.BftBlock)
	}
	return errBftNotImplemented
}
