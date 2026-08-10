# Getting Started

gpu-lab은 로컬 Docker runtime 위에 kind cluster를 만들고, 공식 `kubectl`과 Helm CLI를 사용해 교육용 GPU infrastructure를 설치합니다.

## 의존성

- Docker Engine 또는 Docker Desktop
- kind
- kubectl
- Helm 3+
- Go 1.26+ (소스에서 CLI를 실행할 때만 필요)

수강생은 [Release Binary](release.md)에서 자신의 OS와 architecture에 맞는 `gpu`를 설치한 뒤 다음처럼 실행할 수 있습니다.

```bash
gpu version
gpu doctor
```

이 문서의 `go run ./cmd/gpu-lab ...` 예시는 저장소를 clone해 소스에서 실행하는 개발자용 경로입니다. release binary를 설치했다면 `go run ./cmd/gpu-lab` 부분을 `gpu`로 바꿉니다. `gpu-lab`은 이전 버전 호환 alias입니다.

수강생용 최초 설치는 다음 스크립트를 사용합니다. Docker Desktop 또는 Docker Engine은 먼저 설치하고 실행해야 합니다.

```bash
curl --fail --silent --show-error --location \
  --output gpu-lab-install.sh \
  https://raw.githubusercontent.com/yhkim8046/gpu_labs/main/scripts/install.sh
bash gpu-lab-install.sh
```

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

`create`는 base cluster만 준비합니다.

1. release binary라면 CLI version에 맞는 GHCR runtime image를 pull하고, 소스 실행 모드라면 `gpu-lab:dev` image를 빌드한다.
2. control-plane 1개와 fake GPU worker 3개로 kind cluster를 만든다.
3. runtime image를 kind node에 load하고 모든 node가 Ready일 때까지 기다린다.

이 시점에는 GPU capacity, exporter, Prometheus, Grafana가 아직 없습니다. 이것이 강의의 시작 상태입니다.

또한 `~/.kube/gpu-lab.config`를 만들고 기본 kubeconfig에 `gpu-lab` alias context를 등록합니다. 기존 MLX context는 삭제하거나 덮어쓰지 않습니다.

```bash
gpu context setup
gpu context list
gpu context use gpu-lab
gpu context use 7fa96a70-f1d6-11f0-b997-246e96591a38@mlx-kpb4r/p-example
```

`context use`는 기본 kubeconfig의 current-context를 영구 변경합니다. gpu-lab 내부의 cluster 작업은 current-context에 의존하지 않고 `gpu-lab` context를 명시해 실행합니다.

기본 chart version은 `87.21.0`으로 고정되어 있습니다. 다른 공식 chart version을 검증할 때만 다음 환경 변수를 지정합니다.

```bash
GPU_LAB_HELM_CHART_VERSION=87.21.0 gpu helm install monitoring
```

이미지 동작을 직접 선택할 수도 있습니다.

```bash
# release image 사용
gpu create --registry

# 특정 release image 사용
gpu create --registry --image ghcr.io/<github-owner>/gpu-lab-runtime:1.0.0

# local image build 사용
gpu create --local
make dev-create
```

## GPU Infrastructure를 Helm으로 설치

설치 가능한 강의 component를 확인하고 순서대로 설치합니다.

```bash
gpu helm catalog

gpu helm install nvidia-device-plugin
kubectl --context gpu-lab get nodes

gpu helm install dcgm-exporter
kubectl --context gpu-lab get pods -n gpu-lab-system

gpu helm install monitoring
gpu helm list --all-namespaces
```

각 shorthand는 실제 공식 Helm 프로세스로 다음 release를 만들거나 업데이트합니다.

| 명령 | Helm release | namespace |
|---|---|---|
| `gpu helm install nvidia-device-plugin` | `nvidia-device-plugin` | `gpu-lab-system` |
| `gpu helm install dcgm-exporter` | `dcgm-exporter` | `gpu-lab-system` |
| `gpu helm install monitoring` | `gpu-lab-monitoring` | `gpu-lab-monitoring` |

`nvidia-device-plugin`과 `dcgm-exporter`는 release version과 같은 GPU Lab synthetic OCI chart를 GHCR에서 다운로드합니다. `monitoring`은 고정된 공식 `kube-prometheus-stack` OCI chart를 다운로드합니다. 소스의 `dev` build는 아직 publish되지 않은 chart를 시험할 수 있도록 embedded chart를 사용합니다. 일반 Helm 명령도 그대로 사용할 수 있습니다.

```bash
gpu helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
gpu helm repo update
gpu helm search repo prometheus-community/kube-prometheus-stack
gpu helm install my-release prometheus-community/example-chart
```

cluster-aware 명령에는 `--kube-context gpu-lab`이 자동 적용됩니다. 사용자가 `--kube-context` 또는 `--kubeconfig`를 직접 지정하면 그 값을 존중합니다. 전체 자동 설치가 필요한 CI/개발 환경에서는 `gpu create --all` 또는 `gpu helm install all`을 사용할 수 있습니다.

구성요소 설치는 내부적으로 Helm `upgrade --install`을 사용하므로 `gpu create --all`이나 `gpu helm install all`을 중단 후 다시 실행해도 이미 설치된 release를 업데이트하며 계속 진행합니다.

## Scenario 실습

