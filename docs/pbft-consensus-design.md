# PBFT consensus design (go-metadium)

- **Date:** 2026-09-24 (rev.5 — first review round and PR #143 review folded in, §15)
- **Status:** Design (pre-implementation) — to be reviewed before work starts
- **Scope:** initial configuration of **new private networks**. Existing Metadium Mainnet/Testnet are out of scope
- **Transition:** bootstrap on PoA → switch to PBFT at the `BftBlock` height set in the genesis (§9, option B)
- **Terms:** pre-fork / post-fork mean heights below / at-or-above `BftBlock`.
  `BlockHash(h)` is the header hash without `BftRound` and `CommitSeals`, defined in §5.2.

---

## 1. Goals and scope

### 1.1 Goals
- Let a new private network **choose PBFT** as its consensus when it is set up.
  When chosen, block-production coordination moves from **etcd (raft, CFT)** to **BFT consensus**.
- Get **instant finality** (committed = final) and drop today's `head - (N/2+1)` heuristic
  (`metadium/admin.go:645`).
- Guarantee safety (no forks) and liveness (blocks keep coming) while at most
  `f = floor((N-1)/3)` nodes are Byzantine (equivocating proposals, false votes, silence).
- Use the validator set defined by the governance contract as-is.

### 1.2 Non-goals
- **Moving existing Metadium Mainnet/Testnet to PBFT** (hard-fork migration of a live chain)
- Slashing / penalties (needs a governance-contract change — separate work). **Collecting equivocation evidence is in scope** (§7.1)
- Dynamic validator weighting (stake weighting) — one node, one vote stays
- Removing etcd itself (this design removes it **from the block-production path only**; the operations channel stays)
- Light clients / checkpoint-sync optimisation

---

## 2. Baseline

Code references are against `release/v1.1.4` (`e804c8fe4`, merged into master as `b716f5b03`).

| Item | Current implementation | Location |
|---|---|---|
| Consensus constants | `ConsensusPoW=1, PoA=2, ETCD=3, PBFT=4` | `params/protocol_params.go:240-244` |
| CLI check | rejects `>= ConsensusETCD` → **3 and 4 are unusable** | `cmd/utils/flags.go:2084` |
| PBFT references | constant + `StartAdmin` allow-list, **2 places only**, no logic | `metadium/admin.go:1293` |
| Leader election (pre-Bokbunja) | etcd raft leader → `IsMiner()` | `metadium/legacy.go:561` |
| Leader election (Bokbunja~) | etcd CAS-based mining token (TTL 10s) | entry `metadium/miner/miner.go:104` → impl `metadium/sync.go:145` (wired at `admin.go:2383`), CAS `metadium/etcdutil.go:989` (`acquireTokenSync`, transaction at 1024) |
| Block signature | single signature `MinerNodeId`/`MinerNodeSig` (ECDSA over the **state root**) | `consensus/ethash/consensus.go:642-648` |
| Block signature check | `verifyBlockSig` — matched against the governance enode list | `metadium/admin.go:1721` |
| Proposer limit | no reappearance within the last `N/2` blocks (Pangyo~) | `metadium/miner_limit.go:205`, called at `admin.go:1765` |
| Reward calculation | entry `calculateRewards` → implementing method | `metadium/admin.go:1610` → `1561` |
| Reward verification | **`verifyRewards` is an empty function that only returns `nil`.** The real check is the recomputation in Finalize plus the state-root comparison, and that step **overwrites `header.Rewards`/`Coinbase` with the recomputed values** | `metadium/admin.go:1618`, `consensus/ethash/consensus.go:741, 754` |
| Timestamp | `header.Time` is a **`uint64` in seconds**. The PoA path has no monotonicity check against the parent (only PoW rejects `<=`). Future allowance 15s | `core/types/block.go:79`, `consensus/ethash/consensus.go:234, 238`, `miner/worker.go:1303` |
| Finality | `head - (N/2+1)` heuristic | `metadium/admin.go:645`, `core/blockchain_reader.go:79` |
| Proposer gate | `AcquireMiningToken` / `HasMiningToken` | `miner/worker.go:1671, 1781, 1833` |
| Seal → write | `WriteBlockAndSetHead` **synchronously** right after `Seal()` | `miner/worker.go:1863, 1912` |
| P2P | `meta/66, 68, 69`, highest message code `0x18` | `eth/protocols/eth/protocol.go:50, 57, 92` |

**Four key observations**

1. **The header is already non-standard.** `Header` already carries `Fees`, `Rewards`, `MinerNodeId`
   and `MinerNodeSig` (`core/types/block.go:66`), and RLP goes through
   `headerRlp` (`core/types/block.go:104`, encoding at `:309`). Adding commit-seal fields
   **follows existing precedent**. Note that `headerRlp` declares `ParentBeaconRoot` as its last field,
   but `headerToHeaderRlp` **never fills it**, so it is always the omitted tail: neither sent nor hashed
   (it is also always nil on the PoA path).
   Also note that `Hash()` and `EncodeRLP()` only take the `headerRlp` path in PoA mode; under
   `ConsensusPoW` (the package default in unit tests) they use `HeaderLegacy`, which ignores the Metadium fields.
2. **`SealHash` covers less than the block hash.** `SealHash`
   (`consensus/ethash/consensus.go:664`) **excludes** `Rewards`/`MinerNodeId`/`MinerNodeSig`/
   `MixDigest`/`Nonce`, but the block hash (`headerRlp`) includes all of them.
   **`SealHash` therefore must not be the PBFT signing target** (§5.2).
3. **Reward fields are overwritten, not verified** (table above). PBFT must compare instead (§7.5).
4. **The test network has 3 nodes.** `tests/private-net-poa/docker-compose.yml` runs node1–node3.
   With `N=3`, `f = 0`: **zero Byzantine tolerance**. Validating PBFT needs at least 4 nodes,
   7 recommended (`f=2`).

---

## 3. Protocol choice: IBFT 2.0 family, not textbook PBFT

Castro-Liskov PBFT (1999) assumes **a fixed replica set and a client/reply model**.
Moved to a blockchain as-is, it mismatches here:

| Original PBFT | Mismatch with a blockchain | This design |
|---|---|---|
| client sends request → replicas reply | request = block proposal, nobody to reply to | proposer builds the block from the mempool |
| checkpoint / garbage collection | the chain itself is the log, no GC needed | dropped |
| view change based on stable checkpoints | chain height is a natural sequence | one instance per height |
| static replica set | validators change through governance | swapped at epoch (= `gov.modifiedBlock`) boundaries |
| collect f+1 replies | the block needs a proof | **attach 2f+1 commit seals to the header** |

So this design adopts the **IBFT 2.0 (Besu) family**: 3 phases (PRE-PREPARE/PREPARE/COMMIT),
a `2f+1` quorum and round changes, trimmed for a blockchain. That matches the meaning of the
existing `ConsensusPBFT` constant ("PBFT family").

> Versus Tendermint: Tendermint's lock/unlock rules and POL (proof-of-lock) round handling are
> more refined, but its state machine is larger, and the `prevote/precommit` two-phase + nil-vote
> model changes more of the interface with Metadium's existing block pipeline. IBFT2 is cheaper to port.
> Vote persistence (§6.1), however, follows Tendermint's WAL and signer last-sign-state approach.

---

## 4. Protocol definition

### 4.1 Parameters

```
N         = number of validators (governance getNodeLength)
f         = floor((N-1)/3)
Quorum    = ceil(2N/3)              // N=4→3, 5→4, 6→4, 7→5, 10→7, 13→9
```

- **The quorum is `ceil(2N/3)`, not `2f+1`.** The two agree only when `N = 3f+1`. Safety needs any two
  quorums to share at least `f+1` nodes (`2Q − N >= f+1`); with `N = 6` (`f = 1`), `2f+1 = 3` gives two
  quorums that can be disjoint, and with `N = 5` they share one node that may be the Byzantine one.
  `ceil(2N/3)` is the smallest value that satisfies the inequality for every `N`, and `N − f >= Q` keeps
  liveness (IBFT 2.0 / QBFT use the same rule). rev.5 and earlier wrote `2f+1 = ceil((2N+1)/3)`, which is
  wrong for `N = 5, 6`.

- With `N < 4`, `f=0` → BFT is meaningless. **Precondition for switching to PBFT: `N >= 4`** (§9.3),
  **and `N >= 4` stays a block-validity rule after the switch** (§9.3.1).
- `N` for height `n` is the governance value in the **parent (n-1) state**
  (`verifyBlockSig` already uses `height-1` — `metadium/admin.go:1729`).

### 4.2 Validator set and epochs

- Source: `getMetaNodes` (`metadium/admin.go:462`) — already sorted by `Name`, so
  **every node gets the same indices**. That order is the validator order.
- Identity: the 64-byte enode public key (`metaNode.Enode`). Signing key = node key.
  Same key scheme as the existing `VerifyBlockSig`, so key management does not change.
- Caching: reuse `coinbaseEnodeCache` (keyed by `gov.modifiedBlock`)
  (`metadium/sync.go:58`).
- **Epoch boundary**: the validator set changes at the block where `gov.modifiedBlock` changes.
  Consensus for height `n` runs on the set from state `n-1`, so the block that changes the set is
  itself agreed by the old set. No separate epoch-transition protocol is needed.

### 4.3 Proposer selection

```go
proposer(height, round) = validators[(height + round) % N]
```

- Deterministic; each round moves to the next node, which gives liveness.
- **The existing `isEligibleMiner` (no re-proposal within the last `N/2` blocks) is disabled after the fork.**
  It cannot be satisfied during round changes and would deadlock. Round-robin already gives
  stronger fairness, so it replaces the rule.
  → Next to the `isPangyo` branch at `metadium/admin.go:1762` (`verifyMinerLimit` call at `:1765`), add an
  `IsBft(height)` branch that skips `verifyMinerLimit` at post-fork heights.

### 4.4 Three-phase state machine

Instance for height `n`, round `r`. **Every signed message is written to the WAL and fsynced before it is sent** (§6.1).

```
NEW_ROUND
  ├─ I am proposer(n,r)  → (the locked block if any, else a new one) → broadcast PRE-PREPARE
  └─ otherwise           → wait for PRE-PREPARE (start deadline(n,r) timer, §4.5)

On PRE-PREPARE (from proposer(n,r), valid signature, parent = local head)
  ├─ full block validation (§4.8): VerifyHeader + timestamp bounds + execute body + state root
  │                                + Rewards/Coinbase comparison + N >= 4 after execution
  ├─ fail    → trigger round change (ROUND_CHANGE(r+1))
  └─ success → [WAL] broadcast PREPARE(n, r, digest) → PRE_PREPARED

Quorum-1 PREPAREs collected (+ own) → PREPARED  ([WAL] lock = (r, digest, PREPARE certificate, block))
  └─ [WAL] broadcast COMMIT(n, r, digest, commitSeal)

Quorum COMMITs collected → COMMITTED
  └─ set BftRound=r and CommitSeals in the header → WriteBlockAndSetHead → prune WAL → NEW_ROUND for next height
```

- **`digest = BlockHash(header)`** (§5.2), not `SealHash` — `SealHash` leaves out some fields that
  go into the block hash, so one digest could map to different block hashes (§2 observation 2).
- A validator sends no PREPARE for any proposal other than the (n,r,digest) it already PREPAREd.
  On a new proposal after a round change it follows the lock rule in §4.5.

### 4.5 Round change (view change)

Triggers:
- `deadline(n, r)` expires (no proposal, or no quorum)
- proposal fails validation / wrong proposer
- `f+1` `ROUND_CHANGE(r')` received with `r' > r` → jump to `r'` immediately
  (Bracha amplification: evidence that at least one honest node has already moved on)

```
ROUND_CHANGE(n, r+1, preparedRound, preparedBlock?, prepareCertificate?)
```

- `Quorum` `ROUND_CHANGE`s collected → enter round `r+1`.
- The new proposer **must re-propose the `preparedBlock` with the highest `preparedRound`**
  among the collected `ROUND_CHANGE`s (or build a new one if there is none). This is the core of
  safety: a block some honest node has PREPARED is never overturned in a later round.
- **A re-proposed block's header is not changed by a single byte.** `Coinbase`, `Time`, `MinerNodeId`
  and `MinerNodeSig` all keep the original proposer's values. Changing any of them changes `BlockHash`
  and breaks the lock. `BftRound` is excluded from `BlockHash`, so it is filled with the commit round at commit time (§5.3).
- A re-proposal carries the `ROUND_CHANGE` certificate in its PRE-PREPARE so receivers can check
  that the proposal is justified.

#### Block timing and round timeout

Current block timing (`docs/enterprise-block-timing.md`):

| Network | Setting | Behaviour |
|---|---|---|
| Mainnet / Testnet | governance `blockCreationTime = 2000` | a block every 2s (empty blocks when idle) |
| Private (current operating profile) | `blockCreationTime` 1–2s + `--metadium.block.idleseal 100` + 5s empty blocks | sealed within **~100ms** when transactions arrive, an empty block **every 5s** when idle |

- The block interval comes from governance `EnvStorage.getBlockCreationTime`.
  `params.BlockInterval` (`--metadium.block.interval`) is stored but never read, so it is
  not used for timers.
- PBFT must keep this profile: **propose right away (100ms) when transactions arrive, 5s empty blocks
  when idle.** Validators cannot see the proposer's mempool, so the timer must not mistake the
  normal idle wait (5s) for a failure.

**Timers use the local clock, not the header time.** `header.Time` is in seconds, so 100ms blocks
share values, and above all it is **chosen by the proposer**. Basing timers on it (the rev.3 design)
lets a proposer push `Time` into the future to delay every validator's timer by up to 15s, or pull it
into the past so that **round 0 of the next height expires immediately** (an undetected liveness attack).

```go
// committedAt(n-1): local monotonic time at which height n-1 became this node's head —
//                   through its own consensus commit or through block import (sync, restart)
// roundStart(r):    local monotonic time when this node entered round r
deadline(n, 0) = committedAt(n-1) + EmptyBlockInterval + BftBaseTimeout
deadline(n, r) = roundStart(r)    + BftBaseTimeout * 2^min(r, BftMaxBackoffExp)   // r >= 1

// proposed defaults
//   EmptyBlockInterval = 5s   (current private operating profile)
//   BftBaseTimeout     = 2s   (block building + 3-phase round trips + margin)
//   BftMaxBackoffExp   = 5    (rounds r>=1 capped at 2s * 32 = 64s)
```

- `committedAt` differs between nodes only by COMMIT arrival skew (milliseconds on a LAN), so
  NTP drift does not matter and an attacker has no input to manipulate.
- It is defined by "became head", not "committed through consensus", because a node that received
  n-1 by import — after a restart, while lagging, or at the PoA→PBFT transition (§9.2) — never ran
  consensus for it. One definition covers every case.
- **Early proposals are always accepted.** When transactions arrive and the proposer proposes after
  100ms, validators PREPARE immediately. The timer only filters proposals that are too late.
- **`EmptyBlockInterval` becomes a consensus parameter.** It enters every validator's timeout,
  so **it must not differ between nodes.** It is fixed in the genesis as `bft.emptyBlockInterval` (§8.1).
- `idleseal` (100ms) is local proposer behaviour, unrelated to consensus. The existing flag stays.
- **Without `idleseal`, the proposer paces round 0 to `blockCreationTime`** (the fixed-interval
  profile, §11.3 M-04). A build collects transactions for at most `BftTimeDrift/2`, so that its
  timestamp stays within the proposal time bound. Left alone, it would propose about 1s after the
  parent. Instead, with pending transactions the build starts at
  `parentProposal + blockCreationTime − window`. Here `parentProposal` is when this node made or
  accepted the parent's PRE-PREPARE, falling back to `committedAt(n-1)` if it saw none. The proposal
  then goes out about `blockCreationTime` after the parent's. Counting from the parent's proposal,
  not its commit, keeps the parent's build, check and decision inside the interval, as the PoA timer
  does. Counted from the commit, the interval came out at 2.77s under load instead of 2.0s. This is
  local proposer behaviour like `idleseal`: validators accept an early proposal, and since
  `blockCreationTime <= EmptyBlockInterval`, a paced proposal is always well before the round-0
  deadline. With `idleseal` on, nothing is held: the block is sealed as the pool goes quiet.
- At startup, check `EmptyBlockInterval >= blockCreationTime` (same constraint as today: an
  `emptyinterval` below the on-chain interval has no effect).

#### Timestamp rules (post-fork)

Even with timers decoupled, `header.Time` is exposed to the EVM as `block.timestamp`, so it is bounded.

| Rule | Where | Purpose |
|---|---|---|
| `header.Time >= parent.Time` | `VerifyHeader` (always, including sync) | blocks backdating. A strict `>` would cap seconds-resolution chains at one block per second and conflict with the 100ms profile, so it is not used |
| `\|header.Time − localNow\| <= BftTimeDrift` (default 2s) | **PRE-PREPARE validation only** | blocks pushing the time forward or back. Not applied when syncing historical blocks, which cannot be compared with the local clock; those are verified by commit seals |

- **Resolution stays in seconds.** Moving `Time` to milliseconds would change the meaning of EVM
  `block.timestamp` and break contracts and tooling. With timers decoupled from the header, nothing
  needs millisecond resolution. If it is ever needed, add a separate optional field rather than changing `Time`.

### 4.6 Message format and signatures

```go
type BftMsgType uint8
const (
    MsgPreprepare  BftMsgType = 1
    MsgPrepare     BftMsgType = 2
    MsgCommit      BftMsgType = 3
    MsgRoundChange BftMsgType = 4
)

// signed over: RLP hash of the envelope
type BftMessage struct {
    Type      BftMsgType
    Height    *big.Int
    Round     uint64
    ChainID   uint64        // replay protection: no cross-chain signature reuse
    Digest    common.Hash   // = BlockHash(proposed header), §5.2
    Payload   []byte        // Preprepare: block RLP + RC certificate / RoundChange: certificate
    CommitSeal []byte       // Commit only: sign(commitDigest)
    Signature []byte        // sign(keccak(rlp(fields above, without Signature)))
}

// commit seal is signed over this (a proof kept in the header permanently)
commitDigest = keccak256(rlp([BlockHash(header), Round, ChainID, byte(0x02)]))
```

- `0x02` domain separator: message signatures and commit-seal signatures can never be used for each other.
- `ChainID` included: signatures from another network (customer or environment) cannot be reused (§9.5).
- Signer = `ecrecover` → enode public key → validator index. Unregistered keys are dropped immediately.

### 4.7 Safety argument (summary)

Assumptions: (a) the digest covers the whole block hash (§5.2), (b) honest nodes do not lose their
previous votes or lock across restarts (§6.1), (c) the same node key never signs from two places at once (§6.1 operating rules).

- **Agreement:** for two blocks at the same height to each gather `Quorum` commits, the two quorums
  overlap in at least `2·Quorum − N >= f+1` nodes (§4.1), at least one of them honest. An honest node COMMITs only one digest
  per round — contradiction. Across rounds, the re-proposal rule in §4.5 guarantees it.
  Without assumption (b), an honest node that restarted is effectively Byzantine and the argument fails.
- **Validity:** every honest node fully executes and validates the block before PREPARE (§4.8).
- **Termination:** `f+1` amplification plus exponential backoff end the height in the round where,
  after GST, an honest proposer is selected. Timers do not depend on header time, so the proposer cannot interfere.

### 4.8 PRE-PREPARE validation checklist

All of these must pass before sending PREPARE.

1. Sender = `proposer(n, r)`, valid message signature, parent = local head
2. For a re-proposal: the attached ROUND_CHANGE certificate is valid and the block has **the same `BlockHash`** as the block of the highest `preparedRound`
3. `VerifyHeader` (including post-fork rules, §5.3) + timestamp bounds (§4.5)
4. Execute the body → state root matches
5. **Recompute `Rewards` and `Coinbase` and compare with the header; reject on mismatch** (do not overwrite, §7.5)
6. **Governance node count `N >= 4` in the post-execution state** (§9.3.1)

---

## 5. Header changes and block hash

### 5.1 New fields

```go
// core/types/block.go — in Header, and in headerRlp between BlobGasUsed and ParentBeaconRoot
    // BFT fork: committed round and a quorum of commit seals. Excluded from BlockHash.
    BftRound    uint64   `json:"bftRound,omitempty"    rlp:"optional"`
    CommitSeals [][]byte `json:"commitSeals,omitempty" rlp:"optional"`
```

- `rlp:"optional"` fields can be omitted **only at the tail**. In `headerRlp` the fields go **before
  `ParentBeaconRoot`**, not after it: that field is never filled, so it stays the omitted tail. After it,
  a set `CommitSeals` would force the nil `*common.Hash` to be encoded as an empty string, which does not
  decode back into a 32-byte hash. With both PBFT fields at their zero value they are omitted as well,
  so non-PBFT encodings stay byte-identical.
  Add them to the `headerToHeaderRlp` / `headerRlpToHeader` conversions too (`block.go:187, 216`).
- JSON uses `omitempty`, so non-PBFT headers keep their JSON and RPC output unchanged.
- When a later optional field is set, earlier nil optional fields are encoded as zero and decode as zero,
  not nil (`BaseFee` nil ↔ 0 changes meaning). The chain-config check therefore requires
  **`IsBft` implies `IsCamellia`** — headers after Camellia have every earlier optional field set.
- PBFT blocks must have `ParentBeaconRoot == nil` (`headerToHeaderRlp` never fills it, so it is never sent).

### 5.2 `BlockHash` — one hash for the block and for signing

`Header.Hash()` is `rlpHash(h)`, which goes through `EncodeRLP` → `headerRlp` (`core/types/block.go:295, 309`).
If commit seals were included, **each node would collect a different set of seals**, the same block
would get different hashes, and the chain would split. So the hash leaves out `BftRound`/`CommitSeals`.

```go
// BlockHash: the block hash = the PBFT signing target (digest). Only BftRound/CommitSeals are excluded.
func (h *Header) Hash() common.Hash {
    if h == nil { return common.Hash{} }
    if metaminer.IsPoW() { return rlpHash(HeaderToHeaderLegacy(h)) }
    if h.CommitSeals != nil || h.BftRound != 0 {
        cpy := CopyHeader(h)
        cpy.CommitSeals = nil
        cpy.BftRound = 0
        return rlpHash(cpy)   // with the optional tail empty, identical to the pre-fork encoding
    }
    return rlpHash(h)
}
```

- **The PBFT digest is this hash (`BlockHash`).** `SealHash` leaves out `Rewards`/`MixDigest`/`Nonce`/
  `MinerNodeId`/`MinerNodeSig`; used as the digest, the single digest that a quorum of validators committed
  would map to **several block hashes** differing only in those fields. Nodes storing different hashes
  would disagree on the next block's `ParentHash` — the chain splits even though consensus succeeded.
- **Can seals be stripped or forged if they are not in the hash?** — Yes, but it is pointless.
  Post-fork `VerifyHeader` requires `len(valid seals) >= Quorum`, so no honest node accepts a block
  whose seals were removed.
- **Seals attached to a pre-fork block** — `Hash()` branches on field presence rather than height
  (the Header cannot see the chain config), so attaching arbitrary seals to a pre-fork block keeps its
  hash and yields two RLPs for one hash. **`VerifyHeader` requires `CommitSeals == nil && BftRound == 0`
  when `!IsBft(number)`**, which closes this (§5.3). The same rule also closes the case where
  "round 0 + no seals" is indistinguishable from a pre-fork encoding — post-fork blocks always carry at least Quorum seals.

### 5.3 Header validation rules and the meaning of `BftRound`

**`BftRound` = the committed round.** It must equal `Round` in commitDigest so the seals can be verified.
The round in which the block was first proposed is not recorded (not needed for safety; §4.5 keeps the header unchanged on re-proposal).

| Heights | Rule |
|---|---|
| pre-fork (`!IsBft`) | `CommitSeals == nil`, `BftRound == 0` |
| post-fork (`IsBft`) | `IsCamellia`, `ParentBeaconRoot == nil`, `Time >= parent.Time` |
| post-fork | `MinerNodeSig` is **a signature by one of the validators of height `n-1`** (existing `verifyBlockSig` logic) |
| post-fork | at least `Quorum` `CommitSeals`, all from distinct validators, all valid for `commitDigest(BlockHash, BftRound, ChainID)` |

- The rev.3 rule "`MinerNodeSig` matches the key of `proposer(height, BftRound)`" is **removed**.
  A block re-proposed after a round change keeps the original proposer's `MinerNodeSig`, which differs
  from the commit round's proposer. The commit-round proposer's right to propose was checked during
  consensus through the ROUND_CHANGE certificate, and at import time the quorum of seals proves the outcome.
- `MinerNodeSig` stays as **the identity proof of the node that built the block**
  (`consensus/ethash/consensus.go:642-648`).

### 5.4 Difficulty / fork choice

- `header.Difficulty` keeps its fixed value (currently `params.FixedDifficulty=1`) — backward compatible.
- **Fork choice is decided by finality, not total difficulty.** On the `insertChain` path, reorg requests
  with `blockNumber <= finalizedNumber` are rejected. Committed blocks are final, so this cannot happen
  in normal operation; if it does, it is an attack or a bug.
- As implemented, `reorg` refuses a change of head that would drop a PBFT block (`errReorgBelowFinal`).
  The refused fork's blocks are still written as a side chain first, because `writeBlockWithState`
  runs before `reorg`, as in upstream geth. The insert fails, so the sync layer drops the peer that
  served the fork. The side-chain blocks never become canonical. They are expected, not a leftover
  to clean up (review on #154).

---

## 6. Code layout

```
consensus/metabft/
    engine.go        // consensus.Engine (Prepare/Finalize/Seal/VerifyHeader/SealHash)
    core.go          // state machine: round progression, message handling
    backend.go       // adapter to chain/miner/p2p (core never sees the chain directly)
    validators.go    // validator set, proposer selection, quorum
    messages.go      // BftMessage RLP, sign/verify, domain separators
    roundstate.go    // per-(height, round) collection state, PREPARED lock (in memory)
    wal.go           // vote/lock persistence (§6.1) — fsync before sending signatures
    evidence.go      // equivocation evidence storage (§7.1)
    roundchange.go   // round-change collection and certificate checks
    timer.go         // deadlines on the local monotonic clock (§4.5)
    snapshot.go      // per-epoch validator-set cache
    api.go           // RPC: metabft_getValidators, _getRoundState, _status, _readiness, _getEvidence

eth/protocols/metabft/
    protocol.go      // "metabft/1" sub-protocol definition
    handler.go       // receive/broadcast, dedup + equivocation detection (§7.1)
    peer.go
```

**Core principle:** the state machine in `core.go` depends only on the `backend` interface, so it can
be unit-tested without a chain or network (deterministic simulation is the backbone of verification
for this project). The WAL and the clock are injected as interfaces too, so simulations can replay
"crash → restart" and clock manipulation.

### 6.1 Vote persistence (WAL)

The §4.5 lock and the "one digest per round" rule **must survive restarts.** Kept only in memory,
a node that crashes after sending PREPARE/COMMIT can restart and vote for a different digest at the same
height; once such nodes exceed `f`, the quorum-overlap argument of §4.7 fails.
With rolling BP restarts as routine operations, this is a real risk.

**What is written, and in which order**
- **Before sending** PREPARE, COMMIT or ROUND_CHANGE, write `(height, round, type, digest)` and send
  **only after the fsync completes.** Reversing the order defeats the purpose.
- On entering PREPARED, write the lock `(preparedRound, preparedDigest, PREPARE quorum certificate, block RLP)`.
  After a restart this restores the justification for ROUND_CHANGE and the block to re-propose.
- Once a height commits, prune earlier records (the file stays at one to two heights' worth).

**Location:** an append-only file separate from chaindata (`<datadir>/metabft/wal`).
Chaindata writes are batched, so the fsync point cannot be guaranteed there.

**Restart rules**
- Never sign a different digest at a `(height, round)` at or below the last signature recorded in the WAL.
  A restored lock is followed per §4.5.
- **If the WAL is missing or corrupt** (disk replacement, snapshot restore, reinstall), start in observer
  mode and only vote after seeing the chain commit at least one height past the local head.
- **But a missing WAL is not always a lost one.** At the switch every validator starts without a WAL; if
  all of them waited as observers, nobody would vote and the chain would stop at `BftBlock`. So the WAL
  is **created during the bootstrap segment**, while `head + 1 < BftBlock` — before any PBFT vote is
  possible, since a PRE-PREPARE for height `n` needs `n − 1` as the local head. A missing WAL then means
  observer mode only when `head + 1 >= BftBlock`. A validator added by governance after the switch starts
  as an observer for one height; that is harmless unless the network cannot reach a quorum without it.
- **Why one height is enough — and must not be relaxed below it.** A block is written at COMMITTED, and a
  PRE-PREPARE is only accepted when its parent is the local head. So any vote that was in flight when the
  node crashed can only be at `chaindata head + 1`. Once the other validators commit that height, a lost
  vote has nothing left to conflict with. One height is the exact lower bound, not a safety margin.

**Operating rules**
- **Never run the same node key on two servers at once** (no active-active, no hot standby).
  Two servers mean two WALs, which cannot prevent double signing.
- Failover order: "confirm the original server is fully stopped → move the WAL → start the standby".

**Performance:** about three fsyncs per block (PREPARE, lock, COMMIT). With 100ms blocks that is about
30 per second — small on SSDs, but included in the §11.3 latency measurements.

---

## 7. Integration points

### 7.1 P2P: a **separate sub-protocol**, not a `meta` extension

Putting consensus messages into `meta` would need another `meta` version bump
(see `docs/meta69-blob-replay-design.md`) and would drag non-validator nodes, which take no part in
consensus, into message-code-length negotiation. Leaving the `meta` protocol untouched for networks
without PBFT is also safer.

→ **Add `metabft/1` as an independent devp2p sub-protocol.**
- `p2p.Protocol{Name: "metabft", Version: 1, Length: 8}`
- Only validators advertise this capability (non-validators do not negotiate it at all).
- Registration: append `metabft.MakeProtocols(...)` in `Protocols()` in `eth/backend.go`.
- Message codes: `0x00 PreprepareMsg`, `0x01 PrepareMsg`, `0x02 CommitMsg`,
  `0x03 RoundChangeMsg`, `0x04 SyncRequestMsg`, `0x05 SyncReplyMsg`.
- Replay protection: `BftMessage` itself carries `(Height, Round, ChainID, Signature)`, so
  meta/69's nonce+timestamp scheme (`eth/protocols/eth/metadium_replay.go`) is not needed.
- A **full mesh** between validators is assumed. Governance nodes already connect to each other
  through `metadium/admin.go:1359 addPeer`, so little extra work is needed.

**Deduplication and equivocation detection**

Order: **① verify the message signature → ② check the sender is a validator at that height → ③ look up the cache.**
Verifying before the cache lookup prevents a forged message naming another node as sender from taking
the cache slot first — which would make the genuine message be dropped as a "duplicate", or make an
honest node look like an equivocator.

| Cache key | Cache value |
|---|---|
| `(sender, height, round, type)` | `digest` of the first message received + the signed original |

- Same key, **same digest** → duplicate, dropped.
- Same key, **different digest** → not dropped: **both signed messages are stored on disk as evidence and an alarm is raised.**
  Only **the first message received** keeps counting toward consensus.
- Applies to all four message types. ROUND_CHANGE also has exactly one valid content per node per round,
  so differing content is equivocation. go-ethereum signatures are deterministic, so there is no
  legitimate case of two different COMMIT seals for the same digest.
- The evidence carries signatures and the ChainID, so **third parties can verify it independently.** It is
  queried with `metabft_getEvidence` and is the basis for the "manual removal via governance" in §12.
  It reaches every node: a node that stores a new pair relays both messages (below).
- The cache is pruned when the height commits (bounded size). Evidence only appears when someone equivocates.

**Relaying votes**

A validator relays each PREPARE, COMMIT and ROUND-CHANGE once, the first time its cache sees it, to
the other validators except the peer it came from. PRE-PREPAREs are not relayed: each carries a block,
and the proposer sends it to everyone itself.

- **Why.** Without relaying, the same node key on two servers is invisible. devp2p keeps one
  connection per node ID (`DiscAlreadyConnected`), so each validator is connected to only one of the
  two servers and hears only that one. When both are the proposer, each proposes its own block and
  prepares it, and no single node receives both PREPAREs. On the private network (§11.2 S-13) the two
  servers proposed different blocks at round 0 and no node stored evidence. The chain stayed safe:
  one key is one validator, within f. With relaying, the two conflicting PREPAREs meet at every node,
  which stores them as evidence.
- **The pair, too.** A node relays only the first of two conflicting messages it sees. If every node
  on one side saw the same one first, the other would stop there. So when a node first stores a pair
  as evidence, it relays both messages once, and every node ends up with the pair. This covers
  PRE-PREPARE pairs as well, so an equivocating proposer's evidence reaches every node. That is the
  "gossiping evidence" above, done with the signed messages themselves.
- **Own key.** A node puts its own votes into the cache when it sends them, so a relayed copy of one
  comes back as a duplicate. A different vote under its own key is evidence against itself. It is
  logged as an error: the key is running on another server and one of the two must be stopped (§6.1).
  A vote under its own key that arrives fresh (this node never sent it) is logged as a warning, not
  counted and not relayed. It is most likely the other server, but it could also be a copy that was in
  flight when this node restarted.
- **Cost.** Each vote goes out (N−1)(N−2) more times across the network, 30 at N = 7. Each node
  relays the other validators' 2(N−1) votes per height to N−2 peers each. At N = 7 with 100 ms blocks
  that is about 600 more small messages per second per node, on the order of 100 KB/s. Each relayed
  copy costs its receiver one signature recovery before the cache drops it. Measured on the private
  network (N = 7, idle seal 100 ms): confirmation latency p50 205 / p99 235 ms against 198 / 220 ms
  without relaying, and ~860 transfers/s under load with no round changes.
- The message shapes and protocol version do not change: a node that does not relay is still
  compatible, it just cannot detect a split twin.

### 7.2 Replacing `consensus.Engine`

In the branch (`:187`) of `CreateConsensusEngine` in `eth/ethconfig/config.go:175`, add a path that
builds the PBFT wrapper engine when the chain config sets `BftBlock` (§8.1: the genesis decides the
consensus).

Note: under option B, **a PoA bootstrap segment (`< BftBlock`) and a PBFT segment coexist** in one chain,
so a single binary must validate both (full sync of new nodes). The engine is therefore a wrapper.

```go
// in every method, VerifyHeader and so on
if !chain.Config().IsBft(header.Number) {
    // existing ethash (PoA) path + extra pre-fork rule: CommitSeals == nil && BftRound == 0 (§5.3)
    return e.legacy.VerifyHeader(chain, header)
}
// BFT path (§5.3 post-fork rules)
```

### 7.3 `miner/worker.go` — the biggest change

Today: `commitEx` → `Seal()` → **`sealedBlock = <-resultCh` received synchronously**
(`miner/worker.go:1866`) → `WriteBlockAndSetHead` (`:1912`).

Under PBFT, sealing can take seconds or several rounds, and **the result may be another node's
proposal.** Therefore:

**1) Replace the proposer gate** (`miner/worker.go:1666-1681`):
```go
if w.chain.Config().IsBft(height) {
    if !metaminer.IsBftProposer(height) { w.refreshPending(true); return }
} else if IsBokbunja { ... AcquireMiningToken ... }
else { ... IsMiner() ... }
```
   `IsBftProposer` follows the function-pointer pattern of `metadium/miner`
   (`metadium/miner/miner.go:44`) — consistent with the existing structure.
   In a round with a lock, the worker does not build a new block; the BFT core re-proposes the locked block.

**2) Move the write responsibility**: post-fork, the worker does not call `WriteBlockAndSetHead`.
   `Seal()` hands the block to the BFT core and returns immediately; **on commit, the BFT backend**
   attaches the seals and calls `WriteBlockAndSetHead`. The worker's synchronous `resultCh` wait is
   bypassed with an `IsBft` branch.

**3) Remove `LogBlock` / `ReleaseMiningToken`** (`miner/worker.go:1901-1910`):
   post-fork, etcd work logging is not needed. Skipped with an `IsBft` branch.
   (The code path behind the 2026-05-26 `failed to log latest block` post-mortem disappears entirely.)

