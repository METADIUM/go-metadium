package eth

import (
	"fmt"

	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/params"
)

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
	// it would fail on the first one; say so here instead.
	if syncMode == downloader.SnapSync {
		return fmt.Errorf("chain config sets bftBlock %v, which needs --syncmode full; snap sync cannot verify PBFT headers",
			config.BftBlock)
	}
	return nil
}
