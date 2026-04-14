# Metadium Camellia Fork - Security Audit Report

**Date:** 2026-04-14
**Branch:** `security` (based on `camellia`)
**Scope:** Blockchain core infrastructure vulnerability assessment (feature-preserving)

---

## Executive Summary

A comprehensive security audit was conducted across 5 domains: P2P/Consensus, RPC/API, Crypto/Key Management, Transaction/State, and Script/Config. The Camellia fork features were not modified; this audit was conducted purely from a security perspective.

| Severity | Count | Key Areas |
|----------|-------|-----------|
| CRITICAL | 4 | FeePayer unverified in block execution, P2P sender cache poisoning, block signature init bypass, admin RPC runtime rebinding |
| HIGH | 12 | Etcd security, RPC filesystem access, fee delegation signer, script injection |
| MEDIUM | 14 | DoS vectors, mempool pollution, uint256 overflow, Docker/build security |
| LOW | 8 | Information disclosure, shallow copy, hardcoded test keys |

---

## CRITICAL Findings

### C1. FeePayer Address Not Cryptographically Verified During Block Execution

**Files:**
- `core/state_transition.go:171` -- `TransactionToMessage()`
- `core/state_processor.go:86-91`

**Description:** During block execution, `TransactionToMessage()` reads the FeePayer address directly from the tx struct field without recovering it from the ECDSA signature (FV/FR/FS). Validation only occurs at txpool ingress, not in the block processor.

**Impact:** A malicious PoA authority node can bypass the txpool and include a tx with a forged FeePayer address directly in a block. `buyGas()` deducts gas costs from the forged FeePayer account, enabling arbitrary fund theft.

**Remediation:** Add `types.FeePayer(feeDelegateSigner, tx)` recovery and comparison in `state_processor.go` or `TransactionToMessage()`.

---

### C2. TransactionsEx trustIt -- P2P Sender Cache Poisoning for Account Impersonation

**Files:**
- `eth/protocols/eth/metadium_handlers.go:97`
- `core/types/transaction.go:593-598`

**Description:** When a `TransactionsExMsg` is received from a partner (governance member) node with `trustIt=true`, the peer-supplied `From` address is cached directly in the sender cache without signature verification. Subsequent `types.Sender()` calls return the cached value as-is.

**Impact:** A single compromised partner node can forge transactions with arbitrary sender addresses and inject them into the network -- enabling fund theft, contract calls, and governance manipulation.

**Remediation:** Remove the `trustIt` path or enforce `Sender(signer, tx) == i.From` verification before caching.

---

### C3. Block Signature Verification Bypass During Governance Initialization

**Files:**
- `metadium/admin.go:1669-1670`
- `consensus/ethash/consensus.go:304`

**Description:** `verifyBlockSig` returns `true` (signature valid) when governance contracts are not initialized (`ErrNotInitialized`) or when member count is 0. During node bootstrap, governance upgrades, or transient RPC failures, all block signatures are unconditionally accepted.

**Impact:** Forged block injection during initialization windows, potentially causing chain forks or invalid state transitions.

**Remediation:** Return `false` when governance is not initialized. Queue blocks for later verification.

---

### C4. admin_startHTTP Allows Runtime RPC Rebinding -- Full Node Takeover

**File:** `node/api.go:159-212`

**Description:** `admin_startHTTP` accepts caller-provided host, port, cors, apis, and vhosts parameters. When the `admin` namespace is exposed over HTTP, an attacker can call `admin_startHTTP("0.0.0.0", 8545, "*", "admin,debug,personal,miner", "*")` to expose all admin APIs to all interfaces.

**Impact:** Full administrative API exposure -- peer add/remove, mining control, wallet access, etcd manipulation.

**Remediation:** Restrict to IPC-only or prevent expanding API modules beyond the originally configured set.

---

## HIGH Findings

### H1. FeePayer() Function Accepts Wrong Signer Type

**File:** `core/types/transaction_signing.go:176-193`

`FeePayer()` accepts any `Signer` interface and calls `signer.Sender(tx)`. If a `londonSigner` is passed instead of `feeDelegateSigner`, it recovers the sender address from V/R/S instead of the fee payer from FV/FR/FS. No type-level enforcement exists.

### H2. Etcd Client on Unencrypted HTTP Without Authentication

