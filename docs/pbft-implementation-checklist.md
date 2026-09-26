# PBFT implementation checklist

> Tracks the work in `docs/pbft-consensus-design.md` (rev.5). Every item points at the design section
> it implements; the design is the source of truth, this file only tracks status.
> Status: `[ ]` not started · `[~]` in progress · `[x]` done · `N/A` with a reason.

> Last updated: 2026-09-24. P0: 5 done (P0a), 2 moved to P5, 2 in P0b. Branch: `feature/pbft-consensus`.

---

## Gate 0: before implementation starts

| ID | Item | Design | Status |
|----|------|--------|--------|
| G-01 | The external requirement's wording is confirmed and one branch of the §13 decision table is chosen | §13 | [ ] |
| G-02 | Operations sign-off on the availability trade-off (a 4:3 split halts; downtime figures) | §9.6, §11.2 #6 | [ ] — the downtime figures are in `docs/pbft-test-report.md` §4 (`availability.sh`: the first block 2–12 s after the quorum returns, for outages of 10–180 s). The sign-off itself is for operations |
| G-03 | `tests/private-net-poa` extended from 3 to 7 nodes (node4–node7, ports 8548–8551) | §11.1 | [x] — met by `tests/private-net-pbft` with `BFT_BLOCK=off`, a plain PoA genesis (no `bftBlock`) on 4–9 nodes (8645–8653), rather than rewriting `private-net-poa`'s fixed 3-node layout. `deploy.sh` and every client test take the RPC URL |
| G-04 | Baseline on 7 nodes: `camellia-test.sh`, `blob-tx-e2e`, `mixed-tx-e2e` pass before any PBFT change | §11.1 | [x] — run after the fact, on the same 7-node PoA network: `master` (v1.1.4, before any PBFT change) and this branch. Both: `camellia-test.sh` 14 PASS / 0 FAIL / 3 SKIP, `blob-tx-e2e` and `mixed-tx-e2e` ALL PASS |

G-01 decides whether the phases below run at all; P0–P3 do not touch block production and can
start before it if the schedule needs them.

---

## P0: configuration

