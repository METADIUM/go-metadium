# Private-PoA block timing

A private Metadium PoA deployment usually wants a different bargain than the
public chain does. The public networks take a fixed cadence from governance and
seal on that clock whether or not anyone sent a transaction. An enterprise chain
is typically idle most of the time and then needs a transaction *confirmed*
quickly -- waiting out the rest of a 5 second slot is the whole latency.

Two node flags restore the behavior early Metadium had (`11be6ec99`, 2018-06-29,
"immediate block generation / empty blocks only after maxidleblockinterval"),
without changing what the public networks do.

| Flag | Unit | Default | Effect |
|---|---|---|---|
| `--metadium.block.idleseal` | ms | `0` (off) | Once the block being built holds at least one transaction, seal it as soon as no new transaction has arrived for this long, instead of holding the slot open to its deadline. |
| `--metadium.block.emptyinterval` | s | `0` (off) | While the pool is empty, produce no block at all until this many seconds have passed since the parent. |

Both default to off, which is exactly the behavior of a build without them.

## Configuring a private PoA network

1. **Block interval is on-chain, not a flag.** The sealer reads
   `EnvStorage.getBlockCreationTime` (milliseconds) through
   `getBlockBuildParameters`. Queried on mainnet today it is `2000`, and testnet
   paces identically (block spacing measured at exactly 2.000s over 1000-block
   windows) -- **the 5s used throughout this document is the private test
   chain's own setting**, not a public-network value. Set it when the
   governance contracts are deployed
   -- in `tests/private-net-poa` that is `BLOCK_CREATION_TIME=5000 ./deploy.sh`
   -- or change it later by ballot. Note `timeIt` divides it by 1000, so the
   interval has one-second granularity and anything under 1000ms falls back to
   2 seconds.
   `--metadium.block.interval` looks like the knob for this but is dead: the
   value is stored in `params.BlockInterval` and never read.

2. **Set the two flags identically on every sealer.** They only take effect on
   the node that is building a block, so a fleet with mixed values simply gets
   different latency depending on whose turn it is. Non-sealing RPC and full
   nodes ignore them (`commitWork` returns before this code on a non-miner), so
   passing them there is harmless but pointless.

3. **Typical enterprise profile** -- 5 second heartbeat, ~100ms confirmation:

   ```
   # governance (once, at deploy time)
   blockCreationTime = 5000

   # every sealer
   --metadium.block.idleseal 100
   ```

   Add `--metadium.block.emptyinterval <s>` only if the empty-block stream
   itself is a problem (disk growth on a chain that is idle for long stretches).
   It is not needed for latency.

## Measured behavior

3-node PoA private net (`tests/private-net-poa`, LevelDB, one host), governance
`blockCreationTime = 5000`, all three nodes sealing. "Confirm" is wall clock
from `eth_sendTransaction` returning to the receipt being available.

| Configuration | Idle cadence | Confirm (single tx) | 40-tx burst |
|---|---|---|---|
| flags off (public-network behavior) | empty block every 4.3-5.8s | avg 2544ms (min 1962, max 2634) | 1 block, 5657ms |
| `idleseal=100` | empty block every 4.3s | **avg 117ms** (min 117, max 118) | 1 block, **174ms** |
| `idleseal=100` + `emptyinterval=30` | **1 block / ~30s** | **avg 123ms** (min 116, max 127) | 1 block, 179ms |

A 10-minute soak with `idleseal=100` and one transaction every 3 seconds, to see
whether latency ever spikes: 193 transactions, **min 107ms, p50 118ms, p90 128ms,
p99 130ms, max 130ms**, nothing above 500ms. The gap between p50 and the maximum
is 12ms — there is no tail. That run also produced 193 blocks for 193
transactions: with traffic this steady every slot closes on a transaction, so no
empty blocks appear at all even with `emptyinterval` off.

Across the runs: three nodes stayed in lockstep at the same head, 40 sampled
blocks had no parent-hash break, and the logs carried no `BAD BLOCK`, no panic
and no seal failure. The only ERROR lines were the harness's pre-existing
`static-nodes.json is deprecated` warnings.

**Set `emptyinterval` above the on-chain interval, or it does almost nothing.**
The withhold is measured from the parent's timestamp, so a value at or below the
interval expires before the slot the sealer would have used anyway. Measured on
the same chain (`blockCreationTime = 5000`), 8 single transactions and a 40-60s
idle window per row:

