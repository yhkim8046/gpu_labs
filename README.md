# gpu-lab

GPU가 없는 노트북에서 Kubernetes GPU Infrastructure를 실습하기 위한 교육용 오픈소스 프로젝트입니다.

> 현재 상태: **Phase 1 — Architecture Proposal**

gpu-lab은 GPU나 CUDA를 흉내 내는 프로젝트가 아닙니다. Kubernetes에서 GPU 리소스가 스케줄링되고, 모니터링되며, 장애 상황에서 어떤 신호를 확인하고 복구하는지를 학습하기 위한 로컬 실습 환경입니다.

## 무엇을 실습하는가

- kind 기반 multi-node Kubernetes cluster
- `nvidia.com/gpu` Extended Resource와 GPU scheduling
- NVIDIA Device Plugin의 등록·할당 모델을 본뜬 Fake Device Plugin
- DCGM Exporter의 관측 모델을 본뜬 Mock GPU Exporter
- Prometheus와 Grafana 기반 GPU monitoring
- YAML 기반 GPU incident scenario
- scheduling failure, exporter down, XID-79, VRAM pressure troubleshooting

실제 GPU 장치, NVIDIA driver, CUDA kernel 실행은 범위에 포함하지 않습니다.

## 목표 환경

- Linux
- macOS
- Windows + WSL2
- Docker Engine 또는 Docker Desktop
- kind 기반 실행

MVP 구현 단계에서는 `docker`, `kind`, `kubectl`, `helm`을 CLI가 진단하고 필요한 리소스를 자동 설치하는 방식으로 구성합니다.

## 설계 문서

- [Architecture Proposal](docs/architecture.md)

문서에는 다음 내용이 포함되어 있습니다.

- component diagram과 runtime topology
- Fake GPU와 Extended Resource 설계
- Mock Exporter 및 metric contract
- YAML Scenario Engine과 reset semantics
- CLI와 repository 구조
- integration acceptance criteria
- 기술적 리스크와 Mock 환경의 한계

## 구현 순서

1. Architecture Proposal
2. kind infrastructure, Fake GPU, Prometheus, Grafana
3. Go Mock Exporter
4. YAML Scenario Engine
5. `gpu-lab` CLI
6. Integration Test

각 단계는 검증 가능한 상태로 끝내며, 이전 단계의 동작을 깨뜨리지 않는 것을 원칙으로 합니다.

## OSS 운영 원칙

- 기본 브랜치는 항상 재현 가능한 상태를 유지합니다.
- 기능은 문서, 테스트, 구현 순서로 쪼개어 공개합니다.
- 실제 NVIDIA 환경에서 동작한다고 오해할 수 있는 표현을 피합니다.
- Mock metric과 DCGM metric의 차이를 문서화합니다.
- 공개 전 라이선스와 contribution policy를 확정합니다.

## License

라이선스는 첫 공개 push 전에 확정합니다. 현재 Phase 1에서는 라이선스 파일을 임의로 추가하지 않았습니다.
