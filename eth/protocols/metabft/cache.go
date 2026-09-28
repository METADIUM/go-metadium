package metabft

import (
	"sync"

	"github.com/ethereum/go-ethereum/consensus/metabft"
)

// Cache de-duplicates verified consensus messages and detects equivocation
// (design §7.1). It must only ever see messages that passed
// Message.Verify: an unverified one could take a genuine sender's slot.
//
// Keyed by (signer, height, round, type), it keeps the first message. The
// same content again is a duplicate; different content — another digest, or
// for a ROUND-CHANGE any other signed content — is an equivocation, returned
// as evidence and not forwarded. The first message keeps counting.
type Cache struct {
	mu      sync.Mutex
	entries map[cacheKey]*metabft.Message
	max     int
}

type cacheKey struct {
	signer        string // the signer's public key
	height, round uint64
	typ           metabft.MsgType
}

// DefaultCacheSize bounds a cache. Entries are pruned per height as heights
// commit, so this is only a backstop against a burst within one height.
const DefaultCacheSize = 1 << 16

// NewCache returns a cache holding at most max entries.
func NewCache(max int) *Cache {
	return &Cache{entries: make(map[cacheKey]*metabft.Message), max: max}
}

// Verdict is what the cache made of a message.
type Verdict int

const (
	Fresh     Verdict = iota // first of its kind: forward it
	Duplicate                // already seen: drop it
	Conflict                 // equivocation: drop it, keep the evidence
	Full                     // cache full: drop it
)

// Add records a verified message from signer and says what to do with it.
// For a Conflict it also returns the evidence (first, this one).
func (c *Cache) Add(signer []byte, m *metabft.Message) (Verdict, *metabft.Evidence) {
	key := cacheKey{string(signer), m.Height, m.Round, m.Type}
	c.mu.Lock()
	defer c.mu.Unlock()

	first, seen := c.entries[key]
	if !seen {
		if len(c.entries) >= c.max {
			return Full, nil
		}
		c.entries[key] = m
		return Fresh, nil
	}
	if sameContent(first, m) {
		return Duplicate, nil
	}
	return Conflict, &metabft.Evidence{First: *first, Second: *m}
}

func sameContent(a, b *metabft.Message) bool {
	if a.Type == metabft.MsgRoundChange {
		return a.SigningHash() == b.SigningHash()
	}
	return a.Digest == b.Digest
}

// Prune drops every entry below height.
func (c *Cache) Prune(height uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if k.height < height {
			delete(c.entries, k)
		}
	}
}

// Len returns the number of entries.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