| ID | Item | Design | Status |
|----|------|--------|--------|
| P0-01 | `BftBlock *big.Int` and `Bft *BftConfig` (`emptyBlockInterval`, `baseTimeout`, `maxBackoffExp`, `timeDrift`) in `params/config.go` | §8.1 | [x] |
| P0-02 | `IsBft(num)`, banner line, `checkCompatible` for the switch block and, once past it, the parameters | §8.1 | [x] |
| P0-03 | Config check (`CheckConfigForkOrder` → `checkBft`): `bftBlock > 0`, `bft` present iff `bftBlock`, positive durations, `maxBackoffExp <= 10` | §8.1, §9.2 | [x] |
| P0-04 | Config check: `camelliaBlock <= bftBlock` (`IsBft ⇒ IsCamellia`) | §5.1, §8.1 | [x] |
| P0-05 | Startup check: `EmptyBlockInterval >= blockCreationTime` | §4.5 | moved to P5-21 — `blockCreationTime` is read from governance at runtime |
| P0-06 | A chain config with `bftBlock` refuses to start unless the node runs `ConsensusPoA` (`eth/bft_guard.go`); the CLI keeps rejecting 3 and 4 | §8.2 | [x] |
| P0-07 | `--metadium.block.emptyinterval` vs `bft.emptyBlockInterval`: warn, genesis wins | §8.1 | moved to P5-22 — only meaningful once the engine reads the value |
| P0-08 | `init` warns on chain ID `11`, `12` or a known public-registry value | §9.5 | [x] (#145) — `cmd/geth/chainid_guard.go`: `init` refuses chain ID 0 and warns on Metadium mainnet/testnet, the Ethereum networks and the local defaults (1337, 31337); `TestCheckGenesisChainID` |
| P0-09 | `metadium/scripts/genesis-template.json`: `chainId` becomes a placeholder; `init` refuses it unfilled | §9.5 | [x] (#145) — the template carries `0`; `gmet metadium genesis` takes the value from the data file (`chainId`) and refuses a result without one; `TestApplyGenesisChainID`, `TestConfigExampleChainID` |

**Guard until P5:** a chain config with `bftBlock` refuses to start (`errBftNotImplemented` in
`eth/bft_guard.go`), so no build keeps sealing PoA past the switch block (§9.3). Removed by P5-00.

**Tests:** `params/bft_config_test.go` (every `checkBft` case, `IsBft`, compatibility, JSON and banner),
public configs pinned to no PBFT (`params/metadium_config_test.go`), `init` path
(`core/genesis_bft_test.go`), startup guard (`eth/bft_guard_test.go`). [x]

---

## P1: header and block hash

| ID | Item | Design | Status |
|----|------|--------|--------|
| P1-01 | `BftRound`, `CommitSeals` in `Header`, and in `headerRlp` between `BlobGasUsed` and `ParentBeaconRoot` (never filled, stays the omitted tail) | §5.1 | [x] |
| P1-02 | `headerToHeaderRlp` / `headerRlpToHeader` carry both fields | §5.1 | [x] |
| P1-03 | `Hash()` excludes `BftRound`/`CommitSeals` (`BlockHash`) | §5.2 | [x] |
| P1-04 | `CopyHeader` deep-copies `CommitSeals`; `Size` counts them; `SanityCheck` bounds count (`MaxCommitSeals`) and length (`CommitSealLength`) | §5.2 | [x] |
| P1-05 | JSON (`gen_header_json.go`, regenerated, `omitempty`) and RPC (`RPCMarshalHeader`, only when seals are non-empty) | §12 | [x] |
| P1-06 | PoA `verifyHeader` rejects any header carrying the PBFT fields (`verifyNoPbftFields`), so a node accepts exactly what the current release accepts; needs no chain config because this engine only verifies non-PBFT heights (moved back from P5-02, review on #146) | §5.2, §5.3 | [x] |

**Tests**

| ID | Test | Status |
|----|------|--------|
| P1-T1 | RLP round trip with and without the new fields | [x] |
| P1-T2 | Header hashes unchanged by this change: pre-London, London and Camellia vectors computed in PoA mode on the tree before it (encodings matched byte for byte) | [x] |
| P1-T3 | Same header with different seal sets and rounds → same `BlockHash`; `Rewards`/`MinerNodeId`/`MinerNodeSig`/`Coinbase`/`Time` still change it | [x] |
| P1-T4 | "round 0 + no seals" encodes identically to a pre-fork header | [x] |
| P1-T5 | Without the Camellia fields, a PBFT header does not round-trip (why `IsBft ⇒ IsCamellia`) | [x] |
| P1-T6 | Non-PBFT JSON and RPC output unchanged (also for an empty seal list); PBFT fields round-trip through JSON | [x] |
| P1-T7 | Real mainnet header (block 118,924,592) hashes to its on-chain hash; trailing seals decode (documents P1-06) and are rejected by the PoA engine | [x] |

---

## P2: consensus building blocks (no chain)

| ID | Item | Design | Status |
|----|------|--------|--------|
| P2-01 | `validators.go`: ordered set from enode keys, `proposer(h, r)`, `f`, `Quorum = ceil(2N/3)` (reading `getMetaNodes` at `n-1` is P5) | §4.1–§4.3 | [x] |
| P2-02 | `messages.go`: `Message` RLP, signing, `ecrecover` → validator index, chain ID check, low-s only | §4.6 | [x] |
| P2-03 | `commitDigest = keccak256(rlp([BlockHash, Round, ChainID, 0x02]))`; a COMMIT's seal must be by its signer | §4.6 | [x] |
| P2-04 | `wal.go`: append-only framed file (length + CRC-32C), fsync before return (path chosen in P5) | §6.1 | [x] |
| P2-05 | WAL records: `(height, round, type, digest)` per vote, ROUND-CHANGE by its full signed content; lock `(preparedRound, digest, certificate, block RLP)`; refuses conflicting and past-round votes and locks that contradict a recorded vote | §6.1 | [x] |
| P2-06 | WAL pruning (atomic rewrite + rename); calling it on commit is P3 | §6.1 | [x] |
| P2-07 | `ErrWALMissing` / `ErrWALCorrupt` for the caller; an incomplete final frame (crash mid-append, never sent) is truncated instead | §6.1 | [x] |
| P2-08 | `evidence.go`: verify, store both signed messages (one file per pair, order-independent), survive restart | §7.1 | [x] |

**Tests**

| ID | Test | Status |
|----|------|--------|
| P2-T1 | Quorum table N=1..13, and for N=1..100: two quorums share f+1, honest nodes reach quorum, quorum is minimal | [x] |
| P2-T2 | Message signature and commit-seal signature cannot be swapped (domain separator); tampering any field breaks the signature; high-s rejected | [x] |
| P2-T3 | A signature with another ChainID is rejected | [x] |
| P2-T4 | WAL: votes and lock survive reopen and still refuse conflicts; torn final frame at every cut point is discarded safely; concurrent writers | [x] |
| P2-T5 | WAL: damage inside the file → `ErrWALCorrupt`, never a vote from partial state | [x] |

---

## P3: state machine (deterministic simulation)

| ID | Item | Design | Status |
|----|------|--------|--------|
| P3-01 | `core.go`: single-threaded, event-driven core (`NewHeight`, `HandleMessage`, `Propose`, `Tick`, `Deadline`) | §4.4 | [x] |
| P3-02 | Every signed message (PRE-PREPARE included) written to the WAL before it is sent; lock recorded on PREPARED | §4.4, §6.1 | [x] |
| P3-03 | PRE-PREPARE checks: proposer, digest, height, justification in the core; chain-side checks via `Backend.VerifyProposal` | §4.8 | [x] |
| P3-04 | Round change: timeout, invalid proposal, `f+1` amplification to the highest round f+1 validators reached | §4.5 | [x] |
| P3-05 | Re-proposal of the highest prepared block (QBFT rule), byte-identical | §4.5 | [x] |
| P3-06 | Round-change quorum and PREPARE quorum attached to round > 0 proposals and checked (`justify`); prepared claims need their evidence | §4.5, §4.8 #2 | [x] |
| P3-07 | `timer.go`: `deadline(n,0) = committedAt(n-1) + EmptyBlockInterval + BftBaseTimeout`, backoff for `r >= 1`, `committedAt` = "became head" | §4.5 | [x] |
| P3-08 | Restart: resume at the WAL's highest round, restore the lock, never re-propose in a round already proposed in | §6.1 | [x] |
| P3-09 | Observer mode exits after one height commits past the local head | §6.1 | [x] |
| P3-11 | `OpenNodeWAL`: WAL created during bootstrap (`head + 1 < bftBlock`); missing later or corrupt → observer until head+1 commits (calling it at startup is P5) | §6.1 | [x] |
| P3-12 | `Message.ExtraHash` signed; a message received directly must carry exactly the `Extra` it names, a quoted one none, a non-ROUND-CHANGE never — so a relay cannot alter or strip an attachment and still pass `Verify` (review on #148) | §7.1 | [x] |
| P3-13 | One `RequestProposal` per round; timeouts saturate instead of wrapping; PRE-PREPAREs/PREPAREs/COMMITs kept only up to 64 rounds ahead, while ROUND-CHANGEs beyond that still count for f+1 amplification (latest per sender held, replayed on arrival); WAL pruned every 16 heights (review on #148) | §4.5, §6.1 | [x] |
| P3-S10 | f+1 validators down for 90 minutes; the others climb past the round window; the returning ones rejoin in one amplification step (a validator 100 rounds behind moves in one step) | [x] |
| P3-10 | Simulator with injected backend, clock and WAL (`sim_test.go`) | §6 | [x] |

**Simulation runs** (N = 4, 7, 10)

| ID | Scenario | Status |
|----|----------|--------|
| P3-S1 | Message delay (≤300ms) and 5% loss, N=4/7/10 × 8 seeds | [x] |
| P3-S2 | Byzantine proposer: equivocation, invalid block, silence | [x] |
| P3-S3 | Byzantine voters (f) vote for both blocks of an equivocation, N=4/7/10 × 10 seeds | [x] |
| P3-S4 | Random crashes/restarts with and without the WAL; all validators at once; targeted amnesia after a partial commit | [x] |
| P3-S5 | Proposer `Time` pushed into the past / future | N/A in P3 — no timer takes header time as input by construction; the header bounds are P5-03/P5-06, scenario S-14 |
| P3-S6 | 100ms proposals and 5s idle waits: zero round changes | [x] |
| P3-S7 | Safety invariant checked on every commit of every node; seals checked to prove the decision | [x] |
| P3-S8 | COMMITs withheld from all but one node (sync off), and 30% COMMIT loss: the prepared block is re-proposed and decided again | [x] |
| P3-S9 | Liars (f): per peer, truthful / hides its prepared block / offers an older genuine one / invents one without evidence; as proposer, re-proposes its oldest prepared block. PREPARE and COMMIT loss for 20 minutes, then none | [x] |
| P3-M | Mutation checks: re-proposal off → fork (P3-S8); WAL off → fork (amnesia); prepared claims accepted without evidence → crash (P3-S9); `justify` taking the lowest prepared round → stall (P3-S9) | [x] |

---

## P4: `metabft/1` sub-protocol

| ID | Item | Design | Status |
|----|------|--------|--------|
| P4-01 | `p2p.Protocol{Name: "metabft", Version: 1, Length: 8}`; `MakeProtocols` returns nothing on a non-validator (fixed at startup) | §7.1 | [x] |
| P4-02 | Registered in `Protocols()` in `eth/backend.go` | §7.1 | moved to P5 — nothing can consume the messages until the engine exists |
| P4-03 | Message codes `0x00`–`0x05`; the code must match the message type; `SyncRequest`/`SyncReply` carry (height, round), blocks come by eth sync | §7.1, §7.7 | [x] |
| P4-04 | Order: decode → `Verify(Direct)` (signature, chain ID, membership, attachment) → cache; broken or foreign-chain messages drop the peer, unknown signers and heights are dropped quietly | §7.1 | [x] |
| P4-05 | Cache `(signer, height, round, type)` → first message; different digest (ROUND-CHANGE: different signed content) → evidence to the backend, not forwarded | §7.1 | [x] |
| P4-06 | `Cache.Prune(height)` for the engine to call on commit; size-bounded backstop | §7.1 | [x] |

**Tests**

| ID | Test | Status |
|----|------|--------|
| P4-T1 | Round trip of all four message types over `p2p.MsgPipe`; sync request/reply | [x] |
| P4-T2 | Forged signature rejected before it reaches the cache; a broken signature drops the peer | [x] |
| P4-T3 | Forged sender cannot pre-empt a genuine message; nor can a relayed ROUND-CHANGE with its attachment stripped or altered | [x] |
| P4-T4 | Two digests from one sender (and two ROUND-CHANGE contents) → evidence, first message still counted | [x] |
| P4-T5 | Non-validator nodes do not advertise `metabft/1` | [x] |

---

## P5: integration

| ID | Item | Design | Status |
|----|------|--------|--------|
| P5-00 | Remove the `errBftNotImplemented` startup guard (`eth/bft_guard.go`) once a PBFT chain can produce blocks end to end (P5-07, P5-08, P5-23) and the guards are in (P5-13 `N >= 4`, P5-16 no reorg below finality, P5-25 no snap sync; review on #152). Known exception, by design: `SetHead` (`debug_setHead`, rewind-style recovery) does not go through `reorg` and can still rewind below finality; it is an explicit local operator action, not a fork choice (review on #154); P5-01 alone would let a node start on a chain that then stops at `bftBlock` | §9.3 | [x] — the guard is removed; `checkBftNode` keeps the PoA-method and snap-sync checks, and `TestPbftNodeStarts` starts a node on a PBFT genesis (P5h) |
| P5-01 | Wrapper engine `metabft.Engine` created in `CreateConsensusEngine` when the chain config has `bftBlock`; below it every call goes to the PoA engine (P5a) | §7.2 | [x] |
| P5-02 | `VerifyHeader` pre-fork: `CommitSeals == nil && BftRound == 0` | §5.2, §5.3 | done in P1-06: the PoA engine enforces it for every height it verifies |
| P5-03 | `VerifyHeader` post-fork: the PoA engine's header checks (`VerifyHeaderPBFT`, which covers the Camellia fields and `ParentBeaconRoot == nil`; `IsCamellia` is guaranteed by `checkBft`), plus `Time >= parent.Time` (P5a) | §5.3 | [x] |
| P5-04 | `VerifyHeader` post-fork: `MinerNodeSig` over `keccak256(number ‖ root)` (the Pangyo form) by `MinerNodeId`, a validator of the parent state, and `Coinbase` that validator's governance coinbase; the PoA engine assembles this form at PBFT heights (P5a) | §5.3 | [x] |
| P5-05 | `VerifyHeader` post-fork: `>= Quorum` distinct seals over `commitDigest(BlockHash, BftRound, ChainID)`, every seal valid (`VerifySeals`, P5a) | §5.3 | [x] |
| P5-06 | PRE-PREPARE time bound `\|Time − localNow\| <= timeDrift` (not applied on sync) | §4.5 | [x] — fresh proposals only: a re-proposal of a prepared block keeps its original `Time`, which a quorum already checked, and the core says which is which (`VerifyProposal(p, fresh)`, P5c) |
| P5-07 | Worker proposer gate via `IsBftProposer` (`miner/worker.go:1666-1681`) | §7.3 | [x] — through the engine instead of a `metaminer` hook: `Engine.ProposalWanted` asks the node, and `ProposalWake` wakes the worker on a request and when `EmptyBlockInterval` passes, so proposals do not wait for the 1s tick; the PoA empty-interval, miner and token gates are skipped at PBFT heights (P5d) |
| P5-08 | Worker hands the block to the BFT core; backend writes on commit | §7.3 | [x] — `Engine.Seal` passes the block to the node (`SubmitBlock`) and the worker does not wait for a result or write the block; `Commit` writes through `Chain.InsertBlock` (P5b, P5c, P5d). Blob sidecars are stored at proposal time under the block hash; getting them to the other validators is P5-19 |
| P5-09 | `LogBlock` / `ReleaseMiningToken` skipped post-fork (`miner/worker.go:1901-1910`) | §7.3 | [x] — the PBFT branch of `commitEx` returns before them (P5d) |
| P5-10 | Proposer timestamp `max(parent.Time, now)`; `timeIt` not used post-fork | §7.3 | [x] — the collection window is `blockCreationTime` capped at `timeDrift/2`, so a block still meets the proposal time bound on arrival; the coinbase is also set before the transactions run, since validators execute with the header's (P5d) |
| P5-11 | Non-proposers validate via `ValidateBody` + `Process` + `ValidateState` | §7.3 | [x] — `metabft.BlockChain.VerifyBlock`, on a copy of the head state; header through `Engine.VerifyProposal` (every import rule but the seals) (P5c) |
| P5-12 | Rewards/Coinbase compared, not overwritten, post-fork (`consensus.go:741, 754`) | §7.5 | [x] — `Rewards` is compared with the distribution from the parent state and `Fees`, at import and on proposals; empty where governance has no distribution (`ErrNotInitialized`). `Coinbase` is the builder's governance coinbase (P5-04): the reward coinbase is overwritten by the signer's in `FinalizeAndAssemble` today, so it is not what the header carries (P5c) |
| P5-13 | Block invalid if post-execution governance node count < 4; proposer drops the offending tx | §9.3.1 | [x] — an unreadable count fails with its own error (`errNodeCountUnreadable`), so a broken registry is not mistaken for a removal. Each validator abandons one build on a breaching transaction when it next proposes, once per retry (30 s, P5-27) while the pool keeps it: expected, not a stall. `metadium.bftRegistry` and `bftValidators` fall back to a local RPC round trip on the import path, so a stalled local RPC stalls import; a bounded context is the follow-up (review on #155). Detail: `Engine.VerifyPostState`, run by `BlockValidator.ValidateState` (import and proposals alike, through `consensus.PostStateVerifier`), reads `getNodeLength` from the post-execution state by EVM calls, finding governance through the registry in that state; no count is a failure. The worker checks after each transaction; since a finished transaction cannot be reverted, a breach abandons the build and the transaction is left out for 30 s, then tried again (P5f; time, not heights, since P5-27) |
| P5-14 | `verifyMinerLimit` skipped post-fork | §4.3 | [x] — the PBFT engine never calls the PoA `verifyBlockSig`, where the limit lives (P5a) |
| P5-15 | `getFinalizedBlockNumber` returns head post-fork (`metadium/admin.go:645`) | §7.4 | [x] — in `BlockChain.metaFinalHeader`, its only caller, from the chain config, so `CurrentFinalBlock`/`CurrentSafeBlock` and the blob limbo see it without the governance lookup (P5e) |
| P5-16 | Reorg below the finalized number rejected in `insertChain` | §5.4 | [x] — in `BlockChain.reorg`, which every insert and `SetCanonical` path goes through: a reorg that would drop a block at a PBFT height fails before anything is written, whatever the new chain's weight (P5e) |
| P5-17 | `acceptUnverifiableBlock` forbidden post-fork | §7.7 | [x] — the PBFT engine reads the set through `BftValidators`, which has no fallback, and refuses a height it cannot read (P5a). Import verifies headers before their parents are executed, so while the parent state is not there yet the signer checks move to `VerifyUncles`, which `ValidateBody` runs once the parent is written; nothing is accepted without them |
| P5-18 | Transition: halt at `BftBlock` if governance missing or `N < 4` | §9.3 | [x] — `GovernanceValidators` returns no set below `MinValidators` nodes, and an unreadable set is no set either, so at `bftBlock` the node proposes and accepts nothing and logs why each retry; the worker builds nothing at PBFT heights without the node (P5h) |
| P5-19 | Blob sidecar fetched before PREPARE; PREPARE held until available | §12 | [x] — `metabft.BlockChain.VerifyBlock` makes sure the node holds every blob sidecar before it votes: from the blob pool, from an earlier fetch, or from peers now, trying every meta/69 peer by the block hash within 2 s (`handler.fetchBlobSidecarsBy`); otherwise the proposal is refused. The proposer serves its sidecars before the block is written (`BlockChain.AddProposalSidecars`, which `GetBlobSidecars` falls back to). On the 4-node network a blob tx committed at a PBFT height and every node holds its sidecar; the peer-fetch path is covered by unit tests only, since the pool delivered the blob everywhere first (P5k) |
| P5-20 | RPCs: `metabft_getValidators`, `_getRoundState`, `_status`, `_readiness`, `_getEvidence`; `Stats` also counts failed `InsertBlock`s (a deterministic failure leaves every validator waiting on a decided height) and messages dropped from the node's queue, so the status RPC shows them (review on #151) | §6, §7.1, §9.3 | [x] — `metabft_getValidators`, `_getRoundState`, `_status` (round state, peers, failed inserts, dropped messages, the last refused proposal and why), `_readiness` (governance, N against the minimum, advertising, blocks left to `bftBlock`) and `_getEvidence` (hex, with the RLP to verify independently); checked on the 4-node network before governance, after it and after the switch (P5j) |
| P5-21 | Startup check: `EmptyBlockInterval >= blockCreationTime` (from P0-05) | §4.5 | [x] — a warning, not a refusal: `blockCreationTime` is governance state, read as heads arrive and logged once per value (P5h) |
| P5-22 | Warn when `--metadium.block.emptyinterval` differs from `bft.emptyBlockInterval`; genesis wins (from P0-07) | §8.1 | [x] — a warning at startup; the worker reads the genesis value at PBFT heights (P5d), the flag stays for the PoA segment (P5h) |
| P5-24 | `metabft.Node`: one event loop owns the core and feeds it messages, submitted blocks, heads and timeouts on the local monotonic clock; idle below `bftBlock`; asks the builder for blocks through `ProposalWanted` (round 0 waits for pending transactions or `EmptyBlockInterval`); retries a height whose validator set cannot be read yet (P5b) | §6, §7.3 | [x] |
| P5-25 | Snap sync refused on a PBFT chain (full sync only): without state, no PBFT header can be verified (P5-17); must land before any deployment, so the failure is an explicit error (review on #150) | §7.7 | [x] — `checkBftNode` refuses `--syncmode snap` at startup with that reason; full is the Metadium default (P5e) |
| P5-23 | Register `metabft/1` in `eth/backend.go` and implement its `Backend` on the node (reviews on #149, #151): call `Cache.Prune(height)` on every commit, since an unpruned cache fills at 65,536 entries and then drops every message (test: a few thousand heights without pruning stall); refuse peers whose node key is not in the current set, or treat `SyncReply` as a hint only, since sync messages are unsigned; a per-peer budget for messages from unknown signers, each of which costs an ecrecover; `Broadcast` through a per-peer send queue with a drop policy, since `p2p.Send` blocks on a slow peer | §7.1 | [x] — `eth.bftService` (P5g): registered in `Protocols()`, started and stopped with the node; the cache is pruned on every head (test: more heights than the cache holds unpruned); `RunPeer` admits only peers whose node key is in the current set; `UnknownSigner` allows 256 per minute per peer, then drops it; `Broadcast` queues 256 per peer and drops beyond. The service opens the WAL (`OpenNodeWAL`) and the evidence store under `<datadir>/metabft`. Not reachable until P5-00 lifts the startup guard |
| P5-26 | The PoA admin loop stops at PBFT heights: its etcd work-record sync check (`metadium/sync.go` `syncCheck`, from the admin loop) still runs after the switch and logs `sync check: ahead of work, aborting` whenever consensus pauses; gate it on `!IsBft(head+1)` or retire the record at `bftBlock` (review on #159) | §7.6 | [x] — the admin loop skips `EtcdStart` and `syncCheck` from `bftBlock` on (`metaminer.IsBft`, set by the eth service); mining and the governance peer mesh continue, and the etcd server stays up for the membership RPCs (P5i). etcd is a bootstrap artefact on a PBFT chain: a node that first starts after `bftBlock` never joins it, and the membership RPCs serve operators of the nodes that were there; the etcd work record stays at the last PoA height (review on #161) |
| P5-27 | A transaction left out by the validator floor holds its sender's later transactions (nonce order) until it is retried after `bftExcludeHeights` (64) and, its ballot over, reverts; replacing that nonce frees them at once. Seen on the network: a member whose floor-breaching vote was left out could not vote on the next ballot before it expired. Decide whether 64 heights, an operator note, or a different treatment (drop from the pool) is right (found by `governance.sh`) | §9.3.1 | [x] — decided: keep the transaction in the pool (dropping it would not free the sender, whose later nonces would then wait on a gap, and other nodes would gossip it back), retry by time instead of heights (`metabft.ExcludeFor`, 30 s: ballots are timed, heights stretch with the block rate), and show it: `metabft_status.excludedTxs` lists hash, sender, nonce, reason and retry time, and the log says that replacing the nonce frees the sender |

**Check:** real block production on 4 local nodes, PoA → PBFT transition included. — passed on `tests/private-net-pbft` (`pbft-test.sh`): switch at `bftBlock`, a quorum of seals on every block, all four proposing, agreement and finality, one node down, two down and back. The run found two bugs, fixed in #158: PoA heights of a batch verified without their in-batch parent (sync of the bootstrap stopped), and PBFT proposals without the PoA seal fields (every proposal failed the mixHash check). Governance's real paths ran there: validator set, rewards comparison, node-count floor.

---

**Test config (review on #158):** the unit tests ran PBFT on a config without Avocado, so neither private-network bug (the in-batch parent, the mixHash) showed there. PBFT tests should use a config with the forks a real private network has from genesis (Avocado, Pangyo, Camellia), so the engine tests enforce the same header rules. Done: the `consensus/metabft` engine and chain tests and the miner's PBFT worker tests run with London, Avocado, Pangyo, Applepie, Bokbunja and Camellia from genesis, and their headers carry the PoA seal fields as real ones do. Without `Engine.Seal`'s sealing step the worker test now fails with the mixHash error the network showed.

## P6: verification on 7 nodes

### Fault injection (design §11.2)

| ID | Scenario | Expected | Status |
|----|----------|----------|--------|
| S-01 | Stop 1 validator | production continues | [x] — N=7 and N=4, `pbft-test.sh` |
| S-02 | Stop 2 validators (= f) | production continues, slower | [x] — N=7: 14 blocks with 2 of 7 stopped; both caught up |
| S-03 | Stop 3 validators (> f) | stops; resumes on recovery; no fork | [x] — N=7: no progress with 3 of 7 down (176 → 176), resumed on recovery, all agree |
| S-04 | Equivocating proposer | no commit, round change, evidence ×2, alarm | [x] — `byzantine.sh`, N=7, pbftfault build: the equivocating proposer's rounds never commit, the chain continues, evidence stored on the 3 peers that received both PRE-PREPAREs, all agree. Since S-13, a new evidence pair is relayed, and the evidence reaches all 7 nodes |
| S-05 | Wrong state root / Rewards / Coinbase | PREPARE refused, round change | [x] — `byzantine.sh`, N=7: proposals with a wrong rewards field refused by the others ("rewards field does not match the reward distribution"), none commits, the chain continues. A wrong state root is caught by `ValidateState` in the same path (unit tests) |
| S-06 | 4:3 partition | both sides stop; resumes on heal; no fork | [x] — N=7, `faults.sh`: 3 validators cut off (isolated from each other too, so 4/1/1/1: Docker bridges cannot overlap and the image has no iptables); the 4 stop, resume on heal, no fork, no evidence. True 4:3 split since: iptables in the image, 3 validators cut off while connected to each other; both sides stop (450 → 450), the minority changing rounds (round 4) without committing; healed, all agree, no evidence |
| S-07 | One node clock +5 min | production continues | [x] — `byzantine.sh`, N=7, pbftfault `clock-ahead` on node2 (a container shares the host clock, so the fault moves the node's clock for its proposals and for the local-clock bound): no block of node2's committed in 21 heights, the chain continues, the others refuse its proposals and it refuses theirs ("too far from the local clock", both directions), it follows the chain by import (0 behind), all agree afterwards |
| S-08 | Add/remove validator via governance | switch at epoch boundary | [x] — `governance.sh`, N=5, real ballots: node5 removed, from the next block 3 seals (quorum of 4) and node5 not proposing; added back, 4 seals (quorum of 5) and node5 proposing, no restart, no round change |
| S-09 | New node joins after snap sync | seals verify, joins consensus | [x] — `faults.sh`, N=7: a node that is not a validator, started after the switch, full-synced 459 blocks from genesis (PoA segment and every commit seal) in about 20 s, on the validators' chain. Snap sync is refused by design (P5-25). The joining node runs the same binary as the validators, so this does not cover sync across versions. A release before PBFT cannot follow the PBFT segment at all, so that check applies only once there are two PBFT-capable releases: run S-09 with the earlier one as the joining node (review on #167) |
| S-10 | Block with removed/forged seals | import rejected | [x] — `import.sh`, N=7: the exported chain with PBFT block 125 rewritten by `tamper`, imported through `admin_importChain` on a fresh node with no peers. Every variant stops the import at 124: one seal short ("not enough commit seals: 4, quorum is 5"), a seal by a key outside the set ("not by a validator"), one validator's seal twice ("two commit seals from one validator"), and the round changed under the seals. The untouched chain imports to the end, on node1's chain. Unit: `TestEngineVerifiesPBFTHeader` |
| S-11 | Kill after PREPARE / after COMMIT, restart | no conflicting vote, lock restored | [x] — N=7, `faults.sh`: SIGKILL of validators in turn under transaction load, three rounds; progress, agreement, no equivocation evidence anywhere. The kill point is not aimed at PREPARE/COMMIT; the simulator covers those exactly (`TestSimAmnesiaAfterCommit`). A deterministic network version would need a debug flag that exits right after the WAL write of a COMMIT (review on #164). A validator restarted in the middle of a height can see that height's proposal after the others have already committed it. By then the proposal's timestamp is past the `timeDrift` bound, so the validator refuses it (§4.5), shows it as `lastRejection` in `metabft_status`, and imports the block through sync. This is by design, not a fault (review on #167) |
| S-12 | Restart with WAL deleted | observer mode, joins after one height | [x] — N=7, `faults.sh`: restarted without its WAL, the node reports observer mode, leaves it after a height, all agree |
| S-13 | Same node key on two servers | evidence + alarm | [x] — `twin.sh`, N=7: node2's data directory (key and WAL) copied to a second server running next to it, first with the peers split between the two on their own, then with an explicit 3/3 split. Found first without relaying: devp2p keeps one connection per node ID, so each validator heard one of the two; they proposed different blocks at round 0 and no node stored evidence (the chain stayed safe: one key is one validator, within f). Validators now relay fresh PREPARE/COMMIT/ROUND-CHANGE once, and both messages of a new evidence pair (design §7.1). With that: one chain on all 7 nodes and the twin, the same evidence against node2 on all 6 other validators at every one of node2's six proposer slots in the run, and both servers log that their key signed two different messages, 6 times each. `TestBftServiceRelaysVotes`, `TestBftServiceTwinKey` |
| S-14 | Proposer `Time` past / +10s | rejected; next height round 0 normal | [x] — `byzantine.sh`, N=7: proposals stamped before the parent or 10 s ahead refused by the local-clock bound, which a fresh proposal meets before the header rules; `Time >= parent.Time` at import is covered by `TestEngineVerifiesPBFTHeader` |
| S-15 | Governance removal down to N = 3 | tx never commits, chain continues | [x] — `governance.sh`: the deciding vote of a ballot to go from 4 to 3 never committed; the chain continued at N=4, and the validators that proposed meanwhile logged `3, minimum 4` (the post-state count read from the real contracts). See P5-27 |
| S-16 | Re-proposed block after round change commits | original `MinerNodeSig`, `BftRound` = commit round, imports | [x] — `byzantine.sh`, N=7: with every round-0 COMMIT withheld at heights divisible by 10, height 230 committed in round 1 with round 0's proposer as builder (the prepared block re-proposed unchanged), imported on every node |
| S-17 | Pre-fork block with arbitrary `CommitSeals` | import rejected | [x] — `import.sh`: block 115 (bftBlock 120) given 5 commit seals stops the import at 114 ("PBFT fields on a header outside PBFT"). Unit: `TestEngineDelegatesBelowSwitch` |

### Transition rehearsal

| ID | Item | Status |
|----|------|--------|
| R-01 | Bootstrap → governance → readiness → `BftBlock` switch, following §9.2 step by step | [x] — `transition.sh`, N=7, bftBlock 120: the genesis fixes chainId, bftBlock and `bft.*` (step 0); full mesh, 6 peers each (step 2; NTP is the host clock here, to be confirmed on servers); `metabft_readiness` ready on all 7 nodes (7 validators, each in the set and advertising metabft/1) (step 3); block 119 PoA without seals, block 120 with 5 seals, committed in round 0 and built by `validators[120 % 7]`, all 7 agree (step 4) |
| R-02 | Transition failure (`N < 4` at `BftBlock-1`) halts with a clear error; genesis rebuild recovers | [x] — `transition.sh`: 7 nodes, governance with 3 members (`deploy.sh MEMBERS=3`), bftBlock 80. Readiness on every node reports too few governance nodes beforehand. Every node stops at 79 for 60 s instead of continuing on PoA, with the reason logged: "No validator set; not participating … 3 governance nodes at block 79, PBFT needs 4". The recovery is the R-01 run on a new genesis |

### Performance (design §11.3)

| ID | Measurement | Target | Result | Status |
|----|-------------|--------|--------|--------|
| M-01 | Confirmation latency, N=7 LAN (`idleseal=100`) | p99 < 300ms (PoA baseline p99 130ms) | p50 198 / p99 220 ms (N=7, idleseal 100, `measure.py`; 1121 ms before #165). With vote relaying (S-13): p50 205 / p99 235 ms, M-02 5.10 s, M-03 863 tx/s with 0 round changes. The worker fixes behind it (#165) have no unit test: rerun `measure.py` against the M-01 and M-02 targets on every worker change. With the timers and M-04 pacing: p50 206 / p99 229 ms | [x] |
| M-02 | Idle empty-block interval | `EmptyBlockInterval` ± 10%, 0 round changes | 5.09 s mean over 10 intervals, 0 round changes (6.09 s before #165) | [x] |
| M-03 | Round changes under load (several blocks/s) | 0 | 0 blocks above round 0 in 60 s at ~900 transfers/s | [x] |
| M-04 | Fixed-interval profile (`blockCreationTime = 2000`, no idleseal) | 2.0s interval holds | Before: blocks about 1.1 s after the parent (the build window is capped at `timeDrift/2`). Pacing counted from the commit gave 2.10 s with light load and 2.77 s under load. Now counted from the parent's proposal (design §4.5): one transfer at a time p50 2016 / p99 2036 ms, 2.03 s mean under load (881 tx/s, 0 round changes), M-02 5.10 s. PoA on the same network and load: 1.91 s, 804 tx/s. With idleseal 100 unchanged: p50 206 / p99 229 ms. `TestNodePacesRound0` | [x] |
| M-05 | WAL fsync cost per block | recorded, included in M-01 | `metabft/wal/sync` (`measure.py --metrics`): 6.3 ms mean, p99 16.7 ms per record, about 3 records per validator per height. Two of them (PREPARE, COMMIT) are on the critical path: about 13 ms of the 206 ms M-01 | [x] |
| M-06 | TPS vs Camellia (`scripts/rpc-test-full.sh`, `mixed-tx-e2e`) | no regression beyond agreed margin | Value transfers (`measure.py`): 867–886 tx/s PBFT against 804 tx/s PoA (fixed 2 s) on the same 7-node network. `rpc-test-full.sh` on a PBFT validator: 63 PASS / 0 FAIL / 1 WARN / 3 SKIP, against 64 / 0 / 0 / 3 in the Camellia report. The WARN is the script comparing a balance above 2^63 wei with bash `-gt` (the genesis funds each account with 409,600 META); the balance itself is correct. SKIPs as before (eth-account not installed, no IPC path). `mixed-tx-e2e` and `blob-tx-e2e`: ALL PASS | [x] |
| M-07 | Validator execution cost: each block runs twice (proposal check, then import); if it dominates M-01, keep the processed state for the import (review on #152) | recorded | The proposal check (`metabft/proposal/verify`) has mean 99 ms, p99 447 ms per proposal over the run. Its execution is 84 ms of that, and it is the cold pass. The import of the decided block (`chain/inserts`) has mean 11.5 ms, execution 8.4 ms, since the check has warmed the caches. At M-01 (one-transfer blocks) the check is a few ms. Keeping the state would save only the ~11 ms import, so this is not done | [x] |
| M-08 | Full-sync speed: `Engine.VerifyHeaders` checks a batch sequentially on one goroutine (review on #150) | recorded | `syncspeed.sh`: a fresh node imports node1's chain through `admin_importChain`. The PoA segment (119 blocks) imports at 168 blocks/s. The PBFT segment (167 blocks, 52,368 transfers) imports at 11,250 transfers/s, over 12× production throughput. Execution dominates; the commit seals are 5 signature recoveries per block | [x] |
| M-09 | Proposer cost of the validator-floor check: a fresh EVM and two static calls after every transaction at a PBFT height; if it shows in M-01, look the registry up once per build and call only `getNodeLength` per transaction (review on #155) | recorded | `miner/bft/floorcheck`: 0.09 ms mean, p99 0.15 ms per transaction. At M-01 that is 0.09 ms per block, so it does not show. Under load it is about 150–200 ms of build time for a 2,000-transfer block. The registry lookup once per build is the known fix if a profile needs the throughput | [x] |
| M-10 | Sidecar fetch before PREPARE: a full-blob block fetched within `sidecarWait` (2 s) on the event loop, with validators across regions; if it falls short, scale the wait with the blob count or make it a setting, and remember peers that answer empty for the height (review on #163) | recorded | `sidecar.sh`, N=7, 5 running so the quorum needs node3. node3 runs pbftfault `sidecar-fetch` (it disregards its pool), so it fetches every blob block's sidecars. Its links are slowed both ways with tc netem. A full-blob block on Metadium is 2 blobs, 2 × 128 KiB (`MaxBlobGasPerBlock`), not 6. **The run found a bug first:** the proposer recorded its sidecars under the unsealed block hash, and sealing sets a random PoA nonce, so no validator could fetch them from it. At 25 ms/1 Gbit, height 178 stalled for 14 rounds ("no meta/69 peer has the blob sidecars"). Fixed: `Engine.SealProposal`/`SubmitProposal`, with the sidecars recorded under the sealed hash before the proposal goes out. Validators that find the sidecars in their pool also record them under the block hash, so every validator can serve them, re-proposals included. `TestWorkerRecordsSidecarsUnderSealedHash`, `TestBlockChainSidecars`. After the fix, mean / max fetch time per one-way delay / rate: 25 ms/1 Gbit 216/259 ms, 50 ms/100 Mbit 332/461 ms, 100 ms/20 Mbit 774/888 ms, 150 ms/5 Mbit 1591/1854 ms. Every blob block committed, and no proposal was refused for its sidecars. A quorum-critical validator on a link slower than about 5 Mbit at 300 ms RTT would miss the 2 s wait. That is where to scale the wait or make it a setting | [x] |