**4) Non-proposers must execute blocks too.** Today a non-proposer calls `refreshPending` and returns.
   Under PBFT it must execute and validate the block on PRE-PREPARE (§4.8), so that path does not go
   through the worker: the BFT backend calls the validation API of `core.BlockChain` directly
   (`ValidateBody` + `Process` + `ValidateState`). The worker is not touched.

**5) Timestamp**: post-fork, the proposer uses `max(parent.Time, now)` (`timeIt`'s block-interval
   adjustment is not used in the PBFT segment — it can conflict with the §4.5 timestamp rules).

### 7.4 Finality

- Fork-branch `getFinalizedBlockNumber` in `metadium/admin.go:645`:
  ```go
  if chainConfig.IsBft(headNum) { return new(big.Int).Set(headNum) }  // committed = final
  ```
- `metaFinalHeader` in `core/blockchain_reader.go:83` keeps working as is
  (it already goes through this function).
- This makes `CurrentSafeBlock`/`CurrentFinalBlock`, blob limbo cleanup
  (`core/txpool/blobpool`) and `eth_getBlockByNumber("finalized")` all exact.

### 7.5 Rewards

**Current behaviour** (§2): the registered `verifyRewards` (`metadium/admin.go:1618`) only returns `nil` and has no callers.
When a block is processed, `accumulateRewards` (`consensus/ethash/consensus.go:741`) recomputes the rewards,
applies them to state and **overwrites `header.Rewards` and `header.Coinbase` with the recomputed values** (`:754`).
The proposer's values are therefore never compared, and a wrong reward is only caught as a state-root mismatch.

