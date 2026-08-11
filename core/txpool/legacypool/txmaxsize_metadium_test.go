// txmaxsize_metadium_test.go -- Metadium fork guard.
//
// A go-ethereum rebase once silently reverted txMaxSize to the upstream
// default (4*txSlotSize, 128KB), which rejects max-size contract deployments
// in the pool before the EIP-3860 check. This test makes the next rebase fail
// loudly instead of shipping that regression again.

package legacypool

import (
	"testing"

	"github.com/ethereum/go-ethereum/params"
)

// minCreationOverhead is the headroom the pool ceiling must leave above the
// raw code size: constructor bytecode, ABI-encoded arguments, RLP envelope
// and signature. 8KB matches the historical MaxTransactionSize-MaxCodeSize gap.
const minCreationOverhead = 8192

func TestTxMaxSizeCoversMaxCode(t *testing.T) {
	// A creation tx carries initcode (up to MaxCodeSize) plus constructor
	// arguments, signature and RLP envelope. The pool admission ceiling must
	// leave room for that overhead above the raw code size -- zero headroom
	// would pass a bare ">= MaxCodeSize" check while still rejecting every
	// real max-size deployment.
	if txMaxSize < params.MaxCodeSize+minCreationOverhead {
		t.Fatalf("txMaxSize (%d) < params.MaxCodeSize+minCreationOverhead (%d): "+
			"max-size contract deployments will be rejected by the pool before "+
			"EIP-3860. A rebase likely reverted the Metadium ceiling to the "+
			"upstream default.",
			txMaxSize, params.MaxCodeSize+minCreationOverhead)
	}
	if txMaxSize != params.MaxTransactionSize {
		t.Fatalf("txMaxSize (%d) != params.MaxTransactionSize (%d): the pool "+
			"ceiling must track the fork-specific constant, not an inlined value.",
			txMaxSize, params.MaxTransactionSize)
	}
}
