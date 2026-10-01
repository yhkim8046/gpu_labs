# 캡스톤 1 — 3 worker × 8 GPU 토폴로지의 scheduling/fragmentation

## 학습목표

- 3 worker × 8 GPU, control-plane은 CPU 전용인 lab 토폴로지에서 "클러스터에는 GPU 여유가 있는데 배치가 안 된다"를 판정한다.
- Pending의 원인 항목을 별개로 확인한다: 스케줄러 event, kubelet의 실제 Pod 예약, lab exporter가 노출하는 projection 메트릭.
- 관측 → 가설 → 조치 → 확인의 순서를 원본과 함께 문서화한다.

## 선수지식

- [README.md](README.md)의 온보딩 실습 통과
- 아래 선수 설치가 끝난 상태(각각 Helm release이며 서로 독립):

```bash
gpu create
gpu helm install nvidia-device-plugin
gpu helm install dcgm-exporter
gpu helm install monitoring
```

- Kubernetes의 Pending과 PodEvents 개념([student-guide.md](../student-guide.md))

## 강의 진행안(90분)

| 구간 | 분 | 활동 |
| --- | --- | --- |
| 도입 | 10 | 아래 "토폴로지 조회"로 worker 3·GPU 수를 직접 확인. control-plane에 GPU가 없음 확인 |
| 관측 | 25 | 시나리오 실행 직후 Pending과 예약 분포 관찰 |
| 가설 | 15 | "용량 부족"과 "노드 간 단편화"를 데이터로 구분 |
| 확인·정리 | 20 | RUN→VERIFY, reset, normal 확인, 단독 2-GPU Pod 구동 |
| 제출 | 20 | 진단 보고서 작성 |

## 실습 명령

### 토폴로지 조회

"3×8"은 고정 신뢰이 아니라 조회 결과다. 아래 두 항목을 별개로 본다.

```bash
gpu status
kubectl --context gpu-lab get nodes -o wide
kubectl --context gpu-lab get nodes \
  -o custom-columns=NAME:.metadata.name,GPU:.status.capacity.nvidia\\.com/gpu,GPU-ALLOC:.status.allocatable.nvidia\\.com/gpu
kubectl --context gpu-lab get pods -A -o wide
```

### 시나리오: RUN → VERIFY → (정리) RESET

`gpu verify`는 지금의 시나리오가 기대하는 **장애 상태를 검증**한다. 정상으로 돌아가는 확인이 아니다. 따라서 순서는 RUN → VERIFY → RESET이다.

```bash
gpu scenario list
gpu scenario inspect gpu-fragmentation      # 메트릭 값은 기본 숨김
gpu scenario run gpu-fragmentation
gpu verify gpu-fragmentation                # RUN 직후 실행 (이 시나리오는 Pending이 기대값)
```

`gpu verify gpu-fragmentation`의 계약(`internal/verification/verification.go`): active scenario가 `gpu-fragmentation`이어야 하고, dcgm-exporter target 3개 up, `gpu-lab-fragmentation-worker-01/02/03`이 Running, `gpu-lab-fragmentation-target`이 **Pending**이고 event에 `Insufficient nvidia.com/gpu`가 있어야 통과한다. 시나리오 Pod는 `gpu-lab-demo` namespace에 있다.

### Pending 원인 관찰

```bash
kubectl --context gpu-lab -n gpu-lab-demo get pods -o wide
kubectl --context gpu-lab -n gpu-lab-demo describe pod gpu-lab-fragmentation-target
kubectl --context gpu-lab -n gpu-lab-demo get events --sort-by=.lastTimestamp
kubectl --context gpu-lab describe node gpu-lab-worker | sed -n '/Capacity:/,/Events:/p'
```

### 예약 현황: 스케줄러 사실과 lab projection을 구분

`gpu_lab_node_gpu_capacity` / `gpu_lab_node_gpu_allocatable` / `gpu_lab_gpu_allocated`는 lab dcgm-exporter가 노출하는 **합성 projection(fixture)**이다. 스케줄러가 실제로 남는 값은 kubelet의 node capacity/allocatable과 각 Pod의 requests다. free를 추론할 때는 다음처럼 나눠 쓴다.

- 스케줄러 사실: `kubectl describe node`의 `Allocated resources`(Pod requests 합), node `.status.allocatable`
- lab projection: 아래 exporter 메트릭(시나리오가 값을 고칠 수 있음)

```bash
gpu metrics --query 'gpu_lab_node_gpu_capacity'
gpu metrics --query 'gpu_lab_node_gpu_allocatable'
gpu metrics --query 'max(gpu_lab_node_gpu_allocatable) - min(gpu_lab_node_gpu_allocatable)'
gpu metrics --query 'gpu_lab_gpu_allocated'
gpu metrics --query 'sum(gpu_lab_gpu_allocated) by (node)'
```