**Changes in the PBFT segment**
- `calculateRewards` (entry `metadium/admin.go:1610` → implementation `:1561`) is a deterministic
  function of `(num, blockReward, fees)`, so the calculation itself **does not change**.
- Before PREPARE, a validator **compares the recomputed `Rewards` and `Coinbase` with the header and rejects on mismatch.**
  These fields are part of `BlockHash` (= the digest, §5.2); overwriting them could give nodes different hashes.
- The comparison applies post-fork only. Pre-fork behaviour is unchanged.
- **Improvement:** today a wrong reward is only caught as a state-root mismatch after the block has already
  propagated. Under PBFT it is filtered out before PREPARE, so **the block never commits**
  (the exposure window of the block-18 class of incidents disappears — `docs/block18-reward-race-known-issue.md`).

### 7.6 etcd

| etcd use | After PBFT |
|---|---|
| mining token (`metaTokenKey`) | **removed** — replaced by consensus |
| work log (`metaWorkKey`) | **removed** |
| leader election | **removed** |
| cluster-membership RPCs (`EtcdAddMember` etc.) | kept (operations tooling) |
| `admin.update()` governance polling | kept (unrelated to etcd) |

After the fork, the `acquireMiningToken`/`releaseMiningToken`/`hasMiningToken` paths are not used,
thanks to the `IsBft` branches. **Removing the etcd server itself is deferred to a later release**
(to keep the operations RPC dependencies and a rollback path).

