# 장애 분석 보고서: Metadium - failed to log the latest block

**작성일:** 2026-05-26
**관련 노드:** 동일 역할 노드 4대 (NODE-A ~ NODE-D)
**발생 블록 범위:** #33945287 ~ #33945299 (관찰 구간)
**심각도:** Medium — 채굴은 정상이나 거버넌스 상태 기록 불가

---

## 1. 현상 요약

매 블록(약 5초 간격)마다 아래 에러가 반복 출력됨:

```text
ERROR [05-19|15:29:09.712] Metadium - failed to log the latest block
    height=33945287 hash=ca9845..4fef83 took=927.678µs
```

동시에 블록 채굴 자체는 정상 동작:

```text
INFO  Successfully sealed new block    number=33945287 ...
INFO  block reached canonical chain   number=33945287 ...
INFO  mined potential block           number=33945287 ...
```

로그 후반부에 Raft 리더십 이전 메시지 발생:

```json
{"msg": "aee7ee9ba2c0c39e [term: 339657] starts to transfer leadership to f93056618b2f1103"}
{"msg": "aee7ee9ba2c0c39e became follower at term 339658"}
{"msg": "raft.node: aee7ee9ba2c0c39e lost leader aee7ee9ba2c0c39e at term 339658"}
```

---

## 2. 원인 분석

### 2.1 에러 발생 경로

```text
[블록 채굴 완료]
    │
    └─▶ LogBlock(height, hash)              metadium/legacy.go:575
            │
            ├─▶ json.Marshal(metaWork)       직렬화 (정상)
            │
            ├─▶ etcdPut("metadium-work", …) metadium/etcdutil.go:614
            │       │
            │       └─▶ etcdIsReady()        metadium/etcdutil.go:155
            │               │
            │               └─▶ ma.etcd != nil
            │                   && ma.etcdCli != nil
            │                   && etcdReady        ← 셋 중 하나라도 false
            │                                          → ErrNotRunning 반환
            │
            └─▶ [실패] "failed to log the latest block" 출력
                        에러 전파 없이 실행 계속 진행
```

### 2.2 핵심 코드

**`LogBlock()` — metadium/legacy.go:575**

```go
func LogBlock(height int64, hash common.Hash) {
    // ...
    _, err = admin.etcdPut(metaWorkKey, string(work))
    if err != nil {
        log.Error("Metadium - failed to log the latest block",
            "height", height, "hash", hash, "took", time.Since(tstart))
    }

    admin.blocksMined++  // 에러 여부와 무관하게 카운터 증가
    // ...
    if admin.blocksMined >= admin.blocksPer && height%admin.blocksPer == 0 {
        // 리더십 이전 시도
        admin.etcdMoveLeader(next.Name)
    }
}
```

**`etcdPut()` — metadium/etcdutil.go:614**

```go
func (ma *metaAdmin) etcdPut(key, value string) (int64, error) {
    if !ma.etcdIsReady() {
        return 0, ErrNotRunning  // etcd 준비 안 됨 → 즉시 실패
    }
    ctx, cancel := context.WithTimeout(context.Background(),
        ma.etcd.Server.Cfg.ReqTimeout())
    defer cancel()
    resp, err := ma.etcdCli.Put(ctx, key, value)
    // ...
}
```

**`etcdIsReady()` — metadium/etcdutil.go:155**

```go
func (ma *metaAdmin) etcdIsReady() bool {
    return ma.etcd != nil && ma.etcdCli != nil && etcdReady
}
```

### 2.3 인과 관계 흐름

```text
[etcd 상태 이상 또는 Raft 리더십 이전]
    │
    ├─▶ etcdReady = false  (etcdutil.go:315 부근)
    │
    ├─▶ etcdPut() → ErrNotRunning (매 블록마다 반복)
    │
    ├─▶ "failed to log the latest block" 반복 출력
    │
    ├─▶ blocksMined는 계속 증가 (에러 무관)
    │
    └─▶ blocksMined >= blocksPer 도달
            └─▶ etcdMoveLeader() 호출
                    └─▶ Raft 리더십 이전 발생
                            └─▶ term 339657 → 339658
                                현 노드 follower로 강등
```

### 2.4 설계상 취약점

| 위치 | 문제 |
| --- | --- |
| `legacy.go:593` | `etcdPut` 실패 시 에러를 무시하고 계속 진행 |
| `legacy.go:601` | `blocksMined++`가 etcd 기록 성공 여부와 무관하게 증가 |
| `etcdutil.go:615` | `etcdIsReady` 실패 시 재시도 로직 없음 |

