# InfiniBand/RDMA Fabric 운영 실습

이 문서는 GPU Lab의 synthetic InfiniBand/RDMA fabric 실습 런북입니다. HCA와 port 상태, negotiated rate, 물리 링크 오류, 송신 큐 압박, RDMA retry/timeout을 확인하고 그것이 분산학습의 AllReduce와 step 진행에 어떤 증상으로 나타나는지 연습합니다.

## 먼저 확인할 경계

이 Lab은 실제 InfiniBand NIC, 스위치, 케이블, OFED, RDMA CM, NCCL 또는 NVLink를 사용하지 않습니다. gpu_lab_ib_*, gpu_lab_rdma_*, gpu_lab_training_fabric_* 값은 결정적인 synthetic 상태와 장애 주입 결과입니다. 대시보드의 수치로 실제 장비의 대역폭, latency, MTU, 링크 품질 또는 성능을 추정해서는 안 됩니다.

실제 장비로 번역할 때 필요한 명령은 이 문서의 마지막 절에 따로 적었습니다. 해당 명령이 현재 Lab 컨테이너나 kind 노드에 설치되어 있다고 가정하지 않습니다.

## 사전 조건과 baseline

클러스터와 monitoring, synthetic fabric exporter, 3-rank training worker가 이미 실행 중이어야 합니다.

    gpu doctor
    gpu helm install all
    gpu training run --workers 3 --wait
    gpu ibstat --node gpu-lab-worker
    gpu ibstatus --node gpu-lab-worker
    gpu ibv_devinfo --node gpu-lab-worker -v
    gpu training status

정상 baseline에서 다음을 확인합니다.

- 모든 HCA port가 up이고 physical_state도 LinkUp 계열입니다.
- negotiated rate가 100 Gbps 이상입니다.
- gpu_lab_ib_symbol_errors_total, gpu_lab_ib_link_error_recovery_total, gpu_lab_ib_link_downed_total이 baseline 이후 증가하지 않습니다.
- gpu_lab_ib_xmit_discards_total, gpu_lab_ib_xmit_wait_total, gpu_lab_rdma_retries_total, gpu_lab_rdma_timeouts_total이 안정적입니다.
- 세 training rank가 ready이고 gpu_lab_training_step이 함께 증가합니다.
- gpu_lab_training_fabric_fault_active가 0이며 fabric delay, error, retry counter가 새로 증가하지 않습니다.

상태와 metric을 확인하는 대표 명령은 다음과 같습니다.

    gpu ibstat --node gpu-lab-worker
    gpu ibstatus --node gpu-lab-worker
    gpu ibv_devinfo --node gpu-lab-worker -v
    gpu metrics --query 'gpu_lab_ib_port_up'
    gpu metrics --query 'gpu_lab_ib_port_state'
    gpu metrics --query 'gpu_lab_ib_link_rate_gbps'
    gpu metrics --query 'sum(gpu_lab_training_rank_up)'
    gpu metrics --query 'gpu_lab_training_step'

Grafana에서 GPU Lab InfiniBand / RDMA Fabric 대시보드를 열고 node, hca, port, link_layer 변수를 먼저 All로 둡니다. 대시보드의 첫 패널에 있는 synthetic boundary를 읽은 다음, Port health / state부터 오른쪽·아래 순서로 조사합니다.

## Metric과 조사 의미

| 계층 | Metric | 조사 질문 |
|---|---|---|
| Port | gpu_lab_ib_port_up | 포트가 현재 통신 가능한가? |
| Port state | gpu_lab_ib_port_state{state,physical_state} | 논리 상태와 물리 링크 상태가 일치하는가? |
| Link | gpu_lab_ib_link_rate_gbps | 연결은 살아 있지만 rate가 낮아졌는가? |
| Traffic | gpu_lab_ib_tx_bytes_total, gpu_lab_ib_rx_bytes_total | 선택한 port에 실제 synthetic traffic이 흐르는가? |
| Physical/link | gpu_lab_ib_symbol_errors_total, gpu_lab_ib_link_error_recovery_total, gpu_lab_ib_link_downed_total | 오류와 재복구·단절 이벤트가 증가했는가? |
| Congestion | gpu_lab_ib_xmit_discards_total, gpu_lab_ib_xmit_wait_total | 송신 큐가 밀리거나 discard가 발생했는가? |
| RDMA | gpu_lab_rdma_retries_total, gpu_lab_rdma_timeouts_total | retry가 늘었는가, 통신이 timeout되는가? |
| Training correlation | gpu_lab_training_fabric_fault_active{mode}, gpu_lab_training_fabric_delay_seconds | 어떤 fabric fault가 어느 rank에 전달됐는가? |
| Training outcome | gpu_lab_training_fabric_errors_total, gpu_lab_training_fabric_retries_total, gpu_lab_training_allreduce_seconds, gpu_lab_training_step | fabric 신호가 collective latency와 progress에 영향을 줬는가? |

