// legacy.go - legacy functions for metadium

package metadium

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	metaapi "github.com/ethereum/go-ethereum/metadium/api"
	"github.com/ethereum/go-ethereum/metadium/metclient"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
	"github.com/ethereum/go-ethereum/params"
)

// temporary internal structure to collect data from governance contracts
type govdataLegacy struct {
	blockNum, modifiedBlock                       int64
	blocksPer, maxIdleBlockInterval               int64
	gasPrice                                      *big.Int
	nodes, addedNodes, updatedNodes, deletedNodes []*metaNode
}

var (
	syncLock = &sync.Mutex{}
)

func (ma *metaAdmin) getGovDataLegacy() (data *govdataLegacy, err error) {
	data = &govdataLegacy{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	block, err := ma.cli.HeaderByNumber(ctx, nil)
	if err != nil {
		return
	}
	data.blockNum = block.Number.Int64()
	if data.blockNum <= ma.lastBlock {
		return
	}

	env := &metclient.RemoteContract{
		Cli: ma.cli,
		Abi: envStorageImpLegacyContract.Abi,
		To:  ma.envStorage.To,
	}
	gov := &metclient.RemoteContract{
		Cli: ma.cli,
		Abi: govLegacyContract.Abi,
		To:  ma.gov.To,
	}

	data.modifiedBlock, err = ma.getInt(ctx, gov, block.Number, "modifiedBlock")
	if err != nil {
		return
	}
	if ma.modifiedBlock == data.modifiedBlock {
		return
	}

	data.blocksPer, err = ma.getInt(ctx, env, block.Number, "getBlocksPer")
	if err != nil {
		data.blocksPer = ma.blocksPer
		return
	}
	data.maxIdleBlockInterval, err = ma.getInt(ctx, env, block.Number, "getMaxIdleBlockInterval")
	if err != nil {
		data.maxIdleBlockInterval = int64(params.MaxIdleBlockInterval)
		return
	}
	err = metclient.CallContract(ctx, env, "getGasPrice", nil, &data.gasPrice, block.Number)
	if err != nil {
		return
	}

	data.nodes, err = ma.getMetaNodes(ctx, block.Number)
	if err != nil {
		return
	}

	oldNodes := ma.getNodes()
	sort.Slice(oldNodes, func(i, j int) bool {
		return oldNodes[i].Name < oldNodes[j].Name
	})
	sort.Slice(data.nodes, func(i, j int) bool {
		return data.nodes[i].Name < data.nodes[j].Name
	})

	i, j := 0, 0
	for {
		if i >= len(oldNodes) || j >= len(data.nodes) {
			break
		}
		v := strings.Compare(oldNodes[i].Name, data.nodes[j].Name)
		if v == 0 {
			if !oldNodes[i].eq(data.nodes[j]) {
				data.updatedNodes = append(data.updatedNodes, data.nodes[j])
			}
			i++
			j++
		} else if v < 0 {
			data.deletedNodes = append(data.deletedNodes, oldNodes[i])
			i++
		} else if v > 0 {
			data.addedNodes = append(data.addedNodes, data.nodes[j])
			j++
		}
	}

	if i < len(oldNodes) {
		for ; i < len(oldNodes); i++ {
			data.deletedNodes = append(data.deletedNodes, oldNodes[i])
		}
	}
	if j < len(data.nodes) {
		for ; j < len(data.nodes); j++ {
			data.addedNodes = append(data.addedNodes, data.nodes[j])
		}
	}

	return
}

func (ma *metaAdmin) getRewardAccountsLegacy(ctx context.Context, block *big.Int) (rewardPoolAccount, maintenanceAccount *common.Address, members []*metaMember, err error) {
	var (
		addr  common.Address
		count int64
		stake *big.Int
		input []interface{}
	)

	if ma.registry == nil || ma.registry.To == nil {
		err = metaminer.ErrNotInitialized
		return
	}

	reg := &metclient.RemoteContract{
		Cli: ma.cli,
		Abi: registryLegacyContract.Abi,
		To:  ma.registry.To,
	}
	gov := &metclient.RemoteContract{
		Cli: ma.cli,
		Abi: govLegacyContract.Abi,
		To:  ma.gov.To,
	}
	staking := &metclient.RemoteContract{
		Cli: ma.cli,
		Abi: stakingLegacyContract.Abi,
		To:  ma.staking.To,
	}

	input = []interface{}{metclient.ToBytes32("RewardPool")}
	err = metclient.CallContract(ctx, reg, "getContractAddress", input, &addr, block)
	if err != nil {
		rewardPoolAccount = nil
	} else {
		rewardPoolAccount = &common.Address{}
		rewardPoolAccount.SetBytes(addr.Bytes())
	}

	input = []interface{}{metclient.ToBytes32("Maintenance")}
	err = metclient.CallContract(ctx, reg, "getContractAddress", input, &addr, block)
	if err != nil {
		maintenanceAccount = nil
	} else {
		maintenanceAccount = &common.Address{}
		maintenanceAccount.SetBytes(addr.Bytes())
	}

	count, err = ma.getInt(ctx, gov, block, "getMemberLength")
	if err != nil {
		return
	}

	for i := int64(1); i <= count; i++ {
		input = []interface{}{big.NewInt(i)}
		err = metclient.CallContract(ctx, gov, "getReward", input,
			&addr, block)
		if err != nil {
			return
		}
		input = []interface{}{addr}
		err = metclient.CallContract(ctx, staking, "lockedBalanceOf", input,
			&stake, block)
		if err != nil {
			return
		}

		members = append(members, &metaMember{
			Reward: addr,
			Stake:  stake,
		})
	}

	return
}

func distributeRewardsLegacy(six int, rewardPoolAccount, maintenanceAccount *common.Address, members []*metaMember, rewards []reward, amount *big.Int) {
	n := len(members)

	v0 := big.NewInt(0)
	v1 := big.NewInt(1)
	v10 := big.NewInt(10)
	v45 := big.NewInt(45)
	v100 := big.NewInt(100)
	vn := big.NewInt(int64(n))

	minerAmount := new(big.Int).Set(amount)
	minerAmount.Mul(minerAmount, v45)
	minerAmount.Div(minerAmount, v100)
	maintAmount := new(big.Int).Set(amount)
	maintAmount.Mul(maintAmount, v10)
	maintAmount.Div(maintAmount, v100)
	poolAmount := new(big.Int).Set(amount)
	poolAmount.Sub(poolAmount, minerAmount)
	poolAmount.Sub(poolAmount, maintAmount)

	if n == 0 {
		if rewardPoolAccount != nil {
			poolAmount.Add(poolAmount, minerAmount)
		} else if maintenanceAccount != nil {
			maintAmount.Add(maintAmount, minerAmount)
		}
	}
	if rewardPoolAccount == nil {
		if n != 0 {
			minerAmount.Add(minerAmount, poolAmount)
		} else if maintenanceAccount != nil {
			maintAmount.Add(maintAmount, poolAmount)
		}
	}
	if maintenanceAccount == nil {
		if n != 0 {
			minerAmount.Add(minerAmount, maintAmount)
		} else if rewardPoolAccount != nil {
			poolAmount.Add(poolAmount, maintAmount)
		}
	}

	if n > 0 {
		b := new(big.Int).Set(minerAmount)
		d := new(big.Int)
		d.Div(b, vn)
		for i := 0; i < n; i++ {
			rewards[i].Addr = members[i].Reward
			rewards[i].Reward = new(big.Int).Set(d)
		}
		d.Mul(d, vn)
		b.Sub(b, d)
		for i := 0; i < n && b.Cmp(v0) > 0; i++ {
			rewards[six].Reward.Add(rewards[six].Reward, v1)
			b.Sub(b, v1)
			six = (six + 1) % n
		}
	}

	if rewardPoolAccount != nil {
		rewards[n].Addr = *rewardPoolAccount
		rewards[n].Reward = poolAmount
		n++
	}
	if maintenanceAccount != nil {
		rewards[n].Addr = *maintenanceAccount
		rewards[n].Reward = maintAmount
	}
}

func (ma *metaAdmin) calculateRewardsLegacy(num, blockReward, fees *big.Int, addBalance func(common.Address, *big.Int)) (coinbase *common.Address, rewards []byte, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rewardPoolAccount, maintenanceAccount, members, err := ma.getRewardAccountsLegacy(ctx, big.NewInt(num.Int64()-1))
	if err != nil {
		// all goes to the coinbase
		return nil, nil, err
	}

	if rewardPoolAccount == nil && maintenanceAccount == nil && len(members) == 0 {
		err = metaminer.ErrNotInitialized
		return
	}

	// determine coinbase
	if len(members) > 0 {
		mix := num.Int64() / ma.blocksPer % int64(len(members))
		coinbase = &common.Address{}
		coinbase.SetBytes(members[mix].Reward.Bytes())
	}

	n := len(members)
	if rewardPoolAccount != nil {
		n++
	}
	if maintenanceAccount != nil {
		n++
	}

	six := 0
	if len(members) > 0 {
		six = int(new(big.Int).Mod(num, big.NewInt(int64(len(members)))).Int64())
	}

	rr := make([]reward, n)
	distributeRewardsLegacy(six, rewardPoolAccount, maintenanceAccount, members, rr,
		new(big.Int).Add(blockReward, fees))

	if addBalance != nil {
		for _, i := range rr {
			addBalance(i.Addr, i.Reward)
		}
	}

	rewards, err = json.Marshal(rr)
	return
}

func (ma *metaAdmin) getLatestBlockInfo(node *metaNode) (height *big.Int, hash common.Hash, td *big.Int, err error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := make(chan *metaapi.MetadiumMinerStatus, len(ma.nodes)*2+1)
	sub := metaapi.SubscribeToMinerStatus(ch)
	defer func() {
		sub.Unsubscribe()
		close(ch)
	}()

	timer := time.NewTimer(60 * time.Second)
	err = ma.rpcCli.CallContext(ctx, nil, "admin_requestMinerStatus", &node.Id)
	if err != nil {
		log.Info("Metadium RequestMinerStatus Failed", "id", node.Id, "error", err)
		return
	}

	done := false
	for {
		if done {
			break
		}
		select {
		case status := <-ch:
			if status.NodeName != node.Name {
				continue
			}
			height, hash, td, err = status.LatestBlockHeight, status.LatestBlockHash, status.LatestBlockTd, nil
			return

		case <-timer.C:
			err = ErrNotRunning
			return
		}
	}
	err = ethereum.NotFound
	return
}

// syncLock should be held by the caller
func (ma *metaAdmin) syncWith(node *metaNode) error {
	tsync := time.Now()
	height, hash, td, err := ma.getLatestBlockInfo(node)
	if err != nil {
		log.Error("Metadium", "failed to synchronize with", node.Name,
			"error", err, "took", time.Since(tsync))
		return err
	} else {
		log.Info("Metadium", "synchronizing with", node.Name,
			"height", height, "hash", hash, "td", td, "took", time.Since(tsync))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = ma.rpcCli.CallContext(ctx, nil, "admin_synchroniseWith", &node.Id)
	if err != nil {
		log.Error("Metadium", "failed to synchronize with", node.Name,
			"error", err, "took", time.Since(tsync))
	} else {
		log.Info("Metadium", "synchronized with", node.Name, "took", time.Since(tsync))
	}
	return err
}

// return true if this node still is the miner after update
func (ma *metaAdmin) updateMiner(locked bool) bool {
	if ma.etcd == nil {
		return false
	}

	syncLock.Lock()
	defer syncLock.Unlock()

	var prev, curr uint64
	if value, ok := previousEtcdLeader.Load().(uint64); ok {
		prev = value
	}
	if value, ok := latestEtcdLeader.Load().(uint64); ok {
		curr = value
	}
	lid, lnode := ma.etcdLeader(locked)
	pnode := ma.etcdGetNode(prev)
	if prev != curr && lid == curr && lnode == ma.self && pnode != nil {
		// We are the new leader. Make sure we have the latest block.
		// Otherwise, punt the leadership to the next in line.
		// If all fails, accept the potential fork and move on.

		log.Info("Metadium: we are the new leader")
		tstart := time.Now()
		previousEtcdLeader.Store(curr)

		// get the latest work info from etcd
		getLatestWork := func() (*metaWork, error) {
			var (
				workInfo string
				work     *metaWork
				retries  = 60
				err      error
			)

			for ; retries > 0; retries-- {
				workInfo, err = ma.etcdGet(metaWorkKey)
				if err != nil {
					// TODO: ignore if error is not found
					log.Info("Metadium - cannot get the latest work info",
						"error", err, "took", time.Since(tstart))
					continue
				}

				if workInfo == "" {
					log.Info("Metadium - the latest work info not logged yet")
					return nil, nil
				} else {
					if err = json.Unmarshal([]byte(workInfo), &work); err != nil {
						log.Error("Metadium - cannot get the latest work info",
							"error", err, "took", time.Since(tstart))
						return nil, err
					}
					log.Info("Metadium - got the latest work info",
						"height", work.Height, "hash", work.Hash,
						"took", time.Since(tstart))
					return work, nil
				}
			}
			return nil, ethereum.NotFound
		}

		// check if we are in sync with the latest work info recorded
		inSync := func(work *metaWork) (synced bool, latestNum uint64, curNum uint64) {
			synced, latestNum, curNum = false, 0, 0

			if work == nil {
				synced = true
				return
			}
			latestNum = uint64(work.Height)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cur, err := ma.cli.HeaderByNumber(ctx, big.NewInt(work.Height))
			if err != nil {
				return
			}
			curNum = uint64(cur.Number.Int64())
			synced = bytes.Equal(cur.Hash().Bytes(), work.Hash.Bytes())
			return
		}

		// if we are not in sync, punt the leadership to the next in line
		// if all fails, just move on
		puntLeadership := func() error {
			nodes := ma.getNodes()
			if len(nodes) == 0 {
				return ethereum.NotFound
			}

			ix := 0
			for i, node := range nodes {
				if node.Id == ma.self.Id {
					ix = i
					break
				}
			}
			if ix >= len(nodes) {
				return ethereum.NotFound
			}

			var err error
			for i, j := 0, (ix+1)%len(nodes); i < len(nodes)-1; i++ {
				err = ma.etcdMoveLeader(nodes[j].Name)
				if err == nil {
					return nil
				}
				j = (j + 1) % len(nodes)
			}

			return err
		}

		work, err := getLatestWork()
		if err != nil {
			log.Error("Metadium - cannot get the latest work information. Yielding leadeship")
			err = puntLeadership()
			if err != nil {
				log.Error("Metadium - leadership yielding failed", "error", err)
			} else {
				log.Info("Metadium - yielded leadership")
			}
		} else if work == nil {
			// this must be the first block, juts move on
			log.Info("Metadium - not initialized yet. Starting mining")
		} else if synced, _, height := inSync(work); synced {
			log.Info("Metadium - in sync. Starting mining", "height", height)
		} else {
			// sync with the previous leader
			ma.syncWith(pnode)

			// check sync again
			work, err = getLatestWork()
			if err != nil {
				log.Error("Metadium - cannot get the latest work", "error", err)
			} else if work == nil {
				// this must be the first block, juts move on
			} else if synced, _, height := inSync(work); !synced {
				// if still not in sync, give up leadership
				err = puntLeadership()
				if err != nil {
					log.Error("Metadium - not in sync. Leadership yielding failed",
						"latest", work.Height, "current", height, "error", err)
				} else {
					log.Error("Metadium - not in sync. Yielded leadership",
						"latest", work.Height, "current", height)
				}
			}
		}
	}

	return lnode == ma.self
}

func IsMiner() bool {
	if params.ConsensusMethod == params.ConsensusPoW {
		return true
	} else if params.ConsensusMethod == params.ConsensusETCD {
		return false
	} else if params.ConsensusMethod == params.ConsensusPoA {
		if admin == nil {
			return false
		} else if admin.self == nil || len(admin.nodes) <= 0 {
			if admin.nodeInfo != nil && admin.nodeInfo.ID == admin.bootNodeId {
				return true
			} else {
				return false
			}
		}

		if admin.etcdIsLeader() {
			return admin.updateMiner(false)
		} else {
			admin.blocksMined = 0
			return false
		}
	} else {
		return false
	}
}

func LogBlock(height int64, hash common.Hash) {
	if admin == nil || admin.self == nil {
		return
	}

	admin.lock.Lock()
	defer admin.lock.Unlock()

	work, err := json.Marshal(&metaWork{
		Height: height,
		Hash:   hash,
	})
	if err != nil {
		log.Error("marshaling failure????")
	}

	tstart := time.Now()
	_, err = admin.etcdPut(metaWorkKey, string(work))
	if err != nil {
		log.Error("Metadium - failed to log the latest block",
			"height", height, "hash", hash, "took", time.Since(tstart))
	} else {
		log.Info("Metadium - logged the latest block",
			"height", height, "hash", hash, "took", time.Since(tstart))
	}

	admin.blocksMined++
	height++
	if admin.blocksMined >= admin.blocksPer &&
		height%admin.blocksPer == 0 {
		// time to yield leader role

		_, next, _ := admin.getMinerNodes(height, true)
		if next.Id == admin.self.Id {
			log.Info("Metadium - yield to self", "mined", admin.blocksMined,
				"new miner", "self")
		} else {
			if err := admin.etcdMoveLeader(next.Name); err == nil {
				log.Info("Metadium - yielded", "mined", admin.blocksMined,
					"new miner", next.Name)
				admin.blocksMined = 0
			} else {
				log.Error("Metadium - yield failed", "mined", admin.blocksMined,
					"new miner", next.Name, "error", err)
			}
		}
	}
}

// EOF