**File:** `metadium/etcdutil.go:136-138`

Bound to `http://localhost:{port+2}`. No TLS, no authentication. Any local process can steal mining tokens or corrupt cluster state.

### H3. Etcd Auto-TLS with 10-Year Self-Signed Certificates

**File:** `metadium/etcdutil.go:124-126`

No certificate pinning, no CA verification, no rotation mechanism. MITM attacks on etcd peer communication are possible.

### H4. StatusEx Enables Unverified Peer TD Manipulation

**File:** `eth/protocols/eth/metadium_handlers.go:47-48`

`LatestBlockHash` existence is not verified before accepting TD updates. A malicious partner can advertise extremely high TD to manipulate sync.

### H5. EtcdCluster Message Enables Rogue Cluster Injection

**File:** `eth/protocols/eth/metadium_handlers.go:71-83`

Cluster connection string is forwarded to `etcdJoin` without validation. Attacker can redirect node to an attacker-controlled etcd server.

### H6. debug_etcdPut/Get/Delete -- Unrestricted Etcd Read/Write/Delete

**File:** `internal/debug/api.go:288-299`

No authentication, no key prefix restriction, no rate limiting for full etcd access. Enables governance/consensus data manipulation.

### H7. Debug File Write APIs -- Arbitrary Filesystem Path Access

**File:** `internal/debug/api.go:99-200`

7 APIs (`startCPUProfile`, `goTrace`, `blockProfile`, etc.) accept a `file` parameter and write to arbitrary paths. No path validation.

### H8. eth_signRawFeeDelegateTransaction -- Sender Signature Not Verified Before Co-signing

**File:** `internal/ethapi/api.go:2487-2530`

Raw tx V/R/S signature validity is not verified (ecrecover) before the FeePayer co-signs. A fee payer can be tricked into signing a tx with a forged sender.

### H9. gmet.sh init_gov -- JavaScript Injection

**File:** `metadium/scripts/gmet.sh:103`

`${ACCT}` variable is directly interpolated into `--exec` JS string. Malicious ACCT values can execute arbitrary JS in gmet console.

### H10. gmet.sh .rc File Sourcing -- Arbitrary Code Execution

**File:** `metadium/scripts/gmet.sh:143`

`source "$d/.rc"` is executed unconditionally. An attacker with write access to the data directory can inject arbitrary shell commands.

### H11. deploy-governance.js eval() -- Config File Code Injection

**File:** `metadium/scripts/deploy-governance.js:228`

`eval("var data = " + data)` for JSON parsing. A tampered config.json can execute arbitrary JS in gmet console.

### H12. gmet.sh stop -- eval() on Etcd Data

**File:** `metadium/scripts/gmet.sh:257`

`eval("token = " + token)`. A compromised etcd peer can execute arbitrary code during node shutdown.

---

## MEDIUM Findings

### M1. RPC Bound to 0.0.0.0 with Wildcard CORS and Sensitive APIs Exposed

**Files:** `metadium/scripts/gmet.sh:146,148`, `tests/private-net-poa/docker-compose.yml`

`--http.addr 0.0.0.0`, `--http.corsdomain "*"`, personal/admin/debug/miner APIs exposed, `--allow-insecure-unlock`.

### M2. Fee Delegation buyGas uint256 Overflow Ignored

**File:** `core/state_transition.go:267`

`uint256.FromBig(mgval)` overflow return value discarded with `_`. Extreme gas prices could lead to fee payer undercharge.

### M3. Fee Payer Balance Not Checked in Txpool -- Mempool Pollution

**File:** `core/txpool/validation.go:224-228`

Fee payer balance is only validated at block execution time. Insufficient-balance fee delegation txs occupy mempool slots.

### M4. ExcessBlobGas big.Int -> uint64 Silent Truncation

**Files:** `core/evm.go:59`, `miner/worker.go:1330`

`.Uint64()` silent truncation. No bounds check.

### M5. Blob Transactions on PoA -- No Beacon Chain Data Availability

**File:** `miner/worker.go` (Camellia blob handling)

Blob sidecar is lost after block inclusion. No DA guarantees.

### M6. SendRawTransactions Unbounded Batch Size

**File:** `internal/ethapi/api.go:2078-2095`

