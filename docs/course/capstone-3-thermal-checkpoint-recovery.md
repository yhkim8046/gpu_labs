# 캡스톤 3 — 합성 thermal escalation 관측과 작업 checkpoint/복구 구분

## 학습목표

- 고정값 thermal 시나리오(thermal-throttling, power-throttle)의 온도·전력·throttle·utilization fixture를 읽고, 시나리오 manifest와 exporter 출력을 대조해 thermal 진행을 설명한다.
- 고정값 시나리오와 공통 시작 시각을 사용하는 단계형 timeline을 구분하고, 단계별 메트릭과 자동 복구를 관찰한다.
- `duration` 자동 만료는 lab이 만드는 상태 전이이며 수강생의 조치 성과가 아니다. 이 구분 없이 복구를 주장하지 않는다.
- 분산 학습에서 **환경 복구**(메트릭 정상화)와 **작업 복구**(crash 이후 checkpoint 재개)를 서로 다른 명령·서로 다른 데이터로 확인한다.

## 선수 조건

- 캡스톤 1·2 완료
- 선수 설치: `gpu create`, `gpu helm install nvidia-device-plugin`, `gpu helm install dcgm-exporter`, `gpu helm install monitoring`
- `gpu training run` 경험([distributed-training.md](../distributed-training.md))
- 모든 training 명령의 namespace는 `gpu-lab-demo`로 통일한다(기본값도 같지만 강의 중에는 명시한다).

## 강의 진행안(120분)

| 구간 | 분 | 활동 |
| --- | --- | --- |
| 도입 | 15 | 단계형 thermal-escalation과 고정 시나리오의 차이 설명 |
| thermal 관측 | 25 | thermal-throttling/power-throttle 고정 값으로 온도·전력·throttle 확인, verify로 기계 판정 |
| 작업 복구 | 35 | worker-crash → container 재시작 → checkpoint 재개 관찰, recover로 정리 |
| 판정 | 25 | 환경 복구 × 작업 복구 4분면표 |
| 제출 | 20 | 보고서 |

## 실습 1 — 단계형 thermal 관측

`thermal-escalation`은 ConfigMap의 공통 `started_at`을 기준으로 모든 exporter가 같은 단계를 계산한다. 0초 baseline(45°C), 30초 warning(82°C), 60초 critical(96°C, thermal throttle), 120초 recovery(45°C), 180초 duration 만료 후 normal 순서다. exporter가 늦게 시작하거나 재시작해도 현재 단계로 합류한다.

```bash
gpu scenario inspect thermal-escalation --solution
gpu scenario run thermal-escalation
gpu metrics --query 'gpu_lab_scenario_phase'
gpu metrics --query 'gpu_lab_gpu_temperature_celsius'
gpu metrics --query 'gpu_lab_gpu_throttle_active'
gpu verify thermal-escalation
```

각 단계에서 시각, phase, 온도·전력·utilization·VRAM·throttle을 기록한다. `gpu verify thermal-escalation`은 최신 phase와 24개 GPU 메트릭이 일치하는지 한 질의로 확인한다. 만료 후에는 `gpu_lab_scenario_info`의 normal 전이로 자동 복구를 확인한다. ConfigMap을 정상 상태로 정리하려면 `gpu scenario reset` 후 `gpu verify normal`을 실행한다. phase별 자동 전이는 lab이 만드는 결과이며 수강생의 복구 조치로 해석하지 않는다. 수동 복구 증거는 `gpu scenario reset` 이후 따로 수집한다.

아래 고정 시나리오를 함께 사용해 시간 전이와 고정 fixture의 차이를 비교한다.

### 고정 시나리오 1 — thermal-throttling (temperature fixture 96)

```bash
gpu scenario run thermal-throttling
gpu metrics --query 'gpu_lab_gpu_temperature_celsius'     # fixture: 96
gpu metrics --query 'gpu_lab_gpu_power_watts'             # fixture: 245
gpu metrics --query 'gpu_lab_gpu_utilization_percent'     # fixture: 62
gpu metrics --query 'gpu_lab_scenario_info'
gpu verify thermal-throttling     # 계약: temp > 90 이고 avg util < 80
gpu scenario reset
```

### 고정 시나리오 2 — power-throttle (throttle_active=1, reason=power_cap)

`thermal-throttling` manifest에는 `throttle_active`가 없다. throttle 게이지를 보려면 `power-throttle`을 쓴다(fixture: `throttle_active: 1`, `throttle_reason: power_cap`).

```bash
gpu scenario run power-throttle
gpu metrics --query 'gpu_lab_gpu_throttle_active'
gpu metrics --query 'gpu_lab_gpu_throttle_active{reason="power_cap"}'   # 1
gpu metrics --query 'gpu_lab_gpu_power_violation_total'                 # fixture: 25
gpu metrics --query 'gpu_lab_gpu_utilization_percent'
gpu verify power-throttle
gpu scenario reset
```

