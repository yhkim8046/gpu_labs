# gpu-lab

GPU가 없는 노트북에서 Kubernetes GPU Infrastructure를 실습하기 위한 교육용 오픈소스 프로젝트입니다.

> 현재 상태: **MVP implementation — local tests와 macOS kind/Helm e2e 통과**

gpu-lab은 GPU나 CUDA를 흉내 내는 프로젝트가 아닙니다. Kubernetes에서 GPU 리소스가 스케줄링되고, 모니터링되며, 장애 상황에서 어떤 신호를 확인하고 복구하는지를 학습하기 위한 로컬 실습 환경입니다.

## 무엇을 실습하는가

- kind 기반 multi-node Kubernetes cluster
- `nvidia.com/gpu` Extended Resource와 GPU scheduling
- NVIDIA Device Plugin의 등록·할당 모델을 따르는 synthetic `nvidia-device-plugin`
- DCGM Exporter의 관측 모델을 따르는 synthetic `dcgm-exporter`
- Prometheus와 Grafana 기반 GPU monitoring
- 3-rank synthetic AllReduce 분산학습과 checkpoint/장애 복구 실습
- synthetic InfiniBand/RDMA HCA·port·fabric 장애와 AllReduce 영향 관측
- YAML 기반 GPU incident scenario(normal baseline 1개를 포함해 기본 22개, 이 중 장애 시나리오는 21개)
- scheduling failure, exporter down, thermal, ECC/XID, power throttling, PCIe replay, idle GPU, capacity mismatch, selector mismatch, fragmentation troubleshooting

실제 GPU 장치, NVIDIA driver, CUDA kernel 실행은 범위에 포함하지 않습니다.

## 목표 환경

- Linux
- macOS
- Windows + WSL2
- Docker Engine 또는 Docker Desktop
- kind 기반 실행

CLI는 cluster bootstrap과 component 설치를 분리합니다. `gpu create`는 kind cluster와 runtime image만 준비하고, 수강생은 GPU infrastructure 구성요소를 실제 Helm release로 하나씩 설치합니다.

```bash
gpu create
gpu helm catalog
gpu helm install nvidia-device-plugin
gpu helm install dcgm-exporter
gpu helm install monitoring
gpu helm list --all-namespaces
```

`gpu helm ...`은 Helm을 재구현하지 않습니다. release binary는 버전이 고정된 GPU Lab chart를 GHCR OCI registry에서 내려받아 공식 Helm의 `upgrade --install`을 실행하며, 일반 Helm 명령은 공식 CLI로 전달됩니다. cluster 명령은 항상 `gpu-lab` kube context를 사용합니다.

## 설계 문서

- [Architecture Proposal](docs/architecture.md)
- [Getting Started](docs/getting-started.md)
- [Student Guide](docs/student-guide.md)
- [Course Package](docs/course/README.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Release & Installation](docs/release.md)
- [Scenario Runbook](docs/scenarios.md)
- [Distributed Training Runbook](docs/distributed-training.md)
- [InfiniBand/RDMA Fabric Runbook](docs/ib-fabric.md)
- [GPU-less Model Serving Expansion Proposal](docs/model-serving-expansion.md)
- [Synthetic vs Real](docs/synthetic-vs-real.md)
- [Metric Mapping](docs/metric-mapping.md)
- [Support Matrix](docs/support-matrix.md)
- [Course Readiness & Roadmap](docs/course-readiness.md)

문서에는 다음 내용이 포함되어 있습니다.

- component diagram과 runtime topology
- Fake GPU와 Extended Resource 설계
- `dcgm-exporter` 및 metric contract
- YAML Scenario Engine과 reset semantics
- CLI와 repository 구조
- integration acceptance criteria
- 기술적 리스크와 synthetic 환경의 한계

## 구현 순서

1. Architecture Proposal
2. kind infrastructure, Fake GPU, Prometheus, Grafana
3. Go `dcgm-exporter`
4. YAML Scenario Engine
5. `gpu-lab` CLI
6. Integration Test

각 단계는 검증 가능한 상태로 끝내며, 이전 단계의 동작을 깨뜨리지 않는 것을 원칙으로 합니다.

## Release binary 설치

강의 수강생은 Go toolchain 없이 [Release & Installation](docs/release.md)의 release binary를 설치할 수 있습니다. 기본 명령은 `gpu`이며, 기존 `gpu-lab` 이름도 호환용으로 함께 제공합니다.

```bash
curl --fail --silent --show-error --location \
  --output gpu-lab-install.sh \
  https://raw.githubusercontent.com/yhkim8046/gpu_labs/main/scripts/install.sh
bash gpu-lab-install.sh

gpu version
gpu doctor
gpu create --all
```

