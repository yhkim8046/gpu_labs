# 실행 기록 — 대표 실습 완성도와 검증 (2026-10-01)

이 문서는 "기능 추가 없이 대표 실습 하나를 재현 가능하게 완성한다"는 목표로
2026-10-01(KST)에 진행한 작업과 결과를 남긴 기록이다.
관련 실습 원문은 [capstone-4-rdma-allreduce-recovery.md](course/capstone-4-rdma-allreduce-recovery.md),
코스 상태 표는 [course-readiness.md](course-readiness.md).

## 1. 결과 요지

- 대표 실습 rdma-retry-storm 전과정을 라이브 kind 클래스터에서 처음부터 끝까지 통과시켰다.
  (baseline → fault 주입·판정 → AllReduce 지연 관측 → 정상화 → gpu verify --recovery 21/21 PASS)
- 정상화 판정을 위한 새 명령 gpu verify --recovery [fault]와 판정 코드를 추가했다.
  장애 상태 판정(gpu verify <scenario>)과 정상화 판정을 명령·근거·문서에서 완전히 분리했다.
- 전체 단위 테스트 15패키지 통과, 핵심 4패키지 race 검사 통과, vet/gofmt clean.
- 코드 리뷰에서 4건을 지적해 반영했다(3절).
- 이 기록을 포함해 어떤 변경도 커밋하지 않았다. 로컬 검증까지만 완료된 상태다.

## 2. 변경 파일

### 새로 추가

| 파일 | 내용 |
| --- | --- |
| internal/verification/recovery.go | 정상화 판정 코어. 게이지·진행도·최신성 판정, 누적 카운터는 가독성만 확인 |
| internal/verification/recovery_test.go | 판정 계약 단위 테스트 (stale/stall/invalid 입력의 fail-closed 보장) |
| docs/course/capstone-4-rdma-allreduce-recovery.md | 대표 실습 문서 (설치·사전점검·4단계·루브릭·경계·미검증 제한·재검증 기록) |

### 수정

| 파일 | 내용 |
| --- | --- |
| cmd/gpu-lab/verify.go | gpu verify --recovery [fabric-fault] 하위 명령 (180초 외측 타임아웃) |
| cmd/gpu-lab/help.go | usage에 recovery 경로 노출 |
| cmd/gpu-lab/main_test.go | recovery 분기 단위 테스트 |
| README.md | verify 사례 옆에 recovery 사례 1행 |
| docs/course-readiness.md | 정상화 판정 항목 완료 처리, 2026-10-01 재검증 결과 반영 |
| docs/course/README.md | 캡스톤 4를 대표 실습으로 등록 (90분) |
| .gitignore | 루트 컴파일드 바이너리(/gpu-lab, /ibstat 등) 제외 — 실수 커밋 예방 |

### 판정 설계에서 지킨 원칙

- 판정 분리: gpu verify <scenario>는 "장애 상태 도달", gpu verify --recovery는 "정상화 도달"만
  판정한다. reset 전 verify를 복구 증거로 쓰면 루브릭 감점 대상이다.
- 누적 카운터 0 요구 금지: *_total은 단조 증기량이므로 0을 기대하는 것 자체가 가짜 증거다.
  실습이 끝난 뒤에도 retries=250, timeouts=8가 남아 있는 것이 정상이고, 가독 여부만 확인한다.
- fail-closed: series 누락, 빈 vector, NaN/Inf, stale 채택, stalled step은 모두 실패다.
  "관측이 없다"를 "0"이나 "통과"로 해석하는 지점을 없앴다.
- 최신성은 time() - timestamp(selector)로만 판정: instant query의 value[0]는 평가시각이라
  스크래이프 시각 증거가 아니다. 게이트 3종이 이것을 쓴다 (exporter up / normal 채택 / step series).
- 공유 예산 + fail-fast: 모든 대기 게이트가 하나인 예산(기본 120초)을 공유하고, 예산 소진 후
  질의를 재시도하지 않는다. 회복이 영원히 안 되는 클래스터도 질의 폭증 없이 실패한다.

## 3. 코드 리뷰에서 지적하고 반영한 것

1. training control의 문자열 검색 파싱: strings.Index 기반이라 잘못된 JSON·타입 mismatch를
   normal로 통과시킬 수 있었다. encoding/json typed decode + DisallowUnknownFields +
   trailing-content 검사로 바꿔 invalid/truncated/wrong-type 문서가 fail-closed되게 했다.
2. instant query timestamp 오헤: value[0]를 스크래이프 시각으로 쓰던 것을 timestamp() 함수
   기반 별도 질의로 바꿼다.
