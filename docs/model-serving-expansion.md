# GPU 없는 모델 서빙 운영 실습 확장안

상태: 향후 확장 제안(아직 구현되지 않음)

이 문서는 현재 GPU Lab의 fake GPU device plugin, synthetic DCGM/InfiniBand metric, 장애 시나리오 구조를 기반으로 모델 서빙 운영 실습 환경을 확장하기 위한 설계 방향을 기록합니다. 목표는 GPU 연산 자체를 흉내 내는 것이 아니라, 실제 GPU가 없어도 기업에서 필요한 배포·API·관측·장애 대응 절차를 반복 검증할 수 있게 하는 것입니다.

## 목표

- `nvidia.com/gpu` 요청을 포함한 모델 서빙 workload의 Kubernetes 스케줄링을 검증합니다.
- 실제 서빙 제품과 호환되는 health, metadata, inference API를 제공합니다.
- 모델 다운로드, 초기화, readiness, autoscaling, canary, rollback을 실습합니다.
- 지연, queue 포화, 모델 로드 실패, GPU OOM 유사 상태 등을 결정적으로 재현합니다.
- 같은 배포 구조를 CPU 실습 환경과 실제 GPU 환경에서 최대한 재사용합니다.

## 설계 원칙

1. CUDA ABI나 `/dev/nvidia*` 전체를 모방하지 않습니다.
2. 제어면과 데이터면의 테스트 범위를 명확히 분리합니다.
3. 합성 결과는 항상 재현 가능하고 시나리오 reset으로 복구할 수 있어야 합니다.
4. 외부 클라이언트에는 표준 서빙 API와 실제 제품에 가까운 metric을 제공합니다.
5. 실제 GPU로 전환할 때 Manifest 전체가 아니라 runtime 설정만 교체할 수 있게 합니다.

## 실행 모드

| 모드 | 실행 방식 | 주된 목적 |
|---|---|---|
| `mock` | 모델 weight 없이 결정적인 응답 반환 | API contract, 배포, 장애, 관측, autoscaling 테스트 |
| `cpu-real` | ONNX Runtime, PyTorch CPU 또는 Triton `KIND_CPU`로 소형 모델 추론 | 모델 로드, 전처리·후처리, batching, 결과 검증 |
| `gpu-real` | 실제 NVIDIA device plugin, driver, CUDA/TensorRT backend 사용 | CUDA 호환성, 정확한 VRAM 동작, 성능과 안정성 검증 |

`mock`과 `cpu-real`은 GPU가 없는 개발자 노트북과 CI에서 사용합니다. 운영 전 최종 검증은 동일한 workload를 `gpu-real` 환경에서 실행합니다.

## 목표 구조

```text
Fake GPU Device Plugin
        │
        ├── nvidia.com/gpu scheduling
        ├── synthetic DCGM / IB metrics
        └── scenario state
                │
                ▼
Model Serving Pod
        ├── mock inference backend
        ├── CPU-real backend
        ├── KServe V2 HTTP/gRPC API
        ├── optional OpenAI-compatible API
        └── serving / synthetic GPU metrics
                │
                ▼
Service / Ingress / Autoscaler
                │
                ▼
Prometheus / Grafana / Alertmanager
```

## GPU 없이 검증할 범위

### 검증 가능한 영역

- container image build, pull, startup와 배포 정책
- `nvidia.com/gpu` resource request/limit과 노드 스케줄링
- S3, object storage, PVC를 이용한 모델 artifact 다운로드
- init container와 model repository 초기화
- readiness, liveness, startup probe
- KServe V2 및 선택적인 OpenAI-compatible API contract
- 입력 검증, 전처리, 후처리와 오류 응답
- request batching, concurrency 제한과 queue 동작
- Ingress, TLS, 인증, rate limit, NetworkPolicy
- HPA/KEDA 기반 scale-out 및 scale-in
- rolling update, canary, rollback
- cold start와 모델 load/unload
- Prometheus metric, dashboard와 alert runbook
- GPU/IB 장애가 서빙 readiness와 latency에 미치는 합성 영향

### 실제 GPU에서만 검증할 영역

- CUDA와 NVIDIA driver/container toolkit 호환성
- TensorRT engine build 및 실행
- 실제 VRAM 할당, fragmentation과 OOM 동작
- GPU kernel의 수치 결과와 안정성
- 실제 throughput, latency, 전력과 발열
- CUDA Graph, MIG, MPS 동작
- NCCL, P2P, NVLink, RDMA의 실제 성능

합성 환경의 latency나 GPU 사용률은 운영 수치의 예측값이 아닙니다. 이 값은 alert, dashboard, 자동화, 복구 절차를 검증하기 위한 contract입니다.

## 제안 컴포넌트

### 1. Mock inference server

- KServe V2 health, server/model metadata, inference endpoint 구현
- 모델별 고정 schema와 seed 기반 결정적 응답 제공
- unary, batch 요청과 필요 시 streaming 응답 지원
- readiness와 model load 상태 분리
- latency, error, queue, request counter를 Prometheus 형식으로 노출

### 2. CPU-real runtime

