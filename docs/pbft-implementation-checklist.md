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
| G-02 | Operations sign-off on the availability trade-off (a 4:3 split halts; downtime figures) | §9.6, §11.2 #6 | [ ] |
| G-03 | `tests/private-net-poa` extended from 3 to 7 nodes (node4–node7, ports 8548–8551) | §11.1 | [ ] |
| G-04 | Baseline on 7 nodes: `camellia-test.sh`, `blob-tx-e2e`, `mixed-tx-e2e` pass before any PBFT change | §11.1 | [ ] |

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
| P0-08 | `init` warns on chain ID `11`, `12` or a known public-registry value | §9.5 | [ ] P0b |
| P0-09 | `metadium/scripts/genesis-template.json`: `chainId` becomes a placeholder; `init` refuses it unfilled | §9.5 | [ ] P0b |

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
| P4-01 | `p2p.Protocol{Name: "metabft", Version: 1, Length: 8}`, validators only | §7.1 | [ ] |
| P4-02 | Registered in `Protocols()` in `eth/backend.go` | §7.1 | [ ] |
| P4-03 | Message codes `0x00`–`0x05` incl. `SyncRequestMsg`/`SyncReplyMsg` | §7.1, §7.7 | [ ] |
| P4-04 | Order: verify signature → check validator → cache lookup | §7.1 | [ ] |
| P4-05 | Cache `(sender, height, round, type)` → first digest; different digest → evidence + alarm | §7.1 | [ ] |
| P4-06 | Cache pruned on commit | §7.1 | [ ] |

**Tests**

| ID | Test | Status |
|----|------|--------|
| P4-T1 | Two-node message round trip | [ ] |
| P4-T2 | Forged signature rejected before it reaches the cache | [ ] |
| P4-T3 | Forged sender cannot pre-empt a genuine message | [ ] |
| P4-T4 | Two digests from one sender → evidence stored, first message still counted | [ ] |
| P4-T5 | Non-validator peers do not negotiate `metabft/1` | [ ] |

---

## P5: integration

| ID | Item | Design | Status |
|----|------|--------|--------|
| P5-00 | Remove the `errBftNotImplemented` startup guard (`eth/bft_guard.go`) in the same change that lands P5-01 | §9.3 | [ ] |
| P5-01 | Wrapper engine `metabft.Engine` created in `CreateConsensusEngine` when the chain config has `bftBlock`; below it every call goes to the PoA engine (P5a) | §7.2 | [x] |
| P5-02 | `VerifyHeader` pre-fork: `CommitSeals == nil && BftRound == 0` | §5.2, §5.3 | done in P1-06: the PoA engine enforces it for every height it verifies |
| P5-03 | `VerifyHeader` post-fork: the PoA engine's header checks (`VerifyHeaderPBFT`, which covers the Camellia fields and `ParentBeaconRoot == nil`; `IsCamellia` is guaranteed by `checkBft`), plus `Time >= parent.Time` (P5a) | §5.3 | [x] |
| P5-04 | `VerifyHeader` post-fork: `MinerNodeSig` over the state root by `MinerNodeId`, a validator of the parent state (P5a) | §5.3 | [x] |
| P5-05 | `VerifyHeader` post-fork: `>= Quorum` distinct seals over `commitDigest(BlockHash, BftRound, ChainID)`, every seal valid (`VerifySeals`, P5a) | §5.3 | [x] |
| P5-06 | PRE-PREPARE time bound `\|Time − localNow\| <= timeDrift` (not applied on sync) | §4.5 | [ ] |
| P5-07 | Worker proposer gate via `IsBftProposer` (`miner/worker.go:1666-1681`) | §7.3 | [ ] |
| P5-08 | Worker hands the block to the BFT core; backend writes on commit | §7.3 | [ ] |
| P5-09 | `LogBlock` / `ReleaseMiningToken` skipped post-fork (`miner/worker.go:1901-1910`) | §7.3 | [ ] |
| P5-10 | Proposer timestamp `max(parent.Time, now)`; `timeIt` not used post-fork | §7.3 | [ ] |
| P5-11 | Non-proposers validate via `ValidateBody` + `Process` + `ValidateState` | §7.3 | [ ] |
| P5-12 | Rewards/Coinbase compared, not overwritten, post-fork (`consensus.go:741, 754`) | §7.5 | [ ] |
| P5-13 | Block invalid if post-execution governance node count < 4; proposer drops the offending tx | §9.3.1 | [ ] |
| P5-14 | `verifyMinerLimit` skipped post-fork | §4.3 | [x] — the PBFT engine never calls the PoA `verifyBlockSig`, where the limit lives (P5a) |
| P5-15 | `getFinalizedBlockNumber` returns head post-fork (`metadium/admin.go:645`) | §7.4 | [ ] |
| P5-16 | Reorg below the finalized number rejected in `insertChain` | §5.4 | [ ] |
| P5-17 | `acceptUnverifiableBlock` forbidden post-fork | §7.7 | [x] — the PBFT engine reads the set through `BftValidators`, which has no fallback, and refuses a height it cannot read (P5a). Import verifies headers before their parents are executed, so while the parent state is not there yet the signer checks move to `VerifyUncles`, which `ValidateBody` runs once the parent is written; nothing is accepted without them |
| P5-18 | Transition: halt at `BftBlock` if governance missing or `N < 4` | §9.3 | [ ] |
| P5-19 | Blob sidecar fetched before PREPARE; PREPARE held until available | §12 | [ ] |
| P5-20 | RPCs: `metabft_getValidators`, `_getRoundState`, `_status`, `_readiness`, `_getEvidence` | §6, §7.1, §9.3 | [ ] |
| P5-21 | Startup check: `EmptyBlockInterval >= blockCreationTime` (from P0-05) | §4.5 | [ ] |
| P5-22 | Warn when `--metadium.block.emptyinterval` differs from `bft.emptyBlockInterval`; genesis wins (from P0-07) | §8.1 | [ ] |
| P5-25 | Snap sync refused on a PBFT chain (full sync only): without state, no PBFT header can be verified (P5-17) | §7.7 | [ ] |