### 7.7 Sync

- **New / resyncing nodes**: the commit seals in the header are self-proving, so ordinary header/body
  sync is enough to validate. No consensus replay is needed.
- The snap-sync bypass in `acceptUnverifiableBlock` (`metadium/admin.go:1708`) must be **forbidden** at
  post-fork heights. If the validator set cannot be read, the node waits instead of accepting the block.
  (Compromising here makes the BFT guarantees meaningless.)
- **Lagging validators**: use `SyncRequestMsg`/`SyncReplyMsg` to request the current (height, round) and
  the latest committed block and catch up.

---

## 8. Configuration

### 8.1 The genesis decides the consensus

Every node must run the same consensus, so it is set in the **genesis chain config, not by a CLI flag.**
This rules out nodes being started with different flags.

```json
"config": {
    "chainId": 638200421,
    ...
    "camelliaBlock": 0,
    "bftBlock": 17280,
    "bft": { "emptyBlockInterval": 5, "baseTimeout": 2, "maxBackoffExp": 5, "timeDrift": 2 }
}
```

- Without `bftBlock` (nil): PoA forever — same behaviour as existing networks.
- `bftBlock` **must be greater than 0** (option B: the bootstrap segment is required, §9.2). `init` rejects 0.
- **If `bftBlock` is set, `camelliaBlock <= bftBlock` is required** (§5.1). `init` rejects a violation.
- `bft.*` **enters every validator's timers and checks, so it is fixed in the genesis** (§4.5).
  Only operational values that may differ between nodes are flags.
