# TODOS

Deferred work tracked from /plan-ceo-review (2026-04-04) and /plan-eng-review (2026-04-05, feature/camellia).

---

## P1 — mainnet activation blockers

### [x] Layer 4: Rolling upgrade simulation (private-net-poa)

**What:** Run private-net-poa with one node on old binary (CamelliaBlock=nil) and two on new binary. Verify mixed operation at and after fork block.

**Why:** Mainnet upgrade will not be atomic — some validators will upgrade before others. If old nodes reject new blocks or vice versa during the fork transition, chain can split.

**How to apply:** Add a scenario to `tests/private-net-poa/` that:
1. Starts node1 with old geth binary (CamelliaBlock=nil), nodes 2+3 with new binary (CamelliaBlock=100)
2. Waits for block 100
3. Verifies all three nodes stay on the same chain head
4. Upgrades node1 to new binary mid-run, verifies re-sync

**Effort:** M (human ~4h / CC+gstack ~20min)
**Priority:** P1 — must complete before mainnet CamelliaBlock is set
**Depends on:** Old gmet binary preserved (keep `gmet.bak` from testnet deployment)

---

## P2 — documentation / process

### [x] Update camellia-verification-checklist.md with actual completion status

**Completed 2026-04-05:** Layer 5 SPoA 통합 테스트 22/22 PASS 포함 전체 체크리스트 반영 완료.

---

### [x] /plan-eng-review 수정 사항 (2026-04-05)

수정 완료된 4개 이슈:
- `consensus/clique/clique.go` — `Prepare()` fork transition ExcessBlobGas nil 버그 수정
- `miner/worker.go` — `initExcessBlobGas()` 헬퍼 추출 (중복 제거)
- `core/error.go` — `ErrBlobGasExceeded` → `ErrBlobGasLimitExceeded` 재명명 및 두 호출부 wrap
- `core/camellia_integration_test.go` — `TestBlobTxPreCheckErrors` 추가 (ErrBlobFeeCapTooLow, ErrBlobCountExceeded)

---

### [x] Layer 6: 실서버 동기화 검증 (2026-04-05)

testnet/mainnet × LevelDB/RocksDB 4개 조합 전체 완료:
- 192.168.0.25 — testnet LevelDB → block 84,506,387 DONE
- 192.168.0.150 — mainnet LevelDB → block 111,544,279 DONE
- 192.168.0.150 — mainnet RocksDB → block 111,544,280 DONE
- 192.168.0.151 — testnet RocksDB → block 84,506,388 DONE

---

## NOT in scope (confirmed 2026-04-04)

- **T-10 (Type 22 + Blob tx):** Metadium does not use blob transactions. Confirmed N/A.
- **EIP-4788 (Beacon roots):** PoA chain, no beacon chain. N/A.
- **EIP-4895 (Withdrawals):** PoA chain, no validator withdrawals. N/A.
- **Upstream merge conflicts:** Separate track from Camellia verification (see prior learning: camellia-two-track-problem).
