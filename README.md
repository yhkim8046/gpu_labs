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
- YAML 기반 GPU incident scenario
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

`gpu helm ...`은 Helm을 재구현하지 않습니다. release binary는 버전이 고정된 GPU Lab chart를 GHCR OCI registry에서 내려받아 실제 `helm install`을 실행하며, 일반 Helm 명령은 공식 CLI로 전달됩니다. cluster 명령은 항상 `gpu-lab` kube context를 사용합니다.

## 설계 문서

- [Architecture Proposal](docs/architecture.md)
- [Getting Started](docs/getting-started.md)
- [Student Guide](docs/student-guide.md)
- [Release & Installation](docs/release.md)
- [Scenario Runbook](docs/scenarios.md)
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
gpu version
gpu doctor
gpu create
gpu helm install nvidia-device-plugin
gpu helm install dcgm-exporter
gpu helm install monitoring
```

Git tag `v1.0.0`을 push하면 GitHub Actions가 GHCR에 다음 multi-arch runtime image를 publish합니다.

```text
ghcr.io/<github-owner>/gpu-lab-runtime:1.0.0
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
```

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

## OSS 운영 원칙

- 기본 브랜치는 항상 재현 가능한 상태를 유지합니다.
- 기능은 문서, 테스트, 구현 순서로 쪼개어 공개합니다.
- 실제 NVIDIA 환경에서 동작한다고 오해할 수 있는 표현을 피합니다.
- synthetic metric과 실제 DCGM metric의 차이를 문서화합니다.
- 공개 전 라이선스와 contribution policy를 확정합니다.

## License

이 프로젝트는 [MIT License](LICENSE)로 배포됩니다.
