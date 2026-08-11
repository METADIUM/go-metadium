// tx_sender_resolver - Metadium Sender Resolver for legacypool

package legacypool

import (
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/core/types"
)

// job structure for sender resolver work queue
type senderResolverJob struct {
	f     func(interface{})
	param interface{}
}

// SenderResolver resolves sender accounts from transactions concurrently
// with worker threads.
type SenderResolver struct {
	tx2addr *lru.LruCache
	jobs    chan *senderResolverJob
	busy    chan interface{}
}

// NewSenderResolver creates a new sender resolver worker pool.
func NewSenderResolver(concurrency, cacheSize int) *SenderResolver {
	return &SenderResolver{
		tx2addr: lru.NewLruCache(cacheSize, true),
		jobs:    make(chan *senderResolverJob, concurrency),
		busy:    make(chan interface{}, concurrency),
	}
}

// Run is the sender resolver main loop.
func (s *SenderResolver) Run() {
	for {
		j, ok := <-s.jobs
		if !ok || j == nil {
			break
		}
		go func() {
			s.busy <- struct{}{}
			defer func() {
				<-s.busy
			}()
			j.f(j.param)
		}()
	}
}

// Stop stops sender resolver.
func (s *SenderResolver) Stop() {
	s.jobs <- nil
}

// Post posts a new sender resolver task.
func (s *SenderResolver) Post(f func(interface{}), p interface{}) {
	s.jobs <- &senderResolverJob{f: f, param: p}
}

// ResolveSenders resolves sender accounts from given transactions
// concurrently using SenderResolver worker pool.
func (pool *LegacyPool) ResolveSenders(signer types.Signer, txs []*types.Transaction) {
	s := pool.senderResolver
	var by_ecrecover, failed int64

	var wg sync.WaitGroup
	for _, tx := range txs {
		hash := tx.Hash()
		if addr := types.GetSender(signer, tx); addr != nil {
			s.tx2addr.Put(hash, *addr)
			continue
		}

		data := s.tx2addr.Get(hash)
		if data != nil {
			types.SetSender(signer, tx, data.(common.Address))
			continue
		}

		wg.Add(1)
		atomic.AddInt64(&by_ecrecover, 1)
		s.Post(func(param interface{}) {
			t := param.(*types.Transaction)
			if from, err := types.Sender(signer, t); err == nil {
				s.tx2addr.Put(t.Hash(), from)
			} else {
				atomic.AddInt64(&failed, 1)
			}
			wg.Done()
		}, tx)
	}

	wg.Wait()
	_ = by_ecrecover
	_ = failed
}

// ResolveSender resolves sender address from a transaction.
func (pool *LegacyPool) ResolveSender(signer types.Signer, tx *types.Transaction) {
	pool.ResolveSenders(signer, []*types.Transaction{tx})
}
