# Upstream go-ethereum Gap Survey — Hard-Fork vs Non-Fork Work

Status: survey, 2026-08-13. Baseline `dev` after the m1.1.2 release.

This document records what go-metadium is missing relative to upstream
go-ethereum, and splits it into work that needs a hard fork and work that does
not. Every claim below was measured against upstream release trees rather than
taken from release notes.

## Method

| | Value | How it was established |
|---|---|---|
| This tree | geth **v1.13.14** base | `core/vm/interpreter.go` is byte-identical to v1.13.14; `params/version.go` differs by 4 lines |
| Metadium delta | 366 files, +52,834 / −2,838 | `git diff --shortstat v1.13.14 dev` |
| Upstream latest | **v1.17.5** (2026-07-27) | GitHub releases |
| Upstream delta | 1,878 files, +303,613 / −103,636 | `git diff --shortstat v1.13.14 v1.17.5` |

Two properties of this repository shape the whole survey:

1. **We do not share commit history with upstream.** `merge-base(dev, v1.17.5)`
   is `ce796dcdb` (2014-12-11), because the v1.13.14 rebase replaced the tree
   rather than grafting upstream history. Commit-range comparisons are therefore
   meaningless here; only tree comparison is valid.
2. Consequently the fork inventory was derived from `core/vm/eips.go`
   activators, tree-level file presence, and per-file churn — not from
   `git log`.

To reproduce, add upstream as a remote and fetch the release tags. Objects are
largely shared with our tree, so this is cheap:

```sh
git remote add geth https://github.com/ethereum/go-ethereum.git
git fetch geth --no-tags tag v1.13.14 tag v1.16.9 tag v1.17.0 tag v1.17.5
git show v1.17.5:core/vm/eips.go | grep -oE '^\s+[0-9]{3,4}:\s+enable[0-9]+'
```

## 1. Summary

| Class | Content | Track |
|---|---|---|
| Hard fork required | Prague / Osaka / Amsterdam EIP families | C |
| No fork required | storage, log index, RPC, P2P, tooling | A and B |
| Security | CVE backports; **one real gap** remains | A (now) |
| Constraints | rebase conflict surface, protocol numbering, fork axis | decision input |

## 2. Hard-fork work (consensus rules)

Activators present upstream but absent here: **4762, 7702, 7843, 7939, 8024,
8037/8038**. Everything through Cancun is already covered by Camellia (1153,
2929, 3855, 3860, 5656, 6780, …).

### 2.1 Clear value

| EIP | What | Notes for go-metadium |
|---|---|---|
| **7702** | `SetCodeTxType = 0x04`, delegation from an EOA | Smart-account and sponsored-transaction UX. No type-number clash: our `FeeDelegateDynamicFeeTxType` is `22` (`core/types/transaction.go`) |
| **2537** | BLS12-381 precompiles (`PrecompiledContractsBLS`) | Bridge and proof verification |
| **7951** | `P256VERIFY` precompile | Passkey / WebAuthn wallets; smallest of the group |
| **2935** | Historical block hashes in state (`HistoryStorageAddress`, `HistoryServeWindow`) | Light-client and L2 verification |
| **7939** | `CLZ` opcode | Contract-level optimisation only |

### 2.2 Needs a policy decision before implementation

| EIP | Conflict |
|---|---|
| **7825** — per-transaction gas cap (`MaxTxGas`) | This chain deliberately admits very large transactions: the m1.1.1 verification recorded a 254KB contract deployment at `gasUsed 52,045,896` against a block limit of 105,000,000. Adopting the upstream cap would retract that capability |
| **7623** — calldata floor cost | Raises the cost of exactly those large-calldata transactions; must be recomputed together with the current initcode limit |
| Amsterdam repricing (`ColdAccountAccessAmsterdam`, `StorageWriteAmsterdam`, `MaxCodeSizeAmsterdam`, `BALItemCost`, …) | Gas parameters here are partly governed on-chain (`gasLimitAndBaseFee`), so upstream constants and governance values would have to be reconciled |

These are governance questions first and code changes second.

### 2.3 Not applicable

Consensus-layer features have no counterpart on a PoA chain with no beacon
chain: 4788 (beacon roots), 6110 (deposits), 7002 (withdrawal queue), 7251
(max effective balance), 7594 (PeerDAS) and the `BPO1..5Time` blob schedule,
plus the builder deposit/exit system contracts.

### 2.4 Deferred

