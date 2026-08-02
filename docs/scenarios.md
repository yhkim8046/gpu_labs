# GPU Lab Scenario Runbook

이 문서는 강의 중 각 scenario를 실행하고, 증거를 수집하고, 원인을 설명하고, 정상 상태로 복구하기 위한 실습 runbook입니다.

모든 GPU 값과 XID는 synthetic입니다. 실제 환경에서는 DCGM, `nvidia-smi`, kernel log, GPU Operator 상태를 추가로 확인해야 하며 이 Lab의 `reset`은 실제 GPU reset이나 node reboot를 수행하지 않습니다.

## 공통 조사 흐름

```bash
gpu-lab scenario run <name>
gpu-lab scenario inspect <name>
gpu-lab status
kubectl --context gpu-lab get pods -n gpu-lab-demo -o wide
kubectl --context gpu-lab get events -A --sort-by=.lastTimestamp
```

실습을 마친 뒤에는 항상 복구합니다.

```bash
gpu-lab scenario reset
kubectl --context gpu-lab get pods -n gpu-lab-demo
```

## Scenario 목록

| Scenario | 핵심 학습 포인트 | 대표 증상 |
|---|---|---|
| `normal` | 정상 baseline | 낮은 온도, XID 0, health 1 |
| `gpu-util-high` | 지속적인 GPU 포화 | utilization 95% |
| `vram-pressure` | VRAM 용량 병목 | memory usage 92% |
| `thermal-throttling` | 열과 성능 저하의 상관관계 | 96°C, utilization 하락 |
| `xid-48` | ECC/DBE 장애 대응 | XID 48, unhealthy |
| `xid-79` | bus-level 장애 대응 | XID 79, unhealthy |
| `exporter-down` | 관측 장애와 GPU 장애 구분 | Prometheus target down |
| `scheduling-failure` | 단일 node 용량 초과 | 9 GPU Pod Pending |
| `gpu-idle` | GPU 예약 낭비 | 1 GPU allocated, utilization 2% |
| `node-selector-mismatch` | GPU profile/label 불일치 | node affinity/selector failure |
| `gpu-fragmentation` | cluster 총량과 node 단위 할당 차이 | 총 3 GPU 여유, 2 GPU Pod Pending |

## thermal-throttling

```bash
gpu-lab scenario run thermal-throttling
kubectl --context gpu-lab get prometheusrule gpu-lab-alerts -n gpu-lab-monitoring
```

Grafana에서 utilization, temperature, power를 함께 비교합니다. 온도가 90°C를 넘고 utilization이 포화 상태보다 낮아지는 것을 확인합니다. 실제 환경에서는 `DCGM_FI_DEV_GPU_TEMP`, power, thermal violation 또는 clock-event 계열과 냉각·전력 상태를 함께 조사합니다.

복구는 `gpu-lab scenario reset`입니다. 실제 장비에서는 workload drain, 냉각 상태 확인, power/clock 정책 확인이 별도로 필요합니다.

## xid-48

```bash
gpu-lab scenario run xid-48
kubectl --context gpu-lab get configmap gpu-lab-scenario -n gpu-lab-system -o yaml
```

Grafana에서 XID 48과 health 0을 확인합니다. XID 48은 실제 NVIDIA 환경에서 double-bit ECC 오류와 연관되므로, 강의에서는 단순 Pod 재시작과 node/GPU 격리·복구의 차이를 설명합니다. 이 Lab은 kernel log나 실제 ECC counter를 만들지 않습니다.

## gpu-idle

```bash
gpu-lab scenario run gpu-idle
kubectl --context gpu-lab get pod gpu-lab-idle-workload -n gpu-lab-demo -o wide
kubectl --context gpu-lab describe pod gpu-lab-idle-workload -n gpu-lab-demo
```

Pod가 GPU 1개를 정상 할당받았지만 Grafana utilization은 2%입니다. 장애가 아니라 비용·용량 효율 문제이며, request sizing, queue 정책, idle reclamation을 논의하기 위한 시나리오입니다.

## node-selector-mismatch

```bash
gpu-lab scenario run node-selector-mismatch
kubectl --context gpu-lab describe pod gpu-lab-node-selector-mismatch -n gpu-lab-demo
kubectl --context gpu-lab get nodes --show-labels
```

Pod는 `gpu.lab/node-id=gpu-node-99`를 요구하지만 그런 node가 없어 Pending입니다. `Events`의 node selector/affinity 불일치 메시지를 근거로 원인을 찾습니다. 실제 환경에서는 GFD가 만든 GPU product, memory, MIG 관련 label 오타나 잘못된 profile 요구가 같은 형태의 문제를 만듭니다.

## gpu-fragmentation

```bash
gpu-lab scenario run gpu-fragmentation
kubectl --context gpu-lab get pods -n gpu-lab-demo -o wide
kubectl --context gpu-lab describe pod gpu-lab-fragmentation-target -n gpu-lab-demo
kubectl --context gpu-lab describe nodes
```

각 worker가 8개 중 7개 GPU를 사용하므로 cluster 전체에는 3개가 남습니다. 그러나 Extended Resource는 node를 가로질러 합쳐 할당할 수 없어서 2 GPU를 요구하는 target Pod는 Pending입니다. 이 실습에서는 gang scheduling, workload 크기 조정, queueing, bin-packing 정책이 필요한 이유를 설명합니다.

## 기존 시나리오 빠른 조사 명령

```bash
gpu-lab scenario run scheduling-failure
kubectl --context gpu-lab describe pod gpu-lab-scheduling-failure -n gpu-lab-demo

gpu-lab scenario run exporter-down
kubectl --context gpu-lab get pods -n gpu-lab-system -l app.kubernetes.io/name=dcgm-exporter

gpu-lab scenario run xid-79
kubectl --context gpu-lab get configmap gpu-lab-scenario -n gpu-lab-system -o yaml
```
