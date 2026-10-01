# GPU_LABS 유료 강의 전환 감사 보고서

- 작성: 2026-09-26 (Asia/Seoul), gpu_labs_audit 작업자
- 기준 시점 상태: 브랜치 `agent/distributed-training-rdma-labs`, HEAD `7ef6d1c`(= origin/main, "feat: add distributed training and RDMA labs"), 워킹트리 다수 modified/untracked
- 검증 성격: 파일·git·unit test 기반 정적/단위 검증. 실 kind/Docker/Grafana 라이브 검증은 이 세션에서 수행하지 못했거나 미완이다(아래 미검증 목록 참조).

---

## 0. 결론 요약

기술 골격(Helm 단계 설치 → 시나리오 주입 → Prometheus/Grafana/nvidia-smi/ibstat 다중 관측 → `gpu verify` 자동 검증 → reset의 반복 루프)은 "GPU 없는 노트북에서 GPU 운영 사고조사 절차 훈련"이라는 차별화된 판매 가능 자산이며, 단위 테스트·CI·멀티아치 릴리스·체크섬 설치 스크립트 등 엔지니어링 성숙도는 강의 플랫폼으로서 충분하다. 그러나 2026-09-26 기준 저장소는 여전히 "잘 만들어진 OSS 러닝 플랫폼 + 방금 착수된 코스 패키지" 단계이지 완결된 상품이 아니다. 결제·수강 인프라 0, 영상/슬라이드 0(코스로드 문서에도 미확보로 명시), 라이브 수강생 관점 설치 검증 기록 0, 문서-시나리오-사이트 목록 드리프트가 확인됐고 일부는 이번 감사에서 수정했으나 최종 반영 여부는 일부 항목 미확정이다.

---

## 1. 저장소 구조와 코드 상태

- Go 본체(비테스트) 약 7,500줄, 테스트 약 2,900줄 + 이번 주기를 통한 신규 테스트 다수. `go build ./...` 통과.
- CLI: `cmd/gpu-lab`은 doctor/create/context/helm/scenario/verify/metrics/dashboard/status/nvidia-smi/ibstat·ibstatus·ibv_devinfo·training 서브커맨드가 실 구현. 대부분은 `7ef6d1c` 시점 워킹트리의 untracked 신규 파일(`cmd/gpu-lab/{create,context,dashboard,doctor,helm,help,ib,metrics,scenario,training,verify}.go` 등)로 존재.
- 내부 패키지: `internal/{cluster,scenario,exporter,verification,nvidiasmi,rdmacompat,deviceplugin,monitoring,training,trainingjob,stateclient,testutil,...}`. `internal/scenario`는 이번 주기에 model/manifest/timeline/validate/metrics_validation으로 분리 리팩터링(untracked).
- 배포물: `deploy/charts/{nvidia-device-plugin,dcgm-exporter}`, kube-prometheus-stack 고정 버전 monitoring values, Grafana 대시보드 3종(GPU 27패널/training 10/fabric 10), alert rule 34종(16+6+12).
- CI: `ci.yml`(go test/vet/build + container build + pinned kind/Helm e2e), `release.yml`(GoReleaser GHCR 멀티플랫폼), `publish-charts.yml`, `publish-image.yml`. E2E: `test/e2e/{run,training,ib}.sh`.
- Git 태그: 로컬에 `v0.1.0`~`v0.2.4`. 주의: `v0.2.4`는 2026-08-03 커밋 `3cce645`(nvidia-smi 노드 그룹핑)를 가리키며, `git ls-tree v0.2.4:scenarios` = 16 yaml, `cmd/`에 `training-worker`/ibstat 계열 없음. 즉 최신 태그라도 분산학습·fabric 미포함.
- 워킹트리 위생: 19개 파일 modified(+628/−2041), 다수 untracked 신규 파일. `dist/`에 스냅샷 아카이브 커밋 잔존. 커밋/푸시 시 리팩터링·테스트·문서를 논리적 단위로 나눠야 한다.
- `docs/` 12종(architecture 590줄, student-guide 325줄, scenarios 248줄, ib-fabric 232줄, getting-started 237줄 등 총 2,292줄 + 이번 주기 신규). `docs/course/`(README, capstone 3, templates 4)와 `docs/troubleshooting.md`는 untracked 신규.
- `site/`: vinext/Next.js 소개 랜딩페이지. 판매 기능(결제, 수강생 계정, 콘텐츠 잠금) 없음. D1/R2 바인딩 없음. "21 scenarios/21개 전체 보기" 표기는 실 파일 22개와 불일치(직접 미수정 — site 담당 이관 항목).

