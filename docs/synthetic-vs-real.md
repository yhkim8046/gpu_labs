# Synthetic Lab과 실제 GPU 환경의 경계

gpu-lab은 GPU가 없는 노트북에서 Kubernetes GPU Infrastructure의 운영 흐름을 연습하기 위한 교육용 시뮬레이터입니다. 실제 GPU, CUDA kernel, NVIDIA driver, DCGM host engine을 제공하거나 검증하지 않습니다.

| 영역 | gpu-lab에서 연습하는 것 | 실제 환경에서 추가로 필요한 것 |
| --- | --- | --- |
| Node resource | `nvidia.com/gpu` Extended Resource와 scheduling | NVIDIA driver, kubelet device-plugin registration, 실제 GPU capacity |
| Device plugin | Allocate/ListAndWatch 흐름과 Pod resource request | NVIDIA device files, CDI/device mount, driver compatibility |
| Exporter | DCGM Exporter와 유사한 scrape/health/incident 흐름 | DCGM host engine, NVML/DCGM field availability, GPU hardware |
| Monitoring | Prometheus, ServiceMonitor, Grafana, alert rule | scrape cardinality, retention, federation, production alert routing |
| Incident | utilization, VRAM, thermal, XID, target-down, Pending Pod, synthetic `nvidia-smi` view | 실제 `nvidia-smi`, `dcgmi`, kernel log, driver/GPU Operator 상태 |
| Reset | ConfigMap과 scenario workload 초기화 | 실제 GPU reset, node drain/reboot, incident remediation |

모든 Lab 리소스에는 `gpu-lab.io/implementation: synthetic` annotation을 사용합니다. runtime image와 release에는 이름이 같은 호환용 `nvidia-smi`가 포함되지만, 실제 NVIDIA binary/driver가 아니라 GPU Lab state endpoint를 표시하는 교육용 shim입니다. 강의에서는 `gpu-lab scenario run`으로 신호를 만든 다음 `nvidia-smi`, `gpu-lab verify`, Grafana/Prometheus로 조사 순서를 익히고, 마지막에 위 표를 사용해 실제 GPU 클러스터에서의 명령과 차이를 설명합니다.