- In the PBFT segment, `bft.emptyBlockInterval` replaces `--metadium.block.emptyinterval`.
  If both are given and differ, warn and use the genesis value.

In `params/config.go`, add `BftBlock *big.Int`, `Bft *BftConfig`, `IsBft(num)`, the banner line
(pattern at `config.go:551`) and the `checkCompatible` entry (`config.go:790`).

### 8.2 CLI flags

```
--metadium.block.idleseal 100               # existing flag, unchanged (local proposer behaviour)
--bft.requestsyncinterval <sec, default 10>  # per-node operational value
```

**`--consensusmethod` stays `2` (PoA) on PBFT networks.** It selects the Metadium engine family:
engine creation (`eth/ethconfig/config.go:187`) and the Metadium admin (`metadium/admin.go:1291`) both key
on `ConsensusPoA`, and the PoA bootstrap segment (§9) needs exactly that path. Switching to BFT inside the
family is decided by `bftBlock`, not by the flag. So the CLI keeps rejecting `3` and `4`
(`cmd/utils/flags.go:2084`, unchanged), and a node whose chain config sets `bftBlock` refuses to start
unless it runs with `ConsensusPoA`.

---

## 9. Network setup (option B: PoA bootstrap → PBFT)

### 9.1 Why a bootstrap segment is needed