## 2. 시나리오 / 랩 완성도

- `scenarios/*.yaml` 22개 = normal baseline 1 + 장애 21. 유형: 단순 metric 주입(xid-79 등), exporter fault, workload 생성·Pending(scheduling-failure 9-GPU, fragmentation 7×3+2, capacity-mismatch 8/4), node label/GPU index 타깃, duration 자동 만료, timeline 4단계(thermal-escalation — 유일 timeline, 문서에서 누락됐다가 이번 주기에 목록 반영 시도), IB/RDMA 5종(모두 노드당 1 HCA `mlx5_0`/port 1 fixture, GPU_COUNT와 독립 상수).
- `gpu verify`는 시나리오별 metric/Pod phase/scheduler event 계약을 하드코딩 검증. `exporter-down`은 정의상 target-down만 확인. 미계약 시나리오는 "no verifier contract"으로 실패 처리.
- 분산학습 drills(straggler, worker-crash + checkpoint 복구)와 fabric drills는 `gpu training`/`gpu scenario run` 조합으로 실재, e2e 스크립트가 메트릭 게이트로 검증.
- stub/TODO: 소스에 TODO/FIXME 스텁 없음. 미완의 실체는 기능 스텁이 아니라 (a) release 미포함 기능(v0.2.x binary 대비 training/IB/일부 시나리오), (b) `model-serving-expansion.md`의 명백한 "아직 구현되지 않음" 제안서, (c) course-readiness P1 잔여(MIG/time-slicing 모드, DCGM 호환 alias, 실 대시보드 import, solution 리포지토리)다.
- 문서 드리프트(확인 및 조치): `thermal-escalation` 전체 문서 누락 → scenarios.md/student-guide.md에 추가. student-guide 시나리오 표 IB/RDMA 5종 누락 → 추가. getting-started 실행 예시 6종 누락 → 추가. Windows `$Version = 0.2.0` 하드코딩 → 0.2.3으로 수정하고, "0.2.3 태그는 로컬에서 확인한 것이며 22개/training은 미포함, 현재 수정본 checkout에서 `go build` 필요" 문구로 정정. README에 "기본 22개(normal 1 + 장애 21)" 정량 명시. course-readiness의 과장된 "완료" 서술을 근거 있는 상태(문서/단위테스트 검증만 완료, 실 강의 검증 없음, 영상·슬라이드 미확보)로 대체.

## 3. 합성(synthetic) 환경의 교육 범위와 한계

가르칠 수 있는 것(잘 성립): `nvidia.com/gpu` Extended Resource 스케줄링과 Pending 원인 분석, device-plugin 등록 흐름, DCGM-style 관측 계층(target-down vs GPU 장애 구분, PromQL), XID/ECC/thermal/power/PCIe 신호 판독, fabric 신호↔AllReduce progress 상관분석, checkpoint 재개 vs 단순 Pod 재시작 구분, metric-mapping(synthetic↔DCGM)과 synthetic-vs-real 경계표의 정직한 번역 연습.

구조적 한계: 연산·성능이 없어 수치 실재감이 없고(결정적 상수), "실제라면 어떻게 됐을지"를 강사 구두와 실환경 증거 템플릿으로 메워야 한다. 실제 nvidia-smi/kernel log/GPU Operator/MIG/time-slicing 부재. reset은 GPU reset/drain/reboot가 아니다. ibstat류는 rdma-core v56.0 형식 호환 shim일 뿐 `/dev/infiniband`·verbs가 아니다. 온보딩 난이도: Docker Desktop+kind+kubectl+Helm 4종 선행, 리소스 소모 큼(CI e2e 30분 타임아웃). `gpu doctor`는 의존성 존재/version만 보고(프로덕션 트러블슈팅 가이드는 `docs/troubleshooting.md`로 방금 신설됐고 내용 최종 확정 여부 미확인).