Counter는 현재 값보다 increase(metric[5m]) 또는 Grafana의 rate 패널로 baseline 이후 증가를 봅니다. 한 port의 문제인지 여러 node/HCA에 걸친 문제인지 label을 유지한 채 비교합니다.

## 장애 주입 공통 절차

각 scenario는 다음 순서를 지킵니다. ConfigMap projected volume을 worker가 읽는 데 환경에 따라 최대 180초가 걸릴 수 있으므로, metric이 반영될 때까지 bounded polling을 사용합니다.

    gpu scenario run <scenario>
    gpu verify <scenario>
    gpu ibstat --node <worker-node>
    gpu ibstatus --node <worker-node>
    gpu ibv_devinfo --node <worker-node> -v
    gpu training status
    gpu metrics --query '<scenario-specific PromQL>'
    gpu scenario reset
    gpu training recover
    gpu verify normal

gpu scenario reset은 synthetic exporter와 training control을 정상화할 뿐이며, 실제 포트 flap, NIC reset, switch counter clear 또는 node reboot를 수행하지 않습니다. 실패하더라도 마지막에 gpu scenario reset과 gpu training recover를 실행해 다음 실습의 baseline을 복구합니다.

## Scenario 1: ib-link-down

    gpu scenario run ib-link-down
    gpu verify ib-link-down
    gpu ibstat --node gpu-lab-worker2
    gpu ibstatus --node gpu-lab-worker2 mlx5_0:1
    gpu ibv_devinfo --node gpu-lab-worker2 -d mlx5_0 -i 1 -v
    gpu metrics --query 'min(gpu_lab_ib_port_up)'
    gpu metrics --query 'sum(gpu_lab_ib_link_downed_total)'
    gpu metrics --query 'max(gpu_lab_training_fabric_fault_active)'

예상 관찰:

- 영향을 받은 port의 gpu_lab_ib_port_up이 0이 되고 gpu_lab_ib_port_state의 state/physical_state가 down 계열로 바뀝니다.
- gpu_lab_ib_link_downed_total이 증가하고, 상황에 따라 RDMA timeout과 retry도 증가합니다.
- training dashboard에서 fabric_fault_active{mode="down"}가 활성화되고 affected rank의 fabric error 또는 AllReduce error가 증가합니다. 다른 rank는 같은 AllReduce step에서 대기하므로 gpu_lab_training_step 진행이 멈춥니다.
- GPULabIBPortDown, GPULabIBLinkDownCounterIncreasing, GPULabTrainingFabricFault를 의도된 exercise alert로 확인합니다.

복구 시에는 port가 다시 up인지, training fault가 0인지, step이 다시 증가하는지 확인합니다.

    gpu scenario reset
    gpu training recover
    gpu metrics --query 'min(gpu_lab_ib_port_up)'
    gpu metrics --query 'max(gpu_lab_training_fabric_fault_active)'
    gpu metrics --query 'sum(changes(gpu_lab_training_step[1m]))'

## Scenario 2: ib-rate-degraded

    gpu scenario run ib-rate-degraded
    gpu verify ib-rate-degraded
    gpu ibstat --node gpu-lab-worker3
    gpu ibstatus --node gpu-lab-worker3 mlx5_0:1
    gpu ibv_devinfo --node gpu-lab-worker3 -d mlx5_0 -i 1 -v
    gpu metrics --query 'min(gpu_lab_ib_link_rate_gbps)'
    gpu metrics --query 'max(gpu_lab_training_fabric_delay_seconds)'

