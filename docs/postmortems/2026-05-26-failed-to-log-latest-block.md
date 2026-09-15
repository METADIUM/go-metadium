# 장애 분석 보고서: Metadium - failed to log the latest block

**작성일:** 2026-05-26
**관련 네트워크:** 구축형 프라이빗 네트워크 (기관명 비공개), BP 4대
**관련 노드:** 동일 역할 노드 4대 (NODE-A ~ NODE-D)
**발생 블록 범위:** #33945287 ~ #33945299 (관찰 구간)
**심각도:** Medium — 채굴은 정상이나 거버넌스 상태 기록 불가

---

## 1. 현상 요약

> **메인넷도 공개 테스트넷도 아닙니다.** 메인넷은 2026-05-19 시점 약 113.4M 높이에
> BP 9대(Camellia `117_764_000` @ 2026-08-27 기준 역산), 공개 테스트넷은 약 86.4M
> (Camellia `86_449_000` @ 2026-05-20)입니다. 아래 블록 번호는 어느 쪽과도 맞지
> 않습니다.
>
> 이 문서의 블록 번호와 노드 수를 메인넷 기준으로 읽으면 §3의 영향 범위와 §4.2의
> 긴급 임계("2개 이상의 노드에서 동시 발생")가 달라집니다. **BP 4대 클러스터는
> 2대 장애만으로 쿼럼을 잃습니다.**

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
    └─▶ LogBlock(height, hash)              metadium/legacy.go:588
            │
            ├─▶ json.Marshal(metaWork)       직렬화 (정상)
            │
            ├─▶ etcdPut("metadium-work", …) metadium/etcdutil.go:640
            │       │
            │       └─▶ etcdIsReady()        metadium/etcdutil.go:175
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

**`LogBlock()` — metadium/legacy.go:588**

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

**`etcdPut()` — metadium/etcdutil.go:640**

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

**`etcdIsReady()` — metadium/etcdutil.go:175**

```go
func (ma *metaAdmin) etcdIsReady() bool {
    return ma.etcd != nil && ma.etcdCli != nil && etcdReady
}
```

### 2.3 인과 관계 흐름

```text
[etcd 상태 이상 또는 Raft 리더십 이전]
    │
    ├─▶ etcdReady = false  (etcdutil.go:341 부근)
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
| `legacy.go:605` | `etcdPut` 실패 시 에러를 무시하고 계속 진행 |
| `legacy.go:614` | `blocksMined++`가 etcd 기록 성공 여부와 무관하게 증가 |
| `etcdutil.go:641` | `etcdIsReady` 실패 시 재시도 로직 없음 |

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

- `admin.metadiumInfo.etcd`의 `leader`가 비어 있거나 `members`가 구성보다 적은 경우 (§5.1)
- uncle 블록 비율이 평소보다 현저히 높은 경우
- 로그에서 `"Metadium - yield failed"` 메시지가 함께 발생하는 경우
- 2개 이상의 노드에서 동시에 동일 에러가 발생하는 경우 (**BP 4대 구성에서는 이 시점에 이미 쿼럼 경계** — §1 참조. BP 수가 다른 배포에서는 임계를 다시 잡아야 합니다)

#### 여유를 갖고 조치해도 되는 경우 (비긴급)

아래 조건을 모두 만족하면 **다음 정기 점검 시 처리** 가능:

- 에러가 단일 노드에서만 발생
- uncle 블록 비율이 정상 범위 내
- `admin.metadiumInfo.etcd`에 `leader`가 잡혀 있고 `members`가 구성과 일치
- Raft 리더십이 다른 노드로 정상 이전되어 채굴이 계속됨

#### 코드 수정 우선순위

| 항목 | 우선순위 | 이유 |
| --- | --- | --- |
| 운영 조치 (etcd 복구/노드 재시작) | **긴급** | 현재 발생 중인 에러 해소 |
| `blocksMined` 조건 개선 (§7.1) | **높음** | 리더십 로테이션 오작동 방지 |
| `etcdReady` 복구 경로 수정 (§7.3) | **높음** | 이 장애가 몇 시간씩 지속된 직접 원인 |
| etcdPut 재시도 로직 (§7.2) | **보류** | §7.2 참조 — 이 장애에는 효과 없음 |

---

## 5. 점검 사항

> **etcd는 `gmet` 프로세스에 임베드되어 있습니다** (`embed.StartEtcd`). 별도 etcd
> 프로세스도, 기본 포트 2379/2380도 없습니다. `etcdNewConfig`가 peer를
> `https://<ip>:<self.Port+1>`, client를 `http://localhost:<self.Port+2>`로
> 바인딩합니다(`metadium/etcdutil.go:152-158`). client는 **localhost 바인딩**이라
> 외부에서 붙을 수 없습니다.
>
> 따라서 아래는 `gmet` 콘솔(`gmet attach`)을 기준으로 합니다. `etcdctl`은 노드에
> 별도로 설치돼 있고 `--endpoints=http://localhost:<self.Port+2>`를 지정한
> 경우에만 쓸 수 있으며, 설치를 전제하지 마십시오.

