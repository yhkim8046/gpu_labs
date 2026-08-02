# Contributing

GPU Lab은 교육용 OSS입니다. 기능 제안은 수강생이 실제 GPU Infrastructure의 개념을 더 잘 이해하는지, 설치 재현성과 troubleshooting 관측성을 높이는지를 기준으로 검토합니다.

## 개발 시작

```bash
go test ./...
go vet ./...
go run ./cmd/gpu-lab doctor
```

클러스터를 사용하는 변경은 Docker, kind, kubectl, Helm이 필요합니다. 로컬 통합 검증은 `E2E_KEEP_CLUSTER=1 ./test/e2e/run.sh`로 실행할 수 있습니다. 테스트 후 남은 클러스터가 필요하지 않으면 `go run ./cmd/gpu-lab destroy`를 실행하세요.

## Pull request 기준

- 변경 이유와 강의에서의 학습 목표를 설명합니다.
- unit test와 관련 문서를 함께 갱신합니다.
- 실제 NVIDIA GPU가 필요하다는 표현과 synthetic 동작을 혼동하지 않습니다.
- Kubernetes manifest, scenario, PromQL 변경은 `go test ./...`와 가능한 범위의 E2E 결과를 포함합니다.
- 사용자 경험에 영향을 주는 명령/출력 변경은 README 또는 `docs/`에 예시를 추가합니다.

작은 수정은 한 PR에 하나의 학습 목표로 묶어 주세요. 보안 문제는 공개 issue가 아니라 [SECURITY.md](SECURITY.md)의 절차를 사용합니다.
