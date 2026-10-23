# 캡스톤 2 — 합성 InfiniBand의 상태·속도·오류 진단

## 학습목표

- lab이 합성하는 HCA/포트의 상태 4항목(State, Physical State, Link layer, Rate)을 읽고, 링크 상태를 나타내는 값과 통신량을 나타내는 값을 별개로 다룬다.
- 상태는 `gpu ibstatus`/`gpu ibstat`으로, 오류·재전송·드랍 카운터는 PromQL(`gpu metrics`)로 확인하는 두 갈래를 구분한다.
- 링크 상태가 정상이어도 분산 통신이 정상이라는 증거가 되지 않음을 lab 데이터로 보인다.

lab은 실제 RDMA/verbs 디바이스를 만들지 않는다. `gpu ibstat`·`gpu ibstatus`·`gpu ibv_devinfo`는 rdma-core 명령의 이름과 출력 형식을 맞춘 합성 구현이고, 값은 lab exporter의 Prometheus series에서 온다.

## 선수 조건

- 캡스톤 1 완료
- 선수 설치: `gpu create`, `gpu helm install nvidia-device-plugin`, `gpu helm install dcgm-exporter`, `gpu helm install monitoring` (세 release는 별개이며 monitoring이 Prometheus/Grafana를 담당한다)
- lab의 IB 모델: worker마다 mlx5_0 1개, 포트 1개([ib-fabric.md](../ib-fabric.md))

## 강의 진행안(90분)

| 구간 | 분 | 활동 |
| --- | --- | --- |
| 도입 | 10 | lab IB 모델과 두 갈래 확인(상태 명령 / PromQL) 설명 |
| 상태 조회 | 20 | 3 worker를 `--node`로 각각 조회 |
| 오류·트래픽 | 20 | PromQL로 rate와 카운터 확인 |
| 시나리오 | 25 | RUN→VERIFY로 4개 IB 시나리오 비교 |
| 제출 | 15 | 보고서 |

## 상태 조회 명령

`gpu ibstat --help`가 나열하는 lab 지원 옵션: `-l/--list_of_cas`, `-p/--port_list`, `-s/--short`, `-v/--verbose`, lab 확장 `--node <node>`.

```bash
gpu ibstat --help
gpu ibstat -l            # 이 호스트의 CA 이름 나열
gpu ibstat -s
gpu ibstatus             # worker 하나만 대상(아래 주의)
gpu ibv_devinfo -l
gpu ibv_devinfo -d mlx5_0 -i 1 -v
```

동작 두 가지(구현 기준):

- 호스트에서 `gpu ibstat`/`gpu ibstatus`를 `--node` 없이 실행하면 rdma-core가 한 대의 Linux 호스트를 대변한다는 규칙에 따라 첫 번째 worker만 보인다. 3노드를 비교하려면 `--node`로 지정한다.
- `-d`는 lab이 노출하는 HCA 이름 `mlx5_0`과만 맞는다. 다른 이름을 주면 `IB device '...' wasn't found`로 끝난다. 실제 장비에서 `ibv_devinfo`가 존재하지 않는 경우는 lab이 재현하지 않는 영역이므로 별도 항목이다.

## 오류·트래픽은 PromQL로(상태 명령과 별개)

`gpu ibstat` 출력에는 오류 카운터를 섞지 않는다. 카운터는 아래 series로 본다.

```bash
gpu metrics --query 'gpu_lab_ib_port_up'
gpu metrics --query 'gpu_lab_ib_port_state'
gpu metrics --query 'gpu_lab_ib_link_rate_gbps'
gpu metrics --query 'rate(gpu_lab_ib_symbol_errors_total[2m])'
gpu metrics --query 'rate(gpu_lab_ib_link_error_recovery_total[2m])'
gpu metrics --query 'rate(gpu_lab_ib_link_downed_total[2m])'
gpu metrics --query 'rate(gpu_lab_rdma_retries_total[2m])'
gpu metrics --query 'rate(gpu_lab_rdma_timeouts_total[2m])'
gpu metrics --query 'rate(gpu_lab_ib_xmit_discards_total[2m])'
gpu metrics --query 'rate(gpu_lab_ib_xmit_wait_total[2m])'
gpu metrics --query 'gpu_lab_fabric_delay_seconds'
```

## RUN → VERIFY → RESET

`gpu verify <scenario>`는 해당 시나리오가 만드는 **장애 상태를 검증**한다. 정상 복구를 확인하는 명령이 아니다. 각 시나리오는 RUN 직후 VERIFY하고 끝내면 RESET한다.

```bash
gpu scenario run ib-rate-degraded
gpu verify ib-rate-degraded
gpu scenario reset

gpu scenario run ib-symbol-errors
gpu verify ib-symbol-errors
gpu scenario reset

gpu scenario run rdma-retry-storm
gpu verify rdma-retry-storm
gpu scenario reset

gpu scenario run ib-link-down
gpu verify ib-link-down
gpu scenario reset
```

verify가 확인하는 계약(`internal/verification/verification.go`의 fabric 조회는 `hca="mlx5_0"`, `port="1"`, `link_layer="InfiniBand"` label을 사용):

