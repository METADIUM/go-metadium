// txmaxsize_metadium_test.go -- Metadium fork guard.
//
// A go-ethereum rebase once silently reverted txMaxSize to the upstream
// default (4*txSlotSize, 128KB), which rejects max-size contract deployments
// in the pool before the EIP-3860 check. This test makes the next rebase fail
// loudly instead of shipping that regression again. Each assertion has its
// own failure message so the diagnosis matches the actual cause.

package legacypool

import (
	"testing"

	"github.com/ethereum/go-ethereum/params"
)

func TestTxMaxSizeCoversMaxCode(t *testing.T) {
	// The pool ceiling must track the fork-specific named constant -- an
	// inlined value is invisible to the next rebase.
	if txMaxSize != params.MaxTransactionSize {
		t.Errorf("txMaxSize (%d) != params.MaxTransactionSize (%d): the pool "+
			"ceiling must track the fork-specific constant. A rebase likely "+
			"reverted it to the upstream default.",
			txMaxSize, params.MaxTransactionSize)
	}
	// And the constant itself must keep creation-tx headroom above
	// MaxCodeSize -- with zero headroom every real max-size deployment is
	// still rejected even though the ceiling nominally covers the code.
	if params.MaxTransactionSize < params.MaxCodeSize+params.TxCreationOverhead {
		t.Errorf("params.MaxTransactionSize (%d) < params.MaxCodeSize+"+
			"params.TxCreationOverhead (%d): max-size contract deployments "+
			"will be rejected by the pool before EIP-3860. If MaxCodeSize "+
			"changed intentionally, move MaxTransactionSize with it.",
			params.MaxTransactionSize, params.MaxCodeSize+params.TxCreationOverhead)
	}
}