---

## 3. 영향 범위

| 항목 | 영향 |
| --- | --- |
| 블록 채굴 | **정상** — 로컬 채굴 및 체인 동기화 무관 |
| 거버넌스 상태 기록 | **불가** — etcd에 최신 블록 정보 미기록 |
| 리더십 로테이션 | **오작동 가능** — etcd 기록 실패 상태에서도 리더십 이전 시도 |
| 다른 노드의 상태 인식 | **지연/오류** — etcd에서 최신 작업 정보를 읽지 못함 |

---

## 4. 블록체인 동작에 대한 영향 및 수정 우선순위

### 4.1 블록체인 동작 영향

이 에러는 **체인 자체의 무결성에는 영향을 주지 않지만**, 거버넌스 레이어의 안정성을 서서히 훼손합니다.

#### 영향 없는 항목 (체인 정합성 유지)

- 블록 채굴 및 서명
- 블록 전파 및 피어 동기화
- 트랜잭션 처리 및 상태 전이
- 온체인 데이터 (블록, 트랜잭션, 상태 트리)

#### 영향 있는 항목 (거버넌스/운영 레이어)

| 영향 항목 | 설명 |
| --- | --- |
| 채굴 노드 로테이션 | etcd에 최신 블록이 기록되지 않아 다른 노드가 현재 채굴자를 파악하지 못함 |
| 리더십 이전 타이밍 오차 | `blocksMined` 카운터가 etcd 기록 성공 여부와 무관하게 증가하여 리더십 이전이 예상보다 빨리 또는 늦게 발생 가능 |
| 노드 간 채굴 상태 불일치 | etcd를 통해 채굴 상태를 공유하는 노드들이 stale 데이터를 기반으로 동작 |
| 이중 채굴 가능성 | 리더십 혼선으로 두 노드가 동시에 채굴을 시도할 수 있음 (uncle 블록 발생 가능성 증가) |

#### 시간 경과에 따른 위험도 증가

```text
초기 (수 분)    → 채굴은 정상, etcd 기록 누락만 발생
수십 분 경과   → 리더십 로테이션 오작동, uncle 블록 증가 가능
수 시간 경과   → 노드 간 채굴 역할 혼선 심화, 네트워크 성능 저하
장시간 지속    → etcd 클러스터 쿼럼 손실 위험, 전체 노드 채굴 중단 가능
```

### 4.2 수정 우선순위 판단

#### 즉각 조치가 필요한 경우 (긴급)

아래 중 하나라도 해당하면 **즉시 노드 재시작 또는 etcd 복구** 필요:

- `etcdctl endpoint health` 결과에 unhealthy 노드가 있는 경우
- uncle 블록 비율이 평소보다 현저히 높은 경우
- 로그에서 `"Metadium - yield failed"` 메시지가 함께 발생하는 경우
- 2개 이상의 노드에서 동시에 동일 에러가 발생하는 경우

#### 여유를 갖고 조치해도 되는 경우 (비긴급)

아래 조건을 모두 만족하면 **다음 정기 점검 시 처리** 가능:

- 에러가 단일 노드에서만 발생
- uncle 블록 비율이 정상 범위 내
- `etcdctl endpoint health`에서 클러스터 전체가 healthy
- Raft 리더십이 다른 노드로 정상 이전되어 채굴이 계속됨

#### 코드 수정 우선순위

| 항목 | 우선순위 | 이유 |
| --- | --- | --- |
| 운영 조치 (etcd 복구/노드 재시작) | **긴급** | 현재 발생 중인 에러 해소 |
| `blocksMined` 조건 개선 (§6.1) | **높음** | 리더십 로테이션 오작동 방지 |
| etcdPut 재시도 로직 (§6.2) | **중간** | 일시적 네트워크 오류 내성 강화 |
| etcdReady 자동 복구 워치독 (§6.3) | **낮음** | 장기적 안정성 향상 |

---

## 5. 점검 사항

### 5.1 즉시 점검 (노드 서버에서 실행)

```bash
# etcd 클러스터 전체 상태 확인
etcdctl endpoint health --cluster

# etcd 멤버 목록 및 리더 확인
etcdctl member list

# 현재 etcd 리더 확인
etcdctl endpoint status --cluster -w table

# metadium-work 키 최신 값 확인
etcdctl get metadium-work

# etcd 최근 에러 로그 확인
journalctl -u geth --since "2026-05-19 15:25:00" --until "2026-05-19 15:35:00" | grep -i etcd
```

### 5.2 etcd 상태 진단