The validator set is read from the governance contract (§4.2). But today's setup procedure
(`tests/private-net-poa/setup.sh`) has **node1 produce blocks alone, without governance, and deploy the
governance contract afterwards.** With PBFT from block 0 there would be no validator set to agree on
the first block.

→ **Option B**: before `BftBlock`, bootstrap on the existing PoA; after governance is deployed and nodes
are registered, switch to PBFT at `BftBlock`. It has the same shape as activating Camellia at block 100
on the local private network (`setup.sh`), so existing code paths are reused.

(Option A, considered and rejected — putting an initial validator list in the genesis and running PBFT
from block 0 — would need a new genesis format and would duplicate the list held by the governance contract.)

### 9.2 Setup steps

| Step | Consensus | Work |
|---|---|---|
| 0. Write the genesis | — | fix the customer chain ID (§9.5), `bftBlock` and `bft.*`. **Cannot change afterwards** |
| 1. Start bootstrap | PoA (node1 alone) | same as Phase 1 of the existing `setup.sh` |
| 2. Set up governance | PoA | deploy governance (including `blockCreationTime`), register validator nodes (**N >= 4, N = 7 recommended for production**, §9.6), confirm the full mesh, confirm NTP sync on every node |
| 3. Readiness check | PoA | check every node with the `metabft_readiness` RPC (§9.3) |
| 4. Reach `BftBlock` | **PBFT** | automatic switch. First proposer = `validators[(BftBlock + 0) % N]` |

- Choose `bftBlock` **large enough to finish step 2.** The bootstrap segment is mostly idle, so blocks
  arrive at the empty-block interval (5s in the current profile) and `17280` ≈ one day (faster while
  governance-deployment transactions are flowing). Even if bootstrap finishes early, the chain stays on
  PoA until `BftBlock`.
- **The bootstrap segment has no BFT guarantee.** As an operating rule, no business transactions other
  than governance setup go into it.
- **The transition block's parent is a PoA block**, so it has no commit seals. Proposal validation
  branches on `IsBft(parent.Number)` so it does not require the parent's seals. The transition block's
  deadline needs no special case: `committedAt(n-1)` is the time the PoA parent became head (§4.5).
- A few round changes right after the switch are likely (nodes starting at slightly different times).
  That is normal; it converges within a few rounds.

### 9.3 Transition conditions and failure handling

If any of the following fails in the parent state of `BftBlock` (`BftBlock-1`), the chain **stops
producing blocks instead of continuing on PoA** (with a clear error log). Silently continuing on PoA
would let operators believe they are running BFT.

1. The governance contract is deployed
2. Governance node count `N >= 4`
3. Every validator advertises the `metabft/1` capability (local check on each node, warning only)

Recovery after such a stop: the bootstrap segment holds no business data, so the rule is to
**rebuild with a new genesis.** This is why the operating rule in §9.2 matters.

The `metabft_readiness` RPC returns the conditions above, the current height and the number of blocks left to `BftBlock`.

#### 9.3.1 Keeping `N >= 4` after the switch — reject, don't halt

The §9.3 gate only looks at `BftBlock`. If nodes are removed through governance after the switch and `N`
drops to 3, `f=0` and the quorum argument fails.

**Rule: a block whose post-execution state has fewer than 4 governance nodes is invalid.**
- The proposer leaves out transactions that would produce such a state (checked after executing the candidate block).
- Validators do not PREPARE such a block (§4.8 item 6). Syncing nodes also reject it in the state-validation
  step after `VerifyHeader`.
- Result: a governance transaction that would reduce `N` to 3 or less never commits, and **the chain keeps
  going.** Every rejection leaves an error log and a reason in `metabft_status`.

**Why not halt:** halting in §9.3 is recoverable because the bootstrap segment can be rebuilt from a new genesis.
Halting after the switch leaves **no recovery path**: the governance transaction that would add nodes back
cannot be put in a block either (only a hard fork or a chain rebuild remains, and neither is acceptable
for a chain holding business data).

**Follow-up:** enforcing `N >= 4` in the governance contract itself would be cleanest, but contract
changes are out of scope (§1.2).

### 9.4 Rollback

- **Before `BftBlock`**: can be undone by rebuilding with a new genesis.
- **After `BftBlock`**: the header format differs, so returning to PoA needs a chain rewind.
  → Before a customer network goes into production, switch a staging network with the same setup first.

### 9.5 Chain ID allocation (per customer)

**Premise.** go-metadium identifies Metadium Mainnet/Testnet by **genesis hash**, not chain ID
(`core/genesis.go:411`, `eth/backend.go:112`). A private network's chain ID can therefore be chosen
freely, and it does two jobs: preventing EIP-155 transaction-signature reuse, and domain-separating the
PBFT `commitDigest` (§4.6). Both only work if **every network has a different value.**

**Numbering: `6382 CCCC E` (9 digits)**

```
chainId = 638200000 + (customer number * 10) + environment code

  6382  fixed prefix (M-E-T-A on a phone keypad)
  CCCC  customer number 0001–9999 (0000 = internal)
  E     environment code
```

| E | Environment |
|---|---|
| 0 | reserved (unused) |
| 1 | production |
| 2 | staging |
| 3 | development / QA |
| 4 | DR / disaster recovery |
| 5–9 | spare (additional networks per customer) |

Example: customer 42 production = `638200421`, staging = `638200422`. Internal development = `638200003`.

**Why this scheme**
- **No collisions**: as of 2026-09-23 the chainid.network registry (2,764 chains) has no chain in
  `638200000–638299999`.
- **Decodable from the rule alone**: the chain ID alone tells the customer and environment, so networks
  are not confused in wallet setup or incident response.
- **Compatible range**: the maximum `638299999` is below 2^31−1, safe for JS (2^53), tools using
  32-bit integers and hardware wallets.
- **No mainnet signature reuse**: does not overlap `11`/`12`/`1337`.

**Operating rules**
1. **Keep the allocation register in an internal, non-public document.** The mapping from customer names
   to numbers does not go into this (public) repository.
2. **Never reuse numbers.** A former customer's number is retired, not reassigned.
3. **Set `--networkid` equal to the chain ID.**
4. **Public registration is optional.** Registering a production network on chainlist
   (`ethereum-lists/chains`) rules out outside collisions but makes the customer's existence public.
   Register only with the customer's consent; otherwise re-check the registry for collisions when the
   network is set up.
5. **Check at `init`**: warn when the chain ID is `11`, `12` or a known value from the public registry.
   In particular, `metadium/scripts/genesis-template.json` currently defaults to `"chainId": 11`
   (mainnet); turn the template into a placeholder and have `init` refuse it until the value is filled in.

### 9.6 Validator count and availability

**Formula.** Tolerating `f` faulty nodes needs `N = 3f + 1`, and committing a block needs `ceil(2N/3)`
live nodes (the quorum; `2f + 1` when `N = 3f + 1`, §4.1). `f` is the **sum** of stopped and malicious nodes (with 7 nodes and one stopped,
only one malicious node can be tolerated).

| N | Faults tolerated f | Quorum | Notes |
|---|---|---|---|
| 3 or fewer | 0 | — | BFT meaningless. Switch refused (§9.3), reduction after the switch rejected (§9.3.1) |
| **4** | 1 | 3 | minimum |
| 5, 6 | 1 | 4, 4 | same tolerance as 4 with a larger quorum → **not recommended** |
| **7** | 2 | 5 | recommended for production |
| **10** | 3 | 7 | |
| 13 | 4 | 9 | |

Efficient sizes are **4, 7, 10, 13**.