- 초기 기준 runtime은 Triton `KIND_CPU` 또는 ONNX Runtime으로 구성
- 작은 공개 모델이나 저장소 내부 fixture 모델 사용
- mock과 동일한 외부 API 및 metric label 유지
- runtime 설정 하나로 CPU와 GPU backend를 교체할 수 있도록 구성

### 3. Serving scenario controller

현재 `gpu scenario run/reset` 구조를 재사용해 다음 장애를 추가합니다.

| 시나리오 | 합성 동작 | 기대 검증 |
|---|---|---|
| `model-load-failure` | 모델을 `UNAVAILABLE`로 전환 | readiness 실패, rollout 중단, alert |
| `model-download-timeout` | artifact 준비 시간을 지연 | init 실패, timeout, 재시도 |
| `serving-cold-start` | 최초 요청 또는 load 지연 | startup probe, cold-start metric |
| `serving-latency-spike` | 지정 모델의 응답 지연 | p95/p99 alert, autoscaling |
| `serving-queue-saturation` | 동시성보다 많은 요청을 queue에 유지 | queue metric, 429/503 정책 |
| `serving-gpu-oom` | 합성 VRAM 한계 초과 | 모델 load 실패, replica 격리, rollback |
| `serving-xid` | XID와 unhealthy GPU 상태 연결 | readiness 차단, reschedule |
| `serving-fabric-fault` | IB 장애를 multi-node serving에 연결 | timeout, partial failure, 복구 |
| `model-corruption` | schema/checksum 불일치 | validation 실패, 이전 version 유지 |

### 4. 관측 contract

서빙 runtime metric과 기존 synthetic GPU metric을 함께 제공합니다.

```text
nv_inference_request_success
nv_inference_request_failure
nv_inference_request_duration_us
nv_inference_queue_duration_us
nv_inference_compute_infer_duration_us
gpu_lab_model_loaded
gpu_lab_model_load_duration_seconds
gpu_lab_model_cold_start_total
gpu_lab_serving_fault_active
```

필수 dashboard는 request rate, error rate, p50/p95/p99 latency, queue time, model readiness, replica 수, synthetic GPU memory/utilization, IB 상태를 한 화면에서 연결해야 합니다.

## CLI 초안

```bash
gpu serving deploy \
  --name llama-demo \
  --runtime mock \
  --gpus 1 \
  --replicas 2

gpu serving status llama-demo
gpu serving infer llama-demo --prompt "hello"
gpu serving logs llama-demo

gpu serving inject latency --model llama-demo --delay 3s
gpu serving inject oom --model llama-demo
gpu serving inject load-failure --model llama-demo
gpu serving recover llama-demo
gpu serving reset
```

CPU 실제 추론 예시는 다음 형태를 목표로 합니다.

```bash
gpu serving deploy \
  --name resnet \
  --runtime triton-cpu \
  --model-repository /models \
  --gpus 1
```

## 단계별 구현 순서

### Phase 1 — Mock serving MVP

- mock inference server
- KServe V2 HTTP health, metadata, inference endpoint
- Deployment, Service와 fake GPU resource request
- 기본 request/latency/error metric
- `latency-spike`, `load-failure`, `queue-saturation` 시나리오
- CLI deploy/status/infer/recover/reset

### Phase 2 — CPU-real inference

- 소형 ONNX fixture model
- Triton `KIND_CPU` 또는 ONNX Runtime backend
- model repository와 artifact download 과정
- batching, concurrency와 load test
- mock 응답과 실제 CPU 추론을 동일한 API에서 전환

### Phase 3 — 운영 계층

- KServe 통합 또는 동일한 InferenceService 형태의 경량 controller
- Ingress, 인증, TLS, HPA/KEDA
- canary, rollback과 model version 관리
- Grafana dashboard, alerts와 runbook
- CI E2E에서 정상/장애/복구 자동 검증

### Phase 4 — 실제 GPU parity

- 동일 workload를 실제 GPU 클러스터에 배포
- `triton-cpu`를 `triton-gpu`로 교체
- CUDA/TensorRT, VRAM, 성능, MIG/NCCL 검증 추가
- synthetic 결과와 실제 측정 결과를 별도 dashboard 및 문서로 구분

## MVP 완료 기준

- GPU가 없는 kind 클러스터에서 `nvidia.com/gpu: 1` 서빙 Pod가 스케줄됩니다.
- KServe V2 health, metadata, inference 요청이 정상 동작합니다.
- mock과 CPU-real 모드가 동일한 client contract를 사용합니다.
- 최소 3개 serving 장애 시나리오가 metric, alert, 응답, 복구까지 E2E로 검증됩니다.
- 시나리오 reset 후 모든 모델과 replica가 정상 상태로 돌아옵니다.
- 문서에서 합성 검증과 실제 GPU 검증의 경계가 명시됩니다.

## 참고 자료

- [KServe V2 Inference Protocol](https://kserve.github.io/website/docs/concepts/architecture/data-plane/v2-protocol)
- [Triton Model Configuration — CPU Model Instance](https://docs.nvidia.com/deeplearning/triton-inference-server/archives/triton-inference-server-2600/user-guide/docs/user_guide/model_configuration.html#cpu-model-instance)
- [Triton Metrics](https://docs.nvidia.com/deeplearning/triton-inference-server/user-guide/docs/user_guide/metrics.html)
- [Kubernetes Device Plugins](https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/device-plugins/)
