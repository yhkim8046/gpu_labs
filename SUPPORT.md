# Support

설치와 호환성은 [지원 버전 표](docs/support-matrix.md), 기본 실행은 [Getting Started](docs/getting-started.md), 장애 실습은 [Scenarios](docs/scenarios.md)를 먼저 확인하세요.

질문이나 재현 가능한 버그는 GitHub issue template을 사용해 주세요. 다음 정보를 포함하면 확인이 빠릅니다.

- OS와 CPU architecture
- `gpu-lab version` 및 `gpu-lab doctor` 출력
- Docker/kind/kubectl/Helm 버전
- 실행한 명령과 전체 오류
- `kubectl --context gpu-lab get nodes` 결과

실제 credential이나 private cluster 정보는 issue에 올리지 마세요. 보안 문제는 [SECURITY.md](SECURITY.md)를 사용합니다.