GitHub Actions는 release tag와 같은 버전의 multi-arch runtime image를 GHCR에 publish합니다. 예를 들어 `v0.3.0` tag가 publish되면 image tag는 `0.3.0`입니다.

```text
ghcr.io/<github-owner>/gpu-lab-runtime:0.3.0
```

Release binary는 CLI version에 맞는 GHCR image를 자동으로 사용합니다. 소스에서 `go run`을 실행하거나 `gpu create --local`을 지정하면 local `gpu-lab:dev` image를 build합니다.

## 소스에서 실행하는 명령

```bash
go run ./cmd/gpu-lab doctor
go run ./cmd/gpu-lab scenario list
go run ./cmd/gpu-lab scenario inspect xid-79
go run ./cmd/gpu-lab scenario run xid-79
go run ./cmd/gpu-lab verify xid-79
go run ./cmd/gpu-lab metrics
go run ./cmd/gpu-lab dashboard
go run ./cmd/gpu-lab scenario reset
go run ./cmd/gpu-lab helm repo list
go run ./cmd/gpu-lab context list
go run ./cmd/gpu-lab context use gpu-lab
go run ./cmd/gpu-lab nvidia-smi
go run ./cmd/gpu-lab ibstat --node gpu-lab-worker
go run ./cmd/gpu-lab ibstatus --node gpu-lab-worker
go run ./cmd/gpu-lab ibv_devinfo --node gpu-lab-worker -v
go run ./cmd/gpu-lab training run --workers 3 --wait
go run ./cmd/gpu-lab training status
go run ./cmd/gpu-lab training inject straggler --rank 2 --delay 2s
go run ./cmd/gpu-lab training recover
go run ./cmd/gpu-lab scenario run ib-rate-degraded
go run ./cmd/gpu-lab verify ib-rate-degraded
go run ./cmd/gpu-lab verify --recovery ib-rate-degraded   # reset 이후 정상화만 별도 판정
go run ./cmd/gpu-lab scenario reset
```

분산학습 실습은 세 worker의 rendezvous, AllReduce 지연, rank 장애, checkpoint 기반 재시작과 Prometheus/Grafana 관측 흐름을 재현합니다. InfiniBand/RDMA 실습은 synthetic HCA와 port 상태, link rate, 오류·retry·congestion 신호를 AllReduce 진행과 연결합니다. 연산과 metric은 결정적인 synthetic 값이며 실제 CUDA, NCCL, RDMA 또는 NVLink 성능을 측정하지 않습니다. 전체 절차는 [Distributed Training Runbook](docs/distributed-training.md)과 [InfiniBand/RDMA Fabric Runbook](docs/ib-fabric.md)을 따르세요.

GPU Lab는 synthetic telemetry를 NVIDIA-SMI 형태로 확인할 수 있는 synthetic 조회 명령도 제공합니다. synthetic 조회는 GPU Lab가 배포하는 동명 바이너리 또는 synthetic 모드로 실행하는 명령만 해당하며, `gpu` wrapper의 `auto`(기본)와 `real` 모드는 설치된 실제 `nvidia-smi`를 호출합니다.

```bash
GPU_LAB_MODE=synthetic gpu nvidia-smi
GPU_LAB_MODE=synthetic gpu nvidia-smi --list-gpus
GPU_LAB_MODE=synthetic gpu nvidia-smi --query-gpu=temperature.gpu,memory.used,utilization.gpu --format=csv,noheader,nounits
GPU_LAB_MODE=synthetic gpu nvidia-smi --node gpu-lab-worker
gpu ibstat --node gpu-lab-worker
gpu ibstatus --node gpu-lab-worker
gpu ibv_devinfo --node gpu-lab-worker -v
```

모의 출력은 synthetic 노드별로 하나의 NVIDIA-SMI 블록을 보여주며, 설치된 Prometheus의 `gpu_lab_*` metric을 읽습니다. 특정 노드만 확인하려면 `--node <node-name>`을 사용합니다. GPU Lab의 동명 독립 바이너리와 명시적 `synthetic` 모드는 실제 NVIDIA driver/CUDA를 사용하지 않습니다. `gpu nvidia-smi`의 기본 `auto` 모드는 설치된 실제 도구를 우선하며, 도구가 없고 실험실 근거가 확인되면 내부 모의 명령을 사용합니다. 실제 모드는 원래 명령 인자를 그대로 전달합니다. Release archive의 독립 `nvidia-smi` 호출은 모의 경로로 계속 지원됩니다.

Release와 runtime image에는 rdma-core 명령 이름을 사용하는 모의 `ibstat`, `ibstatus`, `ibv_devinfo` 바이너리도 포함됩니다. 위 예제의 모의 모드에서는 CA·GUID·LID·port state·physical state·rate·MTU 형식으로 synthetic HCA 상태를 표시합니다. `gpu ibstat` 등은 실제 모드에서 호스트에 설치된 해당 도구를 호출하고 인자를 그대로 전달합니다.

