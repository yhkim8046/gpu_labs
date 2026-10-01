# 캡스톤 4 — 대표 실습: 합성 IB/RDMA 이상 → AllReduce 지연 → 원인 진단 → 정상화 검증

이 문서는 코스 패키지의 **대표 실습(representative lab)** 이다. 처음 사용하는 수강생이
single binary 하나로 "합성 InfiniBand/RDMA 장애가 분산학습의 AllReduce 지연으로 번지는
것을 관측하고, 원인을 좁히고, 정상화까지 **기계 판정으로 닫는**" 한 바퀴를 재현한다.

기능 확대가 아니라 하나의 완결된 흐름을 재현 가능하게 만드는 것이 목표다.

- 판정 도구는 두 개를 섞지 않는다.
  - `gpu verify <scenario>` → **장애 상태 도달**만 판정 (reset 이전의 증거).
  - `gpu verify --recovery [fault]` → **정상화 도달**만 판정 (reset 이후의 증거).
- 누적 카운터(`*_total`)는 정상화 판정에서 0을 요구하지 않는다. 게이지와 진행도만으로
  판정하고, 카운터는 "계속 읽히는지"만 확인한다(사고 흔적 보존).

## 0. 문서 지킴용 메타 (이 값은 그대로 옮겨 적는다)

| 항목 | 값 |
| --- | --- |
| 검증 날짜 | 2026-09-30 (KST, Asia/Seoul) |
| 검증 호스트 | macOS(darwin/arm64), 로컬 Docker Desktop 환경 |
| Docker Engine | 29.6.2 (VM 메모리 약 8 GiB) |
| Kubernetes | kind 클러스터 `gpu-lab`, 서버/클라이언트 v1.36.1 |
| Go | go1.26.5 darwin/arm64 |
| lab 빌드 | `go build -o bin/gpu-lab ./cmd/gpu-lab` (version: `gpu-lab dev`) |
| 검증 결과 | 아래 재검증 기록 절이 이번 판정의 원본이다. 아래 수치는 **synthetic lab 실측값**이며 실제 장비 SLA가 아니다 |
| 2026-10-01 재검증 | 전과정 통과(KST 10:39–10:46, 원본은 아래 재검증 기록). 단위: `go test ./...` 15패키지 ok, `-race` 4패키지 ok, `go vet`/`gofmt` clean |

### 재검증 기록(2026-10-01, freshness 게이트 포함 최종 판정)

Docker Desktop 29.6.2 재기동 후 kind 컨테이너 4개 Up 상태에서 다음 순서로 실행했다.
모든 exit code는 0이며, Prometheus 원본 값만 그대로 옮긴 것이다.

| 시각(KST) | 단계 | 원본 관측 | 판정 |
| --- | --- | --- | --- |
| 10:39:29 | baseline | allreduce max `0.00`, rank_up `3`, worker2 delay `0`; `verify normal` 7/7 PASS | 기준선 성립 |
| 10:39:50 | fault 주입 | `scenario run rdma-retry-storm`; `verify rdma-retry-storm` 5/5 PASS(retries `250`, timeouts `8`, delay `1`) | 장애 상태 도달 |
| 10:42:42 | 지연 관측 | allreduce max `1.00`, rank_up `3`, fault_active `1`, delay `1`, `changes(step[2m])=23`; ibstatus worker2 `ACTIVE/LinkUp/200Gb` | 통신 생존·지연 계열, 정지 아님 |
| 10:43:22 | 정상화 조치 | `scenario reset` → `training recover` | fault 통제 해제 |
| 10:45:59 | 복구 판정 | `verify --recovery rdma-retry-storm` 21/21 PASS. 최신성 게이트 3종: exporter up `oldest age 13s`, adoption `13s`, step `5s`; step progress `758 -> 762` | 정상화 도달 |

