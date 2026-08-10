# 분산학습 운영 실습

이 실습은 Kubernetes StatefulSet으로 실행하는 3개 synthetic worker의 운영 흐름을 연습합니다. worker는 결정적인 CPU 계산과 HTTP control-plane AllReduce를 수행하며 checkpoint와 Prometheus 지표를 남깁니다.

## 실습 전제

Docker Desktop(또는 Docker Engine), kind, kubectl, Helm 3+, Go 1.26+가 준비되어 있고 GPU Lab cluster와 monitoring component가 설치되어 있어야 합니다.

    gpu doctor
    gpu helm install all
    kubectl --context gpu-lab get nodes
    kubectl --context gpu-lab -n gpu-lab-monitoring get pods

소스 checkout에서는 아래 명령의 gpu를 go run ./cmd/gpu-lab로 바꿔도 됩니다.

## 시작·상태·로그

    gpu training run --workers 3 --wait
    gpu training status
    gpu training logs --rank 0
    gpu training logs --rank 1
    gpu training logs --rank 2

직접 Kubernetes 상태를 확인할 때는 다음을 사용합니다.

    kubectl --context gpu-lab -n gpu-lab-demo get statefulset,service,pods -l app.kubernetes.io/name=gpu-lab-training -o wide
    kubectl --context gpu-lab -n gpu-lab-demo get events --sort-by=.lastTimestamp
    kubectl --context gpu-lab -n gpu-lab-demo port-forward svc/gpu-lab-training 9401:9401
    curl -s http://127.0.0.1:9401/metrics | rg 'gpu_lab_training_(step|rank_up|allreduce)'

Prometheus와 Grafana는 기존 monitoring 접근 방법을 따릅니다.

    gpu metrics
    gpu dashboard --port 3001

Grafana에서 GPU Lab Distributed Training 대시보드를 열고 rank, pod, node 변수를 선택합니다. Prometheus에서는 다음과 같이 직접 질의할 수 있습니다.

    gpu_lab_training_step
    sum(gpu_lab_training_rank_up) by (rank)
    rate(gpu_lab_training_samples_total[1m])
    gpu_lab_training_step - gpu_lab_training_checkpoint_step

## 정상 기준

- StatefulSet desired/current/ready replicas가 모두 3입니다.
- 세 rank의 gpu_lab_training_rank_up이 1이고 step이 함께 증가합니다.
- loss는 초기값에서 안정적으로 감소하고 samples/s는 0이 아닙니다.
- AllReduce latency가 평상시 기준(이 synthetic lab에서는 대체로 0.5초 미만)에서 유지됩니다.
- gpu_lab_training_allreduce_errors_total 증가가 없고 restart counter가 변하지 않습니다.
- checkpoint step이 현재 step을 계속 따라가며 lag가 5 이하입니다.
- gpu_lab_training_straggler_active가 0입니다.
- Prometheus target이 UP이고 Grafana에 빈 패널이 없습니다.

## Drill 1: straggler

rank 2에 의도적인 지연을 넣습니다.

    gpu training inject straggler --rank 2 --delay 2s
    gpu training status
    gpu training logs --rank 2

예상 증상은 rank 2의 straggler_active=1, AllReduce latency 증가, 다른 rank의 step 진행 둔화입니다. Grafana의 Progress/Loss, AllReduce latency, Straggler active 패널과 GPULabTrainingStragglerActive 알림을 함께 확인합니다. 원인 범위를 worker rank 하나의 지연으로 좁히는 것이 학습 목표입니다.

## Drill 2: worker crash

rank 1 worker를 중단시킵니다.

    gpu training inject worker-crash --rank 1
    kubectl --context gpu-lab -n gpu-lab-demo get pod gpu-lab-training-1 -w
    gpu training status
    gpu training logs --rank 1

예상 증상은 rank 1의 일시적인 rank down, pod restart 증가, AllReduce error 또는 전체 progress stall입니다. kubectl describe pod의 Last State와 이벤트, gpu_lab_training_restarts_total, checkpoint step을 상관 분석합니다. 단순히 pod를 다시 시작하는 것과 checkpoint에서 학습을 재개하는 것을 구분하는 것이 목표입니다.

## 복구와 정리

실습 제어 상태를 정상화하고 checkpoint 기반 재개를 확인합니다.

    gpu training recover
    gpu training status
    gpu training logs --rank 1

다음 실습 전에 리소스와 제어 상태를 정리합니다.

    gpu training reset
    kubectl --context gpu-lab -n gpu-lab-demo get pods -l app.kubernetes.io/name=gpu-lab-training