### 5.1 즉시 점검 (gmet 콘솔)

```javascript
// 클러스터 구성 · 멤버 목록 · self · 현재 리더
//   (etcdctl member list / endpoint status --cluster 에 해당)
admin.metadiumInfo.etcd

// metadium-work 키 최신 값  (etcdctl get metadium-work 에 해당)
admin.etcdGetWork()

// 임의 키 조회  (etcdctl get <key> 에 해당)
debug.etcdGet("metadium-work")
```

`admin.metadiumInfo.etcd`는 `cluster`, `members`, `self`, `leader`를 돌려줍니다
(`metadium/etcdutil.go:1193` `etcdInfo()`, `admin.go:2081`에서 합성).
`leader`가 비어 있거나 `self`와 계속 어긋나면 리더십이 정착하지 못한 상태입니다.

### 5.2 노드 로그 점검

```bash
# 장애 구간의 etcd 관련 로그
journalctl -u <gmet-service> --since "2026-05-19 15:25:00" --until "2026-05-19 15:35:00" | grep -iE "etcd|log the latest block"

# 정상/실패 판정에 직접 쓰이는 두 줄
journalctl -u <gmet-service> -f | grep -E "logged the latest block|failed to log|etcd server ready"
```

`etcd server ready`는 `etcdEventHandler`가 `etcdReady = true`를 찍는 유일한
지점입니다(`etcdutil.go:332-339`). 장애 구간에 이 줄이 없다면 §7.3의 고착
상태입니다.

### 5.3 etcd 데이터 디렉터리

```bash
# 데이터 디렉터리는 <datadir>/etcd (admin.go의 etcdDir)
du -sh <datadir>/etcd
df -h <datadir>
```

디스크가 가득 차면 etcd가 `NOSPACE` alarm을 걸고 쓰기를 거부합니다. alarm 조회·해제와
compaction은 트리에 RPC가 없으므로, `etcdctl`이 있는 경우에만 §6.1을 쓸 수 있습니다.

### 5.4 노드 간 연결 확인

```bash
# Raft peer 포트 = <노드의 p2p 포트> + 1  (기본 2380 아님)
nc -zv <peer-node-ip> <peer-port+1>
```

client 포트(`+2`)는 localhost 바인딩이므로 노드 간 연결 확인 대상이 아닙니다.

---

## 6. 후속 조치 방법

### 6.1 etcd가 alarm 상태인 경우

> alarm 조회·해제와 compaction은 트리에 대응 RPC가 없습니다. 아래는 노드에 `etcdctl`이
> 설치돼 있고 `--endpoints=http://localhost:<self.Port+2>`를 지정할 수 있는 경우에만
> 해당합니다(§5 머리말).

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

임베디드 etcd에는 `etcd --force-new-cluster` 플래그를 넘길 경로가 없습니다. 대신
트리가 같은 일을 하는 RPC를 노출합니다 — `admin.etcdInit()`은 `etcdNewConfig(true)`를
거쳐 `ClusterState=new`, `ForceNewCluster=true`로 임베디드 서버를 띄웁니다
(`etcdutil.go:160-161`, `web3ext.go:199`).

```javascript
// 강제 새 클러스터 구성 (마지막 수단, 데이터 손실 가능성 있음)
//   기존 멤버가 살아 있는 상태에서 실행하면 클러스터가 갈라집니다.
admin.etcdInit()

// 이후 나머지 노드를 다시 합류시킵니다
admin.etcdJoin("<node-name>")      // 합류하는 노드에서
admin.etcdAddMember("<node-name>") // 새 클러스터 쪽에서
```

스냅샷에서 복구해야 한다면 노드를 **정지한 상태에서** `<datadir>/etcd`를 교체한 뒤
기동합니다. 실행 중인 노드에 `etcdctl snapshot restore`를 거는 경로는 없습니다.

### 6.4 정상 복구 확인 기준

