# Getting Started

gpu-lab은 로컬 Docker runtime 위에 kind cluster를 만들고, 공식 `kubectl`과 Helm CLI를 사용해 교육용 GPU infrastructure를 설치합니다.

## 의존성

- Docker Engine 또는 Docker Desktop
- kind
- kubectl
- Helm 3+
- Go 1.26+ (소스에서 CLI를 실행할 때)

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

1. `gpu-lab:dev` image를 빌드한다.
2. control-plane 1개와 fake GPU worker 3개로 kind cluster를 만든다.
3. Fake Device Plugin과 Mock Exporter를 설치한다.
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
gpu-lab scenario reset
```

Scenario는 `scenarios/*.yaml`에 있으며, 외부 파일도 사용할 수 있습니다.

```bash
gpu-lab scenario run my-scenario --file ./my-scenario.yaml
```

## 관찰 포인트

```bash
gpu-lab status
kubectl --context kind-gpu-lab get nodes
kubectl --context kind-gpu-lab get pods -A
kubectl --context kind-gpu-lab describe pod -n gpu-lab-demo gpu-lab-scheduling-failure
```

Grafana 접근 방식은 설치된 Helm chart의 Service를 확인합니다.

```bash
kubectl --context kind-gpu-lab -n gpu-lab-monitoring get svc
kubectl --context kind-gpu-lab -n gpu-lab-monitoring port-forward svc/gpu-lab-monitoring-grafana 3000:80
```

Dashboard의 모든 metric은 `gpu_lab_` prefix를 사용하며, 실제 NVIDIA DCGM metric이 아니라 합성된 교육용 값입니다.

## 정리

```bash
gpu-lab scenario reset
gpu-lab destroy
```