예상 관찰:

- port는 up으로 남아 있지만 negotiated rate가 100 Gbps 미만으로 내려갑니다.
- Port health는 정상처럼 보이므로 state 패널만으로 결론 내리지 말고 rate 패널과 TX/RX throughput을 같이 봅니다.
- gpu_lab_training_fabric_delay_seconds와 gpu_lab_training_allreduce_seconds가 증가하고, 모든 rank의 step이 느려질 수 있습니다.
- GPULabIBRateDegraded와 GPULabTrainingFabricDelay가 핵심 알림입니다.

복구 후에는 rate threshold를 회복하고 AllReduce latency와 step이 baseline으로 돌아오는지 확인합니다.

## Scenario 3: ib-symbol-errors

    gpu scenario run ib-symbol-errors
    gpu verify ib-symbol-errors
    gpu metrics --query 'sum(gpu_lab_ib_symbol_errors_total)'
    gpu metrics --query 'sum(gpu_lab_ib_link_error_recovery_total)'
    gpu metrics --query 'sum(gpu_lab_training_fabric_errors_total)'

예상 관찰:

- gpu_lab_ib_symbol_errors_total이 증가하며 link recovery counter가 동반 상승할 수 있습니다.
- port가 당장 down되지 않아도 물리 링크 품질 저하를 의심해야 합니다.
- synthetic training에서는 fabric retries 또는 delay가 증가한 뒤 AllReduce latency가 흔들릴 수 있습니다.
- GPULabIBSymbolErrorsIncreasing, GPULabIBLinkRecoveryIncreasing을 같은 port label로 확인합니다.

실제 장비에서 케이블·광 모듈·스위치 포트 문제로 단정하기 전에는 counter 증가 시각과 작업 재배치, 다른 port의 동시 증상을 비교합니다.

## Scenario 4: rdma-retry-storm

    gpu scenario run rdma-retry-storm
    gpu verify rdma-retry-storm
    gpu metrics --query 'sum(gpu_lab_rdma_retries_total)'
    gpu metrics --query 'sum(gpu_lab_rdma_timeouts_total)'
    gpu metrics --query 'sum(gpu_lab_training_fabric_retries_total)'

예상 관찰:

- RDMA retry가 빠르게 증가하고 timeout이 동반되거나 뒤따를 수 있습니다.
- training worker의 fabric retry/error counter가 증가하고 AllReduce가 느려지거나 bounded retry 이후 오류를 냅니다.
- gpu_lab_training_step이 일정 시간 변하지 않는지 changes(gpu_lab_training_step[2m])로 확인합니다.
- GPULabRDMARetryStorm, GPULabRDMATimeoutsIncreasing, GPULabTrainingFabricRetries를 correlation alert로 확인합니다.

복구 시에는 retry counter가 reset으로 더 이상 증가하지 않는지와 checkpoint lag가 줄어드는지를 확인합니다. Counter를 0으로 만드는 것이 실제 장비의 누적 hardware counter clear와 같은 의미는 아닙니다.

## Scenario 5: ib-congestion

    gpu scenario run ib-congestion
    gpu verify ib-congestion
    gpu metrics --query 'sum(gpu_lab_ib_xmit_wait_total)'
    gpu metrics --query 'sum(gpu_lab_ib_xmit_discards_total)'
    gpu metrics --query 'max(gpu_lab_training_fabric_delay_seconds)'

예상 관찰:

- gpu_lab_ib_xmit_wait_total 또는 gpu_lab_ib_xmit_discards_total이 증가해 송신 큐 압박을 나타냅니다.
- port up과 negotiated rate는 정상이어도 throughput이 기대보다 낮고 fabric delay가 증가할 수 있습니다.
- AllReduce latency가 올라가고 모든 rank의 progress가 느려지는지 확인합니다. 한 rank만 느리면 straggler와 congestion을 분리해 봅니다.
- GPULabIBXmitCongestion과 GPULabTrainingFabricDelay가 핵심 알림입니다.

실제 환경에서는 endpoint 하나의 문제인지 switch fabric 전체의 hot spot인지 구분하기 위해 port, switch, traffic class, QoS counter를 같은 시간 범위로 비교합니다.

