# Block 18 Reward Distribution Race — Resolved

**Branch:** `fix/block18-testnet-reward-pin`
**Base:** `fix/pre-mainnet-security` (`c15827520`)
**Date:** 2026-06-16
**Author:** Jeffrey Song
**Status:** Resolved — testnet block 18 pinned (commit `766c918eb`); see Resolution

---

## Summary

On a **fresh-genesis full clean-sync**, the testnet chain intermittently failed
to import **block 18** with an `invalid merkle root` error. The failure was
**non-deterministic**: roughly 40% of clean-sync attempts failed, the rest
passed. A 10x clean-sync run reproduced approximately **6 PASS / 4 FAIL**,
confirming a race rather than a deterministic divergence.

This was **not a regression**. The reward-related code on `develop` and on
`c15827520` is byte-identical; the same race existed on both.

The root cause is a race in consensus-level reward calculation. Because the two
networks' canonical block 18 differ on the exact decision involved (see below),
the fix is network-gated rather than a global rule.

---

## Symptom

- Phase: fresh-genesis **full** clean-sync (no snapshot, no pre-existing DB).
- Block: **18**, always the same block.
- Error: `invalid merkle root (remote 599eae… / local 0fa341…)`.
- Blocks 1–17 imported and matched canonical state roots. Block 18's transaction
  executed identically to canonical (`gasUsed` matched); only the
  **consensus-level reward / fee distribution** diverged.
- More reproducible on **RocksDB** than LevelDB, because RocksDB's asynchronous
  batch commit widens the race window.

---

## Root cause

Reward calculation for block N reads the **parent (block N-1) governance state**
to decide how fees are distributed. In the legacy path
(`metadium/legacy.go calculateRewardsLegacy`) this read is
`getRewardAccountsLegacy(num-1)`, performed via an **in-process `eth_call` on a
fresh statedb** that races with block 17's asynchronous trie commit/flush:

- Canonical (correct) read: `{rewardPool=nil, maintenance=nil, members=0}` →
  `ErrNotInitialized` → fees go to the **coinbase** → state root `599eae`.
- Racy read: `{rewardPool=nil, maintenance=0x6d4685…, members=0}` — the
  maintenance account registered by a block 17 transaction is observed → fees
  are **distributed** to it → state root `0fa341` ≠ canonical.

The racy read returns **success with a stale value**, so an error-based retry
cannot catch it. The existing `metaminer.TrieAccessMu` mutex does not close the
gap: `InsertChain` is sequential, so block 17's `writeBlockWithState` has already
released the writer lock by the time block 18's reward read runs, and RocksDB's
async flush/visibility lag happens outside the lock.

---

## Why the fix is network-gated, not a global rule

The two chains' canonical histories are split on this exact decision:

- **testnet** canonical block 18: fees go to **coinbase** (`header.Rewards = "0x"`,
  state root `599eae`, miner = genesis coinbase `0x378360d4…`).
- **mainnet** canonical block 18: fees are **distributed** (maintenance path).

The governance code is the same on both networks. At original mining time the
race landed on opposite outcomes on each chain, and each is now frozen into that
chain's canonical history. **No single deterministic rule reproduces both.**

> A general rule such as *"if `rewardPool == nil` treat as `ErrNotInitialized`"*
> makes testnet deterministic but **breaks mainnet block 18**, where
> distribution is canonical. This was attempted and reverted — do not reintroduce
> it.

---

## Resolution

Pin testnet block 18 to its canonical coinbase outcome, mirroring the existing
`handleBlock94Rewards` special case (commit `766c918eb`):

- `metaAdmin` caches the **genesis hash** (`getGenesisInfo`).
- At the top of `calculateRewardsLegacy`, before the racy governance read:

  ```go
  if num.Int64() == 18 && ma.genesisHash == params.MetadiumTestnetGenesisHash {
      return nil, nil, metaminer.ErrNotInitialized
  }
  ```

Properties:
- **Race-immune** — the pin does not depend on the governance read, so block 18's
  outcome is deterministic regardless of trie-commit timing.
- **Mainnet-safe** — gated on `params.MetadiumTestnetGenesisHash`, so it can never
  trigger on mainnet (different genesis), where block 18 distribution is
  canonical and continues to compute normally.
- **Minimal** — one block, one network; all other blocks and the new (non-legacy)
  path are untouched.

### Validation

On server 47 (RocksDB), fresh-genesis full clean-sync repeated **5×** with the
fix: **5 PASS / 0 FAIL**, block 18 state root = canonical
`0x599eaebad893d28adb480e1d1a015251ea24ca2b46e0eb52dac7f2dbba8cf861` every time
(vs ~40–60% failure before). Sync continues past block 18 normally.

---

## Impact (pre-fix)

Low. Affected only a **fresh-genesis full clean-sync**, at exactly **block 18**.
Not affected: normal operation, block production, nodes bootstrapped from a
snapshot or existing DB, or any block other than 18. Mainnet nodes are not stood
up via fresh-genesis full clean-sync, so there was no meaningful operational
exposure; the fix removes the residual testnet flakiness.

---

## Notes for mainnet

The same race can in principle affect a mainnet fresh-genesis full clean-sync at
block 18 (computing coinbase when distribution is canonical). Mainnet is not
bootstrapped that way in practice, and this fix deliberately does **not** pin
mainnet block 18 (its canonical value depends on governance params, not a fixed
coinbase). A general deterministic-committed-parent-read change remains a
possible future hardening, but is out of scope here and was intentionally not
bundled into a pre-launch consensus change.
