# Getting Started

gpu-lab은 로컬 Docker runtime 위에 kind cluster를 만들고, 공식 `kubectl`과 Helm CLI를 사용해 교육용 GPU infrastructure를 설치합니다.

## 의존성

- Docker Engine 또는 Docker Desktop
- kind
- kubectl
- Helm 3+
- Go 1.26+ (소스에서 CLI를 실행할 때만 필요)

수강생은 [Release Binary](release.md)에서 자신의 OS와 architecture에 맞는 `gpu-lab`을 설치한 뒤 다음처럼 실행할 수 있습니다.

```bash
gpu-lab version
gpu-lab doctor
```

이 문서의 `go run ./cmd/gpu-lab ...` 예시는 저장소를 clone해 소스에서 실행하는 개발자용 경로입니다. release binary를 설치했다면 `go run ./cmd/gpu-lab` 부분을 `gpu-lab`으로 바꿉니다.

먼저 설치 상태를 확인합니다.

```bash
go run ./cmd/gpu-lab doctor
```

현재 CLI는 의존성 설치 프로그램을 내장하지 않습니다. 각 도구는 공식 설치 방법으로 설치하고, 설치된 공식 바이너리를 사용합니다.

## Cluster 생성

저장소 루트에서 실행합니다.

```bash
go run ./cmd/gpu-lab create
```

`create`는 다음 순서로 동작합니다.

1. release binary라면 CLI version에 맞는 GHCR runtime image를 pull하고, 소스 실행 모드라면 `gpu-lab:dev` image를 빌드한다.
2. control-plane 1개와 fake GPU worker 3개로 kind cluster를 만든다.
3. synthetic `nvidia-device-plugin`과 `dcgm-exporter`를 설치한다.
4. 각 worker에 `nvidia.com/gpu: 8`이 등록될 때까지 기다린다.
5. 공식 Helm CLI로 `kube-prometheus-stack`을 설치한다.
6. ServiceMonitor, PrometheusRule, Grafana dashboard를 provision한다.

또한 `~/.kube/gpu-lab.config`를 만들고 기본 kubeconfig에 `gpu-lab` alias context를 등록합니다. 기존 MLX context는 삭제하거나 덮어쓰지 않습니다.

```bash
gpu-lab context setup
gpu-lab context list
gpu-lab context use gpu-lab
gpu-lab context use 7fa96a70-f1d6-11f0-b997-246e96591a38@mlx-kpb4r/p-example
```

`context use`는 기본 kubeconfig의 current-context를 영구 변경합니다. gpu-lab 내부의 cluster 작업은 current-context에 의존하지 않고 `gpu-lab` context를 명시해 실행합니다.

기본 chart version은 `87.21.0`으로 고정되어 있습니다. 다른 공식 chart version을 검증할 때만 다음 환경 변수를 지정합니다.

```bash
GPU_LAB_HELM_CHART_VERSION=87.21.0 go run ./cmd/gpu-lab create
```

이미지 동작을 직접 선택할 수도 있습니다.

```bash
# release image 사용
gpu-lab create --registry

# 특정 release image 사용
gpu-lab create --registry --image ghcr.io/<github-owner>/gpu-lab-runtime:1.0.0

# local image build 사용
gpu-lab create --local
make dev-create
```

## 공식 Helm CLI 사용

Helm 기능이 필요한 경우 gpu-lab이 공식 Helm을 감싸서 호출합니다.

```bash
gpu-lab helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
gpu-lab helm repo update
gpu-lab helm search repo prometheus-community/kube-prometheus-stack
```

`gpu-lab helm <args...>` 뒤의 인자는 변경 없이 공식 `helm` 프로세스에 전달됩니다. chart repository, OCI registry, plugin, authentication, version 선택도 공식 Helm의 동작을 따릅니다.

## Scenario 실습

```bash
gpu-lab scenario list
gpu-lab scenario run normal
gpu-lab scenario run gpu-util-high
gpu-lab scenario run vram-pressure
gpu-lab scenario run xid-79
gpu-lab scenario run exporter-down
gpu-lab scenario run scheduling-failure
gpu-lab scenario run thermal-throttling
gpu-lab scenario run xid-48
gpu-lab scenario run gpu-idle
gpu-lab scenario run node-selector-mismatch
gpu-lab scenario run gpu-fragmentation
gpu-lab verify gpu-fragmentation
gpu-lab scenario reset
```

Scenario는 `scenarios/*.yaml`에 있으며, 외부 파일도 사용할 수 있습니다.

```bash
gpu-lab scenario run my-scenario --file ./my-scenario.yaml
```

## 관찰 포인트

```bash
gpu-lab status
kubectl --context gpu-lab get nodes
kubectl --context gpu-lab get pods -A
kubectl --context gpu-lab describe pod -n gpu-lab-demo gpu-lab-scheduling-failure
```

Grafana 접근 방식은 설치된 Helm chart의 Service를 확인합니다.

```bash
kubectl --context gpu-lab -n gpu-lab-monitoring get svc
kubectl --context gpu-lab -n gpu-lab-monitoring port-forward svc/gpu-lab-monitoring-grafana 3000:80
```

Dashboard의 모든 metric은 `gpu_lab_` prefix를 사용하며, 실제 NVIDIA DCGM metric이 아니라 합성된 교육용 값입니다.

각 장애의 관찰 명령, 원인 가설, 복구 절차는 [Scenario Runbook](scenarios.md)을 참고합니다.

Scenario를 실행한 뒤에는 검증 명령으로 ConfigMap, Prometheus metric, workload phase와 scheduler event를 한 번에 확인할 수 있습니다.

```bash
gpu-lab verify gpu-fragmentation
gpu-lab verify xid-48
```

## 정리

```bash
gpu-lab scenario reset
gpu-lab destroy
```

저장소 개발자는 kind/Helm 통합 테스트를 실행할 수 있습니다. 이 명령은 마지막에 `gpu-lab` kind cluster를 삭제합니다.

```bash
make e2e
```

현재 클러스터를 보존하며 로컬 smoke만 실행하려면 다음과 같이 지정합니다.

```bash
E2E_KEEP_CLUSTER=1 make e2e
```
