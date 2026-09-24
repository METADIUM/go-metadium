package metabft

import "time"

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
	return roundStart + c.BaseTimeout<<exp
}
