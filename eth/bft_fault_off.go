//go:build !pbftfault

package eth

import "github.com/ethereum/go-ethereum/consensus/metabft"

// Fault injection for the private network (tests/private-net-pbft,
// byzantine.sh) is compiled in only with the pbftfault build tag. Without
// it, these hooks change nothing.

// bftFaultProposer is the proposer the engine hands built blocks to.
func bftFaultProposer(p metabft.Proposer, _ *bftService) metabft.Proposer { return p }

// bftFaultBroadcast may send m itself, reporting true; false leaves it to
// the normal broadcast.
func bftFaultBroadcast(*bftService, *metabft.Message) bool { return false }