판정 코드 회귀(단위, 클러스터 불필요): 오래된 scrape(`time()-timestamp()` 300s)·음수 age·stalled step·누락 series·stale 채택은 모두 실패, 공유 예산 고갈 후 재질의 중단(fail-fast), training control의 invalid/trailing/잘못된 타입 문서도 실패함. 누적 카운터(retries 250, timeouts 8)는 복구 후에도 읽기만 보장되고 값 보존을 기대한다.

> lab이 만드는 모든 수치(retries 250, delay 1s, rate 200 Gbps 등)는
> [scenarios/](../../scenarios) fixture와 [synthetic-vs-real.md](../synthetic-vs-real.md)
> 의 합성 값이다. 실측은 "lab이 그 값을 노출했는가"이지 "NIC가 실제로 그 값을 냈는가"가 아니다.

## 1. 설치

```bash
# 저장소 최상위에서 (so스 checkout 기준)
go build -o bin/gpu-lab ./cmd/gpu-lab
./bin/gpu-lab version          # gpu-lab dev ... 가 나오면 설치 성공
```

PATH에 넣어 계속 쓴다면 `alias gpu="$PWD/bin/gpu-lab"` 또는 `make build` 후 산출물을 쓰면 된다.
이후 문서의 `gpu`는 위 바이너리를 가리킨다.

## 2. 사전 점검

```bash
./bin/gpu-lab doctor           # tool 존재·버전만 판정 (cluster 존재를 보증하지 않음)
./bin/gpu-lab create           # 없일 때만. kind 3 worker + control-plane 1 (CPU only)
./bin/gpu-lab helm install nvidia-device-plugin
./bin/gpu-lab helm install dcgm-exporter
./bin/gpu-lab helm install monitoring
./bin/gpu-lab status
./bin/gpu-lab context list
kubectl --context gpu-lab get nodes -o wide
kubectl --context gpu-lab get nodes \
  -o custom-columns=NAME:.metadata.name,GPU:.status.capacity.nvidia\\.com/gpu
```

**사전 점검 성공 조건(각 항목 별개 증거로 판정):**

1. `doctor`의 tool 행 전부가 준비 상태.
2. `get nodes`가 control-plane 1(Ready, GPU 없음) + worker 3(Ready, 각 8 GPU) = **24**.
3. `kubectl --context gpu-lab -n gpu-lab-system get pods`의 dcgm-exporter 3/3 Running.
4. Prometheus 게이트가 살아 있음:

```bash
gpu metrics --query 'sum(up{service="dcgm-exporter"})'      # 기대 3
gpu metrics --query 'count(gpu_lab_scenario_info{scenario="normal"})'  # 기대 3
```

5. training workloads가 준비됨:

```bash
gpu training status
# 기대: statefulset 3/3/3/3, 3개 rank 모두 true·Running, each step이 0보다 크고 증가 중
```

**사전 점검 실패 시(이 저장소에서 실제로 겪은 순서 그대로):**

| 증상 | 우선 확인 | 조치 |
| --- | --- | --- |
| `kubectl`이 `127.0.0.1:<port> connection refused` | `docker ps -a --filter name=gpu-lab` | control-plane이 `Exited (255)`+`OOM=true`면 `docker start gpu-lab-control-plane`(삭제·재생성 금지), 10초 뒤 재시도 |
| 다수 pod가 `OOMKilled`/`CrashLoopBackOff`, restarts 수백 | `docker info --format '{{.MemTotal}}'` | Docker VM 메모리 부족 재현. 재시작만으로 회복되는지 관찰하고 회복 안 되면 VM 메모리 증설 후 재시작 |
| `sum(up{...}) != 3` | `gpu doctor`, `gpu status`, dcgm-exporter pod 상태 | exporter pod가 살아나는지까지 본 뒤 진행. Prometheus가 응답하지 않으면 실습 진행 금지 |
| training rank down | `gpu training status`, `kubectl --context gpu-lab -n gpu-lab-demo get pods -o wide` | `gpu training run --wait`로 재적용. StatefulSet ready까지 대기 |

