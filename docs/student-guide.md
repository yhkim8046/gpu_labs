# GPU Lab 수강생 가이드

GPU가 없는 노트북에서 Kubernetes GPU Infrastructure의 설치, 모니터링, 장애 대응을 실습하는 순서입니다.

gpu-lab은 실제 GPU나 CUDA를 제공하지 않습니다. Kubernetes GPU scheduling, Device Plugin, DCGM Exporter, Prometheus, Grafana의 운영 흐름을 교육용으로 재현합니다.

## 1. CLI 설치

수강생은 Go를 설치하거나 소스 저장소를 clone하지 않고 GitHub Release binary를 내려받아 사용할 수 있습니다. 현재 공개 Release는 `v0.2.0`입니다. 강의에서 `main`의 최신 시나리오를 사용할 때는 해당 버전의 새 Release가 발행된 뒤 `VERSION` 값을 그 버전으로 변경합니다.

Release 페이지: <https://github.com/yhkim8046/gpu_labs/releases>

### macOS / Linux

아래 예시는 Apple Silicon Mac입니다. Intel Mac은 `ARCH=amd64`, Linux는 `OS=linux`로 바꿉니다. WSL2에서는 Windows binary가 아니라 Linux binary를 설치합니다.

```bash
VERSION=0.2.0
OS=darwin
ARCH=arm64
ASSET="gpu-lab_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/yhkim8046/gpu_labs/releases/download/v${VERSION}"

curl -fL -o "$ASSET" "${BASE_URL}/${ASSET}"
curl -fL -o checksums.txt "${BASE_URL}/checksums.txt"
if command -v shasum >/dev/null 2>&1; then
  grep "$ASSET" checksums.txt | shasum -a 256 -c -
else
  grep "$ASSET" checksums.txt | sha256sum -c -
fi

tar -xzf "$ASSET"
mkdir -p "$HOME/.local/bin"
install -m 0755 gpu-lab "$HOME/.local/bin/gpu-lab"
install -m 0755 gpu "$HOME/.local/bin/gpu"

export PATH="$HOME/.local/bin:$PATH"
gpu-lab version
```

새 터미널에서도 사용하려면 macOS에서는 `~/.zshrc`, Linux에서는 `~/.bashrc`에 다음을 추가합니다.

```bash
export PATH="$HOME/.local/bin:$PATH"
```

강의 명령은 `gpu-lab` 기준으로 설명합니다. `gpu`도 같은 CLI의 짧은 이름으로 함께 제공됩니다.

### Windows PowerShell

Windows native 환경에서는 `.zip` asset을 사용합니다. 아래는 일반적인 Intel/AMD 64-bit Windows 예시이며, ARM Windows는 `amd64`를 `arm64`로 바꿉니다.

```powershell
$Version = "0.2.0"
$Arch = "amd64"
$Asset = "gpu-lab_${Version}_windows_${Arch}.zip"
$BaseUrl = "https://github.com/yhkim8046/gpu_labs/releases/download/v$Version"

Invoke-WebRequest "$BaseUrl/$Asset" -OutFile $Asset
Invoke-WebRequest "$BaseUrl/checksums.txt" -OutFile checksums.txt
$Expected = (Select-String -Path checksums.txt -Pattern $Asset).Line.Split()[0].ToLower()
$Actual = (Get-FileHash $Asset -Algorithm SHA256).Hash.ToLower()
if ($Expected -ne $Actual) { throw "checksum verification failed" }

Expand-Archive $Asset -DestinationPath .\gpu-lab-$Version -Force
New-Item -ItemType Directory -Force -Path "$HOME\bin" | Out-Null
Copy-Item ".\gpu-lab-$Version\gpu-lab.exe" "$HOME\bin\gpu-lab.exe" -Force
Copy-Item ".\gpu-lab-$Version\gpu.exe" "$HOME\bin\gpu.exe" -Force

$env:Path += ";$HOME\bin"
& "$HOME\bin\gpu-lab.exe" version
```

새 PowerShell 창에서도 사용하려면 `$HOME\bin`을 Windows 사용자 `PATH`에 추가합니다.

설치가 끝나면 의존성을 확인합니다.

```bash
gpu-lab version
gpu-lab doctor
```

## 2. 사전 준비

Linux, macOS, Windows + WSL2에서 사용할 수 있습니다.

- Docker 또는 Docker Desktop
- kind
- kubectl
- Helm

```bash
gpu-lab doctor
```