아래 로그가 출력되면 정상 복구된 것입니다:

```text
INFO  Metadium - logged the latest block  height=XXXXXXXX hash=... took=...
```

---

## 7. 코드 개선 권고사항

### 7.1 `blocksMined` 카운터 조건 개선 (legacy.go:614)

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

### 7.2 etcdPut 재시도 로직 추가 (etcdutil.go:640) — 보류

> **보류 사유 (#140 참조).** 아래 초안은 `ErrNotRunning`에서 재시도 없이 반환하는데,
> 이 장애에서 `etcdPut`이 반환한 것은 **매번 `ErrNotRunning`** 이었습니다. 즉 재시도가
> 한 번도 수행되지 않습니다. 게다가 `LogBlock`은 `admin.lock`을 함수 전체에 걸고
> 약 5초마다 실행되므로, 그 락 안의 sleep 백오프는 `handleNewBlocks` 및
> `admin.go:1332` 루프와 경합합니다. 개별 시도는 이미 `ReqTimeout()`으로 제한됩니다.
> `ErrNotRunning`이 아닌 일시적 `Put` 실패가 실제로 관측되면 그때 에러 분포를 들고
> 재검토합니다.

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

### 7.3 etcdReady 복구 메커니즘 — #140

**워치독을 새로 만들 필요는 없습니다. 복구 경로가 이미 있고, 하필 필요한 상태에서만
동작을 거부합니다.**

`admin.go:1332-1340`의 주기 루프가 `EtcdStart()`를 부르고, `EtcdStart()`는
`!etcdIsRunning()`일 때 `etcdAutoJoin()`을 띄웁니다. 그런데 두 술어가 한 플래그
차이입니다:

```go
etcdIsRunning() = etcd != nil && etcdCli != nil              // etcdutil.go:171
etcdIsReady()   = etcd != nil && etcdCli != nil && etcdReady // etcdutil.go:175
```

고착 상태는 `running == true && ready == false`입니다. `etcdPut`은 **ready**로
판정하는데 복구 분기는 전부 **running**으로 판정하므로, 세 갈래가 모두 "할 일 없음"으로
빠집니다:

1. `!admin.etcdIsLeader()` — etcd 리더인 노드는 `EtcdStart`에 도달조차 못 함
2. `etcdStart()`는 `ErrAlreadyRunning` 즉시 반환 (반환값은 버려짐)
3. `etcdAutoJoin()`은 `!etcdIsRunning()`일 때만 기동 → 기동 안 됨

수정 방향은 별도 워치독이 아니라 술어 교정입니다 — `EtcdStart`가 `!etcdIsReady()`로
판정하고, `etcdStart`가 `running && !ready`를 "stop 후 start"로 처리하고, `etcdStop`이
`etcdReady`를 정리하고, `admin.go:1336`의 `etcdIsLeader` 게이트를 재검토하는 것.
자세한 내용은 #140에 있습니다.

---

## 8. 참고 코드 위치

| 파일 | 라인 | 설명 |
| --- | --- | --- |
| `metadium/legacy.go` | 588–635 | `LogBlock()` 함수 전체 |
| `metadium/etcdutil.go` | 640–654 | `etcdPut()` 함수 |
| `metadium/etcdutil.go` | 175–177 | `etcdIsReady()` 함수 |
| `metadium/etcdutil.go` | 171–173 | `etcdIsRunning()` 함수 (§7.3의 핵심) |
| `metadium/etcdutil.go` | 338, 341 | `etcdReady` 플래그 토글 위치 |
| `metadium/etcdutil.go` | 332 | `etcdEventHandler()` — 플래그를 true로 만드는 유일한 지점 |
| `metadium/admin.go` | 107–110 | `metaWork` 구조체 정의 |
| `metadium/admin.go` | 410–459 | `getMinerNodes()` 함수 |
| `metadium/admin.go` | 1332–1340 | `EtcdStart()`를 호출하는 주기 루프 |

> **기준 커밋: `560ef3cc2` (`dev`).** 줄 번호는 구조적으로 낡습니다 — 이 문서의
> 최초 작성 시점(2026-05) 숫자는 #132(etcd 3.5.16)가 `etcdutil.go`에 순증 2줄을
> 넣으면서 이미 두 번 어긋났습니다. 위 숫자가 맞지 않으면 함수 이름으로 찾거나
> `git show 560ef3cc2:metadium/etcdutil.go` 로 당시 트리를 직접 여는 편이
> 확실합니다.
