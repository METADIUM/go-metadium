package metabft

import (
	"math"
	"time"
)

// Config holds the genesis PBFT parameters as durations (params.BftConfig).
type Config struct {
	EmptyBlockInterval time.Duration
	BaseTimeout        time.Duration
	MaxBackoffExp      uint64
}

// deadline returns when round round of the current height times out
// (design §4.5), on the node's local monotonic clock:
//
//	round 0:  committedAt + EmptyBlockInterval + BaseTimeout
//	round r:  roundStart + BaseTimeout * 2^min(r, MaxBackoffExp)
//
// committedAt is when the parent became this node's head, however it got
// there. Header timestamps are never an input, so a proposer cannot move
// anyone's timer.
func (c Config) deadline(round uint64, committedAt, roundStart time.Duration) time.Duration {
	if round == 0 {
		return committedAt + c.EmptyBlockInterval + c.BaseTimeout
	}
	exp := round
	if exp > c.MaxBackoffExp {
		exp = c.MaxBackoffExp
	}
	// The genesis caps MaxBackoffExp (params.checkBft), but the core does not
	// rely on that: the timeout saturates instead of wrapping, which would put
	// the deadline in the past and fire a round change on every tick.
	timeout := c.BaseTimeout
	for i := uint64(0); i < exp && timeout < maxTimeout/2; i++ {
		timeout *= 2
	}
	if timeout > maxTimeout-roundStart {
		return maxTimeout
	}
	return roundStart + timeout
}

// maxTimeout keeps deadline arithmetic far from overflowing time.Duration.
const maxTimeout = time.Duration(math.MaxInt64 / 4)