## 4. 학습자 DX/UX

- 강사용 경로가 설계돼 있고 실재: release binary + 설치 스크립트(checksum), `gpu doctor`, 단계적 `gpu helm install`, `gpu dashboard`/`gpu metrics`/`scenario inspect --solution`(정답 숨김, 단 YAML 평문이라 보안 경계 아님 명시), course/의 3-capstone·5-루브릭 평가(합격 기준 명기, `gpu verify` 단독 합격 불가 명시), incident report 양식/체크시트/PromQL 치트시트/실환경 증거 템플릿.
- 결함: 워킹트리 기준 최신 미검증. Windows 문서와 release 태그 간 버전 정합성은 문서로만 보정됐고 새 binary 배포는 미완. "실제 GPU 운영자 관점" 재검토(시간 제한 미션 등)는 이 관점 작업자의 마지막 수정이 제 최종 확인 이후 반영됐는지 미확정.

## 5. 유료화 관점의 치명적 결핍

| 항목 | 상태 |
| --- | --- |
| 영상/슬라이드/원고 | 0. course-readiness에 미확보로 명시. P0 전체 완료 주장 금지. |
| 결제/수강생 계정/콘텐츠 잠금 | 0. site는 소개용. |
| 평가 체계 | 루브릭/합격 기준 문서는 존재(untracked). 자동 채점 파이프라인과 solution 리포지토리 없음. |
| 실 프로덕션 사례 | 실측 로그/사례 수집 0(증거 템플릿은 빈 양식). |
| 라이브 설치/수강 경험 검증 | 수강생 OS별 클린 설치 반복 검증 기록 없음. CI e2e는 개발/CI 결과이지 수강생 검증 증거 아님. |
| 배포 채널 정합성 | 개인 리포 기반. training/IB/22시나리오 포함 release 미발행(최신 태그 v0.2.4도 16개뿐). |
| 사이트 수치 | 21 vs 22 불일치(site 소유권 분리). |
| 다국어 | 한국어 단일. |

결정적 셀링 포인트 대비 보완 우선순위: (1) 워킹트리 커밋/릴리스/문서 동기화 확정, (2) 22개 시나리오+training 포함 release 발행 후 전 문서 버전 표기 고정, (3) verify 기반 과제 채점 구조와 solution 리포 시드, (4) 실환경 스니펫 대비 캡스톤 보강과 사례 수집, (5) 영상/슬라이드 제작, (6) 판매/접근 제어 채널.

## 6. ibstat/rdma 호환성 작업(이번 주기 산출)

- upstream rdma-core `v56.0` `infiniband-diags/ibstat.c`(직접 fetch·대조) 기준 결함 2건 수정: `-p` 선행(-l보다 우선, port 인자 무시), positional sole 모드(`CA: '<ca>'` 헤더 + 무들여쓰기 port 블록, `-s` 병용 시 `ca_dump()` 헤더로 폴백). `internal/rdmacompat/rdmacompat.go` `runIBStat` 재작성 + `printIBStatSolePort` 신설.
- golden 테스트 `TestIBStatSolePortAndListPrecedence`(fixture `gpu-lab-worker2`, Node GUID `0xa088c2030088b308`, Port GUID `...09`, Base lid 3, SM lid 1 고정 전량 문자열 비교; `-l -p`, `-p mlx5_0 99`, `-s mlx5_0 99` 케이스 포함) PASS. `gofmt`/`go vet`/패키지 빌드 클린.
- ibv_devinfo `effective_speed`는 구현돼 있음(`rdmacompat.go` L670) — 과거 문서의 "미구현" 표기는 제 수정으로 정정됨. `--node`는 GPU Lab 확장임을 문서 명시.

