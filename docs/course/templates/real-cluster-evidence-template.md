# 실제 클러스터 증거 수집·익명화 템플릿 (빈 양식)

⚠ 이 문서는 빈 양식이다. 실제 환경을 조사하기 전에는 결론란을 채우지 않는다.
⚠ lab(gpu-lab)의 합성 counter와 실제 DCGM/NIC/switch의 카운터를 같은 물건처럼 적지 않는다.
  이름 대응은 [metric-mapping.md](../../metric-mapping.md)만 참고하고, lab과 실측을 서로 다른 열에 기재한다.

## 0. 수집 메타

| 항목 | 값 |
| --- | --- |
| 수집 일시(KST) |  |
| 수집자/직군 |  |
| 클러스터 식별자(익명화 ID) |  |
| GPU 모델/driver/DCGM·exporter 버전 |  |
| NCCL/NVML 등 관련 라이브러리 버전 |  |
| 네트워크(IB Ethernet) 및 switch vendor/firmware |  |
| 데이터 분류(공용/내부/민감) |  |
| 공유 범위(강의 교재/사내/외부) |  |

## 1. 원본 수집 목록 (무엇을 어떤 명령으로 확보했나)

각 항목은 원본 파일/패스팅 위치와 수집 시각을 남긴다. 요약본만 남기면 요약본이 원본을 대체하지 못한다.

| # | 수집 항목 | 명령/API | 시각 | 산출물 위치 | 데이터 분류 |
| --- | --- | --- | --- | --- | --- |
| 1 | 노드·GPU inventory | `nvidia-smi -L` / `nvidia-smi -q` |  |  |  |
| 2 | Xid 발생 흔적 | kernel log(`dmesg`/journal, `NVRM` + Xid line) |  |  |  |
| 3 | DCGM health | DCGM health check / DCGM exporter series |  |  |  |
| 4 | Bug report | `nvidia-bug-report` |  |  |  |
| 5 | Kubernetes 스케줄링 | Pod events, node capacity/allocatable |  |  |  |
| 6 | InfiniBand 링크 | `ibstat` / `ibstatus` (State, Physical State, Link layer, Rate) |  |  |  |
| 7 | RDMA/통신 | rank별 all-reduce 결과, retry/timeout 카운터 |  |  |  |
| 8 | Thermal/power | 온도·전력·throttle reason series |  |  |  |
| 9 | Checkpoint | save/load 로그, step·restart 시각 |  |  |  |

공식 절차 출처(교차 검증용, lab 구현과 신뢰 등급이 다름):

- NCCL networking troubleshooting: <https://docs.nvidia.com/deeplearning/nccl/user-guide/docs/troubleshooting/networking_troubleshooting.html>
  → ibstat/ibstatus에서 State=Active, Physical State=LinkUp, Link layer, expected Rate를 확인하는 단계다. 이 확인만으로 NCCL 성공을 증명하지 않는다.
- Xid 소개: <https://docs.nvidia.com/deploy/xid-errors/introduction.html>
  → Xid 값만으로 root cause를 단정하지 않는다.
- Xid 작업 흐름: <https://docs.nvidia.com/deploy/xid-errors/working-with-xid-errors.html>
  → kernel의 NVRM Xid 로그, `nvidia-smi -q`, DCGM, bug report를 교차 증거로 모은다.
- DCGM exporter metric field 목록: <https://docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html>
  → 실제 노출 field는 GPU/driver/collector 설정에 따라 달라지므로 위 목록을 기준으로 확인한다.

## 2. 익명화 체크리스트

- [ ] 호스트명·IP·MAC·PCIe BDF·GPU UUID·node GUID/LID를 프로젝트별 대칭 치환자로 치환했다.
- [ ] Pod/namespace/user/service account name, 사내 도메인, URL, ticket/프로젝트 식별자를 제거하거나 마스킹했다.
- [ ] 로그 원본에 남은 개인/영리적 민감정보(이름, 이메일, 토큰, 키, 클라이언트 식별자)를 전수 스캔했다.
- [ ] 시리얼/자산번호·switch 호스트명·포트 번호를 치환했다.
- [ ] 시각은 필요 정도를 유지하되 필요 없으면 상대 시각(예: T+0s)으로 변환했다.
- [ ] 원본과 공유본을 분리 저장했으며, 원본을 제출물에 포함하지 않았다.
- [ ] 원본을 강의에 포함해야 한다면 데이터 분류와 저작권을 확인했고 별도 승인을 받았다.

치환자 기록(재현과 대조용이며 실제 값 자체를 남기지 않는다):

| 실측 식별자 | 치환자 | 대조 가능자 |
| --- | --- | --- |
|  |  |  |

## 3. lab ↔ 실측 분리 표

| 관찰 신호 | lab 표기(합성) | 실측 값/출처 | 단위를 맞춘 근거 | 이 표에서 허용하는 결론 |
| --- | --- | --- | --- | --- |
| 예) IB symbol error | `gpu_lab_ib_symbol_errors_total` |  |  |  |
| 예) 온도 | `gpu_lab_gpu_temperature_celsius` 82/96 |  |  |  |
| 예) throttle | `gpu_lab_gpu_throttle_active` reason=thermal |  |  |  |
|  |  |  |  |  |

## 4. 관측 → 가설 → 조치 → 복구 확인

| 단계 | 기재 내용 | 원본 번호(1번 표) |
| --- | --- | --- |
| 관측 |  |  |
| 가설 A |  |  |
| 가설 B(반증용) |  |  |
| 조치(lab) |  |  |
| 조치(실환경, 승인 기록 번호) |  |  |
| 환경 복구 확인 |  |  |
| 작업 복구 확인(checkpoint/step) |  |  |

## 5. 위험 조작 기록

drain/cordon, GPU reset, switch 포트 shutdown, firmware reload, 케이블 인출, Pod 강제 삭제, preemption 강제는 실제 환경에서 변경 승인이 필요한 항목이고, 실습의 정답으로 취급하지 않는다.

| 검토한 조작 | 실행 여부 | 변경 승인 번호 | 롤백 계획 | 실행하지 않았다면 근거 |
| --- | --- | --- | --- | --- |
|  |  |  |  |  |

## 6. 미결정 사항과 다음 확인

| 미결정 사항 | 지금 할 수 없는 이유 | 다음에 확인할 증거 | 필요 권한/승인 |
| --- | --- | --- | --- |
|  |  |  |  |

## 7. 서명

- 수집·요약 설명이 위 원본을 근거로 작성했음: 서명 ______
- 실측을 직접 보유·검증하지 않은 항목: ______________________
