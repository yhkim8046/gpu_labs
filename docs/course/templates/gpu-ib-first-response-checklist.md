# GPU/IB 장애 초동 대응 체크시트·런북 (교육용)

이 런북의 모든 값과 임계값은 gpu-lab 합성 fixture 기준이고 교육 권장값이다. 실제 클러스터에 적용할 때는 그 환경의 메트릭 정의와 임계값으로 다시 정한다.

## 0. 시작 전 고정(2분)

- [ ] 시각·노드·워크로드·영향 범위를 1줄로 기록
- [ ] 실습 환경 확인: `gpu version && gpu status`
- [ ] 상황 브리핑용 한 화면: `gpu metrics` / Grafana `gpu dashboard --port 3000`
- [ ] 증거나 기록을 먼저 하고 조치는 나중이다. 원본은 `gpu metrics --json` 또는 명령 원본 복사로 남긴다.

## 1. 스케줄링/Pending(GPU) 10분 코스

- [ ] `kubectl --context gpu-lab get pods -A -o wide | grep Pending`
- [ ] `kubectl --context gpu-lab describe pod <pod>` → events에 `Insufficient nvidia.com/gpu` 확인
- [ ] `kubectl --context gpu-lab get nodes -o custom-columns=NAME:.metadata.name,GPU:.status.capacity.nvidia\\.com/gpu,GPU-ALLOC:.status.allocatable.nvidia\\.com/gpu`
- [ ] `gpu metrics --query 'sum(gpu_lab_gpu_allocated) by (node)'`
- [ ] `gpu metrics --query 'min(gpu_lab_node_gpu_allocatable)'`
- 분기: 총 여유 < 요청 → 용량 / 총 여유 ≥ 요청인데 Pending → fragmentation 또는 selector/taint 문제
- 위험 조작 회피: drain·preemption·강제 삭제는 이 런북의 기본 절차가 아니다(3번 참조)

## 2. IB 링크/오류 10분 코스

- [ ] `gpu ibstat -l` , `gpu ibstatus` (필요 시 `--node <gpu-lab-worker|gpu-lab-worker2|gpu-lab-worker3>`)
- [ ] 판정 4종: State=Active / Physical State=LinkUp / Link layer / Rate(expected와 비교)
- [ ] `gpu metrics --query 'gpu_lab_ib_port_up'`, `gpu metrics --query 'gpu_lab_ib_link_rate_gbps'`
- [ ] `gpu metrics --query 'rate(gpu_lab_ib_symbol_errors_total[2m])'`, `rate(gpu_lab_rdma_retries_total[2m])`, `rate(gpu_lab_ib_xmit_discards_total[2m])`, `rate(gpu_lab_ib_link_downed_total[2m])`
- 분기: 링크 상태 정상이면 다음 3번으로 넘어간다(여기서 통신성 정상이란 결론으로 건너뛰지 않는다)
- lab 카운터는 실제 DCGM/NIC 카운터가 아니다. 실제 환경 값은 [real-cluster-evidence-template.md](real-cluster-evidence-template.md)로 수집한다.

## 3. 통신성(NCCL/분산학습) — 링크 상태와 별개 검증

- [ ] `gpu metrics --query 'gpu_lab_training_rank_up'`
- [ ] `gpu metrics --query 'gpu_lab_training_allreduce_errors_total'` 증가 여부
- [ ] `gpu metrics --query 'gpu_lab_training_step'` / `gpu_lab_training_loss` 진행 여부
- [ ] `gpu metrics --query 'gpu_lab_fabric_delay_seconds'`
- 원칙: ibstat의 Active/LinkUp/Rate 확인만으로 NCCL 성공을 증명하지 않는다(NVIDIA NCCL troubleshooting 문서와 같은 절차). 통신 성공은 통신 메트릭·로그가 증거다.

## 4. Thermal/Throttle 10분 코스

- [ ] `gpu metrics --query 'gpu_lab_gpu_temperature_celsius'`, `gpu_lab_gpu_throttle_active`, `gpu_lab_gpu_power_watts`
- [ ] `gpu metrics --query 'rate(gpu_lab_gpu_power_violation_total[5m])'`
- [ ] `gpu metrics --query 'gpu_lab_gpu_utilization_percent'`(throttle로 util이 꺽이면 패턴이 아닌 선후 순서로 판정)
- 분기: 온도 상승 → throttle → util 저하 선후가 맞으면 thermal 경로. 역순이면 다른 원인 탐색
- 복귀 확인은 환경(메트릭)과 작업(`gpu_lab_training_checkpoint_step` vs `gpu_lab_training_step`, `gpu_lab_training_restarts_total`)을 별개로.

## 5. Xid/GPU health(교차 증거)

- [ ] lab: `gpu metrics --query 'max(gpu_lab_gpu_xid_code)'`, `min(gpu_lab_gpu_health)`, `max(gpu_lab_gpu_health_status)`
- [ ] 원칙: Xid 단독 root cause 단정 금지. 실환경은 kernel의 NVRM Xid 라인 + `nvidia-smi -q` + DCGM health + bug report을 교차 수집(NVIDIA Xid 공식 문서 절차). 실로그가 없으면 수집 템플릿만 작성.

## 6. 복귀·종결 게이트

- [ ] `gpu verify <scenario>` 통과 (이는 환경 verify일 뿐 진단 정확도 증거가 아님)
- [ ] 환경 메트릭 복구 + 작업 진행(체크포인트/스텝) 동시 확인
- [ ] 원본 아카이빙(타임스탬프 포함) + 진단 보고서 제출

## 실제 환경에서 별도 승인이 필요한 조작

GPU reset, persistence 재시작, 노드 drain/cordon, switch 포트 shutdown, NIC firmware reload, 케이블 인출, Pod 강제 삭제, preemption 강제, priority 변경은 실습 절차에 포함되지 않는다. 실제 환경에서는 변경 승인과 롤백 계획 이후에 검토한다.

lab에서 상태를 되돌리는 명령: `gpu scenario reset`, `gpu training reset --namespace gpu-lab-demo`, timeline의 `duration` 만료 후 자동 reset.