reset은 이 실습에서 관리하는 training StatefulSet, Service, ConfigMap만 대상으로 합니다. 기존 GPU exporter와 monitoring stack은 제거하지 않습니다.

## 조사 순서와 학습 목표

1. Prometheus target과 rank_up으로 관측 자체가 살아 있는지 확인합니다.
2. 모든 rank의 step/loss를 비교해 한 rank만 느린지 전체가 멈췄는지 구분합니다.
3. AllReduce latency/error를 확인해 통신·rendezvous 문제를 계산 문제와 분리합니다.
4. restart와 checkpoint lag로 재시작이 안전한지 확인합니다.
5. pod 이벤트와 로그로 가설을 검증한 뒤 recover/reset으로 실습 상태를 닫습니다.

## InfiniBand/RDMA fabric 상관 분석

분산학습 실습은 [InfiniBand/RDMA Fabric 운영 실습](ib-fabric.md)과 함께 진행하면 통신 장애가 학습 진행으로 번지는 경로를 연습할 수 있습니다. 이 Lab의 fabric exporter는 HCA·port 상태, negotiated rate, symbol/link error, xmit congestion, RDMA retry/timeout을 synthetic metric으로 투영하고, worker는 다음 correlation metric을 노출합니다.

- gpu_lab_training_fabric_fault_active{mode}: 어떤 fabric 장애 주입 모드가 rank에 적용됐는지
- gpu_lab_training_fabric_delay_seconds: fabric fault가 추가한 synthetic 지연
- gpu_lab_training_fabric_errors_total, gpu_lab_training_fabric_retries_total: 통신 재시도와 오류 누적
- gpu_lab_training_allreduce_seconds, gpu_lab_training_step: collective latency와 실제 progress 영향

운영 조사 순서는 ibstat·ibstatus·ibv_devinfo와 fabric dashboard의 port health/state에서 시작해 negotiated rate, physical/link error, xmit wait/discard, RDMA retry/timeout을 확인한 뒤, 같은 시간 범위의 rank별 AllReduce latency와 step을 비교하는 것입니다. 예를 들어 ib-rate-degraded나 ib-congestion은 port가 up이어도 fabric delay와 AllReduce 시간이 증가하고, ib-link-down이나 rdma-retry-storm은 rank down·communication error·step stall로 이어질 수 있습니다.

    gpu scenario run ib-congestion
    gpu verify ib-congestion
    gpu ibstat --node gpu-lab-worker
    gpu ibstatus --node gpu-lab-worker
    gpu ibv_devinfo --node gpu-lab-worker -v
    gpu training status
    gpu metrics --query 'gpu_lab_training_fabric_delay_seconds'
    gpu metrics --query 'gpu_lab_training_allreduce_seconds'
    gpu metrics --query 'changes(gpu_lab_training_step[2m])'
    gpu scenario reset
    gpu training recover

Grafana의 GPU Lab InfiniBand / RDMA Fabric 대시보드와 GPU Lab Distributed Training 대시보드를 같은 시간 범위로 열어 fabric fault → AllReduce latency → progress stall의 순서를 확인합니다. ConfigMap projected volume 반영은 최대 180초가 걸릴 수 있으므로 장애 주입 후 bounded polling을 사용합니다. 자세한 다섯 가지 fabric scenario와 실제 장비 명령 대응은 [ib-fabric.md](ib-fabric.md)에 있습니다.

## 실제 분산학습과의 경계

이 저장소의 worker는 결정적인 synthetic CPU/network control-plane simulation입니다. 실제 CUDA kernel, NCCL collective, RDMA fabric, NVLink topology, GPU memory pressure 또는 GPU 성능을 측정하지 않습니다. latency 숫자를 실제 GPU cluster의 용량 계획이나 성능 비교에 사용하면 안 됩니다.

실제 multi-GPU training으로 확장하려면 CUDA-enabled image와 NVIDIA device plugin, GPU당 자원 요청, NCCL이 인식할 네트워크·topology 설정, RDMA/GDR 및 NVLink 검증, 실제 rendezvous(예: torchrun/elastic 또는 MPI operator), durable checkpoint 저장소, node/GPU telemetry와 production alert routing이 필요합니다. 그 환경에서는 driver·CUDA·NCCL 버전 호환성과 보안, 데이터 로딩·sharding, 장애 시 재시작 정책까지 별도로 검증해야 합니다.