gpu-lab 명령이 없다면 강의에서 안내한 GitHub Release binary를 설치합니다. 소스에서는 다음처럼 실행할 수 있습니다.

```bash
go run ./cmd/gpu-lab doctor
```

## 3. 클러스터 생성

gpu-lab create는 kind 클러스터와 runtime image만 준비합니다. GPU 구성요소는 수강생이 Helm으로 직접 설치합니다.

```bash
gpu-lab create
gpu-lab context list
kubectl --context gpu-lab get nodes -o wide
```

기본 클러스터는 control-plane 1개와 GPU worker 3개로 구성됩니다.

```text
gpu-lab-control-plane
gpu-lab-worker       # gpu-node-01
gpu-lab-worker2      # gpu-node-02
gpu-lab-worker3      # gpu-node-03
```

## 4. GPU Infrastructure 설치

```bash
gpu-lab helm catalog
gpu-lab helm install nvidia-device-plugin
gpu-lab helm install dcgm-exporter
gpu-lab helm install monitoring
gpu-lab helm list --all-namespaces
```

설치 결과는 다음 명령으로 확인합니다.

```bash
kubectl --context gpu-lab get pods -A
kubectl --context gpu-lab get nodes -o wide
```

gpu-lab helm은 Helm을 대체하는 별도 패키지 관리자가 아닙니다. GPU Lab component는 고정된 교육용 chart를 사용하고, 일반 Helm 명령은 공식 Helm으로 전달합니다.

```bash
gpu-lab helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
gpu-lab helm repo update
gpu-lab helm search repo prometheus-community
```

## 5. Grafana와 Prometheus 접속

Grafana:

```bash
gpu-lab dashboard
```

브라우저에서 http://127.0.0.1:3000을 열고 다음으로 로그인합니다.

```text
Username: admin
Password: admin
```

포트를 변경하려면 gpu-lab dashboard --port 3001을 사용합니다.

Prometheus:

```bash
kubectl --context gpu-lab -n gpu-lab-monitoring \
  port-forward svc/gpu-lab-monitoring-kube-pr-prometheus 9090:9090
```

브라우저 주소는 http://127.0.0.1:9090입니다.

CLI에서 PromQL을 직접 실행할 수도 있습니다.

```bash
gpu-lab metrics
gpu-lab metrics --query 'max(gpu_lab_gpu_temperature_celsius)'
gpu-lab metrics --query 'max(gpu_lab_gpu_ecc_dbe_total)'
gpu-lab metrics --query 'max(gpu_lab_node_gpu_capacity - gpu_lab_node_gpu_allocatable)'
```

## 6. 공통 실습 흐름

```bash
gpu-lab scenario list
gpu-lab scenario inspect <scenario>
gpu-lab scenario run <scenario>
gpu-lab status
gpu-lab metrics
gpu-lab verify <scenario>
```

Kubernetes 상태와 이벤트를 함께 확인합니다.

```bash
kubectl --context gpu-lab get pods -A -o wide
kubectl --context gpu-lab get events -A --sort-by=.lastTimestamp
kubectl --context gpu-lab get nodes -o wide
```

실습이 끝나면 다음 명령으로 scenario와 실습 workload를 초기화합니다.

```bash
gpu-lab scenario reset
```

## 7. 제공되는 시나리오

| Scenario | 실습 내용 | 핵심 관찰 포인트 |
|---|---|---|
| normal | 정상 baseline | utilization, temperature, health |
| gpu-util-high | GPU 포화 | utilization 95% |
| vram-pressure | VRAM 부족 | VRAM usage 92% |
| thermal-throttling | 고온과 성능 저하 | temperature, power, utilization |
| ecc-double-bit | 수정 불가능한 ECC 오류 | ECC DBE, XID 48, health |
| xid-48 | XID 48 장애 신호 | XID 48, unhealthy |
| xid-79 | GPU bus 장애 신호 | XID 79, unhealthy |
| power-throttle | 전력 제한 throttling | power violation, throttle |
| pcie-replay | PCIe link 오류 | replay counter |
| gpu-allocated-idle | 할당됐지만 사용하지 않는 GPU | allocated, utilization |
| gpu-capacity-mismatch | capacity/allocatable 불일치 | Node resource metric |
| scheduling-failure | GPU 요청량 초과 | Pending, Insufficient GPU |
| node-selector-mismatch | 잘못된 GPU selector | Pending, scheduler event |
| gpu-fragmentation | Node별 GPU fragmentation | Node별 여유량 |
| exporter-down | 관측 계층 장애 | Prometheus target down |
| gpu-idle | 기존 idle 실습 | 호환용 alias |