EOF (`CALLF`, `RETF`, `JUMPF`, `EXTCALL`, `DATALOAD`, `RJUMP*`, …) is defined
upstream but its activation plan is still moving. Verkle / EIP-4762 and
`trie/bintrie` amount to replacing the state trie. 8024, 8037/8038 and the
Amsterdam/Bogota/UBT timestamps are still in flight upstream.

## 3. Non-fork work

Sizes are `git diff --numstat v1.13.14 v1.17.5` aggregates.

| Item | Size | Why it matters here |
|---|---|---|
| **Path-based archive** (`--gcmode=archive` with `--state.scheme=path`, v1.16.0) | `triedb/pathdb` +19,895, `core/state` +8,998 | Upstream reports the archive database dropping from >20TB to ~1.9TB on Ethereum mainnet, with `--history.state` controlling retention. Directly relevant to our archive deployments. Caveat: `eth_getProof` is then limited to recent blocks |
| **Log index** (`core/filtermaps`, new) | 5,141 | `eth_getLogs` latency for explorer and exchange queries |
| **EraE history** (`core/history` new, `internal/era` +4,066) | medium | History expiry / externalisation, storage reduction |
| Transaction pool | `core/txpool` +7,388 | Upstream reworked blobpool (`buffer`, `cache`, `lookup`, gap handling) — the same area as our `limbo_metadium.go` PoA-finality shim, so the two need reconciling |
| RPC additions | `internal/ethapi` +5,821 | `simulate.go` (`eth_simulateV1`), `capabilities.go`, `override/`, `logtracer.go` |
| Tracing | `eth/tracers` +9,427 | Trace accuracy and cost for archive/trace users |
| P2P | `eth/protocols` +12,204 | `eth/69`–`eth/72`, snap sync rework, peer dropper, blob fetcher, and the delayed-decoding fix in §4 |
| Crypto performance | `crypto/keccak` (new, 6,377), `crypto/secp256k1` +57,834 | CPU only; low priority while nodes are not CPU-bound |
| Tooling | `cmd/workload` (new), `cmd/devp2p` +18,690, `cmd/evm` +11,705 | Test harness modernisation |
| Libraries | `accounts/abi` +9,584, `signer/core` +6,750 | Downstream library consumers |

## 4. Security backports

Advisories published after v1.13.14, with the upstream fix commit located by
walking adjacent release tags, and the state of this tree:

| CVE | Severity | Upstream fix | State here |
|---|---|---|---|
| 2024-32972 | high | (≥ 1.13.15) | Applied |
| 2025-24883 | high | `159fb1a1d crypto: add IsOnCurve check` | Covered: `elliptic.Unmarshal` validates the point for both curves this tree resolves `S256()` to (see below), so `UnmarshalPubkey` does not return an off-curve point. The explicit check has been ported anyway |
| 2026-22862 / 22868 | high / medium | `638741b08 crypto/ecies: use aes blocksize`, `fdfd1235a core/txpool: drop peers on invalid KZG proofs` | Covered by different means: length guard at `symDecrypt`, and peer drop on `ErrInvalidBlob` in `eth/fetcher/tx_fetcher.go` |
| 2026-26314 | high | `895a8597c crypto/secp256k1: fix coordinate check` | Coordinate bound check present in `crypto/secp256k1/curve.go`. The `ext.h` half has now been ported; the non-cgo curve override has not, and does not apply (see below) |
| 2026-26315 | medium | `46bee92f9 crypto/ecies: fix ECIES invalid-curve handling` | Covered: an off-curve point is refused when `Decrypt` decodes it, before ECDH. The `GenerateShared` check has been ported as insurance |
| **2026-26313** | medium | **`0cba803fb eth/protocols/eth, eth/protocols/snap: delayed p2p message decoding`** (v1.17.0) | **The one real gap.** Nothing in the existing hardening covers it; a scoped mitigation is described below |

Three notes:

- CVE-2026-26313 is a memory-exhaustion issue fixed by deferring message
  decoding. It is unrelated to the secp256k1/ECIES hardening in this tree, so
  the existing coordinate guard does not cover it. The comment in
  `crypto/secp256k1/curve.go` listed 26313 alongside 26314/26315 and has been
  corrected.
- **Why the invalid-curve advisories are already covered here, in both build
  modes.** A point arriving over the wire is decoded by `elliptic.Unmarshal`,
  which range-checks it and calls `IsOnCurve` — but only for curves that do not
  implement its unmarshaler interface, which requires both `Unmarshal` and
  `UnmarshalCompressed`. `secp256k1.BitCurve` implements only `Unmarshal`, and
  btcec's `KoblitzCurve` (what `S256()` resolves to without cgo) implements
  neither, so both builds get the validating path. Upstream needed the explicit
  checks because their curve types do implement the interface and thereby skip
  it. Traced, not assumed: a handshake packet carrying an off-curve point is
  refused at that decode with or without the `GenerateShared` guard.