```bash
# etcd 프로세스 정상 여부
pgrep -fl etcd

# etcd 데이터 디렉터리 용량 확인 (가득 찬 경우 쓰기 실패)
du -sh /path/to/etcd/data

# etcd alarm 확인 (NOSPACE 등)
etcdctl alarm list

# alarm이 있는 경우 해제 시도
etcdctl alarm disarm
```

### 5.3 노드 간 연결 확인

```bash
# Raft 통신 포트(기본 2380) 연결 확인
nc -zv <peer-node-ip> 2380

# 클라이언트 포트(기본 2379) 연결 확인
nc -zv <peer-node-ip> 2379
```

---

## 6. 후속 조치 방법

### 6.1 etcd가 alarm 상태인 경우

```bash
# alarm 목록 확인
etcdctl alarm list

# NOSPACE alarm 해제 (용량 확보 후)
etcdctl alarm disarm

# compaction으로 etcd 용량 확보
etcdctl compaction $(etcdctl endpoint status --write-out="json" | jq '.[0].Status.header.revision')
etcdctl defrag
```

### 6.2 etcdReady가 false로 고착된 경우

etcd 임베디드 서버가 재초기화되지 않으면 `etcdReady`는 `true`로 돌아오지 않습니다.
이 경우 **노드 재시작**이 필요합니다:

```bash
# geth(go-metadium) 프로세스 재시작
sudo systemctl restart geth

# 재시작 후 로그에서 정상 복구 확인
journalctl -u geth -f | grep -E "logged the latest block|failed to log"
```

### 6.3 etcd 클러스터 쿼럼 손실 시

3노드 클러스터에서 2노드 이상 장애 시 쿼럼 손실. 이 경우:

```bash
# 강제 새 클러스터 구성 (마지막 수단, 데이터 손실 가능성 있음)
etcd --force-new-cluster

# 또는 스냅샷에서 복구
etcdctl snapshot restore <snapshot-file> --data-dir <new-data-dir>
```

### 6.4 정상 복구 확인 기준

아래 로그가 출력되면 정상 복구된 것입니다:

```text
INFO  Metadium - logged the latest block  height=XXXXXXXX hash=... took=...
```

---

## 7. 코드 개선 권고사항

### 7.1 `blocksMined` 카운터 조건 개선 (legacy.go:601)

etcd 기록 실패 시 카운터를 증가시키지 않아야 올바른 리더십 로테이션이 가능합니다:

```go
// 현재 코드
_, err = admin.etcdPut(metaWorkKey, string(work))
if err != nil {
    log.Error("Metadium - failed to log the latest block", ...)
}
admin.blocksMined++  // 항상 증가

// 개선안
_, err = admin.etcdPut(metaWorkKey, string(work))
if err != nil {
    log.Error("Metadium - failed to log the latest block", ...)
    return  // 실패 시 카운터 증가 및 리더십 로테이션 스킵
}
admin.blocksMined++  // 성공 시에만 증가
```

### 7.2 etcdPut 재시도 로직 추가 (etcdutil.go:614)

일시적인 네트워크 오류에 대한 재시도를 추가하면 불필요한 에러 로그를 줄일 수 있습니다:

```go
func (ma *metaAdmin) etcdPutWithRetry(key, value string, maxRetry int) (int64, error) {
    var (
        rev int64
        err error
    )
    for i := 0; i < maxRetry; i++ {
        rev, err = ma.etcdPut(key, value)
        if err == nil {
            return rev, nil
        }
        if err == ErrNotRunning {
            return 0, err  // 준비 안 됨은 재시도 불필요
        }
        time.Sleep(time.Duration(i+1) * 100 * time.Millisecond)
    }
    return 0, err
}
```

### 7.3 etcdReady 복구 메커니즘

`etcdReady = false`가 된 후 자동으로 재연결을 시도하는 워치독 고루틴을 추가하는 것을 권고합니다.

---

## 8. 참고 코드 위치

| 파일 | 라인 | 설명 |
| --- | --- | --- |
| `metadium/legacy.go` | 575–622 | `LogBlock()` 함수 전체 |
| `metadium/etcdutil.go` | 614–628 | `etcdPut()` 함수 |
| `metadium/etcdutil.go` | 155–157 | `etcdIsReady()` 함수 |
| `metadium/etcdutil.go` | 312, 315 | `etcdReady` 플래그 토글 위치 |
| `metadium/admin.go` | 106–109 | `metaWork` 구조체 정의 |
| `metadium/admin.go` | 408–457 | `getMinerNodes()` 함수 |
