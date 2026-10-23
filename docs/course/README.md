# GPU Lab 유료 강의 코스 패키지

이 폴더는 gpu-lab 합성(synthetic) 환경을 쓰는 운영자 대상 실습 커리큘럼입니다.
실제 GPU, driver, CUDA, RDMA 장비를 제공하지 않습니다. 모든 수치는 합성 fixture이며,
본 패키지의 모든 값은 합성이며, 실측 로그를 직접 확보·검증한 경우만 그 사실을 명시한다.

## 대상과 선수지식

- Kubernetes 기본: Pod/Deployment/Scheduler/Node 개념, kubectl 기본 사용
- Linux CLI: 파이프, 리다이렉트, Docker Desktop 또는 Docker Engine 실행 경험
- 선수 문서(권장): [getting-started.md](../getting-started.md), [student-guide.md](../student-guide.md), [metric-mapping.md](../metric-mapping.md)
- GPU/IB 경력이 없어도 됩니다. 다만 실환경 적용 전에는 반드시 [synthetic-vs-real.md](../synthetic-vs-real.md)의 경계를 읽고, 실제 환경에서는 해당 환경의 공식 문서를 별도 확인해야 합니다.

합격 기준(전 캡스톤 공통): 각 항목 0~3점, 5항목 합계 15점 만점. 합격: 합계 15점 중 10점 이상, 모든 항목 1점 이상, 그중 "증거 수집"과 "가설 검증"은 각 2점 이상. `gpu verify` 통과는 환경 상태 판정일 뿐 합격 조건으로 단독 사용하지 않는다. 캡스톤에 추가 규칙이 있으면 공통 규칙 위에 겹쳐 적용한다.

## 학습 경로

| 순서 | 모듈 | 문서 | 소요(권장) |
| --- | --- | --- | --- |
| 0 | 온보딩·진단 | 본 문서 아래 "온보딩 doctor 실습" | 30분 |
| 1 | 캡스톤 1: 3×8 GPU 스케줄링·단편화 | [capstone-1-scheduling-fragmentation.md](capstone-1-scheduling-fragmentation.md) | 90분 |
| 2 | 캡스톤 2: IB 상태·속도·오류 진단 | [capstone-2-ib-diagnosis.md](capstone-2-ib-diagnosis.md) | 90분 |
| 3 | 캡스톤 3: thermal escalation과 checkpoint/복구 구분 | [capstone-3-thermal-checkpoint-recovery.md](capstone-3-thermal-checkpoint-recovery.md) | 120분 |
| 4 | **대표 실습: IB/RDMA 이상 → AllReduce 지연 → 정상화 판정** | [capstone-4-rdma-allreduce-recovery.md](capstone-4-rdma-allreduce-recovery.md) | 90분 |

각 캡스톤에 필요한 자료 번들:

- [templates/incident-report-template.md](templates/incident-report-template.md) — 진단 보고서 제출 양식
- [templates/gpu-ib-first-response-checklist.md](templates/gpu-ib-first-response-checklist.md) — GPU/IB 장애 초동 대응 체크시트·런북
- [templates/promql-cheatsheet.md](templates/promql-cheatsheet.md) — lab 실제  지표명 기반 PromQL 치트시트
- [templates/real-cluster-evidence-template.md](templates/real-cluster-evidence-template.md) — 실제 클러스터 증거 수집·익명화 템플릿 (빈 양식)

## 온보딩 doctor 실습

목표: 환경 준비를 "명령 성공"이 아니라 네 항목 별개의 증거로 판정한다.

```bash
gpu version
gpu doctor
gpu status
gpu context list
```

`gpu doctor`가 확인하는 것은 tool의 존재와 버전이다. doctor가 보는 항목과 출력 형식은 doctor 구현이 완성되면 그 결과를 그대로 반영해 갱신한다(현재 문서의 항목은 확인 결과 기준). doctor가 통과해도 cluster가 만들어진 것은 아니고 GPU가 인식된 것도 아니다. cluster 생성은 `gpu status`, context 접근은 `gpu context list`와 `kubectl --context gpu-lab get nodes`가 별개의 확인이다.
설치·자원 문제는 [troubleshooting.md](../troubleshooting.md)를 사용한다.

판정 기준(모두 별개로 기록):

1. doctor: `gpu doctor` 실행 결과의 tool 상태 행 전부가 준비 상태로 나오는지
2. cluster: `gpu create` 후 `gpu helm install nvidia-device-plugin`, `gpu helm install dcgm-exporter`, `gpu helm install monitoring`이 모두 성공했나?
3. 토폴로지 조회: worker 3노드 × 노드당 GPU 8개 = 24, control-plane 1노드(CPU). 아래 명령으로 직접 조회한다.

```bash
kubectl --context gpu-lab get nodes -o wide
kubectl --context gpu-lab get nodes \
  -o custom-columns=NAME:.metadata.name,GPU:.status.capacity.nvidia\\.com/gpu
kubectl --context gpu-lab get pods -A
```

4. 관측: `gpu metrics` 출력과 `gpu dashboard --port 3000` (Ctrl-C로 port-forward 중지).

