# Camellia Fork Test Report — m1.1.1 Release Verification

**Date:** 2026-08-11 ~ 2026-08-12
**Binary under test:** `a91d1b323` (master tip, m1.1.1) and `21c1997e6` (PR #77 candidate = `a91d1b323` + blobpool PoA finality fix)
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
| Production followers (46/47/mykeepin, 5 services) + official testnet 5 nodes | Live deployment, soak, lockstep |
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
| Followers: 46 (LevelDB), 47 (RocksDB), mykeepin (archive) — 5 services | Graceful swaps 0–7s, fork banners correct, lockstep with official networks, zero errors over soak |
| Official testnet: bp1/bp2/bp3/api01/exp01 | Rolling deploy, one node at a time; etcd leader handoff observed (`moving leader to meta1`); sealer rotation intact; block production uninterrupted |

Fresh full syncs from embedded genesis (the #68 path) reproduce the official
genesis hashes on both networks (testnet `0x10c1b0a5…`, mainnet
`0xf1b2a543…`) and are progressing past the historical trouble spots at the
time of writing.

## 5. Verdict

Every axis of the April report passes on the release binary, and the new
axes (256KB ceiling, TRS enforcement, fee-payer eviction, governance
rotation, mixed-version divergence, blobpool finality) pass as well. The one
code change this round of testing produced is PR #77; everything else
verified clean.