## 복구·검증 순서

    gpu scenario reset
    gpu training recover
    gpu verify normal
    gpu ibstat --node gpu-lab-worker
    gpu ibstatus --node gpu-lab-worker
    gpu ibv_devinfo --node gpu-lab-worker -v
    gpu training status
    gpu metrics --query 'min(gpu_lab_ib_port_up)'
    gpu metrics --query 'min(gpu_lab_ib_link_rate_gbps)'
    gpu metrics --query 'max(gpu_lab_training_fabric_fault_active)'
    gpu metrics --query 'sum(changes(gpu_lab_training_step[1m]))'

정상으로 볼 기준은 port up, rate 100 Gbps 이상, active fault 0, training rank ready, step 증가입니다. 누적 counter가 reset 전 값보다 낮아지는 것은 이 synthetic exporter가 baseline을 재생성했기 때문일 수 있으므로, reset 직후 한 번의 값보다 reset 이후의 증가 추세와 대시보드 annotation을 봅니다.

## Troubleshooting 순서

1. **관측 자체 확인**: Grafana fabric dashboard가 로드되는지, Prometheus target이 UP인지, ibstat·ibstatus·ibv_devinfo가 exporter/Prometheus 상태를 읽는지 확인합니다.
2. **범위 축소**: node → hca → port → link_layer 변수 순서로 한 port의 문제인지 공통 문제인지 좁힙니다.
3. **Port health/state**: port_up, state, physical_state를 함께 보고 link down 이벤트 시각을 기록합니다.
4. **Rate와 traffic**: negotiated rate와 TX/RX rate를 비교해 완전 단절, rate 저하, traffic starvation을 구분합니다.
5. **Error/congestion**: symbol/recovery/link-down, xmit wait/discards, RDMA retry/timeout의 counter 증가를 5분 rate로 비교합니다.
6. **Training correlation**: fabric fault mode, delay, errors, retries를 rank별 AllReduce latency와 step 변화에 겹쳐 봅니다.
7. **Kubernetes 상태**: training pod readiness, restart, node placement, events, logs를 조사합니다. synthetic scenario가 실제 node나 device plugin 상태를 바꾸지는 않는다는 점을 기억합니다.
8. **복구**: 실습이면 gpu scenario reset과 gpu training recover를 실행하고, 실제 환경이면 승인된 포트 차단·workload drain·NIC/케이블 교체·job retry 절차를 따릅니다.

## 실제 IB/RDMA 환경으로 번역하기

아래 명령은 실제 호스트나 네트워크 운영 도구가 설치된 debug pod에서 사용하는 예시입니다. GPU Lab에 이 바이너리들이 포함되어 있다고 가정하지 않습니다.

    # HCA와 port의 논리/물리 상태
    ibstat
    ibstatus

    # Port counter, symbol/link error, xmit wait/discard 계열
    perfquery -x
    perfquery -r

    # RDMA device/resource counter
    rdma link
    rdma statistic show

    # 실제 bandwidth/latency microbenchmark (peer와 사전 합의 필요)
    ib_write_bw <server-address>
    ib_write_lat <server-address>

    # Mellanox/NVIDIA NIC의 cable, lane, speed/PCS 상태
    mlxlink -d <device> -p <port>

    # GPU/NIC/NUMA topology와 NCCL 네트워크 선택 확인
    nvidia-smi topo -m
    lspci -tv
    numactl -H
    NCCL_DEBUG=INFO NCCL_DEBUG_SUBSYS=NET,GRAPH <training-command>

실제 운영에서는 위 결과를 Kubernetes node, Network Operator/RDMA device plugin 자원, pod의 /dev/infiniband 노출, NCCL topology 로그와 함께 확인해야 합니다. ib_write_bw나 ib_write_lat의 결과도 애플리케이션 AllReduce 성능과 동일하지 않으므로, 실제 job의 NCCL logs와 workload-level throughput을 별도로 기록합니다.

관련 실습은 [분산학습 운영 실습](distributed-training.md), 전체 synthetic 범위는 [synthetic-vs-real](synthetic-vs-real.md)을 참조하세요.