두 시나리오 모두 값은 `scenarios/thermal-throttling.yaml`, `scenarios/power-throttle.yaml`에 고정된 fixture이고, 시나리오를 reset할 때까지 같은 값이 반복 scrape된다. 시간이 지나면서 값이 변하는 경과 자체를 lab이 재현하지 않는다는 것을 보고서에 명시한다. 고정 시나리오에는 `duration`이 없으므로 자동 만료도 없고 reset이 유일한 되돌림이다.(`duration` 기반 자동 만료는 `duration`이 설정된 시나리오의 `expiresAt` 경로로 구현되어 있다 — thermal-escalation의 180초 만료에서 `gpu_lab_scenario_info`의 normal 전이로 확인할 수 있다. 이 자동 전이는 lab이 만든 결과이지 수강생의 조치가 아니다.)

## 실습 2 — crash 이후 checkpoint 재개(worker-crash/recover)

straggler 주입은 delay만 만든다(`gpu_lab_training_straggler_active`가 1이 되고 allreduce 지연 증가). **restart나 checkpoint 재개는 만들지 않는다.** 재개 증거는 worker-crash로 만든다.

```bash
gpu training run --workers 3 --namespace gpu-lab-demo --wait
gpu training status --namespace gpu-lab-demo
```

(`--image`를 생략하면 cluster 설정의 runtime image를 사용한다. 명시적으로 바꾸려면 `--image`를 지정한다. 이미지 당김 실패는 [troubleshooting.md](../troubleshooting.md#3-kind-클러스터-생성과-중복) 참조.)

기준선 기록 — Pod의 restartCount와 step/checkpoint를 함께 남긴다:

```bash
kubectl --context gpu-lab -n gpu-lab-demo get pods -l app.kubernetes.io/name=gpu-lab-training -o wide
gpu metrics --query 'gpu_lab_training_step'
gpu metrics --query 'gpu_lab_training_checkpoint_step'
gpu metrics --query 'gpu_lab_training_restarts_total'
gpu metrics --query 'gpu_lab_training_rank_up'
```

rank 1에 crash 주입 → StatefulSet이 container를 재시작 → 워커가 checkpoint 파일을 다시 읽어 그 step에서 이어간다(코드의 실제 동작: `internal/training/worker.go`의 checkpoint 로드 → `Restarts++` → `start = cp.Step + 1`).

```bash
gpu training inject worker-crash --rank 1 --namespace gpu-lab-demo
kubectl --context gpu-lab -n gpu-lab-demo get pods -l app.kubernetes.io/name=gpu-lab-training -w
gpu metrics --query 'gpu_lab_training_restarts_total'
gpu metrics --query 'gpu_lab_training_step'
gpu metrics --query 'gpu_lab_training_checkpoint_step'
gpu training logs --rank 1 --namespace gpu-lab-demo
```

인정 기준(보고서에 붙이는 판정):

- crash 후 해당 rank Pod의 `RESTARTS`가 1 증가, `gpu_lab_training_restarts_total` 증가
- 재시작 직후 `gpu_lab_training_step`이 0이 아니라 직전 `gpu_lab_training_checkpoint_step` 부근에서 이어 올라간다(재개 증거)
- 모든 rank의 `gpu_lab_training_rank_up`이 다시 1

**왜 container restart만으로는 checkpoint가 살아남**: lab은 `CHECKPOINT_FILE`을 Pod의 emptyDir volume(`/var/lib/gpu-lab-training`)에 쓴다(`internal/trainingjob/trainingjob.go`). container가 같은 Pod에서 재시작되면 emptyDir가 그대노 남는다. 그러나 Pod가 아예 삭제·재생성되면 emptyDir가 비워져 step 0부터 다시 시작한다. 따라서 "복구"를 스스로 주장하기 전에 restart(같은 Pod)와 재생성(Pod 교체)을 Pod의 UID·`RESTARTS`·`AGE`로 구분한다.

대조군으로 straggler를 붙이면 차이가 보인다: `straggler_active=1`, allreduce 지연, 그러나 `restarts_total`은 그대로다.

```bash
gpu training inject straggler --rank 1 --delay 2s --namespace gpu-lab-demo
gpu metrics --query 'gpu_lab_training_straggler_active'
gpu metrics --query 'gpu_lab_training_restarts_total'
gpu training recover --namespace gpu-lab-demo   # 주입된 fault control 정리
gpu metrics --query 'gpu_lab_training_straggler_active'
```

lab에서 fault를 끄르는정규한 절차는 `gpu training recover`(straggler/crash/fabric control 해제)다. Pod를 직접 지우거나 삭제하지 않는다. 작업 전체를 초기화하려면 `gpu training reset --namespace gpu-lab-demo`.

## 관측 → 가설 → 조치 → 확인

1. 관측: `power-throttle`에서 `gpu_lab_gpu_throttle_active{reason="power_cap"}`=1, `gpu_lab_gpu_power_violation_total`=25(fixture), utilization 62. `thermal-throttling`에서는 온도 96(fixture)과 util 62가 함께 관측된다. 두 시나리오는 별개의 fixed snapshot이고 같은 실습 안에서 시간 경과로 이어지지 않는다.
2. 가설: (a) 열이 아니라 전력 한계 때문 → `power-throttle`의 reason=power_cap과 `thermal-throttling`의 온도 fixture 차이를 manifest와 대조해 구분. (b) 냉각 계통 고장 → lab은 냉각 하드웨어를 재현하지 않으므로 판정 불가, "추가 검증 필요"로 분리.
3. 조치(lab): 시나리오 실행 중에는 측정만 한다. 정리와 초기화는 아래 순서로 한다.

```bash
gpu scenario reset                       # timeline/fault 제거
gpu training recover --namespace gpu-lab-demo
kubectl --context gpu-lab -n gpu-lab-demo get pods -l app.kubernetes.io/name=gpu-lab-training
```

4. 확인: 환경 쪽과 작업 쪽을 다른 표로:

| 상태 | 환경 증거 | 작업 증거 | 판정 |
| --- | --- | --- | --- |
| 환경 복구·작업 미복구 | reset 후 baseline 값(온도 45/util 15 등 normal fixture) | step이 checkpoint에서 이어지지 않음 | worker-crash 실습으로 재개 여부 별도 확인 |
| 환경 미복구·작업 진행 | throttle active | step 진행 | 성능 저하 속 진행일 뿐 종료가 아니다 |
| 모두 복구 | baseline | restarts 증가 후 step≒checkpoint 재개, rank_up 전원 | 종료 |

"메트릭이 돌아왔다"만으로 incident를 종료하지 않고, 작업 로그만 보고 환경 종료를 선언하지도 않는다.

## lab과 실제 환경의 경계

- 96°C/245W(power-throttle은 온도 84°C)와 thermal-escalation manifest의 phase 설계값 82°C/210W는 전부 fixture이고 실제 GPU의 열 설계 한계와 무관하다. lab은 GPU·CUDA·냉각 하드웨어를 제공하지 않는다.
- 실제 환경의 thermal/Xid 조사는 kernel의 `NVRM` 로그, `nvidia-smi -q`, DCGM health, bug report를 교차로 모은다([Xid 작업 흐름](https://docs.nvidia.com/deploy/xid-errors/working-with-xid-errors.html)). lab의 단일 throttle 신호는 그 일부만 교육용으로 옮긴 것이다.
- 실제 값 수집은 [templates/real-cluster-evidence-template.md](templates/real-cluster-evidence-template.md)를 쓰는 별도 작업으로 분리한다.
- lab↔실제 대응표는 [metric-mapping.md](../metric-mapping.md)만 사용한다.
- 냉각 점검, 워크로드 이관, GPU reset 같은 실제 조치와 장비 조작은 이 lab에서 실행하지 않는다. 보고서에 실환경 조건(승인 절차, 롤백 계획)을 설명하는 것까지가 실습 범위다.

## 제출 증거

1. `thermal-throttling`과 `power-throttle` 각각의 메트릭 스냅샷과 시각(시나리오 실행 시각 포함)
2. `gpu verify thermal-escalation`의 contract 미존재 표시, `gpu verify thermal-throttling`과 `gpu verify power-throttle` 원본
3. worker-crash 전후의 `gpu_lab_training_step`/`gpu_lab_training_checkpoint_step`/`gpu_lab_training_restarts_total`과 Pod `RESTARTS` 대조
4. straggler 대조군(`straggler_active=1`, restarts 불변)
5. 환경/작업 분리 4분면표와 판단 문장(원본과 분리)
6. "lab에서 판정 불가"를 명시한 항목 목록

## 루브릭과 합격 기준

| 루브릭 | 평점 근거 |
| --- | --- |
| 증거 수집 | 고정 시나리오 2종 스냅샷과 crash 전후 원본 |
| 가설 검증 | 고부하 가설을 시간 선후 데이터로 반증 |
| 경계 인식 | 공통 시각 기반 timeline 이해, 자동 만료 vs 조치 효과 분리, fixture/실측 구분 |
| 안전 | reset·drain류 미사용 판단과 그 근거 |
| 재현성 | 동일 시나리오 재실행으로 같은 패턴 확인 |

각 항목 0~3점, 5항목 합계 15점 만점. 합격: 합계 10점 이상, 모든 항목 1점 이상, 그중 "증거 수집"과 "가설 검증"은 각 2점 이상. 단 "경계 인식"에서 timeline의 자동 전이나 자동 만료를 자신의 복구 성과로 설명하면 그 항목 0점이어서 합격할 수 없다.