**Check:** real block production on 4 local nodes, PoA → PBFT transition included.

---

## P6: verification on 7 nodes

### Fault injection (design §11.2)

| ID | Scenario | Expected | Status |
|----|----------|----------|--------|
| S-01 | Stop 1 validator | production continues | [ ] |
| S-02 | Stop 2 validators (= f) | production continues, slower | [ ] |
| S-03 | Stop 3 validators (> f) | stops; resumes on recovery; no fork | [ ] |
| S-04 | Equivocating proposer | no commit, round change, evidence ×2, alarm | [ ] |
| S-05 | Wrong state root / Rewards / Coinbase | PREPARE refused, round change | [ ] |
| S-06 | 4:3 partition | both sides stop; resumes on heal; no fork | [ ] |
| S-07 | One node clock +5 min | production continues | [ ] |
| S-08 | Add/remove validator via governance | switch at epoch boundary | [ ] |
| S-09 | New node joins after snap sync | seals verify, joins consensus | [ ] |
| S-10 | Block with removed/forged seals | import rejected | [ ] |
| S-11 | Kill after PREPARE / after COMMIT, restart | no conflicting vote, lock restored | [ ] |
| S-12 | Restart with WAL deleted | observer mode, joins after one height | [ ] |
| S-13 | Same node key on two servers | evidence + alarm | [ ] |
| S-14 | Proposer `Time` past / +10s | rejected; next height round 0 normal | [ ] |
| S-15 | Governance removal down to N = 3 | tx never commits, chain continues | [ ] |
| S-16 | Re-proposed block after round change commits | original `MinerNodeSig`, `BftRound` = commit round, imports | [ ] |
| S-17 | Pre-fork block with arbitrary `CommitSeals` | import rejected | [ ] |

### Transition rehearsal

| ID | Item | Status |
|----|------|--------|
| R-01 | Bootstrap → governance → readiness → `BftBlock` switch, following §9.2 step by step | [ ] |
| R-02 | Transition failure (`N < 4` at `BftBlock-1`) halts with a clear error; genesis rebuild recovers | [ ] |

### Performance (design §11.3)

| ID | Measurement | Target | Result | Status |
|----|-------------|--------|--------|--------|
| M-01 | Confirmation latency, N=7 LAN (`idleseal=100`) | p99 < 300ms (PoA baseline p99 130ms) | | [ ] |
| M-02 | Idle empty-block interval | `EmptyBlockInterval` ± 10%, 0 round changes | | [ ] |
| M-03 | Round changes under load (several blocks/s) | 0 | | [ ] |
| M-04 | Fixed-interval profile (`blockCreationTime = 2000`, no idleseal) | 2.0s interval holds | | [ ] |
| M-05 | WAL fsync cost per block | recorded, included in M-01 | | [ ] |
| M-06 | TPS vs Camellia (`scripts/rpc-test-full.sh`, `mixed-tx-e2e`) | no regression beyond agreed margin | | [ ] |