> control-plane을 `docker start`로 되살리는 것은 안전하다. **cluster 전체 삭제/`gpu destroy`는
> 이번 실습에서 금지**다. OOMKilled는 *과거 종료 사유* 기록이지 현재 원인이 아니다. 현재 상태를
> 단정하려면 `docker ps`·pod 상태·restart 창을 지금 시점에서 함께 본다.

## 3. 대표 실습 흐름

시간 예산: 90분. 관측→가설→조치→복구 네 단계를 매 단계 시간과 원본과 함께 남긴다.

### 3-1. 정상 baseline 채증

```bash
date '+%Y-%m-%d %H:%M:%S %Z'
gpu training status
gpu metrics --query 'max(gpu_lab_training_allreduce_seconds)'          # 기대: 1 미만(lab 기준 ~0.06 이하)
gpu metrics --query 'sum(gpu_lab_training_rank_up)'                    # 기대: 3
gpu metrics --query 'max(gpu_lab_fabric_delay_seconds{node="gpu-lab-worker2",hca="mlx5_0",port="1",link_layer="InfiniBand"})'  # 기대: 0
gpu verify normal                                                       # 기대: verification passed
```

이 시점 수치가 이후 모든 비교의 기준선이다. 원본을 그대로 저장한다.

### 3-2. 이상 주입과 "장애 상태 도달" 판정

`rdma-retry-storm`은 worker2(`gpu-node-02`)의 mlx5_0 포트에 RDMA 재전송/타임아웃 카운터와
1초의 합성 fabric 지연을 얹는 시나리오다.

```bash
gpu scenario run rdma-retry-storm     # configmap/gpu-lab-scenario configured
gpu verify rdma-retry-storm           # 기대: 5개 PASS
```

`gpu verify rdma-retry-storm`이 확인하는 것(**정상화가 아님**):

| check | 의미 | 통과 조건 |
| --- | --- | --- |
| active scenario | ConfigMap이 그 fault를 가리키나 | == rdma-retry-storm |
| exporter targets | 관측 자체의 생존 | sum(up) == 3 |
| RDMA retries | worker2 재전송 카운터 | > 0 (fixture 250) |
| RDMA timeouts | worker2 타임아웃 카운터 | > 0 (fixture 8) |
| fabric delay | worker2 주입 지연 게이지 | >= 1s |

**여기서 verify PASS는 "장애가 걸렸다"의 증거일 뿐이다.** reset 전 verify를 복구 증거로
쓰면 루브릭 감점이다.

### 3-3. AllReduce 지연 관측과 원인 좁히기

ConfigMap projected volume 반영에 최대 180초가 걸리므로 주입 직후가 아니라 2분 정도를 두고
본다. 이 구간에서 확인해야 할 것은 "지연이 통신 쪽에만 뜨는가, 계산 쪽인가"다.

```bash
gpu metrics --query 'rate(gpu_lab_rdma_retries_total[5m])'                 # fault 대조
gpu metrics --query 'rate(gpu_lab_rdma_timeouts_total[5m])'
gpu metrics --query 'max(gpu_lab_training_allreduce_seconds)'              # baseline 대비 증가폭
gpu metrics --query 'sum(gpu_lab_training_rank_up)'                        # 3 유지 = 통신 살아 있음
gpu metrics --query 'gpu_lab_training_fabric_fault_active'                 # target rank만 1
gpu metrics --query 'gpu_lab_training_fabric_delay_seconds'                # target node만 1s
gpu metrics --query 'changes(gpu_lab_training_step[2m])'                   # step은 계속 진행
gpu ibstat --node gpu-lab-worker2 -l
gpu ibstat --node gpu-lab-worker2 -s
gpu ibstatus --node gpu-lab-worker2
```

**이 lab에서 기대하는 판독(2026-09-30 실측과 일치):**

- `gpu ibstatus --node gpu-lab-worker2`는 State=Active를 유지한다. **포트는 다운되지 않았다.**
  카운터만 증가한다 → "링크가 죽었다"는 가설은 기각.
- target 라인의 `gpu_lab_training_fabric_fault_active`가 1, `fabric_delay_seconds`가 1.
  다른 노드는 0 → 원인을 worker2 한 대의 fabric 쪽으로 좁힌다.
