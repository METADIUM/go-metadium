# PBFT private network: operations runbook

This runbook covers setting up a new Metadium private network that bootstraps on PoA and switches to
PBFT at a fixed height, and running it after the switch. It is the operator's version of
`docs/pbft-consensus-design.md` §9. For why each rule exists, see the design sections referenced
here.

Every step below has been rehearsed on the private test network (`tests/private-net-pbft`). A few
steps name the script that does the same thing and can serve as a reference: `transition.sh` walks
steps 0–4 and the failure case.

## 1. Before you start

**Sizing and placement** (design §9.6):
- Validators: N ≥ 4. Efficient sizes are 4, 7, 10 and 13. With 5 or 6, f is the same as with 4 and
  the quorum is larger.
- f = floor((N − 1) / 3) validators may be down or faulty at once. The quorum is ceil(2N / 3).
- A production network has 7 validators, with at most f in any failure domain (site, zone, rack,
  power, switch). A 4-validator network in one lab is a QA-grade network: one validator down is
  tolerated, two stop it.
- Read nodes (RPC, monitoring) are ordinary full nodes. They do not count toward the quorum.

**Per server:**
- The release build needs glibc 2.31 or later (Ubuntu 20.04 or later).
- NTP synchronised. Proposals more than 2 s (`timeDrift`) from a validator's clock are refused.
- A stable address, and the P2P port (30303 by default) open between all validators.
- **One node key per server, never on two at once** (design §6.1). There is no active-standby
  validator; §5.4 below describes failover. Redundancy for client access belongs on the API tier,
  in front of the read nodes.

**What a validator keeps on disk**, in `<datadir>/gmet/` alongside chaindata:
- `nodekey`: the validator's identity.
- `metabft/wal`: its votes and lock. It must survive a restart; §5.3 below covers what to do if it
  is lost.
- `metabft/evidence`: equivocation evidence, if there is any.

## 2. Step 0: the genesis

The genesis fixes the chain ID, the switch height and the PBFT parameters. **None of them can
change afterwards.**

**Start from `metadium/scripts/genesis-template.json`**, which `gmet metadium genesis` (and
`gmet.sh init`) fills in from the data file. Do not write the `config` section by hand: the
template already sets every Ethereum and Metadium fork block to `0`, and some of them matter
even on a new network. In particular, a genesis without `applepieBlock` runs with fee
delegation (type 22 transactions) off, and the node warns about it at startup; if such a
network has already mined fee-delegated transactions, add `"applepieBlock": 0` and re-run
`init` on every node before upgrading past m1.2.1, or those blocks stop validating. The
check that guards a re-`init` does not compare `applepieBlock`, so this takes effect without a
rewind.

On top of the template, set the chain ID, the two heights and the PBFT parameters:

```json
"config": {
  "chainId": <this network's own ID, design §9.5>,
  "camelliaBlock": <at or before bftBlock>,
  "bftBlock": <the switch height>,
  "bft": { "emptyBlockInterval": 5, "baseTimeout": 2, "maxBackoffExp": 5, "timeDrift": 2 }
}
```

- **chainId**: a number no other network uses (design §9.5). `init` refuses 0 and warns on
  well-known IDs. `gmet metadium genesis` takes it from the data file's `chainId`.
- **bftBlock**: the switch happens when the chain reaches this height, whenever governance was set
  up. The validator set is read from the state at `bftBlock − 1`, so steps 2 and 3 must be finished
  before then. Blocks come at governance's `blockCreationTime` while transactions flow, and at the
  empty-block interval when idle. Leave enough room for setup and for fixing problems:
  - with 1 s blocks, 3600 is about an hour;
  - with 5 s idle blocks, 17280 is about a day.

  Finishing early does no harm: the chain stays on PoA until `bftBlock`. Being late does harm,
  because the network then has to be rebuilt (§4).
