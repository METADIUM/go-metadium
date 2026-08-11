// Copyright 2018-2022 The go-metadium / go-metadium Authors

package miner

import (
	"errors"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// TrieAccessMu serializes archive-mode triedb commits against concurrent
// in-process RPC EVM reads issued by the metadium admin module. Without
// this mutex, a triedb.Commit(root, false) performed by writeBlockWithState
// in archive mode can race with an EVM call dispatched from
// accumulateRewards -> admin.calculateRewards -> metclient.CallContract,
// causing a transient "registry not initialized" miss on early historical
// blocks (e.g. testnet block 18) and a divergent stateRoot.
//
// Writers (archive triedb.Commit) take the write lock; readers
// (metclient.CallContract) take the read lock so concurrent contract reads
// remain parallel.
var TrieAccessMu sync.RWMutex

var (
	ErrNotInitialized = errors.New("not initialized")

	IsMinerFunc                 func() bool
	AmPartnerFunc               func() bool
	IsPartnerFunc               func(string) bool
	AmHubFunc                   func(string) int
	LogBlockFunc                func(int64, common.Hash)
	CalculateRewardsFunc        func(*big.Int, *big.Int, *big.Int, func(common.Address, *big.Int)) (*common.Address, []byte, error)
	VerifyRewardsFunc           func(*big.Int, string) error
	GetCoinbaseFunc             func(height *big.Int) (coinbase common.Address, err error)
	SignBlockFunc               func(height *big.Int, hash common.Hash, isPangyo bool) (coinbase common.Address, nodeId, sig []byte, err error)
	VerifyBlockSigFunc          func(height *big.Int, coinbase common.Address, nodeId []byte, hash common.Hash, sig []byte, isPangyo bool) bool
	RequirePendingTxsFunc       func() bool
	VerifyBlockRewardsFunc      func(height *big.Int) interface{}
	SuggestGasPriceFunc         func() *big.Int
	GetBlockBuildParametersFunc func(height *big.Int) (blockInterval int64, maxBaseFee, gasLimit *big.Int, baseFeeMaxChangeRate, gasTargetPercentage int64, err error)
	AcquireMiningTokenFunc      func(height *big.Int, parentHash common.Hash) (bool, error)
	ReleaseMiningTokenFunc      func(height *big.Int, hash, parentHash common.Hash) error
	HasMiningTokenFunc          func() bool
	// GetFinalizedBlockNumberFunc returns the finalized block number given the
	// current head number. Metadium PoA finality is the BFT majority depth:
	// headNum - (GovNodeCount/2 + 1). Returns nil when no block at or below
	// head is final yet, or when governance state is not yet loaded.
	GetFinalizedBlockNumberFunc func(headNum *big.Int) *big.Int
	// Add TRS
	GetTRSListMapFunc func(height *big.Int) (trsListMap map[common.Address]bool, trsSubscribe bool, err error)
)

// GetFinalizedBlockNumber returns the finalized block number for the given
// head, or nil if finality cannot yet be computed (admin uninitialized, head
// shallower than the BFT lookback, etc.).
func GetFinalizedBlockNumber(headNum *big.Int) *big.Int {
	if GetFinalizedBlockNumberFunc == nil {
		return nil
	}
	return GetFinalizedBlockNumberFunc(headNum)
}

func IsMiner() bool {
	if IsMinerFunc == nil {
		return false
	} else {
		return IsMinerFunc()
	}
}

func IsPartner(id string) bool {
	if IsPartnerFunc == nil {
		return false
	} else {
		return IsPartnerFunc(id)
	}
}

func AmPartner() bool {
	if AmPartnerFunc == nil {
		return false
	} else {
		return AmPartnerFunc()
	}
}

func AmHub(id string) int {
	if AmHubFunc == nil {
		return -1
	} else {
		return AmHubFunc(id)
	}
}

func LogBlock(height int64, hash common.Hash) {
	if LogBlockFunc != nil {
		LogBlockFunc(height, hash)
	}
}

func AcquireMiningToken(height *big.Int, parentHash common.Hash) (bool, error) {
	if AcquireMiningTokenFunc == nil {
		return false, ErrNotInitialized
	}
	return AcquireMiningTokenFunc(height, parentHash)
}

func ReleaseMiningToken(height *big.Int, hash, parentHash common.Hash) error {
	if ReleaseMiningTokenFunc == nil {
		return ErrNotInitialized
	}
	return ReleaseMiningTokenFunc(height, hash, parentHash)
}

func HasMiningToken() bool {
	if HasMiningTokenFunc == nil {
		return false
	}
	return HasMiningTokenFunc()
}

func IsPoW() bool {
	return params.ConsensusMethod == params.ConsensusPoW
}

func CalculateRewards(num, blockReward, fees *big.Int, addBalance func(common.Address, *big.Int)) (*common.Address, []byte, error) {
	if CalculateRewardsFunc == nil {
		return nil, nil, ErrNotInitialized
	} else {
		return CalculateRewardsFunc(num, blockReward, fees, addBalance)
	}
}

func VerifyRewards(num *big.Int, rewards string) error {
	if VerifyRewardsFunc == nil {
		return ErrNotInitialized
	} else {
		return VerifyRewardsFunc(num, rewards)
	}
}

func GetCoinbase(height *big.Int) (coinbase common.Address, err error) {
	if GetCoinbaseFunc == nil {
		err = ErrNotInitialized
	} else {
		coinbase, err = GetCoinbaseFunc(height)
	}
	return
}

func SignBlock(height *big.Int, hash common.Hash, isPangyo bool) (coinbase common.Address, nodeId, sig []byte, err error) {
	if SignBlockFunc == nil {
		err = ErrNotInitialized
	} else {
		coinbase, nodeId, sig, err = SignBlockFunc(height, hash, isPangyo)
	}
	return
}

func VerifyBlockSig(height *big.Int, coinbase common.Address, nodeId []byte, hash common.Hash, sig []byte, isPangyo bool) bool {
	if VerifyBlockSigFunc == nil {
		return false
	} else {
		return VerifyBlockSigFunc(height, coinbase, nodeId, hash, sig, isPangyo)
	}
}

func RequirePendingTxs() bool {
	if RequirePendingTxsFunc == nil {
		return false
	} else {
		return RequirePendingTxsFunc()
	}
}

func VerifyBlockRewards(height *big.Int) interface{} {
	if VerifyBlockRewardsFunc == nil {
		return false
	} else {
		return VerifyBlockRewardsFunc(height)
	}
}

func SuggestGasPrice() *big.Int {
	if SuggestGasPriceFunc == nil {
		return big.NewInt(100 * params.GWei)
	} else {
		return SuggestGasPriceFunc()
	}
}

func GetBlockBuildParameters(height *big.Int) (blockInterval int64, maxBaseFee, gasLimit *big.Int, baseFeeMaxChangeRate, gasTargetPercentage int64, err error) {
	if GetBlockBuildParametersFunc == nil {
		// default values
		return 15, big.NewInt(0), big.NewInt(0), 0, 100, ErrNotInitialized
	} else {
		return GetBlockBuildParametersFunc(height)
	}
}

// Add TRS
func GetTRSListMap(height *big.Int) (trsListMap map[common.Address]bool, trsSubscribe bool, err error) {
	if GetTRSListMapFunc == nil {
		err = ErrNotInitialized
		trsSubscribe = false
	} else {
		trsListMap, trsSubscribe, err = GetTRSListMapFunc(height)
	}
	return
}

// TRSRestricted reports whether a transaction between from and to touches the
// TRS restriction list. to is nil for contract creations. This is the single
// definition of the restriction predicate -- the tx pools and the miner all
// call it, so admission, sweeping and block building cannot drift apart.
func TRSRestricted(trsListMap map[common.Address]bool, from common.Address, to *common.Address) bool {
	if len(trsListMap) == 0 {
		return false
	}
	return trsListMap[from] || (to != nil && trsListMap[*to])
}

// EOF