No array size limit. Single RPC call can flood the mempool.

### M7. Private Key Bytes Not Zeroed After Encryption

**File:** `accounts/keystore/passphrase.go:185-186`

`keyBytes` remains in memory until GC. Plaintext private key exposure.

### M8. VRF Scalar Multiplication Possibly Not Constant-Time

**File:** `crypto/vrf/vrf.go:326-337`

Timing side-channel may leak VRF key bits depending on Go compiler/architecture.

### M9. PBKDF2 Parameters Not Validated on Decryption

**File:** `accounts/keystore/passphrase.go:346-352`

Tampered keystore file can set `c=1`, making brute-force trivially fast.

### M10. math/rand Used for RLPx Handshake Padding Length

**File:** `p2p/rlpx/rlpx.go:634`

Non-cryptographic PRNG. Assists traffic fingerprinting.

### M11. No Rate Limiting on Metadium P2P Message Handlers

**File:** `eth/protocols/eth/metadium_handlers.go`

No per-peer rate limit on any Metadium message handler. Goroutine explosion, DoS.

### M12. No Replay Protection on Metadium P2P Messages

**File:** `eth/protocols/eth/metadium_protocol.go`

No nonce/timestamp. Compromised partner key enables infinite message replay.

### M13. IsPartner Governance Refresh Delay (100 blocks)

**File:** `metadium/admin.go:1747-1763`

Removed nodes retain partner privileges for up to 100 blocks.

### M14. Miner Limit Disabled When Member Count <= 2

**File:** `metadium/miner_limit.go:149-152`

Single-node block monopoly possible in small governance configurations.

---

## LOW Findings

| # | Finding | File |
|---|---------|------|
| L1 | Hardcoded test private keys (Hardhat defaults) | `tests/private-net-poa/setup.sh:35-37` |
| L2 | Password file created without restrictive permissions (0644) | `tests/private-net-poa/setup.sh:31` |
| L3 | Password exposed on command-line in deploy.sh | `tests/private-net-poa/deploy.sh:174-177` |
| L4 | Docker uses `alpine:latest` non-deterministic tag | `Dockerfile:22` |
| L5 | Docker containers run as root | `Dockerfile`, `Dockerfile.metadium` |
| L6 | solc binary downloaded without checksum verification | `Makefile:205-213` |
| L7 | Go toolchain downloaded without checksum (Dockerfile.metadium) | `Dockerfile.metadium:14` |
| L8 | Block headers expose minerNodeId/minerNodeSig | `internal/ethapi/api.go` (RPCMarshalHeader) |

---

## Priority Action Plan

### Immediate (< 1 day)

| # | Action | Finding |
|---|--------|---------|
| 1 | Add FeePayer signature verification in `state_processor.go` | C1 |
| 2 | Remove `trustIt` path or enforce signature verification in `TxExs2Txs` | C2 |
| 3 | Return `false` from `verifyBlockSig` when governance is uninitialized | C3 |
| 4 | Replace `eval()` with `JSON.parse()` in deploy-governance.js | H11 |
| 5 | Replace `eval()` with `JSON.parse()` in gmet.sh stop | H12 |

### Short-term (< 1 week)

| # | Action | Finding |
|---|--------|---------|
| 6 | Restrict `admin_startHTTP/WS` to IPC-only or block API expansion | C4 |
| 7 | Remove or disable `debug_etcdPut/Delete` | H6 |
| 8 | Add sender ecrecover verification to `SignRawFeeDelegateTransaction` | H8 |
| 9 | Validate `${ACCT}` input in gmet.sh | H9 |
| 10 | Change default `--http.addr` to `127.0.0.1` in gmet.sh | M1 |
| 11 | Add batch size limit to `SendRawTransactions` | M6 |
| 12 | Add fee payer balance validation in txpool | M3 |

### Long-term (> 1 week)

| # | Action | Finding |
|---|--------|---------|
| 13 | Introduce etcd PKI (self-signed -> CA-based) | H2, H3 |
| 14 | Strengthen StatusEx/EtcdCluster message validation | H4, H5 |
| 15 | Add P2P rate limiting + replay protection | M11, M12 |
| 16 | Docker security hardening (non-root, pinning, checksums) | L4-L7 |
| 17 | Review VRF constant-time implementation | M8 |
