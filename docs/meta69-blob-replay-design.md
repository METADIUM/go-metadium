# meta/69 Design — Blob Sidecar Serving (M5) + Meta-Message Replay Protection (M12)

**Date:** 2026-06-17
**Author:** Jeffrey Song
**Status:** Design (pre-implementation) — to be reviewed before coding
**Targets:** phase-2 items #3 (M5) and #4 (M12), bundled into a new protocol
version and rolled out **with the Camellia mainnet hard fork (2026-08~09)**

---

## 1. Why one bundle, one new version

Both M5 and M12 change the `meta` wire protocol:
- **M12** adds replay-protection fields to existing meta messages → a wire-format
  change (old/new nodes cannot decode each other's frames).
- **M5** adds two new message codes for blob-sidecar request/response.

There is no per-message capability negotiation, so the only safe discriminator is
the **negotiated protocol version** (`handshake.go:101` enforces
`status.ProtocolVersion == p.version`; dispatch is version-keyed in
`handler.go:226`). Doing these piecemeal would force two separate lockstep
upgrades of all governance/partner nodes. Bundling them into **one new version,
`meta/69`**, rolled out with the Camellia fork (which already mandates a
coordinated upgrade), amortizes a single coordinated event.

---

## 2. Current state (baseline)

- `ProtocolName = "meta"`; `ProtocolVersions = {ETH68, ETH66}` (`protocol.go`).
  `ETH66=66`, `ETH68=68`. `protocolLengths = {68:23, 66:23}`.
- Meta message codes: `GetPendingTxsMsg 0x11`, `GetStatusExMsg 0x12`,
  `StatusExMsg 0x13`, `EtcdAddMemberMsg 0x14`, `EtcdClusterMsg 0x15`,
  `TransactionsExMsg 0x16` (highest = 0x16).
- Handler maps `eth68` / `eth66` (`handler.go:165/189`); dispatch:
  `if peer.Version() >= ETH68 { eth68 } else { eth66 }`.
- Meta packets (`metadium_protocol.go`) carry **no nonce/timestamp**:
  `GetStatusExPacket int`, `StatusExPacket = metaapi.MetadiumMinerStatus`,
  `EtcdAddMemberPacket int`, `EtcdClusterPacket string`.
- **No blob-sidecar P2P serving**: a node that learns a block via propagation but
  never mempool-fetched its blob txs has permanently missing sidecar DA
  (`core/blockchain.go:1855` `BlobSidecarFn` returns nil → not stored). Blob txs
  are not broadcast (`handler_eth.go:76`).
- **Interop constraint:** the live mainnet is public and the official sealers run
  **Gmet/v0.10.2** (speaking meta/66 or meta/68). Our nodes MUST keep meta/66+68
  for interop with them. meta/69 is negotiated **only between Camellia-upgraded
  nodes**; legacy links stay on meta/66/68.

---

## 3. meta/69 introduction (scaffolding)

- Add `ETH69 = 69`. `ProtocolVersions = {ETH69, ETH68, ETH66}` (69 primary).
- `protocolLengths[ETH69] = 25` (covers new `0x17`, `0x18`); keep 68/66 at 23.
- New handler map `eth69` = `eth68` + the two blob handlers + replay-aware meta
  handlers (see §4, §5). Dispatch becomes:
  `if v >= ETH69 { eth69 } else if v >= ETH68 { eth68 } else { eth66 }`.
- devp2p already negotiates the highest common version; no handshake change beyond
  registering 69. A node only uses meta/69 features with peers that also
  negotiated 69.

---

## 4. M12 — replay protection (meta/69 only)

**Threat:** meta messages (StatusEx/EtcdCluster/EtcdAddMember/GetStatusEx) carry
no freshness token (`metadium_handlers.go`); a captured partner frame can be
replayed. Most damaging: a stale `EtcdCluster`/`EtcdAddMember` replayed into etcd
self-healing. Today only `IsPartner` + the M11 semaphores gate these.

**Design:**
- Define meta/69 packet variants with `Nonce uint64` + `Timestamp uint64`
  (unix seconds). The bare-`int` request packets (`GetStatusExPacket`,
  `EtcdAddMemberPacket`, currently sent as `common.Big1`) become structs.
- **Version-aware encode/decode**: meta/69 peers exchange the struct-with-fields
  form; meta/66/68 peers keep the legacy form. Selection is by `peer.Version()`
  at send time and by the handler map at receive time (legacy handlers decode the
  legacy shape, eth69 handlers decode the new shape).
- **Receiver state** (new per-`Peer` fields): track the last accepted
  `(nonce, timestamp)` per message kind per peer. Reject if `nonce <= lastNonce`
  or `|now - timestamp| > skew` (bounded clock skew, e.g. 30s). Drop or ignore on
  violation (do not feed etcd).
- **Authentication note:** nonce/timestamp prevents *replay* but not forgery by a
  party that can produce valid frames. These messages are already `IsPartner`-
  gated; binding the token into the existing block-signature scheme (sign the
  `(payload, nonce, timestamp)`) would also prevent forgery — evaluate as a
  follow-up; replay protection is the in-scope minimum.
- **Rollout reality:** replay protection is effective only on meta/69 links. Until
  all partner nodes are on meta/69 (post-fork), legacy links remain unprotected —
  acceptable for the transition window; full coverage once meta/66/68 are retired.

---

## 5. M5 — blob sidecar serving (meta/69 only)

**Goal:** any node that imports a block with blob txs can obtain and persist the
sidecars even if it never had the blob tx in its local pool.

**New messages** (meta/69):
- `GetBlobSidecarsMsg = 0x17` — request: a block hash (or a small list of block
  hashes / blob versioned hashes).
- `BlobSidecarsMsg = 0x18` — response: the `[]*types.BlobTxSidecar` for the
  requested block(s).

**Serve side:** handler reads from `rawdb.ReadBlobSidecars` (already exists) and
replies; bound the response size (reuse `maxMessageSize` and a per-request cap;
enforce the EIP-7594 max-blob limit, **#32246**).

**Request side:** after importing a block where `BlobSidecarFn` returned nil for
some blob txs (`core/blockchain.go:1855`), enqueue a sidecar fetch from a meta/69
peer; on response, validate (below) and persist via `rawdb.WriteBlobSidecars`.

**Validation on receipt (#31219):** verify received sidecar commitments against
the block's blob versioned hashes; reuse `validateBlobSidecar`
(`core/txpool/validation.go`) — which already wraps `txpool.ErrInvalidBlob` after
batch 2 — and **drop the peer on mismatch** (batch 2 already drops on the fetcher
path; mirror it here).

**Primary path hardening (#30125):** backport the fetcher ordering fix so blob
txs reliably land in the pool before the block arrives; the new message is the
fallback when they don't.

**Activation:** gated on Camellia blob support. Mainnet `params CamelliaBlock` is
currently `nil` (no blob txs on mainnet yet), so there is **no mainnet exposure
until Camellia is activated**; testnet already runs Camellia. M5 is a **release
blocker for flipping mainnet `CamelliaBlock`**, not for the current chain.

---

## 6. Rollout & fork sequencing

1. Implement meta/69 (scaffolding + M12 + M5) behind the new version; keep
   meta/66/68 fully functional for interop with official v0.10.2 nodes.
2. Verify on SPoA (all nodes meta/69) + a mixed meta/68↔meta/69 interop test +
   blob-tx-e2e + replay unit tests.
3. Ship the binary ahead of the Camellia mainnet fork; partner nodes upgrade in
   the same coordinated window as the fork.
4. Order: land #30125 fetcher fix + M5 serve/fetch + #31219/#32246 validation
   **before** flipping mainnet `CamelliaBlock`. M12 rides the same version.
5. After all partner nodes are on meta/69, retire meta/66/68 (drop from
   `ProtocolVersions`) in a later release to make replay protection universal.

---

## 7. Implementation phases (proposed)

- **P1 — meta/69 scaffolding**: `ETH69` const, `ProtocolVersions`,
  `protocolLengths`, `eth69` handler map (= eth68 initially), dispatch. No
  behavior change. Build + SPoA smoke.
- **P2 — M12 replay**: version-aware meta packets with nonce/timestamp, per-peer
  tracking, reject stale. Unit tests for accept/reject; SPoA (meta/69) + mixed
  interop.
- **P3 — M5 serving**: `0x17/0x18` messages + handlers + fetch-trigger + persist +
  #31219/#32246 validation + #30125 ordering. blob-tx-e2e (valid) + an
  invalid-sidecar peer-drop test.
- Each phase: own commits on a phase-2/fork branch, CGO unit tests + SPoA, no node
  deploy until the coordinated fork window.

**Implementation status (secfix/mainnet-phase2):** P1 done (e35de7f39),
P2 done (16f953018), **P3 done (5f8eed04b)** — `GetBlobSidecars`/`BlobSidecars`
packets, serve via `BlockChain.GetBlobSidecars`, `MissingBlobSidecarFn` import
trigger + serial fetch loop in `eth/metadium_blobsync.go`, `txpool.ValidateBlobSidecar`
+ versioned-hash/KZG check with peer-drop (#31219), per-block cap inherent (#32246).
Verified: CGO=0/CGO=1 build, unit tests, SPoA meta/69 PASS=19/0/2, blob-tx-e2e ALL
PASS, and a cross-node M5 e2e (node2/3 — which never pooled the blob tx — obtain and
persist the sidecar after importing node1's block). **Remaining:** the #30125 fetcher
ordering backport is primary-path hardening (the M5 message is the fallback when blob
txs don't pre-land in the pool), tracked as a separate lower-priority follow-up.

## 8. Risks

- Protocol-version change is lockstep: a bug in version gating can partition the
  partner set. Mitigate with the mixed meta/68↔69 interop test.
- Replay-protection clock-skew window must tolerate real node clock drift; too
  tight → false rejects of honest partners.
- M5 fetch/serve adds p2p surface; bound response size + apply the same
  ErrInvalidBlob peer-drop as the txpool path.
