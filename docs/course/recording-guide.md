# GPU Lab 강사 촬영 가이드 — v0.3.0

## 촬영 목표와 시작 조건

강의의 목표는 GPU가 없는 환경에서 Kubernetes GPU 리소스 등록·스케줄링, 모니터링, 장애 진단과 복구 판정을 실습하는 것입니다. GPU/IB 지표는 합성이며, 학습 worker는 CPU 계산과 HTTP AllReduce를 사용합니다. 실제 CUDA, NCCL, RDMA 성능 측정으로 소개하지 않습니다.

촬영과 수강생 배포는 같은 `v0.3.0`으로 고정합니다. [Release 페이지](https://github.com/yhkim8046/gpu_labs/releases/tag/v0.3.0)에 파일이 공개되고, 해당 tag의 Release workflow에서 Linux/macOS archive smoke와 설치 기반 E2E가 성공한 뒤 촬영을 시작합니다. PR CI나 snapshot 성공은 이 단계와 별개입니다.

사용할 자료:

- [설치·업그레이드](../release.md), [수강생 가이드](../student-guide.md)
- [과정·과제·평가표](README.md), [장애 진단 보고서 양식](templates/incident-report-template.md)
- [분산학습](../distributed-training.md), [IB/RDMA](../ib-fabric.md)

## 1. 촬영 전용 환경 준비

[지원 버전 표](../support-matrix.md)에 맞춰 Docker Engine/Desktop을 실행하고 kind, kubectl, Helm을 설치합니다. release CLI를 사용하는 수강생에게 Go 설치는 필요하지 않습니다. 기존 작업이 없는 촬영 전용 로컬 환경에서 아래 명령을 실행합니다. 운영·공유 클러스터는 촬영이나 검증 대상으로 사용하지 않습니다.

```bash
curl --fail --silent --show-error --location \
  --output gpu-lab-install.sh \
  https://raw.githubusercontent.com/yhkim8046/gpu_labs/v0.3.0/scripts/install.sh
GPU_LAB_VERSION=v0.3.0 GPU_LAB_INSTALL_DIR="$HOME/.local/bin" bash gpu-lab-install.sh
export PATH="$HOME/.local/bin:$PATH"

gpu version
gpu doctor
gpu scenario list
```

완료 조건: `gpu version`이 `0.3.0`과 release commit을 출력하고, 필요한 도구가 준비됐으며 기본 시나리오 목록이 22개입니다. Windows native CLI 설치는 [설치 문서](../release.md)를 따르며, 이 가이드의 Bash 명령은 macOS/Linux/WSL2 기준입니다.

촬영용 클러스터를 새로 구성합니다. 기존 동명 클러스터를 임의로 삭제하지 않습니다.

```bash
gpu create --registry
gpu helm install nvidia-device-plugin
gpu helm install dcgm-exporter
gpu helm install monitoring
gpu helm list --all-namespaces
gpu verify normal
kubectl --context gpu-lab get nodes -o wide
kubectl --context gpu-lab get pods -A
```

완료 조건: control-plane 1개와 worker 3개가 Ready이고, worker별 `nvidia.com/gpu=8`로 총 24개가 등록되며, 정상 상태 검증이 통과합니다. 학습 실습 전에는 training worker를 띄우지 않습니다. 학습 Pod의 GPU 예약이 스케줄링·단편화 실습 결과에 섞이는 것을 막기 위해서입니다.

다른 터미널에서 Grafana를 엽니다.

```bash
gpu dashboard --port 3000
```

표시되는 localhost 주소로 접속하고 GPU, Distributed Training, InfiniBand/RDMA 대시보드를 준비합니다. port-forward는 그 터미널에서 유지하며, 종료는 Ctrl-C입니다. 인증 방법은 [Getting Started](../getting-started.md)를 사용합니다.

## 2. 첫 영상은 10~15분 파일럿으로 촬영

처음부터 전체 과정을 녹화하지 않고, 준비된 정상 환경에서 단편화 실습 하나를 끝까지 촬영합니다. 터미널 글자 크기와 대시보드 가독성, 음성, 대기 구간 편집을 이 영상으로 점검합니다.

1. 문제 제시: “총 GPU 여유는 있는데 왜 2-GPU Pod가 Pending인가?”
2. 관측: 정상 토폴로지와 노드별 예약을 확인합니다.
3. 주입: 아래 시나리오를 실행합니다.
4. 진단: Pending 이벤트와 노드별 요청량으로 가설을 설명합니다.
5. 조치·확인: 시나리오를 정리하고 정상 상태를 별도로 검증합니다.

```bash
gpu verify normal
gpu scenario inspect gpu-fragmentation
gpu scenario run gpu-fragmentation
gpu verify gpu-fragmentation
kubectl --context gpu-lab -n gpu-lab-demo get pods -o wide
kubectl --context gpu-lab -n gpu-lab-demo describe pod gpu-lab-fragmentation-target
kubectl --context gpu-lab describe node gpu-lab-worker

gpu scenario reset
gpu verify normal
```

예상 결과: 세 worker에 각각 7 GPU를 요청하는 Pod가 올라가고, 2 GPU를 요청하는 target은 Pending입니다. 남은 GPU는 전체 3개이지만 각 노드에 1개씩이므로 어느 노드도 2개 요청을 수용하지 못합니다. 이때 `gpu verify gpu-fragmentation`의 PASS는 장애 재현 성공이며 정상 복구 성공이 아닙니다. node allocatable은 잔여 free GPU가 아니므로 Pod requests도 함께 봅니다.

## 3. 본 촬영 순서

아래 분량은 녹화 편집안입니다. 과정 문서의 90~120분 캡스톤 시간은 수강생 실습·보고서 작성 시간을 포함하므로 영상 분량과 구분합니다. 각 모듈을 5~12분 클립으로 나눕니다.

| 순서 | 모듈 | 영상 목표 | 화면에서 확인할 결과 |
| --- | --- | --- | --- |
| 0 | 소개와 합성 환경의 경계 | 5분 | 실제 하드웨어 측정과 학습 범위 구분 |
| 1 | 설치와 환경 점검 | 15분 | 버전 고정, doctor, 4개 kind 노드 |
| 2 | Device Plugin → Exporter → Monitoring | 20분 | GPU 리소스 등록, exporter 3개, 대시보드 |
| 3 | 스케줄링·단편화 | 20분 | Pending 이유와 정상 복구를 서로 검증 |
| 4 | GPU 장애·thermal timeline | 20분 | baseline → warning → critical → recovery |
| 5 | 학습 지연·worker/coordinator 재시작 | 25분 | restart 증가, 모든 rank의 step·checkpoint 재증가 |
| 6 | IB/RDMA 장애와 학습 영향 | 30분 | 설정 수신, 장애 영향, 정상화, 진행 재개 |
| 7 | 종합 과제와 보고서 | 15분 | 관측 → 가설 → 조치 → 복구 증거 제출 |

모든 22개 시나리오를 같은 깊이로 설명할 필요는 없습니다. 단편화, thermal, worker crash, IB link-down을 대표 시연으로 사용하고 나머지는 변형 과제로 연결합니다. 캡스톤 1~4의 상세 명령과 평가 기준은 [과정 문서](README.md)를 따릅니다.

## 4. 열·학습·IB 모듈의 핵심 시연

열 단계는 다음과 같이 시작합니다.

```bash
gpu scenario run thermal-escalation
gpu verify thermal-escalation
```

공통 시작 시각을 기준으로 0초 45°C, 30초 82°C, 60초 96°C와 thermal throttle, 120초 45°C recovery, 180초 자동 만료를 관측합니다. 시작 시각과 phase를 함께 보여 줍니다. 자동 전이를 강사의 조치 성과로 설명하지 않습니다. 모듈을 끝낼 때 `gpu scenario reset` 후 `gpu verify normal`로 정리합니다.

학습은 스케줄링·열 실습을 정리한 뒤 시작합니다.

```bash
gpu training run --workers 3 --wait
gpu training status
gpu training inject worker-crash --rank 1
kubectl --context gpu-lab -n gpu-lab-demo get pod gpu-lab-training-1 -w
```

재시작을 확인하면 watch를 Ctrl-C로 끝내고 다음을 실행합니다.

```bash
gpu training recover
gpu training status
gpu training logs --rank 1
```

10초 이상 간격을 두고 status를 두 번 확인해 모든 rank의 step과 checkpoint가 다시 증가하는 장면을 남깁니다. rank 0도 같은 방식으로 시연하면 coordinator 교체 후 전체 재합류를 설명할 수 있습니다. 체크포인트는 Pod의 emptyDir에 있어 컨테이너 재시작과 Pod 삭제를 같은 복구로 설명하지 않습니다.

IB link-down은 학습 worker 3개가 진행 중인 상태에서 시연합니다.

```bash
gpu scenario run ib-link-down
gpu verify ib-link-down
gpu metrics --query 'count(gpu_lab_training_fabric_fault_active{namespace="gpu-lab-demo",mode="down"})'
gpu ibstat --node gpu-lab-worker2
gpu training status
```

exporter와 worker의 설정 수신은 서로 독립입니다. 위 mode 질의가 3이 될 때까지 확인하고, targeted fault와 step 정지를 관측합니다. projected ConfigMap 전달에는 실습의 최대 180초 대기 예산을 사용합니다. 명령이 끝난 즉시 장애가 반영됐다고 말하지 않습니다.

```bash
gpu scenario reset
gpu training recover
gpu metrics --query 'count(gpu_lab_training_fabric_fault_active{namespace="gpu-lab-demo",mode="normal"} == 0)'
```

normal mode가 3인 것을 확인한 뒤 복구를 판정합니다.

```bash
gpu verify --recovery ib-link-down
gpu training status
```

환경 게이지 정상화와 학습 진행 재개는 별도 증거로 남깁니다. 누적 오류·retry 카운터를 0으로 만드는 것은 복구 조건이 아닙니다. 촬영을 마친 뒤 촬영용 학습 작업만 정리하려면 `gpu training reset`을 사용합니다.

## 5. 매 클립의 완료 조건

- 촬영 시작 전에 시나리오·training 상태와 버전을 기록합니다.
- 한 클립은 질문 하나와 관측 → 가설 → 조치 → 검증의 흐름 하나를 담습니다.
- 터미널 명령, 기대 출력, 실제 출력, 해석을 구분합니다.
- 장애 상태의 verify PASS와 복구 상태의 PASS를 다른 장면으로 남깁니다.
- 대기 구간은 편집하되 실제 경과와 설정 수신을 생략해서 즉시 반영된 것처럼 만들지 않습니다.
- 같은 버전으로 한 번 더 재현한 뒤 본 촬영을 시작합니다. 운영·공유 환경에서 리허설하지 않습니다.

오늘 시작할 순서는 release 검증 확인 → 촬영 전용 환경 설치 → 단편화 파일럿 한 편 → 본 촬영 목록 확정입니다.