- `max(gpu_lab_training_allreduce_seconds)`가 0.0x초 → **1.00초 부근**으로 오른다. AllReduce는
  collective이므로 한 rank의 지연이 전체 latency로 관측된다.
- `sum(gpu_lab_training_rank_up)`이 3이고 `changes(step[2m])`가 양수 → **step은 멈추지 않는다.**
  이 시나리오는 지연(latency) 계열이고 정지(stall) 계열이 아니다. rank down/step stall은
  `ib-link-down`·`worker-crash` 쪽 증거이며 이 fault의 관측으로 주장하지 않는다.

대체 가설 비교를 보고서에 남긴다: (a) 포트 다운 → 기각(port_up=1), (b)네고시에이션 속도 저하 → 기각
(rate 200 유지), (c) 실제 원인 후보(케이블/수신 감도/switch 포트) → lab은 재현하지 못하므로
"추가 검증 필요"로 보류.

### 3-4. 정상화와 "정상화 도달" 판정

```bash
gpu scenario reset        # ConfigMap을 normal로 되돌린다
gpu training recover      # training control-plane의 fault 통제를 normal로 되돌린다
```

worker가 control ConfigMap을 보고 게이지를 되돌리는 데 시간이 걸리므로(수준 반영 + 스크래이프)
바로 재판정하지 않는다. **권장 대기 ≥ 100초.** 그다음:

```bash
gpu verify --recovery rdma-retry-storm
```

**정상화 판정 계약(`gpu verify --recovery`):**

| check | 판정 대상 | 통과 조건 | 0 요구? |
| --- | --- | --- | --- |
| active scenario | ConfigMap | == normal | — |
| training control | control ConfigMap 실독 | fabric_mode normal, target/delay/straggler/crash 잔여 없음 | — |
| exporters adopted normal | stale 가드 | `count(gpu_lab_scenario_info{scenario="normal"})` == 3 | — |
| worker×{port up, state, rate, delay} | fabric 게이지 | up=1, ACTIVE/LINK_UP series 존재, rate=200, injected delay=0 | 게이지만 |
| rdma_retries/timeouts readable | **누적 카운터** | series가 계속 읽힘(count>0) | **아님** |
| ranks published/up | 통신 world | count=3이고 sum=3 | — |
| training observed fabric delay | worker 관측 게이지 | 0 | 게이지만 |
| allreduce error counter readable | 누적 카운터 | series 존재 | **아님** |
| training step progress | stale/stall 가드 | step이 두 관찰에서 **단조 증가** | — |

명시적 실패 규칙(fail-closed):

- series가 없거나 Prometheus가 빈 vector를 주면 **실패**(0으로 취급하지 않음).
- 값이 NaN/비유한이면 실패.
- exporter가 normal 시나리오를 채택하지 않은 채 fault 게이지를 계속 내면 실패(stale 통과 금지).
- step이 증가하지 않으면 실패 — metrics endpoint가 살아 있어도 정지된 worker는 통과하지 못한다.

```bash
gpu verify --recovery          # fault 없이 공통 게이트만 (5개 fault 모두에 동일하게 적용)
gpu verify normal              # (선택) normal fault-state contract 확인
```

### 3-5. 정리

```bash
gpu training reset       # 이 실습이 관리하는 StatefulSet/Service/ConfigMap만 제거
gpu training status      # 제거 확인
```

`gpu training reset`은 training 리소스만 대상으로 하고 기존 exporter·monitoring stack을
건드리지 않는다. 기존 클러스터를 유지할 것.

## 4. 제출물

1. baseline 원본: `training status` 1종, `metrics`(allreduce/rank_up/delay) 각 1종, `verify normal`
2. `scenario run` 직후의 `gpu verify rdma-retry-storm` 원본과 timestamp
3. 주입 2분 후: `training status`, fault series 원본, `ibstat -s`/`ibstatus` 3노드 대조
4. reset/recover 후: `gpu verify --recovery rdma-retry-storm` 원본과 timestamp
5. 관측→가설→조치→복구 4단계 표(각 셀에 시각·원본·해석)