```bash
gpu scenario list
gpu scenario inspect xid-79
gpu scenario run normal
gpu scenario run gpu-util-high
gpu scenario run vram-pressure
gpu scenario run xid-79
gpu scenario run exporter-down
gpu scenario run scheduling-failure
gpu scenario run thermal-throttling
gpu scenario run xid-48
gpu scenario run gpu-idle
gpu scenario run ecc-double-bit
gpu scenario run power-throttle
gpu scenario run pcie-replay
gpu scenario run gpu-allocated-idle
gpu scenario run gpu-capacity-mismatch
gpu scenario run node-selector-mismatch
gpu scenario run gpu-fragmentation
gpu verify gpu-fragmentation
gpu scenario reset
```

Scenario는 `scenarios/*.yaml`에 있으며, 외부 파일도 사용할 수 있습니다.

```bash
gpu scenario run my-scenario --file ./my-scenario.yaml
```

## 관찰 포인트

```bash
gpu status
gpu metrics
gpu metrics --query 'max(gpu_lab_gpu_xid_code)'
gpu metrics --query 'max(gpu_lab_gpu_ecc_dbe_total)'
gpu metrics --query 'max(gpu_lab_gpu_power_violation_total)'
gpu metrics --query 'max(gpu_lab_gpu_pcie_replay_total)'
gpu metrics --query 'max(gpu_lab_node_gpu_capacity - gpu_lab_node_gpu_allocatable)'
gpu nvidia-smi
nvidia-smi --list-gpus
nvidia-smi --query-gpu=temperature.gpu,memory.used,utilization.gpu --format=csv,noheader,nounits
gpu nvidia-smi --node gpu-lab-worker
gpu ibstat --node gpu-lab-worker
gpu ibstatus --node gpu-lab-worker
gpu ibv_devinfo --node gpu-lab-worker -v
kubectl --context gpu-lab get nodes
kubectl --context gpu-lab get pods -A
kubectl --context gpu-lab describe pod -n gpu-lab-demo gpu-lab-scheduling-failure
```

`gpu nvidia-smi`는 synthetic 클러스터의 노드마다 실제 `nvidia-smi`에 가까운 별도 블록을 출력합니다. 특정 노드만 확인하려면 `--node <node-name>`을 지정하면 됩니다. 이 명령은 노드에 SSH로 접속하지 않고 Prometheus의 `gpu_lab_*` metric을 조회합니다.

일반 운영 스크립트에서 자주 사용하는 identity, PCI, memory 필드도 CSV로 조회할 수 있습니다. `memory.free`와 `utilization.memory`는 synthetic VRAM 값에서 계산한 값이며, fan/clock처럼 Lab이 관측하지 않는 물리 센서는 `N/A`로 표시됩니다.

```bash
gpu nvidia-smi \
  --query-gpu=index,name,uuid,driver_version,pci.bus_id,serial,memory.used,memory.free,memory.total,utilization.gpu,utilization.memory \
  --format=csv
```

`nvidia-smi`는 GPU Lab release에 포함된 호환 명령입니다. 실제 NVIDIA driver나 CUDA를 호출하지 않고 Prometheus의 synthetic metric을 표시하므로, 실제 장비의 `nvidia-smi`와 동일한 성능·device file·driver 정보는 제공하지 않습니다.

`ibstat`, `ibstatus`, `ibv_devinfo`도 release와 runtime image에 같은 이름으로 포함됩니다. 호스트에서는 `--node` GPU Lab 확장 옵션으로 worker를 선택하고, exporter Pod 안에서는 옵션 없이 실제 명령처럼 실행합니다. 출력 형식과 rdma-core 옵션은 호환하지만 `/dev/infiniband`나 실제 verbs context를 생성하지는 않습니다.

Grafana 접근 방식은 설치된 Helm chart의 Service를 확인합니다.

```bash
kubectl --context gpu-lab -n gpu-lab-monitoring get svc
kubectl --context gpu-lab -n gpu-lab-monitoring port-forward svc/gpu-lab-monitoring-grafana 3000:80

# 위 port-forward를 짧게 쓰는 CLI shortcut
gpu dashboard
gpu dashboard --port 3001
```

GPU Lab는 교육용 disposable 환경이므로 Grafana 로그인은 다음 자격증명을 사용합니다.

- Username: `admin`
- Password: `admin`

이 고정 자격증명은 강의 실습 편의를 위한 것이며 production 환경에서 사용하면 안 됩니다.

Dashboard의 모든 metric은 `gpu_lab_` prefix를 사용하며, 실제 NVIDIA DCGM metric이 아니라 합성된 교육용 값입니다. 실제 field와의 대응은 [metric mapping](metric-mapping.md)을 참고합니다.

각 장애의 관찰 명령, 원인 가설, 복구 절차는 [Scenario Runbook](scenarios.md)을 참고합니다.

Scenario를 실행한 뒤에는 검증 명령으로 ConfigMap, Prometheus metric, workload phase와 scheduler event를 한 번에 확인할 수 있습니다.

```bash
gpu verify gpu-fragmentation
gpu verify xid-48
```

## 정리

```bash
gpu scenario reset
gpu destroy
```

저장소 개발자는 kind/Helm 통합 테스트를 실행할 수 있습니다. 이 명령은 마지막에 `gpu-lab` kind cluster를 삭제합니다.

```bash
make e2e
```

현재 클러스터를 보존하며 로컬 smoke만 실행하려면 다음과 같이 지정합니다.

```bash
E2E_KEEP_CLUSTER=1 make e2e
```
