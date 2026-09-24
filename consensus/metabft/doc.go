// Package metabft implements the PBFT (IBFT 2.0 family) consensus for new
// Metadium private networks, as specified in docs/pbft-consensus-design.md.
//
// This package is being built phase by phase (docs/pbft-implementation-checklist.md).
// It currently holds the parts that need no chain: the validator set and
// quorum, the signed message format and commit seals, the vote WAL and the
// equivocation evidence store.
package metabft