| 시나리오 | verify 판정 |
| --- | --- |
| `ib-link-down` | `gpu-lab-worker2`의 `gpu_lab_ib_port_up`=0, state `DOWN`/physical `DISABLED`, `gpu_lab_ib_link_downed_total`>0, `gpu_lab_fabric_delay_seconds`=0 |
| `ib-rate-degraded` | `gpu-lab-worker3`의 `gpu_lab_ib_link_rate_gbps`=25, fabric delay ≥ 1.5 |
| `ib-symbol-errors` | `gpu-lab-worker`의 `gpu_lab_ib_symbol_errors_total`>0, `gpu_lab_ib_link_error_recovery_total`>0 |
| `rdma-retry-storm` | `gpu-lab-worker2`의 `gpu_lab_rdma_retries_total`>0, `gpu_lab_rdma_timeouts_total`>0, fabric delay ≥ 1 |
| `ib-congestion` | `gpu-lab-worker3`의 `gpu_lab_ib_xmit_wait_total`>0, `gpu_lab_ib_xmit_discards_total`>0, fabric delay ≥ 0.8 |

위 target 노드와 값은 `internal/verification/verification.go`와 `scenarios/ib-*.yaml`에 고정된 합성 fixture다. 25 Gbps, 1.5초 같은 수치는 SLA 기준이 아니다.

## 관측 → 가설 → 조치 → 확인

1. 관측: `ib-rate-degraded`에서 `gpu ibstatus --node gpu-lab-worker3`은 State가 Active이고 Rate만 낮다. PromQL의 `gpu_lab_ib_link_rate_gbps`가 이를 뒷받침.
2. 가설: (a) 포트 다운 → 기각, `gpu_lab_ib_port_up`=1. (b) 네고시에이션 속도 저하 → 지지. (c) 실제 원인의 후보(케이블, 수신 감도, switch 포트) → lab은 재현하지 못하므로 "추가 검증 필요"로 남긴다.
3. 조치(lab): `gpu scenario reset`. 실제 환경 analogue(포트 교체, 재결선, 서브네트워크 점검)는 문서화만 하고 lab에서 실행하지 않는다.
4. 확인: RESET 후 아래가 baseline으로 돌아오는지 본다. RESET 전의 verify는 장애 검증이므로 복구 증거로 쓰지 않는다.

```bash
gpu metrics --query 'gpu_lab_ib_link_rate_gbps'
gpu metrics --query 'gpu_lab_ib_port_up'
gpu scenario run normal
gpu verify normal
```

## 링크 상태가 좋아도 통신은 별개 확인

NVIDIA의 NCCL 네트워크 트러블슈팅 절차는 State=Active, Physical State=LinkUp, Link layer, 기대 Rate 확인으로 시작하지만 그것만으로 통신 성공을 증명하지 않는다. lab에서도 같은 원칙을 데이터로 확인할 수 있다. 링크 메트릭이 정상인 상태에서 통신 쪽 series만 나빠지는 사례를 보면 된다.

```bash
gpu metrics --query 'gpu_lab_training_rank_up'
gpu metrics --query 'gpu_lab_training_allreduce_errors_total'
gpu metrics --query 'rate(gpu_lab_training_fabric_errors_total[2m])'
gpu metrics --query 'gpu_lab_training_straggler_active'
gpu metrics --query 'gpu_lab_fabric_delay_seconds'
```

통신성 판정에 쓰는 series와 링크 상태 판정에 쓰는 series를 보고서에서 다른 표로 나눠 쓴다. Xid처럼 단일 신호로 원인을 확정하지 않는다([Xid 소개](https://docs.nvidia.com/deploy/xid-errors/introduction.html)).

## lab과 실제 환경의 경계

- `gpu_lab_ib_*`, `gpu_lab_rdma_*`는 lab이 만드는 합성 값이고, 실제 DCGM·NIC·switch의 카운터와 같은 물건이 아니다. 이름 대응과 주의점은 [metric-mapping.md](../metric-mapping.md)에만 있다.
- lab 시나리오의 값은 [scenarios/](../../scenarios)의 fixture다.
- 실제 장비 조사(`perfquery`, `ibping`, switch manager 조회)는 lab 범위 밖이다. 필요하면 [templates/real-cluster-evidence-template.md](templates/real-cluster-evidence-template.md)를 쓰는 별도 작업으로 분리하고, lab과 같은 표에 섞지 않는다.

## 제출 증거

1. `gpu ibstat -l`, `gpu ibstatus --node gpu-lab-worker|gpu-lab-worker2|gpu-lab-worker3` 원본 3종
2. 정상과 `ib-rate-degraded` 각각의 Rate·port_up·rate(symbol_errors)·rate(retries) PromQL 출력
3. 4개 시나리오 RUN 직후의 `gpu verify` 원본 4종
4. 링크 상태 series와 통신 series를 나눈 표 1장
5. 관측→가설→조치→확인 4단 표

## 루브릭과 합격 기준

| 루브릭 | 평점 근거 |
| --- | --- |
| 증거 수집 | 3노드 상태 원본과 4개 verify 원본, 시각 |
| 가설 검증 | State와 Rate를 분리해 판정한 근거 |
| 경계 인식 | lab series와 실제 장비 카운터를 분리 |
| 안전 | switch·NIC 물리 조작을 문서화로 대신한 이유 |
| 재현성 | RUN→VERIFY→RESET을 반복해 같은 결과 |

각 0~3점, 5점 이상을 합격으로 한다. 그중 "경계 인식" 0점이면 과제물을 되돌려 다시 받는다.