- **bft**: the defaults above are what was measured.
  - `emptyBlockInterval` is the idle block interval after the switch.
  - The round timeout starts at `baseTimeout` and doubles each round, up to
    `baseTimeout · 2^maxBackoffExp`, which is 64 s.

`init` every server, validators and read nodes, with the same genesis.

## 3. Steps 1–3: bootstrap, governance, readiness

**Step 1: start the nodes on PoA.** Run every node with `--consensusmethod 2`, which stays PoA on a
PBFT network, and the validators with `--mine`. Initialise etcd on the first node (`admin.etcdInit()`)
exactly as on an existing Metadium PoA network. The others join on their own.

**This segment has no BFT guarantee:** blocks can still reorganise. Put nothing in it but the
governance setup.

**Step 2: governance.**
- Deploy the governance contracts with every validator as a member, as on an existing network
  (`metadium/scripts/deploy-governance.js`). Each member entry carries its node ID (the node key's
  public key), staking and coinbase address, IP and port.
- Set `blockCreationTime` no higher than `emptyBlockInterval`, for example 1000 ms.
- Register at least 4 members.
- Check the mesh: every validator should have all the others as peers (`net_peerCount` ≥ N − 1).
  Nodes connect to the governance node list every 30 s.
- Check NTP on every server.

**Step 3: readiness.** On every node, call `metabft_readiness`:

```json
{ "head": "0x…", "bftBlock": "0x…", "blocksLeft": 57, "governance": true,
  "validators": 4, "minimum": 4, "advertises": true, "inSet": true, "ready": true }
```

- On a validator, `ready`, `inSet` and `advertises` must all be true, and `validators` must be N.
- On a read node, `inSet` is false, and that is fine.
- `problems` explains anything that is not ready. For example, "validator but does not advertise
  metabft/1; restart it" means a node started before it was registered: restart it.

## 4. Step 4: the switch

Nothing needs doing at `bftBlock`: the nodes switch on their own. The first proposer is
`validators[bftBlock mod N]`. With transactions pending, it builds at once when idleseal is on, and
one `blockCreationTime` after the parent without it (design §4.5). With none pending, it waits
`emptyBlockInterval` and builds an empty block. A few round changes right after the switch are normal.

**Check once past `bftBlock`:**
- `eth_getBlockByNumber(bftBlock − 1)` has no `commitSeals`; it is the last PoA block.
- `eth_getBlockByNumber(bftBlock)` has at least a quorum of `commitSeals`, and a `bftRound`.
- `metabft_status` on every validator: `height` moves, `admittedPeers` is N − 1,
  `insertFailures` and `droppedMessages` stay at 0, and there is no `lastRejection` other than
  timing noise.
- Every node, the read nodes included, has the same block hash at the same height.
- From here on the head is final: blocks do not reorganise.

**If the switch conditions are not met** (governance missing, or fewer than 4 governance nodes in
the state at `bftBlock − 1`):
- The chain **stops at `bftBlock − 1`**; it does not continue on PoA (design §9.3).
- Readiness reported the problem beforehand, and the logs say why:
  `No validator set; not participating … 3 governance nodes at block …, PBFT needs 4`.
- **The recovery is a new genesis.** The bootstrap segment holds nothing but the governance setup,
  which is why §3 keeps business transactions out of it. Fix the cause, write a new genesis, and
  start again from §2.

## 5. After the switch

### 5.1 Monitoring

| What | Where | Normal |
|---|---|---|
| height and round being agreed on | `metabft_getRoundState` | height rises; round 0 |
| peers, failures, last refused proposal | `metabft_status` | `admittedPeers` = N − 1; counters at 0 |
| transactions held back by the validator floor | `metabft_status.excludedTxs` | empty |
| validator set at a height | `metabft_getValidators` | N entries |
| equivocation evidence | `metabft_getEvidence` | empty |

Alert on these log lines:
- `Validator equivocated`: a validator signed two different messages. The evidence is stored.
  Decide on its removal through governance.
- `This node's key signed two different messages; it is running on another server`: **the node key
  is running on two servers.** Stop one now (§5.4).
