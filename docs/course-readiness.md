# Course Readiness & Roadmap

## 결론

gpu-lab의 주제는 강의 상품으로 차별화할 수 있습니다. 일반 Kubernetes 강의는 GPU 운영을 거의 다루지 않고, 일반 MLOps 강의는 모델 배포와 파이프라인에 집중하는 경우가 많습니다. 이 프로젝트는 GPU가 없는 수강생도 GPU scheduling, monitoring, incident response를 반복 실습하게 한다는 점이 핵심 가치입니다.

현재 구현은 데모 가능한 MVP이지만, 유료 강의를 촬영하기 전에는 설치 경험, 실제 환경과의 대응표, 검증 가능한 troubleshooting 절차를 더 강화해야 합니다.

## 촬영 전 P0

1. **배포 가능한 CLI**: GoReleaser와 tag 기반 GitHub Actions가 macOS/Linux/Windows용 release binary와 SHA-256 checksum을 만들도록 구성되어 있고, [설치·업그레이드·제거 문서](release.md)를 제공합니다. 강의 기준 `v0.3.0`의 실제 공개 여부는 [Release 페이지](https://github.com/yhkim8046/gpu_labs/releases/tag/v0.3.0), archive·설치 검증은 해당 tag의 Release workflow 결과로 확인합니다.
2. **재현성 있는 E2E(파이프라인 존재 기준)**: CI 파이프라인이 create → Device Plugin/DCGM Exporter/Monitoring Helm 설치 → 모든 기본 scenario → verify → reset → destroy를 검증하고 로컬 `make e2e` runner가 존재한다. 결과는 해당 commit의 PR/CI run에서 확인한다. 수강생 OS별 클린 설치 경험은 CI 통과와 별도로 검증한다.
3. **Scenario 성공 조건(코드 구현 기준)**: `gpu-lab verify <scenario>`가 metric, Pod phase, scheduler event를 자동 검증하도록 구현돼 있고 단위 테스트가 통과한다. 실클러스터 라이브 환경에서의 verify 전수 통과 여부는 별도 검증 항목이다.
4. **실제 DCGM 대응표**: 완료. [`docs/metric-mapping.md`](metric-mapping.md)에 `gpu_lab_*`와 `DCGM_FI_*`/`DCGM_EXP_*` metric, label, 단위의 대응을 정리했습니다.
5. **강의용 접근 UX**: 완료. `gpu helm install <component>` 단계형 설치와 `gpu dashboard`, `gpu metrics`, `gpu scenario inspect`를 제공합니다.
6. **정직한 synthetic 경계**: 완료. 리소스 annotation, CLI doctor 경고, [`synthetic-vs-real.md`](synthetic-vs-real.md)를 제공합니다.
7. **OSS 기본 파일**: 완료. LICENSE, CONTRIBUTING, CODE_OF_CONDUCT, SECURITY, SUPPORT, 지원 버전 표, issue/PR template을 추가했습니다.

## P1 기능

- 완료: node label과 GPU index 단위 scenario target 선택
- 완료: `duration` 만료 후 exporter telemetry/fault 자동 reset
- 구현·로컬 회귀 검증 완료: 한 scenario 안에서 baseline → warning → critical → recovery로 변하는 단계형 timeline. 공통 시작 시각, exporter 재시작 시 단계 복원, GPU/node 선택, 만료 및 현재 단계 검증을 지원한다.
- device plugin 미등록, 잘못된 taint/toleration, GPU node NotReady, image pull failure 시나리오
- Pod/namespace/container label을 포함한 GPU metric과 workload 상관분석
- MIG와 time-slicing의 resource model 교육 모드
- DCGM-compatible metric alias와 실제 Grafana dashboard import 실습
- scenario별 정답 숨김 모드와 instructor solution 모드
- 완료: 수강생 결과를 확인하는 `gpu-lab verify <scenario>`
- 완료: 복구 상태를 별도 판정하는 `gpu verify --recovery [fault]`(게이지·진행도 판정, 누적 counter 0 요구 없음, missing/stale fail-closed). 2026-10-01 실cluster에서 rdma-retry-storm 기준으로 fault verify → reset → recovery verify 전 과정 통과(최신성 게이트 3종 포함 21/21 PASS) 기록은 [capstone-4](course/capstone-4-rdma-allreduce-recovery.md)에 있다

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

부가 자료와 강의 준비 현황:

- 문서·단위 검증을 마친 것: [코스 패키지](course/README.md)의 4개 capstone, incident report 양식, GPU/IB first-response 체크시트·런북, lab 지표 기반 PromQL cheat sheet, 실제 환경 증거 수집 템플릿, [`synthetic-vs-real.md`](synthetic-vs-real.md)·[`metric-mapping.md`](metric-mapping.md)의 실제 환경 대응표, 수강생 평가 루브릭과 합격 조건. 실 강의 진행 검증은 별도다.
- 추가 검증 과제: 수강생 OS별(Docker Desktop + macOS/Linux/WSL2) 클린 설치 반복 검증, 실제 GPU 환경에서 수집한 로그·장애 사례, 그리고 영상과 발표 슬라이드. 현행 kind E2E와 snapshot archive 결과는 PR/CI run으로 기록하고, 공개 archive와 설치 기반 검증은 release workflow에서 따로 확인한다. CI가 통과해도 수강생 설치 경험이나 실제 GPU 동작의 증거로 쓰지 않는다.
- 버전 정합성: 강의 실습 binary는 기본 22 scenario를 포함한 release tag와 대응해야 한다. 로컬에서 확인한 `v0.2.3`과 `v0.2.4` tag 스냅샷에는 GPU/XID 시나리오 16개만 담겨 있었고 `training-worker`, `ibstat` 계열 바이너리도 없었다. 강의 기준은 22 scenario와 IB/training/recovery 변경을 포함하는 `v0.3.0`이다. 공개·검증 상태는 Release 페이지와 해당 tag의 Release workflow를 기준으로 확인한다. archive checksum smoke와 설치 기반 Linux E2E 결과가 확인되기 전에는 강의 기준 release로 취급하지 않는다.

## 기술 기준 자료

- [Kubernetes GPU scheduling](https://kubernetes.io/docs/tasks/manage-gpus/scheduling-gpus/)
- [Kubernetes Device Plugins](https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/device-plugins/)
- [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/overview.html)
- [NVIDIA DCGM Exporter metrics](https://docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html)
- [NVIDIA XID error guidance](https://docs.nvidia.com/deploy/xid-errors/)
- [NVIDIA Kubernetes Device Plugin](https://github.com/NVIDIA/k8s-device-plugin)
