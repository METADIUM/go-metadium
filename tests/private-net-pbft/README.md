# PBFT private network (4 to 9 nodes)

A local network that bootstraps on PoA and switches to PBFT at `bftBlock`
(docs/pbft-consensus-design.md §9.2). The PoA network next door
(`../private-net-poa`) is unchanged; this one reuses its Dockerfile and
entrypoint (its own Dockerfile adds iptables).

```bash
go build -o build/bin/gmet ./cmd/geth          # from the repository root
go build -o build/bin/bootnode ./cmd/bootnode  # generates the node keys
cd tests/private-net-pbft
NODES=7 ./setup.sh  # node keys and accounts, genesis (BFT_BLOCK=200 by default),
                    # docker-compose.yml and image; NODES=4 by default, up to 9
./start.sh          # the nodes on 127.0.0.1:8645.., full mesh
./deploy.sh         # governance with every node as a member; before bftBlock
./pbft-test.sh      # switch, seals, rotation, agreement, finality, f and f+1 down
./faults.sh         # partition, kill -9 under load, WAL loss
./governance.sh     # NODES=5: remove a validator by ballot, try going below 4, add it back
./byzantine.sh      # with a pbftfault build: wrong rewards, bad timestamps, equivocation, re-proposal
./measure.py        # latency, idle interval, load (§11.3); with NODE_ARGS="--metadium.block.idleseal 100"
                    # for the private operating profile
./stop.sh --clean   # remove containers, data and the generated files
```

With N nodes, f = floor((N-1)/3) and the quorum is ceil(2N/3).

`pbft-test.sh` checks (§11.2 S-01..S-03):
- block `bftBlock-1` has no seals, and each block of a window from `bftBlock` has at least a quorum
- every validator proposes within the window
- every node has the same block, and the finalized block is the head
- with 1..f nodes stopped, production continues; restarted, they catch up
- with f+1 stopped, production stops; with the quorum back, it resumes, and every node agrees
- a block committed after a round change imports everywhere

`faults.sh` checks:
- S-06: f+1 validators cut off from the network: the rest cannot proceed either; healed, the
  chain resumes without a fork. Docker bridges cannot overlap subnets and the image has no
  iptables, so the cut-off nodes are isolated from each other too (4 | 1 | 1 | 1 at N=7) rather
  than forming a group of three; no side has a quorum either way.
- S-11: validators killed with SIGKILL in turn while blocks carry transactions: no equivocation
  evidence on any node, and every node agrees.
- S-12: a validator restarted without its WAL reports observer mode, leaves it once a height
  commits, and every node agrees.
- S-06, true split: iptables inside the containers (the image has it, the nodes run with
  NET_ADMIN) cut the last f+1 validators off from the rest while each side keeps its own links
  (4:3 at N=7). Neither side commits, although the minority exchanges its votes; healed, the
  chain resumes, every node agrees and no evidence exists.
- S-09: a node that is not a validator joins after the switch, syncs from genesis (the PoA
  segment, then every commit seal) and reaches the validators' head on the same chain.

Notes:
- The `metabft` RPC namespace is enabled on every node: `metabft_readiness` before
  the switch, `metabft_status` and `metabft_getRoundState` after it.
- RPC is published on `127.0.0.1` only; node1 runs with an unlocked account.
- Only node1 runs etcd. The PoA bootstrap takes its mining token from it
  (Bokbunja), and the other nodes' etcd never joins, so they log
  `etcd failed to start ... cannot fetch cluster info` during the
  bootstrap. That is expected here and irrelevant from `bftBlock` on, where
  PBFT replaces the token.
- `pbft-test.sh` stops nodes with `docker stop --time 60`, giving the node
  time to close its databases.

`governance.sh` (NODES=5, §11.2 S-08, S-15) drives the governance contract through
`gov.py`, which proposes and votes as the members (their test keys are imported into
node1, the one node that allows unlocking over HTTP):
- removing node5 by ballot: from the next block the quorum is the 4-set's and node5 no
  longer proposes;
- a ballot that would leave 3 nodes: the deciding vote never commits, the chain continues
  and N stays 4; the validators that proposed meanwhile log leaving it out;
- adding node5 back: it seals and proposes again, without a restart.

A vote left out by the floor is listed in `metabft_status` (`excludedTxs`: hash, sender,
nonce, reason, retry time). It holds its sender's later transactions (nonce order) until a
retry, every 30 s, finds it includable (its ballot over, it reverts); replacing that nonce
frees them at once.

`byzantine.sh` (§11.2 S-04, S-05, S-14, S-16) needs a build with fault injection, which
the release build does not contain:

```bash
go build -tags pbftfault -o build/bin/gmet-fault ./cmd/geth
GMET_BIN=../../build/bin/gmet-fault NODES=7 ./setup.sh ...
```

It switches one validator's fault on at a time through `METABFT_FAULT` (see
`eth/bft_fault.go`), recreating that node's container, and every node back to normal at
the end:
- S-05: its proposals carry a wrong rewards field; the others refuse them ("rewards field
  does not match"), none of its blocks commits, the chain continues;
- S-14: its proposals are stamped before their parent, or 10 s ahead; the local-clock bound
  refuses both (a fresh proposal meets it before the header rules; `Time >= parent.Time`
  guards import and is covered by the engine tests);
- S-04: it sends two PRE-PREPAREs for its rounds, B first to half its peers and A to all;
  no block of its commits, the peers that saw both store the evidence;
- S-16: every validator withholds its round-0 COMMIT at heights divisible by 10; such a
  height commits in round 1, re-proposed unchanged, with round 0's proposer as its builder.
