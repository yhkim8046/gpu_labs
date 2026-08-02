# Support matrix

아래 조합을 release와 강의 촬영의 기준으로 사용합니다. Patch release가 달라지면 `gpu-lab doctor`와 CI E2E를 먼저 실행하세요.

| Component | Supported baseline | Notes |
| --- | --- | --- |
| Host | Linux, macOS, Windows 11 + WSL2 | Docker Desktop 또는 Docker Engine이 필요합니다. |
| Architecture | `amd64`, `arm64` | release binary와 runtime image가 두 아키텍처를 제공합니다. |
| Docker | Docker Engine/Desktop with kind support | Docker daemon이 실행 중이어야 합니다. |
| kind | `v0.32.0` | CI acceptance baseline |
| Kubernetes node image | `kindest/node:v1.36.1` | `kind/cluster.yaml`에서 관리 |
| kubectl | `v1.36.x` | cluster minor version과 맞추는 것을 권장합니다. |
| Helm | `v4.2.3` 이상 | OCI chart install을 지원해야 합니다. |
| Go | `1.26.x` | 소스에서 `--local` 개발 모드를 사용할 때 필요합니다. |
| Monitoring chart | kube-prometheus-stack `87.21.0` | `GPU_LAB_HELM_CHART_VERSION`으로 override 가능 |

지원 범위를 벗어난 환경에서는 먼저 `gpu-lab doctor`, `docker info`, `kind version`, `kubectl version`, `helm version` 결과를 수집하세요.