| Configuration | Idle cadence (median) | Confirm (median) |
|---|---|---|
| flags off | 4.34s (3.9-5.9) | 2825ms |
| `idleseal=100` | 5.81s (1.3-5.9) | 121ms |
| `idleseal=100` + `emptyinterval=5` | **4.97s (4.3-5.2)** — barely moved | 125ms |
| `idleseal=100` + `emptyinterval=30` | **29.8s (17.0-30.1)** | 126ms |

Pick the gap you actually want between empty blocks. A `5` on a 5-second chain
reads like "one empty block every five seconds", which is what it was already
doing.

## Interactions worth knowing

**Three governance values are easy to confuse, and one of them is inert.**
`getBlockCreationTime` is the block interval and is read every block (mainnet
`2000`). `getMaxIdleBlockInterval` is the idle heartbeat -- mainnet has it set
to `5` -- and **nothing reads it for block production**: it is loaded into a
struct and never consulted, the same fate as `throttleMining`'s call site. That
is why an idle chain still mints an empty block every slot, and why
`emptyinterval` is a node flag here rather than a revival of the on-chain
value: re-wiring it would silently change mainnet's idle cadence from 2s to 5s.

**The drift correction still owns the empty-block cadence, and only that.**
`timeIt` compares recent block density against the nominal interval and either
shortens the next slot (behind: `(interval-1)s + BlockMinBuildTime`) or
stretches it (ahead: `interval + BlockMinBuildTime + 500ms`). At a 5s interval
that is 4300ms and 5800ms -- which is why the idle cadence above reads 4.3s or
5.8s rather than a flat 5s. Both branches are **fixed formulas, not
proportional to the drift**, so however far ahead the chain runs the empty-slot
deadline never grows past `interval + 800ms`. There is no runaway.

Early sealing does feed that loop: a busy chain produces blocks faster than the
interval, so the correction settles on "ahead" and slows the *empty* slots to
the 5.8s ceiling. It does not touch confirmation latency, because `idleseal`
fires relative to the last transaction while the correction only moves the slot
deadline, which is an upper bound. Measured: 45s of load at 10 tx/s produced 65
blocks (692ms/block, ~7x the nominal rate) and left the chain firmly ahead;
single-transaction confirmation immediately afterwards was **avg 123ms (min 116,
max 128)**, the same as on an undrifted chain, while the idle gaps stretched to
5821-5823ms. The sealer's own log shows both halves at once:

```
DEBUG time-it   ahead=1,788,860,385 duration=5800
DEBUG Sealing early, transaction pool went quiet number=626 txs=1 idle-ms=100 slot-left=1.693s
```

The correction had set a 5800ms deadline; the idle seal closed the block with
1.693s of it still unused.

**How much late does an idle chain actually run after a fast burst?** The
question an operator will ask, measured directly: 100 blocks were produced at
122ms each, then transactions stopped and every subsequent gap was recorded.

```
#1099..#1119   5.8s each  (+829, +806, +813, ... , +829ms vs the nominal 5s)   21 blocks
#1120..#1132   4.3s each  (-669, -709, -661, ... , -682ms)                     13 blocks
#1133..        5.8s and 4.3s mixed
n=43  min=4291  avg=5330  max=5846 ms
```

So the whole penalty is **+846ms at worst, for 21 blocks -- about 17 seconds of
accumulated delay** -- and then the correction flips to `behind` and gives it
back at 4.3s per block. Nothing accumulates beyond that, whatever the burst
was: both branches are fixed values.

All four numbers fall out of the judgement window rather than being incidental
to the run. `timeIt` looks back over `1, 24, 240, 2400, 17280` blocks (24 =
`BlockTimeAdjBlocks / interval`, then ten-fold, capped at `86400 / interval`)
and calls the window `behind` once its elapsed time passes the nominal
`24 x 5 = 120s`, stopping at the first window that says so. With k slow blocks
in that window, `k*5.82 + (24-k)*0.122 > 120` gives k=20 -> 116.9s (still
ahead), **k=21 -> 122.6s -> flip**. Coming back is the same arithmetic:
with m blocks at 4.3s, `139.7 - 1.51m < 120` gives **m=13 -> 120.1s**. The two
values then interleave because `dt` sits on the 120s boundary and header
timestamps are whole seconds. On mainnet's 2s interval the window is
`120 / 2 = 60` blocks and the deadlines become 1300 / 1700 / 2800ms -- which
matches its measured spacing (1s 26.0%, 2s 55.8%, 3s 10.5%, 4s 7.7%).

Only the empty heartbeat is affected. A block carrying a transaction is sealed
by the idle rule long before either deadline -- the 10-minute soak below saw a
maximum of 130ms.