**Compared with the current etcd/raft**

| | raft (current) | PBFT |
|---|---|---|
| Nodes needed | `N = 2f + 1` (majority) | `N = 3f + 1` (more than 2/3) |
| Tolerate 1 fault | 3 | 4 |
| Tolerate 2 faults | 5 | 7 |
| Malicious nodes | not defended | up to `f` |

**Expected yearly block-production downtime**, assuming 99% availability per node and independent failures:

| Setup | Expected downtime per year |
|---|---|
| raft, 3 nodes | about 157 min |
| PBFT, 4 nodes | about 311 min |
| raft, 5 nodes | about 5 min |
| PBFT, 7 nodes | about 18 min |
| PBFT, 10 nodes | about 1 min |

At the same size PBFT is less available than raft: it gives up some availability in exchange for
"no forks" safety.

**Operational notes**
1. **4 nodes leave no maintenance headroom.** Taking one node down, for an upgrade for example, uses up
   the whole fault budget (`f=1`); one more problem on any other node stops block production. 7 nodes
   survive one node in maintenance plus one failure. Rolling restarts are only safe with the WAL (§6.1).
2. **A stop does not fork.** Without a quorum only production stops; it resumes automatically once nodes
   recover (§11.2 scenario 3).
3. **A partition can stop both sides.** 7 nodes split 4:3 leave neither side with the quorum of 5
   (§11.2 scenario 6). So **never put more than `f` nodes in one failure domain (data centre, availability zone).**
   7 nodes (`f=2`) over 3 zones (3/2/2) stop when the 3-node zone is lost, so spread them over
   **4 or more zones (e.g. 2/2/2/1).**
4. **RPC and full nodes do not count toward consensus.** Design block-production availability and query (RPC)
   availability separately.

**Recommended setups**

| Environment | Validators | Placement |
|---|---|---|
| Development / QA | 4 | a single zone is fine |
| Staging | 7 | same placement as production (for transition rehearsal, §9.4) |
| Production | 7 (10 for high availability) | 4+ failure domains, at most `f` per domain |

---

## 10. Implementation phases

| Phase | Content | Deliverable / check |
|---|---|---|
| **P0** | genesis `bftBlock`/`bft`, `IsBft()`, `camelliaBlock <= bftBlock` check, flag consistency check, chain-ID `init` check, `genesis-template.json` placeholder | `params` unit tests, `init` rejection tests |
| **P1** | header fields (`headerRlp`, before `ParentBeaconRoot`) + `BlockHash` exclusion rule + no seals pre-fork + RLP round trip | `core/types` round-trip tests, pre/post-fork hash stability tests, **test rejecting a pre-fork block with arbitrary seals** |
| **P2** | `consensus/metabft` validator set / proposer / quorum / message signing / WAL / evidence storage | pure unit tests (no chain), WAL crash-point injection tests |
| **P3** | state machine `core.go` + round change + byte-identical re-proposal, with mocked backend, clock and WAL | **deterministic simulation**: N=4/7/10, injecting delay, loss, Byzantine behaviour, **crash-restart and clock manipulation** |
| **P4** | `metabft/1` P2P sub-protocol + dedup and equivocation detection | two-node message round trip, forged-signature rejection, equivocation evidence |
| **P5** | integrate engine / worker / finality / etcd / reward comparison / `N>=4` validity branches | real block production on 4 local nodes |
| **P6** | fault injection + performance + bootstrap → transition rehearsal (§9.2, including the §9.3 failure case) | test plan below |

P3 is the centre of gravity. **If the state machine is not designed to be tested deterministically
without a network, later phases become impossible to debug.** The schedule is re-estimated once P3 is done.

---

## 11. Test plan

### 11.1 Extend the test network (prerequisite)

Extend `tests/private-net-poa` from **3 to 7 nodes** (`f=2`).
- add node4–node7 to `docker-compose.yml`, ports 8548–8551
- extend the initial governance node registration in `setup.sh`
- first confirm the existing `camellia-test.sh`, `blob-tx-e2e` and `mixed-tx-e2e` still pass
  (baseline before PBFT work)

### 11.2 Fault-injection scenarios (N=7, f=2)