- That coverage is a property of the curve types, not of the protocol code, and
  adding an `UnmarshalCompressed` method to either curve would silently remove
  it. The ported checks make the guarantee local, which is the reason to carry
  them even though they close nothing today.

For CVE-2026-26313 the upstream commit does not transplant: it defers decoding
across the eth/69-72 protocol layout and turns `p2p/tracker` into a per-peer
instance, 26 files against code this tree does not have. The part that closes
the unsolicited path does transplant, and is scoped as its own change: decode
only the request id of a response, confirm it belongs to an in-flight request,
and decode the payload after that. The rest — deferring until the response is
checked against the request's limits, and the same treatment for
`eth/protocols/snap` — belongs with the base jump in Track B. The snap response
path is in any case unreachable here while `--syncmode snap` is rejected
outright.

## 5. Constraints

### 5.1 Rebase conflict surface

Files this fork modified that upstream also churned heavily:

| Ours (lines) | Upstream (lines) | File |
|---:|---:|---|
| 727 | 1,332 | `miner/worker.go` |
| 451 | 1,615 | `internal/ethapi/api.go` |
| 229 | 1,241 | `cmd/utils/flags.go` |
| 210 | 1,034 | `core/txpool/legacypool/legacypool.go` |
| 177 | 1,130 | `params/config.go` |
| 117 | 1,439 | `core/txpool/blobpool/blobpool.go` |
| 105 | 2,360 | `core/blockchain.go` |
| 97 | 1,054 | `core/state_transition.go` |
| 44 | 962 | `core/vm/contracts.go` |

Upstream rewrote `miner/` around the PoS payload builder (−980 / +352) while
this fork added +704 lines to the same file. Any base jump is decided by these
nine files.

### 5.2 The PoA host survives

`consensus/ethash` still exists in v1.17.5 (4 files, 37,564 lines), so the
package this chain's PoA engine is built on has not been removed upstream.

### 5.3 Protocol numbering collides in meaning

This tree advertises `{ETH69, ETH68, ETH66}` where 69 is Metadium's own
definition. Upstream v1.17.5 advertises `{ETH72, ETH71, ETH70, ETH69}` with
message counts 18/18/20/22. The names differ (`meta` vs `eth`), so live
networks are unaffected, but adopting upstream `eth/70+` requires deciding how
to renumber.

### 5.4 Fork axis differs

Forks here are block-numbered (`CamelliaBlock`); every recent upstream fork is
timestamp-gated (`PragueTime`, `OsakaTime`, …). The next fork has to pick an
axis, and a timestamp gate interacts with the block-time assumption and with
how governance carries the setting.

## 6. Recommended sequencing

### Track A — continuous, no fork

Security and node-side improvements on the current base, shipped as ordinary
patch releases. No fork block, no consensus change, so no exchange coordination
window is needed; operators upgrade at their convenience and mixed versions
interoperate. First items:

1. Backport CVE-2026-26313 (`0cba803fb`) — the only outstanding security gap.
2. The defence-in-depth group from §4 plus the comment correction.

### Track B — base jump, still no fork

Move the base from v1.13.14 to a current upstream release (v1.16.x / v1.17.x).
This is not a consensus change, but it is the largest single piece of work:
§5.1 is the conflict surface, and a state-scheme decision (hash vs path)
determines whether nodes migrate or resync.

The reason to sequence it before the fork: **the consensus features in §2 are
already implemented upstream.** After a base jump they exist in the tree and
only need to be gated and tested, so the hard fork becomes an activation and
verification exercise rather than an implementation project. Implementing 7702,
2537, 7951 and 2935 by hand on the v1.13.14 base would cost more and widen the
divergence that makes every later rebase harder.

Track B also carries the §3 items that cannot realistically be backported on
their own — the path-based archive, the log index, and EraE history are
entangled across `triedb/pathdb`, `core/state` and new packages.

### Track C — one hard fork

A single fork activating the §2.1 set once Track B has been deployed and
soaked. One fork event is enough: consensus changes bundle, exactly as Camellia
bundled Shanghai and Cancun. The §2.2 gas items should only join if the
governance decision has been made by then; otherwise they wait for a later
fork rather than delaying this one.

Track A runs throughout and does not wait for B or C.
