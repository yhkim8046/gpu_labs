# Synthetic metric과 실제 DCGM 대응표

`gpu-lab`의 `dcgm-exporter`는 이름과 Prometheus 관측 흐름을 교육용으로 재현하지만, NVIDIA driver/DCGM API를 호출하지 않습니다. 아래 표는 수강생이 synthetic dashboard에서 본 신호를 실제 GPU 클러스터의 조사 포인트로 연결하기 위한 대응표입니다.

실제 DCGM Exporter는 collector 설정에 선택된 DCGM field를 Prometheus metric으로 노출하며, 어떤 field가 실제로 나오는지는 GPU, driver, DCGM, exporter 설정에 따라 달라집니다. 공식 필드 목록은 [NVIDIA DCGM Exporter Metrics](https://docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html)를 기준으로 확인하세요.

| gpu-lab metric | synthetic 의미/단위 | 실제 DCGM 관측 포인트 | 주의점 |
| --- | --- | --- | --- |
| `gpu_lab_gpu_utilization_percent` | GPU 사용률, `%` | `DCGM_FI_DEV_GPU_UTIL` | 실제 exporter의 field 이름은 그대로 노출되거나 설정에 따라 바뀔 수 있습니다. |
| `gpu_lab_gpu_memory_used_bytes` | 사용 VRAM, `bytes` | `DCGM_FI_DEV_FB_USED` | DCGM 기본 field의 용량 단위와 synthetic bytes 단위를 반드시 확인하고 변환합니다. |
| `gpu_lab_gpu_memory_total_bytes` | 총 VRAM, `bytes` | `DCGM_FI_DEV_FB_USED + DCGM_FI_DEV_FB_FREE` | 실제 exporter가 total을 직접 제공한다고 가정하지 말고 used/free로 계산합니다. |
| `gpu_lab_gpu_temperature_celsius` | GPU 온도, `°C` | `DCGM_FI_DEV_GPU_TEMP` | 보드/메모리 온도는 서로 다른 field일 수 있습니다. |
| `gpu_lab_gpu_power_watts` | 전력 사용량, `W` | `DCGM_FI_DEV_POWER_USAGE` | 실제 field의 canonical 이름은 `DCGM_FI_DEV_BOARD_POWER_WATTS`입니다. |
| `gpu_lab_gpu_xid_code` | 현재 synthetic XID, 정수 | `DCGM_FI_DEV_XID_ERRORS`, 또는 `DCGM_EXP_XID_ERRORS_*` | 실제 환경에서는 error code label, count/total, kernel log를 함께 봐야 합니다. |
| `gpu_lab_gpu_health` | `1=healthy`, `0=unhealthy` | `DCGM_EXP_GPU_HEALTH_STATUS` | DCGM health status는 `0=PASS`, `10=WARN`, `20=FAIL`이므로 값 체계가 다릅니다. |
| `gpu_lab_exporter_up` | synthetic exporter process 상태 | Prometheus `up{job=...}` | exporter 자체와 GPU health는 별개의 장애 축입니다. |
| `gpu_lab_scenario_info` / `generation` | 현재 실습 scenario 식별자 | 실제 환경에 직접 대응 없음 | Lab control-plane 메타데이터입니다. |

## Label 대응

Lab metric은 교육 편의를 위해 `node`와 `gpu` label을 사용합니다. 실제 DCGM Exporter는 설정과 버전에 따라 `gpu`, `UUID`/`uuid`, `pci_bus_id`, `device`, `modelName`, `hostname`을 제공하고, Kubernetes mapping을 활성화하면 `pod`, `namespace`, `container` 같은 label을 추가할 수 있습니다. 따라서 PromQL을 복사할 때 label 이름과 대소문자를 먼저 확인해야 합니다.

## 강의에서 강조할 경계

1. Lab의 XID 숫자는 실제 GPU fault를 발생시키지 않습니다.
2. Lab의 VRAM 수치는 host 메모리나 GPU 메모리를 점유하지 않습니다.
3. Lab의 `nvidia.com/gpu`는 Kubernetes scheduling 연습용 Extended Resource입니다.
4. 실제 클러스터에서는 `nvidia-smi`, DCGM health, node driver 상태, kubelet/device-plugin 로그, kernel log를 함께 조사합니다.
