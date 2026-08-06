# Camellia Mainnet Hard Fork — Activation Plan

**Status:** confirmed — fork block `117,764,000`, activating 2026-08-27 12:00 KST
**Date:** 2026-08-03 (decision recorded 2026-08-06)
**Target binary:** `v1.1.0-stable` (`e3f51b3e6`) **plus the shutdown deadlock fix** — see
[Shutdown behaviour](#shutdown-behaviour). `e3f51b3e6` on its own must not be rolled out.

## Current state

| Network | `CamelliaBlock` | Notes |
|---|---|---|
| Mainnet | `117,764,000` | activates 2026-08-27 12:00 KST (`params/config.go`) |
| Testnet | `86,449,000` | activated 2026-05-20 12:00 KST, ~2.5 months with no incident |

## Choosing the fork block

Measured 2026-08-03 13:04 KST: head **116,728,688**, block time **2.000 s** (identical over the
last 1k / 10k / 100k blocks), **43,200 blocks/day**.

| Activation (12:00 KST) | Fork block | Notice deadline (D-14) |
|---|---|---|
| 2026-08-20 | 117,461,000 | 2026-08-06 |
| 2026-08-25 | 117,677,000 | 2026-08-11 |
| **2026-08-27** | **117,764,000** | **2026-08-13** |
| 2026-08-31 | 117,936,000 | 2026-08-17 |
| 2026-09-03 | 118,066,000 | 2026-08-20 |

Conventions, both following the testnet precedent: round the fork block to the nearest
**1,000**, and pick an activation time of **12:00 KST**.

**Confirmed: `117,764,000` (2026-08-27 12:00 KST)** — leaves a two-week window for
exchange notice while keeping enough lead time to build, stage and roll out.

Re-measured 2026-08-06 14:51 KST against a synced node: head **116,861,511**, block time
**2.000014 s** over the last 100k / 500k / 1M blocks. The exact block for 12:00 KST is
`117,763,549`; rounded to the nearest 1,000 the fork lands at roughly **12:15 KST**, which
keeps activation inside working hours.

## Code change

Activating the fork is a single line:

```go
// params/config.go — MetadiumMainnetChainConfig
- CamelliaBlock:       nil, // Not yet activated on mainnet
+ CamelliaBlock:       big.NewInt(117_764_000), // 2026-08-27 12:00 KST mainnet activation
```

### No genesis redistribution is required

Mainnet nodes start without an explicit genesis, so `SetupGenesisBlockWithOverride` resolves
the config through `configOrDefault(MetadiumMainnetGenesisHash)`, which returns the hardcoded
`MetadiumMainnetChainConfig`. That value takes precedence over the config stored in the
database — the branch that keeps the stored config (`core/genesis.go`, the
`genesis == nil && stored != …` case) explicitly excludes the Metadium mainnet genesis hash.

**Swapping the binary is enough to schedule the fork.**

### Deploying before the fork block is safe

`CheckCompatible` reaches `isForkBlockIncompatible(nil, N, head)`, which is
`(isBlockForked(nil, head) || isBlockForked(N, head)) && !configBlockEqual(nil, N)`.
While `head < N` both `isBlockForked` calls are false, so the check passes and no rewind is
requested. This is what makes it possible to ship the binary days ahead of activation.

Once `head >= N`, a node whose stored config still has `CamelliaBlock = nil` will fail to
start with a `rewind to N-1` compatibility error. Only a node left on a pre-fork binary past
the activation block can get into that state.

### Fork ordering

`camelliaBlock` is an optional entry in `CheckConfigForkOrder`, and the preceding mainnet fork
(Bokbunja, `73,225,410`) is well below the proposed block, so ordering validation passes.

### Escape hatch

`--override.camellia=<block>` (`cmd/utils/flags.go`, applied in `core/genesis.go`) injects a
fork block without rebuilding. Useful for testing and emergencies.

> Do not use it in steady-state operation: if nodes disagree on the value, the chain splits.

## Restoring an older chaindata snapshot

A snapshot taken before the upgrade stores `CamelliaBlock = nil`, but as described above the
hardcoded mainnet config overrides it, and `CheckCompatible` passes while the restored head is
behind the fork block. Such a node catches up and applies Camellia at the fork block normally —
no extra step is needed, provided it is running a binary that knows the fork.

## Shutdown behaviour

A rolling upgrade stops every node once, so shutdown has to be reliable. Two problems were
found and fixed while preparing this rollout.

### The node could ignore SIGTERM indefinitely

`metclient.CallContract` held the global `metaminer.TrieAccessMu` read lock across the whole
contract read, and its callers pass a context with no deadline. `contract.Cli` is an in-process
client, so once the node begins shutting down the call can stop being answered — and the read
lock is then never released:

- the admin loop parks inside the call while holding the read lock
- `writeBlockWithState`, running under the downloader's block import, blocks on the write lock
- that goroutine is one of the downloader's fetchers, so `Downloader.Cancel` never returns
- `chainSyncer.loop` never releases its `handler.wg` entry, so `handler.Stop` blocks in
  `wg.Wait` and `node.Close` never returns

The process then survives SIGTERM indefinitely and only dies on SIGKILL — the unclean shutdown
the lock exists to prevent. The fix bounds the lock hold with a deadline and releases it via
`defer`; the lock itself is unchanged, so the state-commit serialisation it provides is intact.

Reproduced on isolated nodes and confirmed with a goroutine dump: **4 of 4 hangs before the
fix** (LevelDB and RocksDB alike, surviving 150–200 s+), **9 of 9 clean shutdowns after**
(1–17 s, each logging `Blockchain stopped`).

### The control script escalated to SIGKILL on a fixed timer

`gmet.sh stop` waited a hardcoded 200 s and then sent SIGKILL unconditionally, so any node
slower than that — or wedged, as above — was killed with an unclean database. The timeout and
the escalation are now `.rc` settings (`STOP_TIMEOUT`, `STOP_FORCE`, `LOCK_TIMEOUT`) whose
defaults reproduce the old behaviour. With `STOP_FORCE=0` the stop fails instead of killing,
which also required fixing `restart`: it previously ran `start` regardless of whether the stop
succeeded, which would leave two nodes running against one datadir.

### Operational rule

**Treat a stop as complete only when the process is gone _and_ the log shows
`Blockchain stopped`.** A missing `Blockchain stopped` after `Got interrupt` means the shutdown
did not finish, and anything that reads the datadir afterwards — a tarball, a snapshot, a
restore — is being taken from a node that never flushed. Automation that stops a node in order
to copy its data must verify this rather than assume it, and must not fall through to the copy
when verification fails.

If a node does hang, capture `kill -QUIT <pid>` before resorting to SIGKILL: the runtime dumps
every goroutine to stderr, which is what identified the cause above. The IPC socket is already
closed by that point, so `debug.stacks()` over IPC is not available.

## Rollout

- **One tarball per database engine.** The fleet runs both LevelDB and RocksDB builds; a
  mismatched binary cannot open the datadir.
- **Restart one sealer at a time.** Consensus is PoA backed by an etcd quorum, so stopping
  several block producers together is never acceptable. Non-sealers first as a canary, then
  sealers one by one, leader last.
- **Graceful shutdown only** — never `kill -9`, especially with RocksDB. Stops on large
  datadirs can take a while; run them detached so a client timeout does not interrupt them.
- **Ship binaries ahead of activation.** The fork block is compiled in, so deployment and
  activation are naturally separate events. The rollback window is the gap between them; once
  the fork activates, rollback means a chain split and is no longer possible.
- **Verify every node before the activation day**: `gmet version` plus the
  `Camellia Fork: #<block>` line in the startup banner. This also catches any node that was
  rebuilt or restored from an image carrying an older binary.
- **Upgrade snapshot-publishing nodes first.** Any node that periodically stops, tars its
  chaindata and publishes it for others to bootstrap from must be on the fork-aware binary
  *before* its first publish after activation — otherwise it follows a pre-fork chain past the
  activation block and hands that chaindata to downstream users. Their next publishing window,
  not the activation day, is the real deadline.
- **Bootstrapping after the fork** requires the fork-aware binary. A pre-fork chaindata
  snapshot is fine as a starting point (see the section above), but pairing it with a pre-fork
  binary stalls at the activation block. Say so in the release notes.

### Relative timeline

| When | Step |
|---|---|
| D-14 | Exchange notice sent; fork block committed and merged |
| D-12 | Build one tarball per engine |
| D-10 | Stage on follower nodes, confirm the fork block in the banner |
| D-7 | Fleet rollout: canary → non-sealers → sealers one at a time → leader |
| D-3 | Fleet-wide check of `gmet version` and the fork block value |
| D-day 12:00 KST | Fork activates; monitor |
| D+1 | Post-activation report |

## Verification status

| Item | Status |
|---|---|
| Testnet fleet on `e3f51b3e6` (meta/69) | done — no incident since rollout |
| Camellia transition matches canonical on testnet | done — pre/post fork block hashes match |
| Clean sync genesis → tip, both chains | done — no BAD BLOCK, no state mismatch |
| Private-net PoA suite | 19 pass / 0 fail / 2 skip |
| Blob-tx and mixed-tx e2e | done |
| CGO unit tests (secp256k1, KZG guards) | done |

## Included in the target binary

`e3f51b3e6` carries, on top of the Camellia fork itself:

- secp256k1 coordinate validation, ECIES guards, full sync as the default
- cgo `ScalarMult` guard, blob KZG peer-drop
- meta/69: replay protection (M12) and blob sidecar serving (M5)
- upstream #30125 — arrival-order transaction fetching

It stays backward compatible with the current mainnet release over meta/66 and meta/68.

On top of that, the rollout binary must carry the shutdown fixes described in
[Shutdown behaviour](#shutdown-behaviour): the bounded `TrieAccessMu` hold in
`metadium/metclient`, and the configurable stop behaviour in `metadium/scripts/gmet.sh`. The
deadlock only manifests while stopping, which is why months of steady-state operation did not
surface it.
