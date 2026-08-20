package eth

import (
	"fmt"

	"github.com/ethereum/go-ethereum/core/types"
	metaapi "github.com/ethereum/go-ethereum/metadium/api"
	metaminer "github.com/ethereum/go-ethereum/metadium/miner"
	"github.com/ethereum/go-ethereum/rlp"
)

// Concurrency caps for the asynchronous Metadium message handlers below. Each
// handler spawns a goroutine per inbound message; without a bound, a compromised
// partner (these messages are IsPartner-gated) could flood StatusEx/EtcdCluster
// and exhaust memory through unbounded goroutine creation (audit finding M11).
// When the cap is reached the message is dropped instead of queued: status
// gossip is periodic and etcd-cluster/join messages are retried, so dropping
// under flood is safe and self-healing. Caps are sized well above the expected
// governance member count so normal operation never drops.
var (
	statusExSem = make(chan struct{}, 128)
	// etcd add-member (slow raft write) and cluster gossip (fast feed send) use
	// separate caps so a slow/stuck AddMember cannot starve cluster-gossip
	// handling and block etcd membership self-healing.
	etcdAddSem     = make(chan struct{}, 16)
	etcdClusterSem = make(chan struct{}, 64)
)

// maxBlobSidecarBlocksServe bounds how many blocks a single GetBlobSidecars
// request may ask for (M5). Each Metadium block carries at most
// MaxBlobGasPerBlock/BlobTxBlobGasPerBlob blobs (~128KB each), so this keeps the
// reply well under maxMessageSize while limiting disk lookups.
const maxBlobSidecarBlocksServe = 16

func handleGetPendingTxs(backend Backend, msg Decoder, peer *Peer) error {
	// not supported, just ignore it.
	return nil
}

// --- meta/69 blob-sidecar serving (M5) ---

// handleGetBlobSidecars69 serves blob sidecars for the requested block hashes
// from local storage (rawdb), bounding the number of blocks per request.
func handleGetBlobSidecars69(backend Backend, msg Decoder, peer *Peer) error {
	var req GetBlobSidecarsPacket
	if err := msg.Decode(&req); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	hashes := req.Hashes
	if len(hashes) > maxBlobSidecarBlocksServe {
		hashes = hashes[:maxBlobSidecarBlocksServe]
	}
	response := make([][]*types.BlobTxSidecar, len(hashes))
	for i, hash := range hashes {
		response[i] = backend.Chain().GetBlobSidecars(hash)
	}
	return peer.ReplyBlobSidecars(req.RequestId, response)
}

// handleBlobSidecars69 forwards a received blob-sidecar reply to the backend,
// which correlates it with the in-flight request and validates/persists it.
func handleBlobSidecars69(backend Backend, msg Decoder, peer *Peer) error {
	// Confirm this reply answers a fetch we issued before decoding the sidecars.
	// A sidecar is up to 128KB of blob plus commitments, so this is the largest
	// amplification the protocol offers an unsolicited sender. See
	// response_gate.go.
	env := new(responseEnvelope)
	if err := msg.Decode(env); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	if !peer.solicited(env.RequestId) {
		peer.dropUnsolicited(BlobSidecarsMsg, env.RequestId)
		return nil
	}
	peer.untrackPending(env.RequestId)

	res := BlobSidecarsPacket{RequestId: env.RequestId}
	if err := rlp.DecodeBytes(env.Payload, &res.Sidecars); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	return backend.Handle(peer, &res)
}

// --- shared processing cores (used by both legacy and meta/69 handlers) ---

