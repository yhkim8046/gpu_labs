# 진단 보고서 제출 템플릿 (lab/실환경 공용)

제출자: ______  실습 일시(KST): ______  캡스톤: 1 / 2 / 3  
환경: gpu-lab synthetic(기본) / 실제 클러스터(별도 승인 시에만)

## 0. 결론 한 문장

- 판정: ______________________
- 신뢰도: 높음 / 중간 / 낮음 + 근거 한 문장
- ⚠ lab에서 관측한 값은 `[합성]`, 실제 환경에서 직접 수집한 값은 `[실측]`, 본인 해석은 `[해석]`으로 라벨한다. `[실측]`은 직접 수집한 증거가 있을 때만 쓴다.

## 1. 관측 (원본만, 해석 금지)

타임스탬프를 붙여 명령 원본을 붙인다. 해석은 4번에 쓴다.

| # | 일시 | 명령/쿼리 | 원본 출력(발췌) |
| --- | --- | --- | --- |
| 1 |  | `gpu status` |  |
| 2 |  | `kubectl --context gpu-lab get pods -A -o wide` |  |
| 3 |  | `kubectl --context gpu-lab describe pod <pod>` |  |
| 4 |  | `gpu metrics --query '<PromQL>'` |  |
| 5 |  | `gpu ibstat -l` / `gpu ibstatus --node <node>` |  |
| 6 |  | `gpu verify <scenario>` |  |

## 2. 가설 (최소 2개)

| 가설 | 지지 증거(1번 번호) | 반증 시도 | 결과(채택/기각) |
| --- | --- | --- | --- |
| A:  |  |  |  |
| B:  |  |  |  |

단일 신호(Xid, 상태 문자열 등)만으로 root cause를 단정했으면 이유를 쓴다. NVIDIA 공식 문서도 Xid 단독으로 root cause를 단정하지 않는다.

## 3. 조치

- lab에서 실행한 명령: (시나리오 run/reset, training inject/recover 등 안전 조작만)
- lab에서 실행하지 않기로 한 것: (drain, cordon, GPU reset, 포트 shutdown, Pod 강제 삭제 …)
- 실행하지 않은 이유와 실환경이라면 필요한 조건(승인·변경 기록·대체 워크로드 위치): ____________

## 4. 복구 확인

| 확인 계통 | 확인 명령/쿼리 | 복구 판정 기준 | 실측/fixture | 판정 |
| --- | --- | --- | --- | --- |
| 환경(메트릭·상태) |  |  |  |  |
| 작업(checkpoint/step/restart) |  |  |  |  |
| 관측 파이프라인(exporter up 등) |  |  |  |  |

- [ ] 환경과 작업 두 계통을 별개로 판정했다.
- [ ] `gpu verify`가 검증하는 것은 시나리오의 기대 상태(장애 상태 포함)이고, 진단 설명의 정확도와 별개임을 명시했다.
- [ ] 설명 평정은 위 원본을 근거로 한다.

## 5. 실환경 대체 시 필요한 추가 증거

- [ ] kernel/driver 로그(NVRM/Xid 라인, timestamp 포함) — 실측 보유 시만
- [ ] `nvidia-smi -q`, DCGM health, bug report — NVIDIA 공식 문서 절차 대로
- [ ] switch/서브넷 측 counter — lab 미재현 항목임을 명시
- [ ] [real-cluster-evidence-template.md](real-cluster-evidence-template.md) 작성 여부: ______

## 6. 루브릭 셀프 체크(0~3 자평, 강사 평정과 별개)

증거 수집 __ / 가설 검증 __ / 경계 인식 __ / 안전 __ / 재현성 __
