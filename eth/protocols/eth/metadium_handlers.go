package eth

import (
	"fmt"

	"github.com/ethereum/go-ethereum/core/types"
	metaapi "github.com/ethereum/go-ethereum/metadium/api"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
)

func handleGetPendingTxs(backend Backend, msg Decoder, peer *Peer) error {
	// not supported, just ignore it.
	return nil
}

func handleGetStatusEx(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}

	go func() {
		statusEx := metaapi.GetMinerStatus()
		if statusEx == nil {
			// ignore the error, most likely server is shutting down
			return
		}
		statusEx.LatestBlockTd = backend.Chain().GetTd(statusEx.LatestBlockHash,
			statusEx.LatestBlockHeight.Uint64())
		if err := peer.SendStatusEx(statusEx); err != nil {
			// ignore the error
		}
	}()

	return nil
}

func handleStatusEx(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}
	var status metaapi.MetadiumMinerStatus
	if err := msg.Decode(&status); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}

	go func() {
		if _, td := peer.Head(); status.LatestBlockTd.Cmp(td) > 0 {
			peer.SetHead(status.LatestBlockHash, status.LatestBlockTd)
		}
		metaapi.GotStatusEx(&status)
	}()

	return nil
}

func handleEtcdAddMember(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}

	go func() {
		cluster, _ := metaapi.EtcdAddMember(peer.ID())
		if err := peer.SendEtcdCluster(cluster); err != nil {
			// ignore the error
		}
	}()

	return nil
}

func handleEtcdCluster(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}
	var cluster string
	if err := msg.Decode(&cluster); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}

	go metaapi.GotEtcdCluster(cluster)

	return nil
}

// handleTransactionsEx handles the Metadium extended transactions message.
// It converts TransactionEx packets to regular transactions and delivers to the pool.
func handleTransactionsEx(backend Backend, msg Decoder, peer *Peer) error {
	if !backend.AcceptTxs() {
		return nil
	}
	var txexs TransactionsExPacket
	if err := msg.Decode(&txexs); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	signer := types.MakeSigner(backend.Chain().Config(), backend.Chain().CurrentBlock().Number, backend.Chain().CurrentBlock().Time)
	txs := types.TxExs2Txs(signer, txexs, metaminer.IsPartner(peer.ID()))
	for i, tx := range txs {
		if tx == nil {
			return fmt.Errorf("%w: transaction %d is nil", errDecode, i)
		}
		peer.markTransaction(tx.Hash())
	}
	txsp := TransactionsPacket(txs)
	return backend.Handle(peer, &txsp)
}

// handleNewPooledTransactionHashes66 handles the eth/66 NewPooledTransactionHashes
// message, which contains only hashes (no type/size info as in eth/68).
func handleNewPooledTransactionHashes66(backend Backend, msg Decoder, peer *Peer) error {
	if !backend.AcceptTxs() {
		return nil
	}
	var hashes NewPooledTransactionHashesPacket66
	if err := msg.Decode(&hashes); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	for _, hash := range hashes {
		peer.markTransaction(hash)
	}
	// Synthesize a eth/68-style packet with empty types/sizes for the backend handler.
	ann := &NewPooledTransactionHashesPacket{
		Types:  make([]byte, len(hashes)),
		Sizes:  make([]uint32, len(hashes)),
		Hashes: hashes,
	}
	return backend.Handle(peer, ann)
}

// handleGetNodeData handles a GetNodeData request from an eth/66 peer.
// Node data serving is not supported in this version; return empty response.
func handleGetNodeData(backend Backend, msg Decoder, peer *Peer) error {
	var query GetNodeDataPacket
	if err := msg.Decode(&query); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	return peer.SendNodeData([][]byte{})
}

// handleNodeData handles a NodeData response from an eth/66 peer.
// Since we don't request node data, this is a no-op.
func handleNodeData(backend Backend, msg Decoder, peer *Peer) error {
	var data NodeDataPacket
	if err := msg.Decode(&data); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	return nil
}