// serveStatusEx sends this node's miner status back to the peer (response to a
// GetStatusEx request), bounded by the status concurrency cap.
func serveStatusEx(backend Backend, peer *Peer) error {
	select {
	case statusExSem <- struct{}{}:
	default:
		return nil // concurrency cap reached, drop under flood
	}
	go func() {
		defer func() { <-statusExSem }()
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

// applyStatusEx applies a received miner status (head update + delivery).
func applyStatusEx(backend Backend, peer *Peer, status *metaapi.MetadiumMinerStatus) error {
	select {
	case statusExSem <- struct{}{}:
	default:
		return nil // concurrency cap reached, drop under flood
	}
	go func() {
		defer func() { <-statusExSem }()
		if _, td := peer.Head(); status.LatestBlockTd.Cmp(td) > 0 {
			peer.SetHead(status.LatestBlockHash, status.LatestBlockTd)
		}
		metaapi.GotStatusEx(status)
	}()
	return nil
}

// serveEtcdAddMember adds the peer to the etcd cluster and replies with the
// resulting cluster, bounded by the add-member concurrency cap.
func serveEtcdAddMember(peer *Peer) error {
	select {
	case etcdAddSem <- struct{}{}:
	default:
		return nil // concurrency cap reached, drop under flood
	}
	go func() {
		defer func() { <-etcdAddSem }()
		cluster, _ := metaapi.EtcdAddMember(peer.ID())
		if err := peer.SendEtcdCluster(cluster); err != nil {
			// ignore the error
		}
	}()
	return nil
}

// applyEtcdCluster feeds a received etcd cluster string into self-healing.
func applyEtcdCluster(cluster string) error {
	select {
	case etcdClusterSem <- struct{}{}:
	default:
		return nil // concurrency cap reached, drop under flood
	}
	go func() {
		defer func() { <-etcdClusterSem }()
		metaapi.GotEtcdCluster(cluster)
	}()
	return nil
}

// --- legacy (meta/66, meta/68) handlers ---

func handleGetStatusEx(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}
	return serveStatusEx(backend, peer)
}

func handleStatusEx(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}
	var status metaapi.MetadiumMinerStatus
	if err := msg.Decode(&status); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	return applyStatusEx(backend, peer, &status)
}

func handleEtcdAddMember(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}
	return serveEtcdAddMember(peer)
}

func handleEtcdCluster(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}
	var cluster string
	if err := msg.Decode(&cluster); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	return applyEtcdCluster(cluster)
}

// --- meta/69 handlers (M12 replay protection) ---
//
// Each decodes the replay-protected packet, rejects stale/replayed frames
// (silently dropping rather than disconnecting, to avoid false positives from
// clock skew or reordering), then runs the shared processing core.

func handleGetStatusEx69(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}
	var pkt GetStatusEx69Packet
	if err := msg.Decode(&pkt); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	if !peer.acceptMetaReplay(pkt.Nonce, pkt.Timestamp) {
		peer.Log().Debug("Dropping replayed meta GetStatusEx", "nonce", pkt.Nonce)
		return nil
	}
	return serveStatusEx(backend, peer)
}

func handleStatusEx69(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}
	var pkt StatusEx69Packet
	if err := msg.Decode(&pkt); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	if !peer.acceptMetaReplay(pkt.Nonce, pkt.Timestamp) {
		peer.Log().Debug("Dropping replayed meta StatusEx", "nonce", pkt.Nonce)
		return nil
	}
	return applyStatusEx(backend, peer, &pkt.Status)
}

func handleEtcdAddMember69(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}
	var pkt EtcdAddMember69Packet
	if err := msg.Decode(&pkt); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	if !peer.acceptMetaReplay(pkt.Nonce, pkt.Timestamp) {
		peer.Log().Debug("Dropping replayed meta EtcdAddMember", "nonce", pkt.Nonce)
		return nil
	}
	return serveEtcdAddMember(peer)
}

func handleEtcdCluster69(backend Backend, msg Decoder, peer *Peer) error {
	if !metaminer.AmPartner() || !metaminer.IsPartner(peer.ID()) {
		return nil
	}
	var pkt EtcdCluster69Packet
	if err := msg.Decode(&pkt); err != nil {
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}
	if !peer.acceptMetaReplay(pkt.Nonce, pkt.Timestamp) {
		peer.Log().Debug("Dropping replayed meta EtcdCluster", "nonce", pkt.Nonce)
		return nil
	}
	return applyEtcdCluster(pkt.Cluster)
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
	head := backend.Chain().CurrentBlock()
	signer := types.MakeSigner(backend.Chain().Config(), head.Number, head.Time)
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
//
// This node never sends GetNodeData, so every one of these is unsolicited by
// definition and there is nothing to match it against — the message carries no
// request id. Discarding it without decoding is the same property the response
// gate gives the dispatcher-path responses: a peer cannot make us expand a
// payload we never asked for.
func handleNodeData(backend Backend, msg Decoder, peer *Peer) error {
	return nil
}