3. age 계산 산술 결함: epoch 초값을 time.Duration으로 취급해 1970년 부근이 되던 계산 버그를
   삭제하고 PromQL age 값(0 이상 90초 이하, 음수/nonfinite 실패)으로 대체했다.
4. 게이트마다 별도 타임아웃: 게이트 수 × 대기시간 누적되던 것을 공유 예산 + fail-fast로 차단했다.

회귀 테스트로 고착 확인: 300초 오래된 scrape·음수 age·stalled step·누락 series·stale 채택은 실패,
shared budget 소진 후 질의 수는 상한(200)을 넘지 않음.

## 4. 최종 검증 결과 (2026-10-01)

### 단위·빌드 (클러스터 불필요)

| 검사항목 | 결과 | 비고 |
| --- | --- | --- |
| gofmt -l | clean | exit 0 |
| go build ./... | pass | exit 0 |
| go vet ./... | pass | exit 0 |
| go test ./... -count=1 | 15패키지 ok | exit 0. 로컬 모듈 캐시가 읽기 전용이라 임시 GOCACHE/GOMODCACHE/GOPATH로 실행 |
| go test -race (verification, training, exporter, cmd) | 4패키지 ok | exit 0 |
| bin/gpu-lab version | pass | gpu-lab dev (commit unknown) — ldflags 미주입 실행 경로 |

### 라이브 실습 (kind gpu-lab, KST)

| 시각 | 단계 | 원본 관측 |
| --- | --- | --- |
| 10:39:29 | baseline | allreduce max 0.00, rank_up 3, worker2 delay 0, verify normal 7/7 PASS |
| 10:39:50 | fault 주입 | retries 250, timeouts 8, delay 1s, verify rdma-retry-storm 5/5 PASS |
| 10:42:42 | 지연 관측 | allreduce max 1.00(baseline 대비 큰 폭 증가), rank_up 3 유지, fault_active 1, changes(step[2m])=23, ibstatus ACTIVE/LinkUp/200Gb |
| 10:43:22 | 정상화 | scenario reset 후 training recover, 100초 이상 대기 |
| 10:45:59 | 복구 판정 | verify --recovery rdma-retry-storm 21/21 PASS — 최신성 게이트 3종 ages 13s/13s/5s, step progress 758→762 |

이 관측이 실습이 가르치는 세 지점을 뒷받힌다: (1) 포트는 살어 있고 카운터만 움직이는 "장애 아닌
장애", (2) 한 rank의 지연이 collective 전체의 AllReduce 지연으로 관측됨, (3) rank_up과 step이
살어 있는 한 정지(stall)와 지연(latency)을 구별해야 한다.

### 환경 상태 변화 (이 세션에서 일어난 것)

- Docker Desktop이 종료된 상태로 발견돼 open -a Docker로 재기동했다 (Docker Engine 29.6.2, VM 8GiB).
- kind 클래스터(59일) 컨테이너 4개가 Up이고 node 전부 Ready·worker당 GPU 8 확인. 삭제·재생성은 하지 않았다.
- 실습이 끝난 시점의 클래스터는 normal 상태다 (scenario=normal, training 3-rank Running).

## 5. 남은 작업 (우섛)

1. 커밋·푸시: 이 변경 포함 50여 개 파일이 미커밋이다 (modified 19 + untracked). origin에는
   7ef6d1c까지만 있다. 검증된 상태가 저장소에 반영되기 전까지 "통과"는 로컬 사실이다.
2. clean install e2e: 이번 재검증은 기존 클래스터를 재기동해 돌린 것이다. gpu create부터
   새 클래스터를 만드는 경로는 CI의 kind e2e가 대신하고 있고, 로컬 재현은 미수행이다.
3. 릴리스 정합성: 로컬 바이너리는 dev (commit unknown)이다. 기능 포함 릴리스 재생성과
   version 주입이 필요하다.
4. Grafana 시각 확인: Prometheus 질의와 CLI로만 판정했고 패널 렌더는 확인하지 않았다.
5. straggler/worker-crash 정상화 E2E와 step stall 실cluster 재현: 단위 테스트로만 보장돼 있다.

## 6. 한계 (과장 방지를 위해)

- 모든 수치는 synthetic lab 값이다. 실 NIC/switch 카운터가 아니며 synthetic-vs-real.md,
  metric-mapping.md의 경계를 따른다.
- "복구 통과"는 lab이 normal 시나리오·게이지·진행도를 복원했음을 판정한 것이고, 하드웨어 복구
  절차의 성공을 뜻하지 않는다.
- Docker VM과 Prometheus의 시계 편차는 별도 측정하지 않았다 (동일 VM 내 전제).

