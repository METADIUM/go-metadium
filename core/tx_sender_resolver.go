// tx_sender_resolver - kept for compatibility

package core

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
)

// SenderResolverCache is a simple LRU cache for resolved sender addresses.
// Used by Metadium to cache ecrecover results.
type SenderResolverCache = lru.LruCache

// NewSenderResolverCache creates a new sender resolver cache.
func NewSenderResolverCache(cacheSize int) *lru.LruCache {
	return lru.NewLruCache(cacheSize, true)
}

// GetCachedSender looks up a sender address from the cache by tx hash.
func GetCachedSender(cache *lru.LruCache, hash common.Hash) *common.Address {
	if data := cache.Get(hash); data != nil {
		addr := data.(common.Address)
		return &addr
	}
	return nil
}
