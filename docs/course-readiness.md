# Course Readiness & Roadmap

## 결론

gpu-lab의 주제는 강의 상품으로 차별화할 수 있습니다. 일반 Kubernetes 강의는 GPU 운영을 거의 다루지 않고, 일반 MLOps 강의는 모델 배포와 파이프라인에 집중하는 경우가 많습니다. 이 프로젝트는 GPU가 없는 수강생도 GPU scheduling, monitoring, incident response를 반복 실습하게 한다는 점이 핵심 가치입니다.

현재 구현은 데모 가능한 MVP이지만, 유료 강의를 촬영하기 전에는 설치 경험, 실제 환경과의 대응표, 검증 가능한 troubleshooting 절차를 더 강화해야 합니다.

## 촬영 전 P0

1. **배포 가능한 CLI**: 완료. GoReleaser와 tag 기반 GitHub Actions가 macOS/Linux/Windows용 release binary와 SHA-256 checksum을 만들고, [설치·업그레이드·제거 문서](release.md)를 제공합니다.
2. **재현성 있는 E2E**: 완료. CI에서 create → 모든 기본 scenario → verify → reset → destroy를 검증하고, 로컬 `make e2e` runner와 release별 지원 기준을 제공합니다.
3. **Scenario 성공 조건**: 완료. `gpu-lab verify <scenario>`가 metric, Pod phase, scheduler event를 자동 검증하며 CI acceptance flow에 연결되어 있습니다.
4. **실제 DCGM 대응표**: 완료. [`docs/metric-mapping.md`](metric-mapping.md)에 `gpu_lab_*`와 `DCGM_FI_*`/`DCGM_EXP_*` metric, label, 단위의 대응을 정리했습니다.
5. **강의용 접근 UX**: 완료. `gpu-lab dashboard`, `gpu-lab metrics`, `gpu-lab scenario inspect`를 제공합니다.
6. **정직한 synthetic 경계**: 완료. 리소스 annotation, CLI doctor 경고, [`synthetic-vs-real.md`](synthetic-vs-real.md)를 제공합니다.
7. **OSS 기본 파일**: 완료. LICENSE, CONTRIBUTING, CODE_OF_CONDUCT, SECURITY, SUPPORT, 지원 버전 표, issue/PR template을 추가했습니다.

## P1 기능

- node/GPU 단위 scenario target 선택
- `duration` 만료 후 자동 reset
- 한 scenario 안에서 baseline → warning → critical → recovery로 변하는 단계형 timeline
- device plugin 미등록, 잘못된 taint/toleration, GPU node NotReady, image pull failure 시나리오
- Pod/namespace/container label을 포함한 GPU metric과 workload 상관분석
- MIG와 time-slicing의 resource model 교육 모드
- DCGM-compatible metric alias와 실제 Grafana dashboard import 실습
- scenario별 정답 숨김 모드와 instructor solution 모드
- 수강생 결과를 확인하는 `gpu-lab verify <scenario>`

## 권장 강의 구성

1. GPU 서버와 Kubernetes GPU stack 구조
2. kind multi-node cluster와 Extended Resource
3. device plugin registration과 GPU scheduling
4. Prometheus, Grafana, dcgm-exporter 관측 흐름
5. utilization, VRAM, thermal, power 분석
6. XID와 GPU health incident
7. Pending Pod, selector mismatch, GPU fragmentation
8. GPU idle cost와 capacity planning
9. 실제 GPU Operator, MIG, time-slicing과 Lab의 차이
10. 종합 장애 대응 capstone

## 무료 OSS와 유료 강의의 경계

OSS에는 cluster, scenario engine, dashboard, 기본 runbook을 모두 공개하는 편이 좋습니다. 유료 강의의 가치는 소스 비공개가 아니라 체계적인 설명, 장애 조사 순서, 실제 운영 사례와의 연결, 과제·퀴즈·capstone, 버전 업데이트에 둡니다.

추천 부가 자료는 다음과 같습니다.

- GPU incident checklist 한 장 요약
- PromQL cheat sheet
- scheduler event 판독표
- XID별 first response 표
- synthetic 환경과 실제 GPU cluster 대응표
- 강의 버전과 호환되는 release tag

## 기술 기준 자료

- [Kubernetes GPU scheduling](https://kubernetes.io/docs/tasks/manage-gpus/scheduling-gpus/)
- [Kubernetes Device Plugins](https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/device-plugins/)
- [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/overview.html)
- [NVIDIA DCGM Exporter metrics](https://docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html)
- [NVIDIA XID error guidance](https://docs.nvidia.com/deploy/xid-errors/)
- [NVIDIA Kubernetes Device Plugin](https://github.com/NVIDIA/k8s-device-plugin)
