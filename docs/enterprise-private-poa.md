# Enterprise private PoA: fast confirmation setup

How to configure a private Metadium PoA network so that a transaction is
confirmed in roughly a tenth of a second instead of waiting out the block
interval. This is the operator's guide; the design record and the measurements
behind it are in [enterprise-block-timing.md](enterprise-block-timing.md).

> **Private networks only.** The node **refuses to start** if this
> configuration is applied to a chain whose genesis is the Metadium mainnet or
> testnet genesis. Nothing here can be used on a public network, by design.

## What it does

A Metadium sealer takes its cadence from governance and closes a block when the
slot elapses. On an enterprise chain — idle most of the time, then one
transaction that someone is waiting on — that means the transaction sits until
the slot ends. With a 5 second interval that is 2.5 seconds on average, and it
is the entire latency.

`--metadium.block.idleseal <ms>` changes one thing: **once the block being built
holds at least one transaction, it is sealed as soon as no new transaction has
arrived for that many milliseconds.** Measured on a 3-sealer network with a 5
second interval:

| | Interval only (default) | With `idleseal 100` |
|---|---|---|
| Confirmation, single transaction | avg 2.5–3.4 s | **avg 0.12 s** |
| 40 transactions submitted at once | 1 block, 5.7 s | 1 block, **0.18 s** |
| Empty-block cadence when idle | every 4.3–5.8 s | unchanged, every 4.3–5.8 s |

The last row is the important one: **`idleseal` leaves empty blocks alone.** The
heartbeat of the chain stays the on-chain interval, and only blocks that carry
transactions close early.

If that heartbeat is itself the problem — a chain idle for long stretches, where
the empty blocks are just disk growth — there is a second flag for it,
`--metadium.block.emptyinterval <s>`: with an empty pool, no block is produced
until that many seconds have passed since the parent. It is **not needed for
confirmation latency** and this guide does not use it; measured behaviour, the
one empty block that can trail a transaction block, and the effect on finality
depth are in [enterprise-block-timing.md](enterprise-block-timing.md). The rest
of this guide is about `idleseal`.

## Prerequisites

- A `gmet` build that carries the flag on every sealer — `dev` at or after the
  merge of #116, and the first release cut from it. Releases up to and
  including `m1.1.3` do not have the flag: passing it to one of those makes the
  node exit at startup with `flag provided but not defined`.
- A private chain — its own genesis, not a copy of a public one.
- Access to change the on-chain environment values (a governance ballot, or the
  initial deploy if the chain is not yet live).

## Step 1 — set the block interval on-chain

The interval is **not** a command-line flag. The sealer reads
`EnvStorage.getBlockCreationTime()` (milliseconds) on every block.

```
# check what the chain is set to
eth_call -> EnvStorage.getBlockCreationTime()      # e.g. 5000
```

Set it when the governance contracts are deployed, or change it later by
ballot. In the bundled test harness:

```bash
cd tests/private-net-poa
BLOCK_CREATION_TIME=5000 ./deploy.sh
```

Two things to know about the value:

- `timeIt` divides it by 1000, so the interval has **one-second granularity**,
  and anything below 1000 ms falls back to 2 seconds.
- `--metadium.block.interval` looks like the knob for this. It is dead: the
  value is stored in `params.BlockInterval` and never read. Ignore it.

## Step 2 — enable the idle seal on every sealer

Add the flag to each sealing node. It takes effect only on the node that is
building a block, so a fleet with mixed values simply gets different latency
depending on whose turn it is — set it identically everywhere.

**Stock deployment (`gmet.sh` with an `.rc` file):**

```sh
# /opt/meta/.rc
GMET_OPTS="--metadium.block.idleseal 100"
```

`GMET_OPTS` is appended verbatim to the command line. Restart the node
normally (`gmet.sh stop` then `gmet.sh start`) — one sealer at a time, never
two at once.

**Direct invocation or a container:**

```
gmet --datadir /opt/meta ... --metadium.block.idleseal 100
```

Non-sealing nodes (RPC, full nodes) ignore the flag — `commitWork` returns
before this code on a non-miner — so passing it there is harmless but
pointless.

### Choosing the value

