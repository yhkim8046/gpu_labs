# gpu-lab PromQL 치트시트 (lab에서 확인한 실제 노출하는 이름 기준)

- 아래 이름은 이 리포 `internal/exporter/model.go`, `internal/training/metrics.go`에서 확인한 lab 내부 노출 이름이다. 실제 DCGM/DCGM exporter의 이름이 아니다 — 대명표는 [metric-mapping.md](../../metric-mapping.md)를 쓴다.
- 모든 임계값 예시는 교육 권장값(fixture)이고 SLA 근거가 아니다. 실환경 적용 시 값을 재정의하라.
- 실행 방법: `gpu metrics --query '<PromQL>'` (stdout), Grafana `gpu dashboard --port 3000`에서 paste, `--json`으로 기계가 읽는 출력.
- lab의 기본 label: `node`, `gpu`(training 계열은 `rank` 등 — 실제 라벨은 메트릭 출력에서 직접 확인). label 대소문자·이름이 실환경과 다르므로 실환경 재사용 시 `gpu metrics`로 먼저 확인한다.

## GPU 기본 게이지

```promql
gpu_lab_gpu_utilization_percent
gpu_lab_gpu_memory_used_bytes / gpu_lab_gpu_memory_total_bytes * 100
gpu_lab_gpu_temperature_celsius
gpu_lab_gpu_power_watts
max(gpu_lab_gpu_utilization_percent) by (node)
min(gpu_lab_gpu_health)                       # 0 = unhealthy
max(gpu_lab_gpu_health_status)                # 0 PASS / 10 WARN / 20 FAIL
```

## Throttle·전원

```promql
gpu_lab_gpu_throttle_active
rate(gpu_lab_gpu_power_violation_total[5m])
rate(gpu_lab_gpu_pcie_replay_total[5m])
rate(gpu_lab_gpu_ecc_dbe_total[5m])
gpu_lab_gpu_xid_code                          # 0 = 없음
```

## 스케줄링·용량

```promql
gpu_lab_node_gpu_capacity
gpu_lab_node_gpu_allocatable
sum(gpu_lab_gpu_allocated)
sum(gpu_lab_gpu_allocated) by (node)
min(gpu_lab_node_gpu_allocatable)             # fragmentation 판정 출발점
gpu_lab_node_gpu_allocatable >= bool 2        # 노드당 2장 가능 노드 존재 여부
```

## InfiniBand / RDMA

```promql
gpu_lab_ib_port_up
gpu_lab_ib_port_state
gpu_lab_ib_link_rate_gbps
rate(gpu_lab_ib_symbol_errors_total[2m])
rate(gpu_lab_ib_link_error_recovery_total[2m])
rate(gpu_lab_ib_link_downed_total[2m])
rate(gpu_lab_ib_xmit_discards_total[2m])
rate(gpu_lab_ib_xmit_wait_total[2m])
rate(gpu_lab_ib_tx_bytes_total[2m])
rate(gpu_lab_ib_rx_bytes_total[2m])
rate(gpu_lab_rdma_retries_total[2m])
rate(gpu_lab_rdma_timeouts_total[2m])
gpu_lab_fabric_delay_seconds
```

## 분산 학습(workload)

```promql
gpu_lab_training_step
gpu_lab_training_checkpoint_step
gpu_lab_training_loss
gpu_lab_training_samples_total
gpu_lab_training_rank_up
gpu_lab_training_restarts_total
rate(gpu_lab_training_allreduce_errors_total[5m])
gpu_lab_training_straggler_active
gpu_lab_training_fabric_fault_active
rate(gpu_lab_training_fabric_errors_total[5m])
```

## 관측 파이프라인 무결성

```promql
gpu_lab_exporter_up                             # lab projection availability
up{service="dcgm-exporter"}                     # gpu-lab 기본 쿼리에 사용되는 target 확인
gpu_lab_scenario_info
gpu_lab_scenario_generation
```

## 복귀 확인 조합 예(교육 권장값)

```promql
# 온도 복귀: 정상 기준선과 비교하되 임계값은 fixture일 뿐임
gpu_lab_gpu_temperature_celsius < 80

# 환경+작업 동시 복구: 두 series를 별개 판정
gpu_lab_gpu_throttle_active == 0
gpu_lab_training_rank_up == 1
delta(gpu_lab_training_step[2m]) > 0
```

## 흔한 오판

| 증상 | 원인 | 확인 |
| --- | --- | --- |
| 쿼리가 빈 결과 | lab에 없는 실환경 DCGM 이름(`DCGM_FI_*`) 사용 | [metric-mapping.md](../../metric-mapping.md)로 환산 |
| 값이 0만 나옴 | 시나리오 미실행 상태 | `gpu_lab_scenario_info` 확인 |
| rate가 없음 | counter가 아니라 gauge에 rate 적용 | gauge는 `rate` 금지 |