## 7. 검증 통과 항목

- `go test` 통과 확인 패키지: `internal/rdmacompat`, `internal/nvidiasmi`(진공 테스트 `TestCourseTopologyTotalAcrossNodesIsTwentyFour` 삭제 후 ok), `cmd/gpu-lab`(수 회 ok; 단 마지막 doctor/dashboard 변경 이후 재실행은 생략), `go vet ./cmd/gpu-lab`, `gofmt -l` 무출력, `go build ./...`(일부 중간 시점 doctor.go 일시 구문 오류 관측 → 제 소유 파일 아님에 따라 원복 대조만 수행, 이후 관측 상태는 빌드 가능).
- 토폴로지 사실 고정: kind worker 3(`gpu-node-01..03`)×8 = 24 GPU, control-plane GPU 없음(kind node 총 4). values/cmd/deviceplugin/exporter 4경로 전부 8로 일치. `topology_regression_test.go`가 노드 블록 3×8행을 실제 `Run()` 출력으로 고정. `deploy/dashboard_topology_test.go`가 대시보드 "3x8=24/단일 노드 8" 설명 텍스트 고정.

## 8. 미검증 목록 (통과로 주장 금지)

1. 실 kind 클러스터 e2e(`make e2e`, run/training/ib 스크립트) — 세션 내 Docker daemon 미실행으로 미수행.
2. Grafana UI 실 렌더링, 템플릿 $node/$gpu 변수 동작, 대시보드 aggregate의 24↔8 필터 semantics(단위 텍스트 테스트만 존재).
3. `gpu verify`의 라이브 실 클러스터 통과(단위 테스트만 통과).
4. 수강생 OS별(Docker Desktop/macOS/Linux/WSL2) 클린 설치.
5. 원격 GitHub release/태그 실제 상태(로컬 태깅과 상이할 수 있음).
6. 타 담당 산출물의 최종본 반영 여부: doctor(호스트 진단 확장·topology 게이트), dashboard 수정, course 패키지 내용 최종본, troubleshooting.md — 작성 시점 기준으로 관측된 상태이며, 제 마지막 확인 이후 해당 담당자의 추가 수정이 반영됐는지 **미확정**.
7. doctor.go 관련: 세션 중 일시적 구문 오류/undefined 심볼 관측 이력 있음(타 담당 WIP). 최종 green 여부는 담당자 통보("현재는 빌드/테스트 모두 green")에 의존.
8. 한글 문서 내 최종 오탈자 스윕 상태 — 코드포인트 기반 스캔은 통과로 관측됐으나 사람이 읽는 최종 본검수는 미완.

## 9. 경계와 소유권

- 이 작업자는 제 할당 파일(README.md, docs/getting-started.md, docs/student-guide.md, docs/course-readiness.md, docs/ib-fabric.md, docs/scenarios.md 목록 동기화, internal/rdmacompat/*, internal/nvidiasmi/topology_regression_test.go)만 수정했다. doctor.go는 관측된 원본 상태로 보존했고 그 외 타 담당 파일(course/, troubleshooting.md, dashboard, cmd/gpu-lab 신규군, site/)은 무수정이다.
- 커밋·푸시·브랜치 생성·클러스터 리소스 변경은 수행하지 않았다.

## 10. 다음 담당자 인수 항목

1. 워킹트리를 논리적 단위(시나리오 엔진 리팩터링, ibstat 호환성, doctor/topology, 코스 패키지, 문서 동기화)로 나눠 커밋하고 release 발행 → 문서 버전 표기를 새 태그로 재고정.
2. `site/`의 "21" 수치와 코스 홍보문구를 실 상태(22, release 미포함 사실)와 동기화.
3. `gpu verify`+루브릭 채점 파이프라인(제출물 → 체크 → 스코어)과 solution 리포 시드 작성.
4. 영상/슬라이드는 제작 전까지 어떤 문서에도 "완료"로 표기하지 말 것.
5. 본 보고서 8장 미검증 항목은 라이브 환경에서 재검증 후 별도로 승격 기록할 것.
