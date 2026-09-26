# PBFT consensus: private-network test report

This report covers the PBFT consensus for new Metadium private networks (`bftBlock` in the genesis,
design in `docs/pbft-consensus-design.md`). It collects what the private-network runs showed. The item
list and its status stay in `docs/pbft-implementation-checklist.md`.

It is meant as input for the two sign-offs that code cannot answer:

- **G-01:** the requirement's wording and the §13 branch;
- **G-02:** operations accepting the availability trade-off (§4 below).

## 1. Environment

- `tests/private-net-pbft`: 4–9 Docker nodes on one host, full mesh, `bftBlock` 120 unless noted.
  Nodes run release builds (`CGO_ENABLED=0`), or the `pbftfault` build for fault injection. N = 7
  (f = 2, quorum 5) unless noted.
- Genesis `bft`: `emptyBlockInterval` 5 s, `baseTimeout` 2 s, `maxBackoffExp` 5, `timeDrift` 2 s.
- Operating profile: governance `blockCreationTime` 1000 ms, `--metadium.block.idleseal 100`.
  Fixed-interval profile: `blockCreationTime` 2000 ms, no idleseal.
- One host means LAN latency, a shared clock, and shared CPU and disk between the nodes. Section 6
  lists what this leaves unmeasured.

Every figure below comes from a script in `tests/private-net-pbft` (README there) and can be
reproduced with it.

## 2. Functional scenarios (design §11.2)

All 17 pass.

| ID | Scenario | Result | Script |
|---|---|---|---|
| S-01 | 1 validator stopped | production continues | `pbft-test.sh` |
| S-02 | f = 2 stopped | continues, slower; both catch up | `pbft-test.sh` |
| S-03 | f + 1 stopped | stops; resumes when the quorum returns; no fork | `pbft-test.sh`, `availability.sh` |
| S-04 | equivocating proposer | its rounds never commit; evidence reaches all 7 nodes | `byzantine.sh` |
| S-05 | wrong rewards field | refused by every other validator | `byzantine.sh` |
| S-06 | 4:3 partition | both sides stop; resumes on heal; no fork | `faults.sh`, `availability.sh` |
| S-07 | one clock 5 min ahead | its proposals refused and it refuses others'; follows by import | `byzantine.sh` |
| S-08 | add/remove a validator by ballot | the set changes at the next block | `governance.sh` |
| S-09 | a new node joins after the switch | full-syncs PoA and PBFT segments | `faults.sh` |
| S-10 | removed, forged, duplicated or re-rounded seals | import stops below the block | `import.sh` |
| S-11 | SIGKILL under load, restart | no conflicting vote; agreement | `faults.sh` |
| S-12 | restart without the WAL | observer for one height, then rejoins | `faults.sh` |
| S-13 | same node key on two servers | evidence against the key on every validator; both servers alarm | `twin.sh` |
| S-14 | proposal stamped before the parent / 10 s ahead | refused by the local-clock bound | `byzantine.sh` |
| S-15 | ballot that would leave N = 3 | never commits; chain continues at N = 4 | `governance.sh` |
| S-16 | prepared block re-proposed after a round change | commits unchanged, round-0 builder kept | `byzantine.sh` |
| S-17 | seals on a block below `bftBlock` | import refused | `import.sh` |

**Switch rehearsal** (`transition.sh`, R-01/R-02):
- With governance of 3 members, readiness reports the shortfall on every node. The chain then stops at
  `bftBlock − 1` instead of continuing on PoA, and logs
  `3 governance nodes at block 79, PBFT needs 4`.
- A new genesis then goes through design §9.2 to a round-0 switch at block 120, built by
  `validators[120 % 7]` with 5 seals.

## 3. Performance (design §11.3)

`measure.py --metrics`, N = 7:

| ID | Measurement | Result | Target |
|---|---|---|---|
| M-01 | confirmation latency, operating profile | p50 206 ms / p99 229 ms | p99 < 300 ms |
| M-02 | idle empty-block interval | 5.10 s, 0 round changes | 5 s ± 10 % |
| M-03 | round changes under load | 0 at ~870 transfers/s | 0 |
| M-04 | fixed-interval profile (2 s) | 2016 ms one transfer at a time, 2.03 s under load (PoA 1.91 s) | 2.0 s holds |
| M-05 | WAL fsync | 6.3 ms mean per record, ~13 ms of M-01 | recorded |
| M-06 | RPC suite, e2e suites | `rpc-test-full.sh` 63 / 0 FAIL / 1 WARN (script integer overflow) / 3 SKIP; blob and mixed e2e pass | no regression |
| M-07 | proposal check vs import | check 99 ms mean, import 11.5 ms | recorded |
| M-08 | full-sync verification | PoA 168 blocks/s; PBFT 11,250 transfers/s | recorded |
| M-09 | validator-floor check | 0.09 ms per transaction | recorded |
| M-10 | sidecar fetch before PREPARE (2 × 128 KiB) | 216 ms at 25 ms/1 Gbit … 1.6 s at 150 ms/5 Mbit | within 2 s |

- On a slower host, a reviewer measured M-01 at p50 245 ms / p99 288 ms. That is still under target,
  but with less margin.
- Throughput figures are bounded by the load generator on the same host. Read them as "no round
  changes at this rate", not as the chain's capacity.

## 4. Availability (for G-02)

PBFT trades liveness for safety. Without a quorum, blocks stop; they never fork. With N = 7, 3
validators stopped or a 4:3 split halts the chain. When the quorum returns, the chain resumes on
its own.

Measured with `availability.sh`. It takes the quorum away for each outage length, then times the first
block after it is restored:

| Outage | Stop f + 1 validators | 4:3 network split |
|---|---|---|
| 10 s (round 2) | 3.8 s | 0.02 s |
| 60 s (round 4) | 2.0 s | 4.0 s |
| 180 s (round 6) | 9.0 s | 11.9 s |

- "Stop" includes starting the stopped containers and letting them sync.
- Every node agreed afterwards.
- Round timeouts back off up to `baseTimeout · 2^maxBackoffExp` = 64 s. So an outage of any length
  should not take much longer than this to recover, because the nodes resynchronise their round
  when peers return; the 180 s runs, at round 6, took 9–12 s.

What operations should plan for:

- **Downtime = outage + seconds.** Losing one or two validators of seven costs nothing but some
  latency (S-01, S-02). Losing a third stops the chain until one comes back.
- **No manual step is needed** after an outage, a restart, or a lost WAL (S-11, S-12). The one
  exception is the switch itself failing (§9.3), which needs a new genesis. The bootstrap segment
  holds no business data, so this is by design.
- **Never run one node key on two servers** (design §6.1). It is detected and alarmed (S-13), but
  cannot be prevented. The failover rule stays: stop the old server, move the WAL, then start the
  standby.

## 5. Defects found by the private-network runs

The unit tests did not catch these; the network runs did. Each is fixed, with a test.

- Headers in a batch across `bftBlock` looked for their parent in the database instead of the
  batch, which broke full sync.
- Proposals lacked the PoA seal fields (nonce, mixHash) that every header needs.
- A reorg guard checked the lowest dropped block, so a fork rooted in the PoA segment could
  displace a PBFT block (#154).
- An import overwrote a proposal's complete fetched sidecars with the pool's partial set (#163).
- Lost worker wake-ups gave p99 latency of 1.1 s, and idle intervals were 6.1 s instead of 5 s (#165).
- A transaction left out by the validator floor blocked its sender for 64 heights (#169).
- The same key on two servers went undetected, since devp2p keeps one connection per node ID. Fixed
  by relaying votes and evidence pairs (#172).
- The fixed-interval profile did not hold: blocks came about 1.1 s apart instead of 2 s (#174).
- Proposers recorded blob sidecars under the unsealed block hash. A validator that had to fetch them,
  and whose vote the quorum needed, stalled the height (#175). The same PR fixed a worker shutdown
  hang.

## 6. Not covered here

- **Real servers across regions.** M-10 emulates latency and bandwidth with `tc netem`. A
  quorum-critical validator on a link slower than about 5 Mbit at 300 ms RTT would miss the 2 s
  sidecar wait; that is where to make the wait a setting.
- **NTP.** Containers share the host clock. Clock skew is covered by fault injection (S-07), but
  NTP sync on real servers is an operations check (design §9.2 step 2).
- **Sync across versions.** This needs two PBFT-capable releases (S-09 note).
- **Longer soaks.** The runs here last minutes to an hour per script.