주의: `nvidia.com/gpu`는 교육용 Extended Resource이며 실제 GPU가 아닙니다.
"3×8"는 lab의 kind manifest(`deploy/kind/cluster.yaml`)와 device-plugin 설정이
기준이며, 수치는 교육 권장값입니다. 실제 클러스터 용량과 아무 관계가 없습니다.
결론은 항상 별도 노드의 worker 3개 × 각 8 GPU이고, control-plane은 CPU only입니다.

## 자주 묻는 질문(FAQ)

- Q. 실제 GPU 없이 무엇을 연습하나?
  A. scheduling, exporter/PromQL 관측, 시나리오 기반 장애 대응, 진단 순서와 문서화까지의 운영 흐름입니다. CUDA 실행과 하드웨어 신뢰성은 다루지 않습니다.
- Q. `gpu ibstat`은 실제 IB 명령인가?
  A. rdma-core `ibstat`의 이름·출력 형식을 호환한 합성 구현입니다. 상태는 lab exporter/Prometheus에서 옵니다. 실장비의 `ibstat`/`perfquery`와 혼동하지 마세요. 옵션은 `gpu ibstat --help`가 나옵니다(`-l`, `-p`, `-s`, `-v`, lab 확장 `--node <node>`).
- Q. `gpu nvidia-smi`로 실제 GPU가 보이나?
  A. 아니오. synthetic telemetry 명령입니다. 실제 환경 조사는 [troubleshooting.md](../troubleshooting.md#9-실제-gpu-증거-초동-수집)의 교차 증거 흐름을 따릅니다.
- Q. 합성 수치를 실환경 SLA/root cause에 바로 쓸 수 있나?
  A. 없습니다. 모든 임계값은 lab fixture입니다. 실환경 적용은 메트릭 정의와 임계값을 그 환경 기준으로 재설정한 뒤 별개로 검증합니다.
- Q. 과제를 AI에게 맡기면?
  A. 채점의 목표는 증거 기록과 진단 추론이다. 제출물에 도구 출력(원본)과 자기 해석을 구분하는 기록이 없으면 루브릭 상한이 제한됩니다.

## 제출과 채점 원칙

- 기계 판정(환경 verify): `gpu verify <scenario>`와 lab 메트릭·명령 결과만 자동 확인한다. 이는 "환경이 목표 상태에 도달했나"만 판정하며 "진단 결론이 맞았나"를 판정하지 않는다.
- 장애 상태와 복구 상태는 서로 다른 명령으로 판정한다. `gpu verify <scenario>`는 reset 이전의 **장애 상태 도달**만, `gpu verify --recovery [fault]`는 reset 이후의 **정상화 도달**만 판정한다. reset 전에 실행한 verify를 복구 증거로 쓰면 안 된다. 정상화 판정에서 누적 `*_total` 카운터는 0을 요구하지 않는다(누적치는 보존되는 것이 정상이다).
- 설명 평가(reasoning rubric): 아래 루브릭을 강사/TA가 원본을 읽고 평점한다.
- `gpu scenario inspect --solution`은 답을 숨기는 통제가 아니라 답을 고를 수 있는 학습 기능이다.

| 항목 | 0 | 1 | 2 | 3 |
| --- | --- | --- | --- | --- |
| 증거 수집 | 생략 | 부분 기록 | 네 단계 표기 | 관측→가설→조치→복구 확인이 타임스탬프·원본과 연결 |
| 가설 검증 | 단정형 결론 | 1회 시행 | 대체 가설 비교 | 반증 시나리오 설계 |
| 경계 인식 | lab=실제로 취급 | 조건부 참고 | 합성/실제 측정치 분리 명시 | 실환경 재검증 절차 제시 |
| 안전 | 위험 조작 무조건 실행 | 조건부 실행 | 위험 조작 사전 합의·회피 | 승인·기록과 함께 조건화 |
| 재현성 | 수동만 | 수동 재현 가능 | 스크립트화 | 고정 fixture·타임스탬프 포함 |

## 패키지의 신뢰 경계

- 합성 시나리오의 값: [scenarios/](../../scenarios) manifest fixture. 실측 로그가 아닙니다.
- 실환경 증거 템플릿은 빈 양식으로 제공하며, 수집 전에 결론을 작성하지 않습니다.
- 외부 참조: NVIDIA 공식 문서 — [NCCL networking troubleshooting](https://docs.nvidia.com/deeplearning/nccl/user-guide/docs/troubleshooting/networking_troubleshooting.html)(Active/Physical State LinkUp/Link layer/expected Rate 확인만으로는 NCCL 성공을 증명하지 않음), [Xid errors 소개](https://docs.nvidia.com/deploy/xid-errors/introduction.html)(Xid만으로 root cause 단정 불가), [Xid 작업 흐름](https://docs.nvidia.com/deploy/xid-errors/working-with-xid-errors.html)(kernel NVRM:Xid, nvidia-smi -q, DCGM, bug report 교차 증거). 이 링크들은 절차의 출처이며 lab 구현과 신뢰 등급이 다릅니다. lab counter를 실제 DCGM 메트릭과 직접 오인하지 마세요 — 대응은 [metric-mapping.md](../metric-mapping.md)만 사용한다.