| # | Scenario | Expected result |
|---|---|---|
| 1 | stop 1 validator | production continues, one round change on that node's turn |
| 2 | stop 2 validators (= f) | production continues (slower) |
| 3 | stop 3 validators (> f) | **production stops**, resumes automatically on recovery, no fork |
| 4 | proposer sends two different blocks to two groups (equivocation) | no commit, round change, no fork, **2 pieces of evidence stored, queryable via `metabft_getEvidence`, alarm raised** |
| 5 | proposer proposes a wrong state root / wrong Rewards or Coinbase | PREPARE refused → round change |
| 6 | 4:3 network partition | the group of 4 cannot proceed either (4 < Quorum=5, so **both sides stop**); resumes on healing, no fork |
| 7 | clock skew (one node +5 min) | production continues (that node's proposals may be rejected by the timestamp bound) |
| 8 | add/remove a validator through governance | switch at the epoch boundary without interruption |
| 9 | new node joins after snap sync | commit seals verify, joins consensus |
| 10 | inject a block with commit seals removed or forged | import rejected |
| 11 | **kill right after PREPARE / right after COMMIT → restart** | never votes for a different digest at the same height, lock restored |
| 12 | **restart with the WAL deleted** | observer mode → joins after seeing one height commit |
| 13 | **start two servers with the same node key** | equivocation evidence and alarm |
| 14 | **proposer sets `Time` in the past (before the parent) / future (+10s)** | rejected by `VerifyHeader` / the bound check; round 0 of the next height is normal |
| 15 | **try to remove nodes through governance down to N=3** | the transaction never commits, the chain continues, the reason is recorded |
| 16 | **commit a block re-proposed after a round change** | original proposer's `MinerNodeSig` kept, `BftRound` = commit round, import check passes |
| 17 | **inject a pre-fork block with arbitrary `CommitSeals` attached** | import rejected |

> Scenario 6 is counter-intuitive, so to be explicit: with `Quorum = 2f+1 = 5`, a 4:3 split leaves
> **neither side able to proceed.** This is the decisive difference from CFT (raft proceeds with a
> majority of 4) and is PBFT's intended behaviour of "giving up availability for safety". The operations
> team needs to understand and accept this.

### 11.3 Performance measurements

- **Transaction confirmation latency** (`eth_sendTransaction` → receipt): current PoA with `idleseal=100`
  is p50 118ms / p99 130ms (`docs/enterprise-block-timing.md`). PBFT adds three-phase round trips,
  validator block execution and **WAL fsyncs**. The target is **p99 < 300ms** for N=7 on a LAN, to be confirmed by measurement.
- Idle empty-block interval (target: `EmptyBlockInterval` ± 10%, **zero round changes while idle**)
- **Round changes under load** (target: zero while several blocks per second are produced — checks that the rev.3 timer flaw does not come back)
- Comparison with the fixed-interval profile (`blockCreationTime = 2000`, no idleseal — the public-network setting): whether the 2.0s interval holds
- Message complexity: `O(N²)` per round — about 98 messages per block for N=7. Above N=30,
  review bandwidth (900 messages, since it is `O(N²)`).
- TPS regression: compare against Camellia with `scripts/rpc-test-full.sh` and `mixed-tx-e2e`.

---

## 12. Risks and open issues

| Risk | Impact | Mitigation |
|---|---|---|
| **Lower availability** | raft proceeds with a majority (N/2+1), PBFT needs 2f+1 — for N=7, 4 vs 5. Expected yearly downtime is higher than raft at the same size (§9.6) | 7+ nodes in production, spread over 4+ failure domains, at most f per domain. Needs operations sign-off |
| **No maintenance headroom with 4 nodes** | one node in maintenance plus one more failure stops production | 4 nodes only for development/QA; 7 for production and staging (§9.6) |
| **Vote state lost on restart** | a restarted node votes for another digest → quorum argument fails | WAL (§6.1), scenarios 11 and 12 |
| **Same node key on two servers** | both servers sign with the same key → the WAL cannot prevent double signing | no active-active, failover procedure (§6.1), evidence detection (§7.1) |
| **Timestamp manipulation** | proposer uses `Time` to skew timers or EVM time | timers on the local clock (§4.5), `Time >= parent.Time` + PRE-PREPARE bound check |
| **`N²` message complexity** | limits validator count | no problem at current sizes. Consider signature aggregation (BLS) at 30+ |
| **No slashing** | equivocation can be **detected and evidenced** but not punished | manual removal through governance based on evidence (§7.1). On-chain punishment is out of scope |
| **Header format change** | affects that private network's explorers/indexers (existing Mainnet/Testnet unaffected) | expose the fields in `internal/ethapi/api.go:1383`, share with customer tooling in advance |
| **No rollback after the switch** | cannot return to PoA after `BftBlock` | rebuild from a new genesis before the switch (§9.4). Switch staging with the same setup first |
| **No BFT guarantee during bootstrap** | blocks before `BftBlock` are produced by PoA alone | no business transactions other than governance setup in that segment (§9.2) |
| **Chain ID collision / misconfiguration** | reusing the template's mainnet (`11`) default risks signature reuse | `6382CCCCE` scheme (§9.5), `init` check, template placeholder |
| **Blob sidecar availability** | a validator without the sidecar at commit time | fetch via meta/69 `GetBlobSidecarsMsg` before PREPARE; hold PREPARE until available |
| **`snap sync` vs BFT validation** | the `acceptUnverifiableBlock` bypass defeats the BFT guarantees | block that path at post-fork heights (§7.7) |
| **N < 4 when `BftBlock` is reached** | block production stops (§9.3) | check with the `metabft_readiness` RPC before the switch; choose a generous `bftBlock` |
| **Attempt to go below N = 4 after the switch** | quorum argument fails | such blocks are invalid — reject, don't halt (§9.3.1) |
| **Schedule pressure** | forcing a full rollout on a short schedule lets safety items (§4.5, §5.2, §6.1, §7.1) in without verification | if time is short, reduce scope with a §13 alternative and state explicitly what is and is not guaranteed |

---

## 13. Alternatives — lighter options

Intermediate steps for when a full PBFT rollout is too much or does not fit the schedule. **Pick one, not several.**
The cost ratios in rev.3 (25%/10%) were unverified estimates and are removed; the work needed is listed by section instead.

### Alternative 1: add commit seals only (keep the current consensus)
The etcd token still picks the proposer; after a block propagates, validators collect commit seals and
**carry them in the header of a later block.**
- Gains: a **verifiable finality proof**, one block late (replacing today's heuristic)
- Does not gain: defence against a Byzantine proposer (a bad block still propagates first)
- **Work needed**: header fields and hash rules (all of §5), seal-collection P2P (part of §7.1), signing rules (§4.6),
  **the "never sign two blocks at one height" rule and the WAL (§6.1)**, and **no reorg below a sealed block (§5.4).**
  Without the last two, the result looks like a proof but can still be overturned.
  → Hard to finish, verification included, on a short schedule.

### Alternative 2: on-chain signature records (no consensus or header change) — added in rev.4
Each validator periodically signs `(height, block hash)` and records it in a dedicated contract as a
transaction. A height with a quorum (`ceil(2N/3)`) of signatures becomes the evidence.
- Gains: multi-party signed evidence that third parties can verify. No changes to the header, P2P or consensus code, so existing chains are unaffected
- Does not gain: real-time Byzantine defence. The guarantee that there is no reorg still rests on the current etcd/raft
- Difference: the limitation can be **stated openly alongside the evidence** instead of hidden. The most realistic evidence option on a short schedule

### Alternative 3: keep etcd + detect equivocation
Keep the current structure; validators watch each other's block signatures and detect and alarm on
double signing at the same height.
- Gains: after-the-fact detection, operational visibility
- Does not gain: real-time defence, a finality guarantee

### Decision criteria

**When an external requirement drives the choice**, what it actually asks for decides the option,
so confirm its wording first:

| The requirement asks for | Options that qualify |
|---|---|
| a BFT-family consensus protocol itself | full PBFT or alternative 1 — **alternative 2 does not qualify**, since block production stays on etcd/raft |
| finality that a third party can verify | alternative 2 is sufficient |
| nothing specific (the direction is our own) | keep the current architecture; PBFT becomes separate work |

**Otherwise**, decide by who runs the validators:
- If the network is a **trusted consortium** (one organisation or contract controls every validator),
  raft (CFT) is enough and alternative 3 is enough.
- If **mutually distrusting parties** run validators, or regulation or audit requires proving that
  "forks are impossible", a full PBFT rollout is justified.

The design body (§3–§12) holds whichever branch is chosen.

---

## 14. Summary

- Today `ConsensusPBFT` is an **empty shell**: a constant and CLI text only (2 references).
- The implementation is **IBFT 2.0 family**: a new `consensus/metabft` package + an independent
  `metabft/1` sub-protocol + commit-seal fields in the header.
- It targets **new private networks**; PBFT is selected with `bftBlock` in the genesis.
  Before `BftBlock` the chain bootstraps on PoA (governance deployment, node registration), then switches to PBFT (option B).
- Chain IDs are allocated per customer and environment with the `6382 CCCC E` scheme (§9.5).
- Core safety assumptions: **digest = the whole block hash** (§5.2), **a vote WAL** (§6.1),
  **timers on the local clock** (§4.5), **N >= 4 after the switch too** (§9.3.1).
- The biggest change is moving `miner/worker.go` from **synchronous sealing to asynchronous commit.**
- Prerequisites before work starts: **extend the test network to 7 nodes**, **secure N>=4 validators
  (7 recommended for production, §9.6) for customer networks**, and **operations sign-off on the
  availability trade-off that a 4:3 split halts the chain.**

---

## 15. Revision history

| Rev | Content |
|---|---|
| rev.1 | first draft (assumed a hard fork of existing chains) |
| rev.2 | scope changed to new private networks, option B (PoA bootstrap → switch), chain ID scheme, block timing |
| rev.3 | validator count and availability (§9.6) |
| rev.4 | first review round (below) |
| rev.5 | PR #143 review: `committedAt` defined as "became head" (§4.5, §9.2), why observer mode waits exactly one height (§6.1), requirement-driven decision criteria for the alternatives (§13). §8.2 corrected during P0: PBFT networks keep `--consensusmethod 2`. §2/§5.1 corrected during P1: `headerRlp` declares `ParentBeaconRoot` (never filled), and the PBFT fields go before it. Corrected during P2: quorum is `ceil(2N/3)` (§4.1, §4.7, §9.6 — `2f+1` is unsafe for N = 5, 6), and the WAL is created during bootstrap so the switch does not deadlock (§6.1) |

**rev.4 review changes**

| Review | Finding | Change |
|---|---|---|
| 1 | the deadline depended on the proposer-chosen, seconds-resolution `parent.Time` → round-change storms under load, timestamp manipulation | timers moved to the local monotonic clock, `Time >= parent.Time` + PRE-PREPARE bound check, resolution kept in seconds (§4.5). A strict `>` was not adopted because it conflicts with the 100ms profile |
| 2 | no persistence of the PREPARED lock → double voting after restart | WAL, restart and observer-mode rules, no same node key on two servers (§6.1) |
| 3 | the dedup cache silently dropped equivocations → contradicted the §12 mitigation | keep the digest as the cache value; a different digest is stored as evidence with an alarm. Signature check before the cache lookup (§7.1) |
| 4 | seals attached to a pre-fork block left its hash unchanged | pre-fork `CommitSeals == nil && BftRound == 0` enforced, fields at the optional tail of `headerRlp`, `IsBft ⇒ IsCamellia` (§5.1–5.3) |
| 5 | no rule for N < 4 after the switch | a block leaving N < 4 after execution is invalid — **reject** chosen over the suggested halt, because a halt has no recovery path (§9.3.1) |
| 6 | the `MinerNodeSig` check contradicted the re-proposal rule, `BftRound` was ambiguous | `BftRound` = committed round, re-proposed header unchanged, `MinerNodeSig` only checked for validator membership (§4.5, §5.3) |
| 7 | code-reference line numbers off | §2 table and body corrected. `etcdutil.go:989` kept since it is the `acquireTokenSync` declaration (CAS at 1024 added) |
| 8 | a full rollout does not fit a short schedule; alternative costs need a second look | §13 cost ratios removed and work listed, alternative 2 (on-chain signature records) added, schedule-pressure risk in §12 |
| extra | found while checking the reviews against the code: `SealHash` as the digest maps one digest to several block hashes; `verifyRewards` is empty and reward fields are overwritten | digest = `BlockHash` (§4.4, §4.6, §5.2), reward comparison (§7.5, §4.8) |
