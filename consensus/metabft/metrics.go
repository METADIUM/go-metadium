package metabft

import "github.com/ethereum/go-ethereum/metrics"

// Timers for the §11.3 measurements, collected with --metrics (see
// tests/private-net-pbft/measure.py).
var (
	// M-05: the fsync of one WAL record; about three per block.
	walSyncTimer = metrics.NewRegisteredTimer("metabft/wal/sync", nil)
	// M-07: a validator's check of a proposal, and within it the
	// execution (Process and ValidateState), which the import of the
	// decided block repeats (chain/execution, chain/validation).
	proposalVerifyTimer  = metrics.NewRegisteredTimer("metabft/proposal/verify", nil)
	proposalExecuteTimer = metrics.NewRegisteredTimer("metabft/proposal/execute", nil)
	// M-10: waiting for a proposal's blob sidecars from peers.
	sidecarFetchTimer = metrics.NewRegisteredTimer("metabft/proposal/sidecars", nil)
)