allocatable은 free가 아니다. `gpu metrics`는 자체적으로 Prometheus에 접속하므로 `kubectl port-forward`를 겹치게 열지 않는다(충돌은 [troubleshooting.md](../troubleshooting.md#7-dashboardmetrics-port-forward-충돌)).

## 관측 → 가설 → 조치 → 확인

1. 관측: `gpu-lab-fragmentation-target`이 Pending, events에 `Insufficient nvidia.com/gpu`. node별 예약을 보면 3 worker가 대칭으로 예약되어 있다.
2. 가설 A(전역 용량 부족): 기각 — node별 합을 보면 요구량 2를 수용할 여유가 남는다. 가설 B(노드 간 단편화): 채택 — 어떤 단일 node도 2장을 남지 않는다.
3. 조치(lab): 시나리오가 만든 예약을 정리한다.

```bash
gpu scenario reset            # normal로 되돌림. 시나리오 Pod도 함게 정리
kubectl --context gpu-lab -n gpu-lab-demo get pods
```

4. 확인(복구라는 표기 대신 두 항목을 별개로):

```bash
# (a) normal 상태 확인
gpu scenario run normal
gpu verify normal

# (b) 정상 리소스 확인
gpu metrics --query 'min(gpu_lab_node_gpu_allocatable)'

# (c) 단독 2-GPU Pod가 실제로 구동하는지(스케줄링 판정)
kubectl --context gpu-lab apply -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: course1-two-gpu-check
  namespace: gpu-lab-demo
spec:
  restartPolicy: Never
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
      resources:
        requests: {nvidia.com/gpu: "2"}
        limits:   {nvidia.com/gpu: "2"}
YAML
kubectl --context gpu-lab -n gpu-lab-demo wait --for=jsonpath='{.status.phase}=Running' pod/course1-two-gpu-check --timeout=120s
kubectl --context gpu-lab -n gpu-lab-demo get pod course1-two-gpu-check -o wide
```

reset으로 사라진 `gpu-lab-fragmentation-target`을 Running으로 "복구"했다고 쓰면 안 된다. 그 Pod는 reset으로 삭재될 뿐이다. 위 (c)는 lab의 예약 남힘을 새 워크로드로 확인하는 별개의 판정이다. 확인이 끝나면 정리한다.

```bash
kubectl --context gpu-lab -n gpu-lab-demo delete pod course1-two-gpu-check --wait=false
gpu scenario reset
```

실환경 analogue(배치 우선순위 조정, preemption, gang scheduling)는 lab에서 실행하지 않고 보고서의 "실환경 적용" 항목으로 분리 기재한다.

## 합성 fixture와 실제 환경의 경계

- 이 실습의 예약 분포는 `scenarios/gpu-fragmentation.yaml` fixture다. lab은 GPU·CUDA를 제공하지 않으므로 값은 합성이다.
- 용량 수치는 교육 권장값이며 실제 클러스터 요구사항·용량을 대변하지 않는다. 실제 적용은 [templates/real-cluster-evidence-template.md](templates/real-cluster-evidence-template.md)로 그 환경의 값을 직접 수집해 판단한다.
- lab 대 실제 대명표는 [metric-mapping.md](../metric-mapping.md)만 사용한다.

## 제출 증거

[templates/incident-report-template.md](templates/incident-report-template.md)에 붙인다.

1. `kubectl get nodes` 커스텀 컬럼 출력(토폴로지 조회 결과)
2. `gpu verify gpu-fragmentation` 원본(RUN 직후)
3. `-n gpu-lab-demo describe pod gpu-lab-fragmentation-target`의 Events 원본
4. node별 `sum(gpu_lab_gpu_allocated) by (node)`과 `kubectl describe node`의 `Allocated resources`를 나눈 표
5. `gpu verify normal` 결과와 단독 2-GPU Pod의 Running 확인 원본
6. 관측→가설→조치→확인 4단 표와 본인 해석(원본과 분리)

## 루브릭과 합격 기준

| 루브릭 | 이 캡스톤에서 보는 것 | 관찰 포인트 |
| --- | --- | --- |
| 증거 수집 | events·메트릭·describe node 원본과 타임스탬프 | 원본 5종 + 시각 |
| 가설 검증 | 용량 부족 가설을 데이터로 기각 | node별 합 계산이 원본과 일치 |
| 경계 인식 | projection/fixture와 kubelet 사실을 분리 | 두 항목이 다른 열로 존재 |
| 안전 | drain·preemption을 lab에서 실행하지 않은 판단 | 미실행 이유와 실환경 조건 설명 |
| 재현성 | RUN→VERIFY→RESET→(c) 순서 재현 | 동일 명령으로 같은 결과 |

합격: 5항목 모두 1 이상, 그중 "증거 수집"과 "가설 검증"이 2 이상이다. `gpu verify` 통과만으로 합격으로 보지 않는다(verify는 환경 상태 판정). 설명 평가는 강사/TA가 원본을 읽고 한다.
