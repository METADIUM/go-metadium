# TODOS

Deferred work tracked from /plan-ceo-review (2026-04-04), /plan-eng-review (2026-04-05), and Camellia test session (2026-04-08).

---

## P1 -- mainnet activation blockers

### [ ] Deploy 4-node private network on server 36

**What:** Set up a long-running private network on server 36 (10.150.255.36) with 3 miner nodes + 1 sync node, mixed LevelDB/RocksDB, CamelliaBlock=100.

**Why:** Need multi-day stability testing with governance rewards before activating Camellia on testnet/mainnet.

**How to apply:**
1. Build LevelDB and RocksDB Docker images
2. Deploy 4-node docker-compose (Node 1: Miner/LevelDB, Node 2: Miner/RocksDB, Node 3: Miner/LevelDB, Node 4: Sync/RocksDB)
3. Deploy governance contracts
4. Monitor for: block production, governance rewards, blobpool stability, memory/disk usage
5. Run `rpc-test-full.sh` daily

**Effort:** M (human ~4h / CC+gstack ~30min)
**Priority:** P1
**Depends on:** Server 36 access (verified: cplabs@10.150.255.36)

---

### [ ] Layer 4: Rolling upgrade simulation (private-net-poa)

**What:** Run private-net-poa with one node on old binary (CamelliaBlock=nil) and others on new binary. Verify mixed operation at and after fork block.

**Why:** Mainnet upgrade will not be atomic. If old nodes reject new blocks during fork transition, chain can split.

**How to apply:**
1. Start node1 with old geth binary (CamelliaBlock=nil), nodes 2+3 with new binary (CamelliaBlock=100)
2. Wait for block 100
3. Verify all three nodes stay on the same chain head
4. Upgrade node1 to new binary mid-run, verify re-sync

**Effort:** M (human ~4h / CC+gstack ~20min)
**Priority:** P1 -- must complete before mainnet CamelliaBlock is set
**Depends on:** Old gmet binary preserved

---

### [ ] Testnet Camellia fork block activation

**What:** Set CamelliaBlock on testnet config and deploy to servers 25, 151.

**Why:** Validate fork transition on live testnet with real peers and DApp traffic.

**How to apply:**
1. Complete server 36 long-term validation first
2. Set `CamelliaBlock` in `MetadiumTestnetChainConfig` to a future testnet block number
3. Deploy to servers 25 and 151
4. Monitor fork transition

**Effort:** S (human ~2h / CC+gstack ~15min)
**Priority:** P1
**Depends on:** Server 36 validation passing

---

### [ ] Mainnet Camellia fork block activation

**What:** Set CamelliaBlock on mainnet config and deploy to all 9 mainnet nodes.

**Why:** Final production activation of Shanghai + Cancun EIPs.

**How to apply:**
1. Complete testnet fork validation first
2. Deploy canary node (1 of 9) with CamelliaBlock set
3. If stable, deploy remaining 8 nodes
4. Fork block should be at least 1 week in the future

**Effort:** M (human ~8h / CC+gstack ~30min)
**Priority:** P1
**Depends on:** Testnet fork validation passing

---

## P2 -- improvements

### [ ] GitHub Actions SHA pinning

**What:** Pin GitHub Actions to full SHA instead of version tags.

**Why:** Security audit finding (MEDIUM). Tag-pinned actions can be moved by upstream maintainers.

**Effort:** S (human ~1h / CC+gstack ~5min)
**Priority:** P2

---

### [ ] Redeploy servers 25/150/151 with latest commits

**What:** Deploy commits after `98d29566` (test fixes, shellcheck, rpc script update) to production servers.

**Why:** These are test-only and script changes with no functional impact on node operation. Low priority.

**Effort:** S (human ~30min / CC+gstack ~5min)
**Priority:** P2

---

### [ ] genesis.go IsCamellia check for WithdrawalsHash/blob gas fields

**What:** Add IsCamellia() check to genesis.go ToBlock() so CamelliaBlock=0 + ShanghaiTime=nil configs correctly initialize WithdrawalsHash and blob gas fields in the genesis block.

**Why:** genesis block state may be inconsistent with post-genesis blocks. Currently tests pass and runtime is unaffected, but correctness improvement for fork transition edge cases.

**Effort:** S (human ~1h / CC+gstack ~10min)
**Priority:** P2
**Depends on:** None

---

## Completed

### [x] Camellia fork implementation and testing (2026-04-08)

All EIP implementations verified: EIP-3855 (PUSH0), EIP-1153 (TLOAD/TSTORE), EIP-5656 (MCOPY), EIP-4844 (Blob Tx), EIP-3651 (Warm COINBASE), EIP-6780 (SELFDESTRUCT restriction), EIP-3860 (initcode size limit), Type 22 Fee Delegation.
- `make test`: 119 packages PASS, 0 FAIL
- `camellia-test.sh`: 14 PASS, 0 FAIL
- `blob-tx-e2e` + `mixed-tx-e2e`: ALL PASS
- `rpc-test-full.sh`: 67 PASS, 0 FAIL
- Code health: 10.0/10
- See `docs/camellia-test-report.md` for full details.

### [x] Update camellia-verification-checklist.md with actual completion status
**Completed:** 2026-04-05

### [x] /plan-eng-review fixes (2026-04-05)
4 issues fixed: clique ExcessBlobGas nil bug, initExcessBlobGas helper, ErrBlobGasExceeded rename, TestBlobTxPreCheckErrors added.

### [x] Layer 6: Server sync verification (2026-04-08)
All 4 server/DB combinations verified syncing with v1.0.0-stable:
- 192.168.0.25 -- testnet LevelDB (block 84,626,806)
- 192.168.0.150 -- mainnet LevelDB (block 111,664,697)
- 192.168.0.150 -- mainnet RocksDB (block 111,664,698)
- 192.168.0.151 -- testnet RocksDB (block 84,626,806)

---

## NOT in scope

- **EIP-4788 (Beacon roots):** PoA chain, no beacon chain. N/A.
- **EIP-4895 (Withdrawals):** PoA chain, no validator withdrawals. Metadium sets EmptyWithdrawalsHash for Camellia blocks but does not process actual withdrawals.
- **Upstream merge conflicts:** Separate track from Camellia verification (see prior learning: camellia-two-track-problem).
