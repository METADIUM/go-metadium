# Camellia Fork Test Report — m1.1.1 Release Verification

**Date:** 2026-08-11 ~ 2026-08-12
**Binary under test:** `a91d1b323` (master tip, m1.1.1), `21c1997e6` (PR #77 candidate), `a8d626726` (final release tree after the #77 merge; see §5), and `e6a9f31ce` (that tree plus this report — the binary deployed fleet-wide in §4.1)
**Author:** Jeffrey Song

This report re-validates every functional axis of the original Camellia test
report (`camellia-test-report.md`, 2026-04-08) against the release-line
binary, and extends coverage to axes the original did not have: the restored
256KB txpool ceiling (#64), pool-side TRS enforcement (#67), fee-delegation
eviction (#67), governance-driven multi-BP rotation, mixed-version fork
divergence, and the blobpool PoA finality fix (#76/#77).

## 1. Environments

| Environment | Purpose |
|---|---|
| 3-node PoA private net (Docker, chainId 1337, Camellia @ block 100, genesis gasLimit pinned to the production 105,000,000) | All functional suites |
| 2-node mixed-version net (release binary + production `0.10.2-stable/ce4a95e93`) | Fork-boundary divergence |
| Production followers (46/47/183, 5 services) + official testnet 5 nodes | Live deployment, soak, lockstep |
| Fresh full syncs: testnet (embedded genesis) and mainnet (embedded genesis) | #68 embedded-genesis validation, in progress at time of writing |

## 2. Regression Suites (all on the #77 candidate binary)

| Suite | Result |
|---|---|
| `camellia-test.sh` (EIP transitions, block 99 vs 100) | **PASS 14 / FAIL 0 / SKIP 3** (skips environmental, unchanged from v1) |
| `camellia-contract-e2e` (solc 0.8.28 cancun: PUSH0/MCOPY/TSTORE) | ALL PASS |
| `blob-tx-e2e` (EIP-4844 lifecycle + sidecar) | ALL PASS |
| `mixed-tx-e2e` (Type 2 + Type 22 + Type 3 in one flow) | ALL PASS |
| `rpc-test-full.sh` (Execution API surface) | **64 PASS / 0 FAIL / 0 WARN / 3 SKIP** |
| `bigtx-e2e` (new, see §3) | ALL PASS |

## 3. New Coverage

### 3.1 256KB txpool ceiling (`tests/private-net-poa/bigtx-e2e`, #64)
- 262,248-byte tx rejected: `oversized data: 256.10 KiB, limit 262144`
- 254,056-byte max-code deployment admitted, **propagated to peer pools
  pre-mining** (pending=true observed on both non-miner nodes), mined with
  status 1; deployed code reads back at exactly 253,952 bytes
- gasUsed **52,045,896** against the production block gas limit
  **105,000,000** — roughly 2x headroom

### 3.2 Fee-delegation eviction (`tests/private-net-poa/feepayer-evict-e2e`, #67)
A queued Type-22 tx whose fee payer is drained to zero after admission is
evicted by the promote/demote sweep within seconds. 3/3 PASS.

### 3.3 Governance Phase 2 — multi-BP rotation
`deploy.sh` on a fresh chain: Registry at the deployer's nonce-0 CREATE
address, etcd initialized first attempt, node2/node3 registered, and all
three sealers observed rotating block production (15-block sample: 3/7/5).

> Operational note learned the hard way: registry discovery scans the genesis
> coinbase's CREATE addresses for nonces 0–9, so governance must be deployed
> before that account sends other transactions.

### 3.4 TRS pool-side enforcement (#67)
- `addToTRSList(victim)` alone does **not** activate enforcement — a node
  enforces only after its governance address calls `TRSList.subscribe()`
  (per-node opt-in, matching 0.10.x semantics)
- Once subscribed: transactions **from** and **to** the restricted address
  are both rejected at admission with `included in the TRSList`
- Follow-up for the mainnet runbook: verify BP subscription state after the
  rolling upgrade; an unsubscribed node silently skips enforcement

### 3.5 Sync-mode policy (docs/#69 claim, verified on the binary)
`--syncmode snap` and `--syncmode light` fail fast with the documented
full-only message; `fast` is rejected by the flag parser.

### 3.6 Mixed-version fork divergence (production 0.10.2 binary)
A real `0.10.2-stable/ce4a95e93` node (taken from the mainnet fleet) peered
with a release-binary miner on a fresh Camellia@100 chain:
- pre-fork: peers and syncs normally (0 → 99)
- at activation: imports **exactly through block 99 and stops** — no crash,
  no BAD BLOCK; peer count drops to 0 (fork-id divergence) and the node
  idles logging `Looking for peers`
This is the expected behavior for any operator who has not upgraded when
mainnet reaches 117,764,000.

### 3.7 Blobpool PoA finality (#76 → #77)
Before the fix, every Camellia-active node logged `Nil finalized block cannot
evict old blobs` once per block (live on testnet after the 1.1.1 rollout;
7,189 lines in one follower's log) and included blob sidecars never left the
limbo store. With the #77 candidate binary: zero occurrences across all three
nodes (including with pre-existing limbo entries on disk), fresh blob e2e
unaffected, and 64-plus blocks of post-eviction silence.

## 4. Live Deployments (a91d1b323)

| Fleet | Result |
|---|---|
| Followers: 46 (LevelDB), 47 (RocksDB), 183 (archive) — 5 services | Graceful swaps 0–7s, fork banners correct, lockstep with official networks, zero errors over soak |
| Official testnet: bp1/bp2/bp3/api01/exp01 | Rolling deploy, one node at a time; etcd leader handoff observed (`moving leader to meta1`); sealer rotation intact; block production uninterrupted |

Fresh full syncs from embedded genesis (the #68 path) reproduce the official
genesis hashes on both networks (testnet `0x10c1b0a5…`, mainnet
`0xf1b2a543…`) and are progressing past the historical trouble spots at the
time of writing.

### 4.1 Final-tree deployment (`e6a9f31ce`)

Before the promotion was merged, the final tree was deployed to **every node we
operate** and verified in place. All twelve instances report
`1.1.1-stable-e6a9f31c`:

| Target | Result |
|---|---|
| 46 — mainnet + testnet services (LevelDB) | Graceful restarts 5s / 6s; fork banners correct (#117764000 / #86449000); static peers re-attached (3 / 5); zero errors |
| 47 — mainnet service (RocksDB) | Restart 4s, peers 3, head advancing, zero errors |
| 47 — testnet service (RocksDB) | Restart after abandoning a long-running `debug_setHead` experiment: stop 301s, resumed cleanly at the rewound head (89,826,500) with no deeper state rollback, then re-aligned its header chain |
| 183 — archive (LevelDB) | Restart 1s, `debug_traceBlockByNumber` serving, head advancing |
| Fresh-sync instances (testnet + mainnet, embedded genesis) | Restarted onto the final binary, peers re-attached, syncing continues |
| **Official testnet: api01, exp01, bp2, bp1, bp3** | Rolling deploy one node at a time, per-node engine verified against `chaindata` before extraction (`.sst`/`.ldb`) and tarball md5 checked. Stops 1–7s; every node passed a block-progression gate before the next was touched; head stayed in lockstep across the fleet throughout; **sealer rotation even afterwards** (30-block sample 11/6/13, independent 20-block sample 6/8/6) — block production never paused |

### 4.2 Live 254KB deployment on the official testnet (#64 acceptance)

Executed against the deployed fleet (api01 for submission, bp2 and bp3 for
propagation), on the final binary, with a freshly funded account:

- 262,246-byte creation tx **rejected**: `oversized data: transaction size 256.10 KiB, limit 262144`
- 254,054-byte max-code creation tx **accepted**, then observed `pending` in **both** sealers' pools (propagation, not block-relay)
- **mined in block 90,079,900, status 1, gasUsed 52,045,896** against the live block gas limit **105,000,000**
- deployed code reads back at exactly **253,952 bytes** (independently re-queried on api01)

This closes the one verification the release had outstanding: the 256KB ceiling
now has a production-network measurement, not only private-net evidence.

The published m1.1.1 assets are rebuilt from the master tip after the
promotion merges; that binary carries the same tree as `e6a9f31ce`.

## 5. Final-tree re-verification (`a8d626726`)

After #77 merged, the entire private-net verification was repeated **from a
full reset** on a binary built at `a8d626726` — the dev tip whose tree is
bit-identical to the dev→master promotion (#80). Every axis reproduced the
candidate-binary results:

| Axis | Result |
|---|---|
| `camellia-test.sh` | PASS 14 / FAIL 0 / SKIP 3 (identical to §2) |
| `camellia-contract-e2e`, `blob-tx-e2e`, `mixed-tx-e2e` | ALL PASS |
| `bigtx-e2e` | 4/4 — 253,952-byte code deposit, gasUsed 52,045,896 under the production 105,000,000 limit |
| `rpc-test-full.sh` | Final: PASS — no FAILs |
| `feepayer-evict-e2e` | 3/3 PASS |
| Blobpool finality (#77 acceptance) | `Nil finalized` **0** and eviction failures **0** on all three nodes, measured over 70-plus-block windows both **before and after** governance activation |
| Governance Phase 2 | Registry discovered, `metadiumInfo` self/nodes populated, etcd up, all three sealers rotating (24-block sample: 18/3/3) |
| TRS live cycle | subscribe → `addToTRSList` → admission rejected with `included in the TRSList` on the subscribed node; the same signed tx accepted by an unsubscribed node (opt-in confirmed); `removeFromTRSList` → admission accepted and mined |
| Mixed-version divergence | Re-run with the final binary: identical halt at block 99 + peer drop; the release node continued past 345 |
| Protocol negotiation | Observed via `admin.peers` caps: 0.10.2 advertises `meta/65, meta/66`; the release binary advertises `meta/66, meta/68, meta/69`; the session settles on the shared `meta/66` |

Note that the governance-less phase of this harness is itself the #77 target
case: chain-level finality never resolves there (permanent fallback window),
so the zero-error result directly exercises the surrogate path, while the
fleet measurement recorded on #77 covers the steady-state path
(`finalized`/`safe` returning real blocks).

Two harness constraints were root-caused during this run and are recorded
for future re-runs (they are measurement artifacts, not binary defects):

1. **Suite ordering.** Registry discovery scans the genesis coinbase's
   CREATE addresses at nonces 0–9 (§3.3). The regression suites spend that
   account's nonces, so on any chain that will host governance, `deploy.sh`
   must run before the suites; conversely the two measurements below must
   run before governance activates.
2. **Single-sealer assumptions.** The `camellia-test.sh` Warm-COINBASE probe
   calls with a fixed `from` that equals another sealer's coinbase, so under
   rotation EIP-2929 pre-warming can make blocks 99 and 100 measure equal;
   and the `mixed-tx-e2e` fee-payer check assumes the fee payer is not also
   a block-reward recipient. Both self-invalidate under active rotation and
   pass in the pre-governance phase, which is how §2 and this section ran
   them.

## 6. Outstanding at time of writing

One axis is still running rather than complete: the **fresh full sync of the
testnet through its historical Camellia boundary** (block 86,449,000) on the
release binary. The instance is progressing from genesis and will need about
ten more days to reach that height, so it is tracked as a soak observation
rather than a release gate. Two independent results already cover the same
behavior: the phase-2 binary completed exactly this sync (genesis → fork →
tip, fork-block hash matching canonical, zero BAD BLOCKs) on 2026-06-23, and
the final tree crosses an activation boundary cleanly on every private-net run
in §5. Should the soak surface anything, it lands well before the mainnet
rolling upgrade.

## 7. Verdict

Every axis of the April report passes on the release binary, and the new
axes (256KB ceiling, TRS enforcement, fee-payer eviction, governance
rotation, mixed-version divergence, blobpool finality) pass as well. The one
code change this round of testing produced is PR #77 — and §5 re-verifies
the complete matrix on the exact tree being promoted to master, while §4.1–4.2
show that tree running on every node we operate, including a live 254KB
deployment on the official testnet. Everything else verified clean.
