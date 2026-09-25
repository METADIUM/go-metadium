# PBFT private network (4 nodes)

A local network that bootstraps on PoA and switches to PBFT at `bftBlock`
(docs/pbft-consensus-design.md §9.2). The PoA network next door
(`../private-net-poa`) is unchanged; this one reuses its Dockerfile and
entrypoint.

```bash
go build -o build/bin/gmet ./cmd/geth          # from the repository root
go build -o build/bin/bootnode ./cmd/bootnode  # generates the node keys
cd tests/private-net-pbft
./setup.sh          # 4 node keys and accounts, genesis (BFT_BLOCK=200 by default), image
./start.sh          # 4 nodes on 8645..8648, full mesh
./deploy.sh         # governance with the 4 nodes as members; before bftBlock
./pbft-test.sh      # switch, seals, rotation, agreement, finality, 1 and 2 nodes down
./stop.sh --clean   # remove containers and data
```

`pbft-test.sh` checks:
- block `bftBlock-1` has no seals, and each of the 20 blocks from `bftBlock` has at least a quorum (3)
- all four validators propose within those 20 blocks
- every node has the same block, and the finalized block is the head
- with one node stopped, production continues; restarted, it catches up
- with two nodes stopped (more than f = 1), production stops; with the quorum back, it resumes, and every node agrees afterwards

Notes:
- RPC is published on `127.0.0.1` only; node1 runs with an unlocked account.
- Only node1 runs etcd. The PoA bootstrap takes its mining token from it
  (Bokbunja), and the other nodes' etcd never joins, so they log
  `etcd failed to start ... cannot fetch cluster info` during the
  bootstrap. That is expected here and irrelevant from `bftBlock` on, where
  PBFT replaces the token.
- `pbft-test.sh` stops nodes with `docker stop --time 60`, giving the node
  time to close its databases.