실제 cluster lifecycle을 실행하려면 Docker, kind, kubectl, Helm을 설치한 뒤 저장소 루트에서 다음을 실행합니다.

```bash
go run ./cmd/gpu-lab create
go run ./cmd/gpu-lab helm install nvidia-device-plugin
go run ./cmd/gpu-lab helm install dcgm-exporter
go run ./cmd/gpu-lab helm install monitoring
go run ./cmd/gpu-lab status
go run ./cmd/gpu-lab metrics
go run ./cmd/gpu-lab dashboard --port 3000
go run ./cmd/gpu-lab verify normal
go run ./cmd/gpu-lab destroy
```

개발 검증은 다음 명령으로 실행합니다. `make e2e`는 마지막에 `gpu-lab` kind cluster를 삭제합니다.

```bash
make test
make e2e
```

## GPU 점검 (gpu inspect)

`gpu inspect`는 NVIDIA 소스나 바이너리를 복제하지 않고, 로컬에 설치된 실제 `nvidia-smi`를 호출하는 점검 명령입니다. 이 기능은 사용자가 설치한 도구를 호출하는 비공식 GPU Lab 기능입니다. NVIDIA와 제휴하거나 인증받은 도구가 아닙니다. 점검은 명령을 실행하는 호스트의 로컬 GPU를 대상으로 하며, Kubernetes worker의 원격 하드웨어를 점검하는 기능이 아닙니다. 모든 결과에 real/synthetic 출처와 비공식 안내가 포함됩니다.

```bash
gpu inspect                      # 기본값 auto
gpu inspect --mode real          # 설치된 nvidia-smi만, 없으면 unavailable
gpu inspect --mode synthetic     # 실험실 근거만
gpu inspect --json
gpu inspect --timeout 10s

gpu --mode real nvidia-smi -q              # 전역 모드 선택
GPU_LAB_MODE=real gpu nvidia-smi -q        # 환경 변수
gpu --mode real dcgmi discovery -l        # 설치된 DCGM 도구 호출
```

- `auto`(기본)는 번들된 `nvidia-smi` shim을 제외하고 PATH의 실제 `nvidia-smi`를 찾아 `nvidia-smi -q -x`를 호출합니다. `nvidia-smi`가 없으면 실험실의 긍정적 근거를 사용하고, 둘 다 없으면 `unavailable`입니다. `nvidia-smi` 탐색이나 호출이 실패해도 synthetic으로 대체하지 않습니다. `GPU_LAB_NVIDIA_SMI=/absolute/path/nvidia-smi`로 공식 `nvidia-smi` 경로를 직접 지정할 수 있습니다.
- `real`은 `nvidia-smi`가 필수이고 `dcgmi`는 선택입니다. `dcgmi`는 `dcgmi discovery -l`만 호출하며 `reset`, `config`, `diag`를 자동 실행하지 않습니다. `synthetic`은 두 제조사 도구가 없어도 실행할 수 있습니다.
- `gpu nvidia-smi`의 real 경로는 NVIDIA-SMI 인자를 변경 없이 설치된 `nvidia-smi`에 그대로 전달합니다. `--query-gpu`, `--format=csv,noheader,nounits`, `--list-gpus`, `--node` 부분집합 제한은 synthetic에만 적용됩니다.
- 상태는 `pass`/`warn`/`fail`/`unavailable`/`unsupported`입니다. 드라이버가 없으면 `unavailable`이며 샘플 출력은 하드웨어 검증이 아닙니다.
- 명령 전달 안내는 stderr에 출력해 CSV를 보존하고, 점검 JSON에는 비공식 메타데이터를 포함합니다. 세부 사용법은 [GPU 점검 문서](docs/inspect.md)와 공식 참고 문서 [NVIDIA-SMI](https://docs.nvidia.com/deploy/nvidia-smi/index.html), [DCGM](https://docs.nvidia.com/datacenter/dcgm/latest/user-guide/feature-overview.html)를 참고하세요.

## OSS 운영 원칙

- 기본 브랜치는 항상 재현 가능한 상태를 유지합니다.
- 기능은 문서, 테스트, 구현 순서로 쪼개어 공개합니다.
- 실제 NVIDIA 환경에서 동작한다고 오해할 수 있는 표현을 피합니다.
- synthetic metric과 실제 DCGM metric의 차이를 문서화합니다.
- 공개 전 라이선스와 contribution policy를 확정합니다.

## License

이 프로젝트는 [MIT License](LICENSE)로 배포됩니다.