**`BlockMinBuildTime` is not a floor for idle sealing.** It only shapes the
deadline `timeIt` computes; nothing enforces it against an early seal. An
`idleseal` below it (the 100ms above, against a 300ms `BlockMinBuildTime`) is
honored as written.

**`throttleMining` was removed here, and it is worth knowing why.**
`miner/worker.go` carried `throttleMining`/`ancestorTimes` from `7ed4d8b27`
(2019-03, "added block generation throttling"): a hard rate cap where 1000
blocks must span 2000s, 500 blocks 500s, 100 blocks 50s, 50 blocks 10s, and a
violation sleeps for the shortfall **in whole seconds**. It belonged to the
era this document is restoring -- sealing happened as soon as the pool drained,
there was no slot deadline, so block production needed a brake.

The `f113fe913` go-wemix import (2023-07) brought `timeIt` and dropped the call
site; the two overlap, and `timeIt` is the better-behaved of the pair. The dead
definition survived the import and sat unreferenced through every release from
`m0.10.0` (2023-10) to `m1.1.3`.

| | `timeIt` (alive) | `throttleMining` (removed) |
|---|---|---|
| direction | both -- shortens the slot when behind, stretches it when ahead | brake only; does nothing when the chain is late |
| bound | `interval + 800ms`, a fixed formula | none: up to `2000 - elapsed` **seconds** |
| basis | block density vs the nominal interval | absolute counts over the last 1000 blocks |

Restoring the brake would have been actively harmful here. On a chain sealing
on idle at the 692ms/block measured above, the 1000-block rule alone would
sleep `2000 - 692 = 1308s` -- **block production stops for ~22 minutes**, and
the faster the chain, the longer the stall. Nothing is lost by removing it:
catching up when the chain runs *late* was never its job, and that half is
`timeIt`'s, which stays.

What genuinely disappears with idle sealing is the slot deadline acting as a
natural ceiling on block *rate*: traffic arriving just slower than
`idleseal` gives every transaction its own block. If a deployment needs a floor
under that, the shape to add is a bounded minimum spacing, not this.

**With `emptyinterval` on, a transaction block may be trailed by one empty
block.** The withhold decision is taken in `commitWork` from the pool's own
view, before the mining-token gate. A node that has not yet reset its pool after
someone else sealed the transaction still sees it as pending, does not skip, and
builds a round that comes out empty. Deciding after the fill instead -- where
emptiness is known exactly -- was measured and rejected: a round discarded at
that point has already taken the mining token and leaves it to expire (TTL 10s),
which pushed confirmation from 117ms to **4.4s average**. One trailing empty
block is the cheaper trade, and the comment in `commitTransactionsEx` records it
so the next reader does not redo the experiment.

**Timestamps are seconds, and several blocks can share one.** PoA permits it --
the `header.Time <= parent.Time` rejection in `consensus/ethash/consensus.go` is
guarded by `metaminer.IsPoW()` -- so this is consensus-legal. Explorers and
indexers that compute block time by subtracting timestamps will show 0s gaps.

**Finality depth is measured in blocks, not seconds.** `GetFinalizedBlockNumber`
returns `head - (govNodeCount/2 + 1)`. Withholding empty blocks means those
confirmations arrive only as fast as real traffic produces blocks, so on a very
quiet chain a transaction is included in ~100ms but finalized later than the
fixed-cadence chain would have finalized it. Use `emptyinterval` deliberately.

## Why mainnet and testnet are unaffected

- **Off by default.** `params.BlockIdleSealTime` and `params.BlockEmptyInterval`
  are 0, and every new path is behind a `> 0` test. With the flags unset the
  sealer runs the same code it ran before: the idle timer is never created, no
  extra transaction subscription is opened, and `commitWork` gains one integer
  comparison. Measured on the same binary with the flags omitted: idle cadence
  and confirmation latency matched the pre-change baseline.
- **Refused outright on the public chains.** `eth.New` looks up the genesis hash
  and returns an error -- the node exits rather than starting -- if either flag
  is set on a chain whose genesis is `MetadiumMainnetGenesisHash` or
  `MetadiumTestnetGenesisHash`. Keyed on genesis rather than chain id, because a
  private chain may reuse a chain id but never the public genesis.
- **No consensus rule is touched.** Block spacing is not validated by
  `verifyHeader`; producer rotation is height-based
  (`admin.go: ix := int(height/blocksPer) % len(nodes)`); rewards are per block.
  A node running these flags produces blocks that any stock node accepts, and
  the flags change nothing about validation, so a mixed fleet stays in
  agreement -- the sealers just close blocks sooner.
- **Sealer-side only.** Nothing in the import, sync or RPC path reads either
  value.