**Keep it small. 100 ms is the tested default, and a larger window does not buy
tolerance for network delay between sealers.** That is measured, and it is the
opposite of what the setting's shape suggests, so it is worth knowing why.

The propagation cost is paid *before* the quiet window opens. A transaction
submitted at one node reaches the sealer as an announcement, then a request for
the body, then the body — two delay hops plus the batching geth applies to
announce and fetch — and only then does the sealer start counting quiet time:

```
tx submitted at node A
  A announces the hash to the sealer     + one delay hop
  the sealer asks for the body           (+ announce/fetch batching)
  A sends the body                       + one delay hop
  ------ the quiet window starts here ------
  quiet window                           + idleseal
  seal; the block propagates back
```

Widening the window adds to that instead of absorbing it. Measured on the
3-node harness with `tc netem` injecting delay toward two of the three sealers,
10 transactions per cell (median confirmation):

| One-way delay between sealers | `idleseal 100` | `idleseal 300` | `idleseal 500` |
|---|---|---|---|
| 0 ms | **117 ms** | 324 ms | 521 ms |
| 30 ms | **272 ms** | 469 ms | 635 ms |
| 80 ms | **741 ms** | 1018 ms | 1132 ms |
| 120 ms | 623 ms | 666 ms | **542 ms** |
| 200 ms | **635 ms** | 727 ms | 913 ms |

Ten samples per cell, so differences under roughly 150 ms are inside the noise.
The 120 ms row is the one place a wider window came out ahead on the median, and
its worst case was worse there (1548 ms against 1146 ms).

What a wider window does buy is **batching under delay**: a 40-transaction burst
stayed in a single block at every delay with `idleseal 500`, while `100` and
`300` split it into two blocks once the delay reached 120 ms. Raise the value for
that reason if blocks-per-burst matters more than median latency — not for delay
tolerance.

So:

- **Sealers on one host or one LAN segment**: 100 ms, and expect ~120 ms.
- **Sealers across regions**: still 100 ms for the lowest median. Expect
  0.6–0.9 s at 80–200 ms one-way, with occasional cases near 1.9 s — all of it
  still well below the 2.5–3.4 s that the interval alone gives on a 5 second
  chain.
- Raising it moves the median up by roughly the amount you add. The floor is
  `idleseal` plus about 20 ms.

**Agreement was unaffected throughout that sweep**: 15 configurations, up to
200 ms one-way (a 400 ms round trip, worse than any pair of AWS regions), zero
`BAD BLOCK`, and the three nodes were never more than one block apart.

A negative value is rejected at startup. `0` means off.

## Step 3 — verify

**The sealer says so in its log** (needs `--verbosity 4` or higher, since the
line is DEBUG):

```
DEBUG Sealing early, transaction pool went quiet number=626 txs=1 idle-ms=100 slot-left=1.693s
```

`slot-left` is how much of the slot was still unused when the block closed —
that is the latency this configuration removes.

**Measure it end to end.** Send a transaction from an otherwise idle pool and
time from submission to the receipt being available:

Submit one transaction, then poll `eth_getTransactionReceipt` until it answers,
and take the wall-clock difference. On a 5-second-interval chain with
`idleseal 100` that should be **110–130 ms**; if it comes back at half the
interval or more, the flag is not in effect on the node that sealed the block
(see Troubleshooting).

For reference, a 10-minute soak at one transaction every 3 seconds on the test
network produced 193 transactions in 193 blocks with p50 117 ms, p99 129 ms and
a maximum of 130 ms — nothing above 500 ms.

**Confirm the fleet is in agreement**: every node at the same head, no
`BAD BLOCK` in any log. A chain produced with this setting is ordinary to every
other node — verified by syncing a stock `m1.1.3` node (which does not have the
flag at all) against such a chain: 9,887 blocks imported, no rejected headers.

## What to expect in operation

- **Empty blocks keep coming at the interval.** This setting does not sparsen
  them. An idle chain still mints one block per slot.
- **The idle cadence wobbles between 4.3 s and 5.8 s** on a 5 second interval.
  That is the pre-existing drift correction (`timeIt`), not this setting: when
  the chain has been running fast it stretches empty slots, when slow it
  shortens them. Both bounds are fixed formulas, so the wobble neither scales
  with load nor accumulates. Blocks carrying transactions are unaffected.
