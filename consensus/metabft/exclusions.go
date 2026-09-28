package metabft

import (
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// ExcludeFor is how long a transaction that would break the validator floor
// is left out of this node's proposals before it is tried again (design
// §9.3.1). It is time, not heights: what makes such a transaction
// includable is governance changing or its ballot ending, and ballots are
// timed; a height count would stretch from seconds to minutes with the
// block rate.
const ExcludeFor = 30 * time.Second

// ExcludedTx is a transaction this node leaves out of its proposals.
type ExcludedTx struct {
	Hash   common.Hash
	Sender common.Address
	Nonce  uint64
	Reason string
	Until  time.Time // tried again from then
}

// ExcludeTx records that tx would break the validator floor, so the miner
// leaves it out until ExcludeFor has passed. Its sender's later
// transactions wait behind it in nonce order; replacing its nonce frees
// them at once.
func (e *Engine) ExcludeTx(hash common.Hash, sender common.Address, nonce uint64, reason string) time.Time {
	e.exclMu.Lock()
	defer e.exclMu.Unlock()
	until := time.Now().Add(ExcludeFor)
	e.excluded[hash] = ExcludedTx{Hash: hash, Sender: sender, Nonce: nonce, Reason: reason, Until: until}
	return until
}

// IsExcluded reports whether the miner leaves hash out now.
func (e *Engine) IsExcluded(hash common.Hash) bool {
	e.exclMu.Lock()
	defer e.exclMu.Unlock()
	x, ok := e.excluded[hash]
	if ok && !time.Now().Before(x.Until) {
		delete(e.excluded, hash)
		return false
	}
	return ok
}

// ExcludedTxs lists the transactions left out now, for the status RPC.
func (e *Engine) ExcludedTxs() []ExcludedTx {
	e.exclMu.Lock()
	defer e.exclMu.Unlock()
	now := time.Now()
	out := make([]ExcludedTx, 0, len(e.excluded))
	for h, x := range e.excluded {
		if !now.Before(x.Until) {
			delete(e.excluded, h)
			continue
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until.Before(out[j].Until) })
	return out
}