## 8. 핵심 5개 시나리오

### ECC Double-bit

```bash
gpu-lab scenario run ecc-double-bit
gpu-lab verify ecc-double-bit
gpu-lab metrics --query 'max(gpu_lab_gpu_ecc_dbe_total)'
gpu-lab metrics --query 'max(gpu_lab_gpu_health_status)'
```

ECC DBE counter, XID 48, GPU health를 함께 확인합니다. 실제 환경에서는 workload drain, GPU 격리, node 상태 확인으로 이어집니다.

### Power Throttle

```bash
gpu-lab scenario run power-throttle
gpu-lab verify power-throttle
gpu-lab metrics --query 'max(gpu_lab_gpu_power_violation_total)'
gpu-lab metrics --query 'max(gpu_lab_gpu_throttle_active{reason="power_cap"})'
```

Grafana에서 utilization, temperature, power, throttle을 함께 비교해 전력 제한으로 인한 성능 저하를 분석합니다.

### PCIe Replay

```bash
gpu-lab scenario run pcie-replay
gpu-lab verify pcie-replay
gpu-lab metrics --query 'max(gpu_lab_gpu_pcie_replay_total)'
```

GPU health가 정상이어도 PCIe replay가 증가할 수 있습니다. 실제 GPU 환경에서는 PCIe link speed/width, host bridge, kernel log를 추가로 확인합니다.

### GPU Allocated but Idle

```bash
gpu-lab scenario run gpu-allocated-idle
gpu-lab verify gpu-allocated-idle
gpu-lab metrics --query 'max(gpu_lab_gpu_allocated)'
gpu-lab metrics --query 'avg(gpu_lab_gpu_utilization_percent)'
kubectl --context gpu-lab get pod gpu-lab-allocated-idle-workload -n gpu-lab-demo -o wide
```

GPU를 할당받은 workload가 실행 중이지만 utilization이 2%입니다. 장애가 아니라 capacity 낭비 문제이며 request sizing과 idle workload 회수 정책을 토론합니다.

### GPU Capacity Mismatch

```bash
gpu-lab scenario run gpu-capacity-mismatch
gpu-lab verify gpu-capacity-mismatch
gpu-lab metrics --query 'max(gpu_lab_node_gpu_capacity)'
gpu-lab metrics --query 'max(gpu_lab_node_gpu_allocatable)'
kubectl --context gpu-lab get pod gpu-lab-capacity-mismatch-workload -n gpu-lab-demo -o wide
```

Synthetic telemetry는 capacity 8, allocatable 4를 보고하지만 실제 scheduler는 workload를 배치합니다. 이 시나리오는 device-plugin registration, kubelet Node status, scheduler 결과를 대조하는 연습입니다.

## 9. 장애 조사 명령

Pending Pod:

```bash
kubectl --context gpu-lab describe pod -n gpu-lab-demo <pod-name>
kubectl --context gpu-lab get events -n gpu-lab-demo --sort-by=.lastTimestamp
kubectl --context gpu-lab describe nodes
```

Exporter 장애:

```bash
kubectl --context gpu-lab get pods -n gpu-lab-system -l app.kubernetes.io/name=dcgm-exporter
kubectl --context gpu-lab get servicemonitor -n gpu-lab-monitoring
gpu-lab metrics --query 'sum(up{service="dcgm-exporter"})'
```

현재 scenario 확인:

```bash
kubectl --context gpu-lab get configmap gpu-lab-scenario -n gpu-lab-system -o yaml
gpu-lab scenario inspect <scenario>
```

## 10. 종료

시나리오만 초기화:

```bash
gpu-lab scenario reset
```

클러스터 전체 삭제:

```bash
gpu-lab destroy
```

## 11. 반드시 기억할 한계

- 실제 NVIDIA GPU와 CUDA kernel을 사용하지 않습니다.
- XID, ECC, PCIe replay는 synthetic metric입니다.
- gpu_lab_* metric은 실제 DCGM metric과 의미가 완전히 같지 않습니다.
- gpu-lab scenario reset은 실제 GPU reset, node drain, reboot를 수행하지 않습니다.
- 실제 환경에서는 nvidia-smi, dcgmi, kernel log, GPU Operator 상태를 함께 확인해야 합니다.

이 프로젝트의 목표는 실제 GPU 장애를 발생시키는 것이 아니라, GPU Infrastructure 장애를 관찰하고 조사하는 운영 절차를 반복 연습하는 것입니다.
