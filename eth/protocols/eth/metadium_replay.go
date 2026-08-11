package eth

import "time"

// metaReplaySkewSeconds bounds how far a meta/69 message Timestamp may deviate
// from local time. Generous enough to tolerate real node clock drift, tight
// enough to keep the replay window small. (M12)
const metaReplaySkewSeconds = 60

// nextMetaNonce returns the next outbound meta/69 nonce for this peer. Nonces
// start at 1 and increase monotonically for the lifetime of the connection.
func (p *Peer) nextMetaNonce() uint64 {
	return p.metaSendNonce.Add(1)
}

// acceptMetaReplay reports whether an inbound meta/69 message carrying the given
// nonce/timestamp is fresh, and records it. It rejects:
//   - timestamps outside +/- metaReplaySkewSeconds of local time (stale or future), and
//   - nonces that do not strictly increase versus the highest already accepted
//     from this peer (verbatim replays / out-of-order duplicates).
//
// It is safe for concurrent use; in practice it is called from the serial
// per-peer message-read path before the message is acted upon.
func (p *Peer) acceptMetaReplay(nonce, timestamp uint64) bool {
	now := uint64(time.Now().Unix())
	if timestamp+metaReplaySkewSeconds < now || timestamp > now+metaReplaySkewSeconds {
		return false
	}
	for {
		last := p.metaRecvNonce.Load()
		if nonce <= last {
			return false
		}
		if p.metaRecvNonce.CompareAndSwap(last, nonce) {
			return true
		}
	}
}