## 5. 루브릭과 합격 기준

공통 루브릭(README)에 더해 이 캡스톤만:

| 항목 | 0 | 3 |
| --- | --- | --- |
| 판정 구분 | reset 전 verify를 복구 증거로 사용 | fault verify와 recovery verify를 다른 근거로 명확 분리 |
| 누적 카운터 | 복구 후 카운터를 0이었다고 주장 | 카운터가 누적 보존됨을 근거로 제시 |
| stale 가드 | metrics endpoint 생존만으로 정상 처리 | scenario adoption과 step 증가를 stale 판정 근거로 사용 |

각 0~3점, "판정 구분" 0점이면 회수 후 재제출. 나머지 5항목 공통 루브릭과 합쳐 판정한다.

## 6. 실제 환경과의 경계

- `gpu_lab_ib_*`, `gpu_lab_rdma_*`, `gpu_lab_training_*`는 lab이 만드는 합성 series다.
  실제 NIC/switch 카운터가 아니며 [metric-mapping.md](../metric-mapping.md) 외 대응표는 없다.
- lab은 `/dev/infiniband`, RDMA verbs, NCCL collective을 만들지 않는다.
  NVIDIA 공식 [NCCL networking troubleshooting](https://docs.nvidia.com/deeplearning/nccl/user-guide/docs/troubleshooting/networking_troubleshooting.html)의
  State/Physical/Rate 확인이 통신 성공을 증명하지 않는다는 원칙을 lab은 "데이터로 재현"할 뿐이다.
- 실제 장비의 `perfquery`, `ibping`, switch manager 조회는 lab 범위 밖이며
  [templates/real-cluster-evidence-template.md](templates/real-cluster-evidence-template.md)로 분리 수집한다.
- 1s 지연·250 재전송은 fixture다. 실환경 SLA/root cause로 쓰려면 메트릭 정의와 임계값을
  그 환경 기준으로 재설정하고 별개 검증한다.

## 7. 미검증 제한 (2026-10-01 기준)

- **실 hardware 부재**: driver/NVML/CUDA/RDMA 디바이스 없음. 위 수치는 전부 synthetic.
- **monitoring 시각 검수 미수행**: 이번 세션은 Prometheus 쿼리 응답(직접 프록시)과 CLI 출력으로
  판정했다. Grafana 대시보드 패널의 시각적 렌더 확인은 하지 않았다. 이번 세션에서 grafana pod는
  3/3 Running이지만 패널 그림까지 확인한 것은 아니다.
- **straggler / worker-crash 정상화 E2E 미수행**: 이번 검증한 recovery 판정 경로는
  fabric fault(`rdma-retry-storm`)다. training inject(straggler, worker-crash) 계열의
  E2E 정상화는 이번 세션 실측 대상이 아니며, `training control` check이 잔여 주입을
  차단하도록 단위 테스트되어 있을 뿐이다. 해당 경로 실습은 별도로 시간 배정 후 수행한다.
- **step stall 계열 미검증**: "통신 오류가 step을 멈춘다"는 주장은 이 fault의 실측이 아니다.
  stalled-progress 단위 테스트로 stale 가드는 검증했지만, 실cluster stall 재현은 하지 않았다.
- 재현성 전제: 시나리오 주입~복구 관찰 창은 ConfigMap 반영(≤180초)과 scrape 주기(fabric 15s,
  training 5s)에 좌우된다. 위 명령을 즉시 연달아 실행하면 관측이 빈 구간이 생길 수 있다.
- **clean install e2e 미수행(2026-10-01)**: 이번 재검증은 기존 kind 클래스터를 Docker 재기동해
  재현했다. gpu create부터 새 클래스터를 만드는 설치 경로는 이번 세션에서 다시 돌리지
  않았고, CI의 kind e2e 잡이 그 경로를 대신한다. gpu doctor는 tool 존재·버전만 판정하며
  클래스터·리소스 상태를 보증하지 않는다.
