# Private-PoA block timing

A private Metadium PoA deployment usually wants a different bargain than the
public chain does. The public networks take a fixed cadence from governance and
seal on that clock whether or not anyone sent a transaction. An enterprise chain
is typically idle most of the time and then needs a transaction *confirmed*
quickly -- waiting out the rest of a 5 second slot is the whole latency.

One node flag restores the confirmation behavior early Metadium had
(`11be6ec99`, 2018-06-29, "immediate block generation / empty blocks only after
maxidleblockinterval"), without changing what the public networks do.

| Flag | Unit | Default | Effect |
|---|---|---|---|
| `--metadium.block.idleseal` | ms | `0` (off) | Once the block being built holds at least one transaction, seal it as soon as no new transaction has arrived for this long, instead of holding the slot open to its deadline. |

It defaults to off, which is exactly the behavior of a build without it. Empty
blocks are untouched: with an empty pool the sealer still runs the slot to its
deadline, so the on-chain interval remains the chain's heartbeat.

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

2. **Set the flag identically on every sealer.** It only takes effect on the
   node that is building a block, so a fleet with mixed values simply gets
   different latency depending on whose turn it is. Non-sealing RPC and full
   nodes ignore it (`commitWork` returns before this code on a non-miner), so
   passing it there is harmless but pointless.

3. **Typical enterprise profile** -- 5 second heartbeat, ~100ms confirmation:

   ```
   # governance (once, at deploy time)
   blockCreationTime = 5000

   # every sealer
   --metadium.block.idleseal 100
   ```

## Measured behavior

3-node PoA private net (`tests/private-net-poa`, LevelDB, one host), governance
`blockCreationTime = 5000`, all three nodes sealing. "Confirm" is wall clock
from `eth_sendTransaction` returning to the receipt being available.

| Configuration | Idle cadence | Confirm (single tx) | 40-tx burst |
|---|---|---|---|
| flag off (public-network behavior) | empty block every 4.3-5.8s | avg 2544ms (min 1962, max 2634) | 1 block, 5657ms |
| `idleseal=100` | empty block every 4.3s | **avg 117ms** (min 117, max 118) | 1 block, **174ms** |

A 10-minute soak with `idleseal=100` and one transaction every 3 seconds, to see
whether latency ever spikes: 193 transactions, **min 107ms, p50 118ms, p90 128ms,
p99 130ms, max 130ms**, nothing above 500ms. The gap between p50 and the maximum
is 12ms -- there is no tail. That run also produced 193 blocks for 193
transactions: with traffic this steady every slot closes on a transaction, so no
empty blocks appear at all.

Across the runs: three nodes stayed in lockstep at the same head, 40 sampled
blocks had no parent-hash break, and the logs carried no `BAD BLOCK`, no panic
and no seal failure. The only ERROR lines were the harness's pre-existing
`static-nodes.json is deprecated` warnings.

## Interactions worth knowing

**Three governance values are easy to confuse, and one of them is inert.**
`getBlockCreationTime` is the block interval and is read every block (mainnet
`2000`). `getMaxIdleBlockInterval` is the idle heartbeat -- mainnet has it set
to `5` -- and **nothing reads it for block production**: it is loaded into a
struct and never consulted, the same fate as `throttleMining`'s call site. That
is why an idle chain still mints an empty block every slot. Re-wiring it is not
the small fix it looks like: mainnet has the value set to `5`, so honoring it
would move mainnet's idle cadence from 2s to 5s. This flag deliberately leaves
empty blocks alone and touches only the block that carries a transaction.

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
by the idle rule long before either deadline -- the 10-minute soak above saw a
maximum of 130ms.

**`BlockMinBuildTime` is not a floor for idle sealing.** It only shapes the
deadline `timeIt` computes; nothing enforces it against an early seal. An
`idleseal` below it (the 100ms above, against a 300ms `BlockMinBuildTime`) is
honored as written.

**What disappears is the slot deadline acting as a ceiling on block rate.**
Traffic arriving just slower than `idleseal` gives every transaction its own
block. Nothing pathological appeared in testing -- steady traffic produced one
block per transaction and dense traffic coalesced (40 transactions into one
block) -- but if a deployment needs a floor under the block rate, the shape to
add is a bounded minimum spacing since the parent.

**Timestamps are seconds, and several blocks can share one.** PoA permits it --
the `header.Time <= parent.Time` rejection in `consensus/ethash/consensus.go` is
guarded by `metaminer.IsPoW()` -- so this is consensus-legal. Explorers and
indexers that compute block time by subtracting timestamps will show 0s gaps.

**Finality depth is measured in blocks, not seconds.**
`GetFinalizedBlockNumber` returns `head - (govNodeCount/2 + 1)`. Empty blocks
keep arriving on the on-chain interval, so that depth fills at the same rate as
before; sealing early only brings it forward, never delays it.

## Why mainnet and testnet are unaffected

- **Off by default.** `params.BlockIdleSealTime` is 0, and every new path is
  behind a `> 0` test. With the flag unset the sealer runs the same code it ran
  before: the quiet timer is never created and the wait selects on a nil
  channel, which never fires. Measured on the same binary with the flag
  omitted: idle cadence and confirmation latency matched the pre-change
  baseline.
- **Refused outright on the public chains.** `eth.New` looks up the genesis hash
  and returns an error -- the node exits rather than starting -- if the flag is
  set on a chain whose genesis is `MetadiumMainnetGenesisHash` or
  `MetadiumTestnetGenesisHash`. Keyed on genesis rather than chain id, because a
  private chain may reuse a chain id but never the public genesis.
- **No consensus rule is touched.** Block spacing is not validated by
  `verifyHeader`; producer rotation is height-based
  (`admin.go: ix := int(height/blocksPer) % len(nodes)`); rewards are per block.
  A node running this flag produces blocks that any stock node accepts, and the
  flag changes nothing about validation, so a mixed fleet stays in agreement --
  the sealer just closes blocks sooner.
- **Sealer-side only.** Nothing in the import, sync or RPC path reads the value.