- `No validator set; not participating`: the validator set cannot be read at this height.
- `Leaving a transaction out of PBFT proposals: it breaks the validator floor`: a governance
  transaction that would leave fewer than 4 validators. It is retried every 30 s. Replacing its
  nonce frees the sender's later transactions.

### 5.2 Outages and maintenance

- **Up to f validators down: nothing to do.** The chain continues. Latency rises a little, and the
  rounds of the missing validators' slots go to round 1.
- **More than f down:** block production stops. It never forks. It resumes on its own once a
  quorum is back. Measured: the first block 2–12 s after the quorum returned, for outages of 10–180 s
  (`docs/pbft-test-report.md` §4). There is no manual recovery step.
- **Maintenance, one validator at a time.** With N = 4, one validator out uses up f, so nothing
  else is taken down meanwhile. Stop a validator gracefully (SIGTERM, or `docker stop` with a
  generous timeout), never with `kill -9` as routine. The WAL keeps its votes across the restart.
- **Upgrades** are rolling, one validator at a time, each back in the round before the next one
  goes.

### 5.3 A lost or corrupt WAL

- A validator that restarts without its WAL, or with a corrupt one, joins as an **observer**: it
  signs nothing until the height it may have voted on commits, then takes part again. This is
  automatic (design §6.1, S-12).
- A corrupt WAL is kept next to the new one for inspection.

### 5.4 Moving a validator to another server

The two servers must never run the key at the same time: the WAL cannot prevent a key that runs
twice from signing twice.

1. Stop the old server and confirm it is stopped.
2. Move `nodekey` and `metabft/wal`, and the chain data or a fresh full sync, to the new server.
3. Start the new server.

If both ever run at once, every validator stores evidence against the key and both servers log the
alarm in §5.1 (S-13).

### 5.5 Adding and removing validators

Validators are governance members, changed by ballot in the governance contract:
- propose with `addProposalToAddMember` or `addProposalToRemoveMember`;
- members vote with `vote(ballot, true)`;
- a ballot whose voting period has ended is closed with `finalizeEndedVote()`.

The new set applies from the block after the one that decides the ballot (S-08).

- **Adding.**
  - Start the new node from the same genesis and let it full-sync. Snap sync is refused on a PBFT
    chain.
  - Run the ballot.
  - Once it is decided, **restart the new node**, so that it advertises metabft/1. Readiness says
    so if it is needed.
  - Check it with `metabft_readiness` and `metabft_status`.
- **Removing.**
  - A ballot that would leave fewer than 4 validators never commits. The chain continues at N = 4
    (S-15).
  - So at N = 4, **replacing** a validator is add first, then remove.
- **Size.** Going from 4 to 5 or 6 does not raise f. Going to 7 does.

### 5.6 Read nodes and new nodes

- Any number of full nodes can follow the chain. Each verifies every block's commit seals against
  the governance validator set, so they need no trust in the validators' RPC.
- New nodes use full sync (`--syncmode full`). Snap sync is refused on a PBFT chain.

## 6. Reference

| RPC | Returns |
|---|---|
| `metabft_readiness` | the switch conditions for this node (§3) |
| `metabft_status` | round state, peers, failure counters, last refusal, held-back transactions |
| `metabft_getRoundState` | height, round, proposer, observer mode |
| `metabft_getValidators(height?)` | the validator set (node IDs, coinbases) at a height |
| `metabft_getEvidence` | stored equivocation evidence, verifiable by anyone |

| Design | Topic |
|---|---|
| §4.5 | block timing, timeouts, pacing to `blockCreationTime` |
| §6.1 | WAL, observer mode, failover rules |
| §7.1 | message relay and equivocation evidence |
| §7.6 | what etcd still does after the switch |
| §9.2–§9.3 | setup steps; transition conditions and failure handling |
| §9.5 | chain ID allocation |
| §9.6 | validator count, availability and placement |