- **Roughly one block per transaction under steady traffic**, and coalescing
  under dense traffic (40 transactions arriving together produced one block).
  Coalescing weakens as delay between sealers grows: at 120 ms one-way the same
  burst split into two blocks with `idleseal 100`. Plan disk growth accordingly
  — a chain with steady low-rate traffic produces more blocks than the same
  chain on interval-only sealing.
- **Finality depth is counted in blocks, not seconds.**
  `GetFinalizedBlockNumber` returns `head - (govNodeCount/2 + 1)`. Faster
  blocks means finality arrives sooner, never later.
- **Block timestamps are whole seconds, and several blocks can share one.**
  This is consensus-legal on Metadium PoA. Explorers and indexers that compute
  block time by subtracting timestamps will show 0-second gaps.

## Limits and cautions

- **Public networks are refused, not warned.** With the flag set on a chain
  whose genesis is the Metadium mainnet or testnet genesis, the node exits:
  `Fatal: Failed to register the Ethereum service: --metadium.block.idleseal is
  for private networks only, but this node is on the Metadium mainnet`. The
  check is keyed on the genesis hash, so a private chain that reuses a public
  chain id is fine — but a private chain that clones a public genesis outright
  cannot use the flag.
- **No floor on block rate.** Transactions arriving slightly slower than
  `idleseal` each get their own block. Nothing pathological appeared in
  testing, but if a deployment sees that pattern and wants a floor, that is a
  feature request (a bounded minimum spacing since the parent), not a setting
  that exists today.
- **`BlockMinBuildTime` is not a floor for an early seal.** It shapes the
  deadline the drift correction computes; it does not hold a block open. An
  `idleseal` below it is honoured as written.
- **Untested combinations**, stated so a rollout does not assume them: sealers
  on genuinely separate hosts (delay between sealers has been measured, but by
  injecting it with `tc netem` on one host — real links also bring jitter, loss
  and reordering, none of which was injected), the RocksDB build with this flag
  (it compiles and is verified, but no chain has been run on it), and sustained
  high block rates over hours (the longest run is the 10-minute soak, so there
  is no disk or state growth figure).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Node exits: `--metadium.block.idleseal is for private networks only` (or the same for `--metadium.block.emptyinterval`, or both named at once) | The chain's genesis is the Metadium mainnet or testnet genesis | Not usable there. Confirm the chain has its own genesis |
| Node exits: `flag provided but not defined` | Binary predates the flag (`m1.1.3` or older) | Upgrade the sealer |
| Node exits: `Invalid metadium.block.idleseal: -1, must not be negative` | Negative value | Use `0` (off) or a positive millisecond count |
| Confirmation is fast for some transactions and slow for others | The flag is set on some sealers but not all; latency then depends on whose turn it is | Set the same value on every sealer |
| Confirmation is consistently ~half the interval | The flag is not in effect anywhere — typo in `GMET_OPTS`, or the node was not restarted | Check the running command line (`ps`), restart one sealer at a time |
| No `Sealing early` lines in the log | The line is DEBUG | Raise to `--verbosity 4` |
| Empty blocks still appear on an idle chain | Expected — this setting does not suppress them; the interval remains the heartbeat | Nothing to fix. Suppressing them is not a supported option today |
| A transaction lands one block later than expected | It arrived after the quiet window closed. With delay between sealers this is normal, and it costs one short round rather than a full slot | Not fixable by raising `idleseal` — the delay is spent before the window opens (see [Choosing the value](#choosing-the-value)) |

## Rolling back

Remove the flag from the `.rc` (or the command line) and restart the sealers
one at a time. There is no data migration and no chain state to undo: the
blocks already produced are ordinary blocks, and the node simply returns to
closing every block at its slot deadline.

## Reference

- [enterprise-block-timing.md](enterprise-block-timing.md) — why the design is
  shaped this way, the drift-correction arithmetic, the full measurements, and
  the record of two designs that measurement rejected.
- `tests/private-net-poa/` — the 3-node harness these numbers come from.
