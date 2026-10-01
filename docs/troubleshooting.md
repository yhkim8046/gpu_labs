# Troubleshooting — 설치·클러스터·관측 편향

대상: gpu-lab 합성(lab) 환경의 설치·생성·관측 실패. GPU 하드웨어 자체의 장애는 다루지 않는다(실제 환경에서 모을 증거의 초동 절차만 [9번](#9-실제-gpu-증거-초동-수집)에 있다).
여기의 모든 자원 수치·권장치는 교육 권장값이다. lab 수치가 실제 환경에서 똑같이 통한다는 보증은 없고, 복구 절차를 실제 환경에 적용하기 전에는 그 환경에서 먼저 확인한다.

## 1. 설치 도구와 gpu doctor

`gpu doctor`는 tool의 존재와 버전을 확인한다. 어떤 tool을 보고 어떤 상태로 출력하는지는 doctor 구현이 완성되면 그 결과를 기준으로 이 절을 갱신한다(구현 담당과 정합 대기). 아래 표는 실패 유형별 판단 구조이며, tool 나열이 바뀌어도 순서는 같다: tool 확인 → daemon/cluster 상태 확인 → 명령 별개의 확인.

```bash
gpu doctor
which docker kind kubectl helm gpu
docker version --format '{{.Client.Version}}'
kind version && kubectl version --client=true --output=yaml && helm version --short
```

| 증상 | 확인 | 복구 |
| --- | --- | --- |
| tool이 missing으로 나옴 | PATH·설치 여부, Docker는 데몬 기동 여부(macOS는 Desktop, Linux는 service) | `command -v <tool>`과 daemon 실행 확인을 먼저 한다 |
| tool은 찾았는데 버젼 출력이 실패 | PATH·권한·네트워크 실패 | 각 tool 명령을 직접 실행해 에러를 본다 |
| tool 확인은 통과했는데 `gpu create` 실패 | daemon 건간성 등 tool 외 요인 | 2번으로 |
| install.sh checksum 실패 | 미러/네트워크 중간변경 | Release 페이지에서 직접 대조 후 재수. 우회 설치 금지 |

## 2. Docker daemon과 자원

```bash
docker info --format '{{.ServerVersion}} / {{.OperatingSystem}} / storage={{.Driver}}'
docker info | grep -i 'total memory\|total space\|cpus'
docker context ls
```

- 데몬 미기동(`Cannot connect to the Docker daemon`): macOS/Windows는 Desktop 기동, Linux는 `systemctl`로 데몬 상태 확인(조치 전 담당자 확인).
- kind 노드 컨테이너가 기동하지 않음: 디스크·메모리 고갈이 흔하다. `docker system df`로 사용량 확인.
- 교육 권장값(측정 근거 없음, 실환경 요구사항 아님): Docker VM에 여유의 CPU·메모리·디스크를 두고 시작한다. 보장되는 최소 수치는 없고, `docker info`와 `docker system df`가 노출하는 직접 관측치를 기준으로 판단한다.
- 정리 조치(`docker system prune` 등)은 다른 작업의 이미지·container를 날릴 수 있으므로 기본 절차로 권하지 않는다. 필요한 경우 대상 열거를 명시한 후 실행한다.

## 3. kind 클러스터 생성과 중복

```bash
gpu status
kind get clusters
docker ps --filter name=gpu-lab
```

| 증상 | 원인 후보 | 복구 |
| --- | --- | --- |
| `cluster "gpu-lab" already exists` | 이전 실습 잔여 | `gpu destroy` 후 `gpu create`. 파괴가 불가하면 새 실습은 다른 클러스터로 구분하는 것을 권장 |
| control-plane 대기 타임아웃 | 2번의 자원 부족, proxy/프락시 설정 | `docker logs <control-plane container>` 확인 후 재시작 |
| 이미지 pull 실패 | 네트웨어나 레지스트리 정책 | `docker pull <image>` 직접 실로 재현. 이미지 소스는 help의 `GPU_LAB_IMAGE_*` 환경 변수 문서 참조 |
| 노드 4개가 아니라 3개 | 일부 워커 생성 실패 | `docker ps -a`와 노드 로그. kind는 control-plane 1 + worker 3 = 4 컨테이너가 기본값(manifest `deploy/kind/cluster.yaml` 기준) |

## 4. kubectl context

```bash
gpu context list
gpu context use gpu-lab
kubectl --context gpu-lab get nodes
kubectl config view /contexts
```

- `This context does not exist`: lab kubeconfig가 아직 생성되지 않음 → `gpu create` 또는 `gpu context setup` 실행 후 재확인.
- 기본 cluster에 다른 클러스터가 섞여 실습을 건드리지 않도록, 실습 중에는 `--context gpu-lab`(또는 `gpu context use`)를 고정한다.
- 실습이 끝나고 정리할 때의 순서는 destroy → context 확인이다. context만 지우고 컨테이너를 남기지 않는다.

## 5. Helm OCI pull

```bash
gpu helm catalog
gpu helm install nvidia-device-plugin
gpu helm install dcgm-exporter
gpu helm install monitoring
```

- `gpu helm install <component>`는 기본값으로 OCI 레지스트리(`oci://` 형태의 chart 소스)를 사용한다. 사내 프락시·오프라인에서는 `GPU_LAB_COMPONENT_CHART_SOURCE=embedded` 또는 사내 레지스트리로 repository를 재지정할 수 있다(변수명은 `gpu help`의 Image environment 섹션 기준).
- `manifest unknown with namespace ...`: 레지스트리 접근·인증 또는 네트워크 실패일 수 있으므로 `helm pull <oci-url>`을 직접 실해 에러 원문을 본다.
- chart 버젼 고정이 필요하면 강사 공지 값으로 `--version`을 붙인다. `latest`에 의존해 강의판과 실습판이 갈리지 않게 한다.
- unistall 후 재설치는 `gpu helm uninstall <component>` → 같은 install 명령. 리소스 강제 삭제를 요구하는 절차는 이 문서에 없다.

## 6. 3×8인지 직접 조회

`gpu-lab` 토폴로지는 조회로 판정한다. "3×8"은 이 리포 manifest와 device-plugin 기본값(교육 권장값)이고, 실 클러스터를 대변하지 않는다.

```bash
kubectl --context gpu-lab get nodes -o wide
kubectl --context gpu-lab get nodes \
  -o custom-columns=NAME:.metadata.name,GPU:.status.capacity.nvidia\\.com/gpu,GPU-ALLOC:.status.allocatable.nvidia\\.com/gpu
gpu metrics --query 'gpu_lab_node_gpu_capacity'
gpu metrics --query 'sum(gpu_lab_gpu_allocated) by (node)'
```

- control-plane은 GPU가 없는 CPU 노드, worker 3개 × 각 8 = 24가 기본 기대치다.
- capacity가 비어 있거나 0이면 device plugin 미설치/taint 후보: `gpu helm install nvidia-device-plugin` 후 Pod 상태를 본다.
- allocatable만 0이면 다른 워크로드가 예약한 상황(시나리오 잔여) 후보: `gpu scenario reset`으로 되돌릴 수 있다.

## 7. dashboard/metrics port-forward 충돌

`gpu dashboard`는 3000(가변), `gpu metrics`는 매 쿼리마다 자체적으로 Prometheus에 임시 포트를 연다. 같은 로컬 포트를 다른 터미널·앱이 점유하면 bind 실패가 난다.

```bash
gpu dashboard --port 3100          # 충돌 시 포트를 바꿔 재실행
lsof -nP -i :3000                        # macOS/Linux: 포트를 점유한 프로세스 확인
ss -ltn | grep 3000                      # Linux 대체
kubectl --context gpu-lab get svc -n gpu-lab-monitoring
```

- 이전 세션에서 Ctrl-C를 누르지 않은 port-forward가 잔여한 경우가 가장 흔하다. 해당 터미널에서 종료한다.
- `gpu metrics`가 "connection refused"라면 monitoring component 미설치일 수 있다(5번). dashboard와 메트릭 소스는 별개의 release다.
- `gpu dashboard`는 127.0.0.1 로컬 포워드를 유지한다. 커스 절차에서 0.0.0.0 바인딩이나 방화벽 개방은 필요하지 않다.

## 8. OS 별 차이: WSL2 / macOS / Linux

| 항목 | WSL2 | macOS | Linux |
| --- | --- | --- | --- |
| Docker | Docker Desktop + WSL2 integration 또는 WSL2 내 engine. Windows 자산이 아니라 WSL2(Linux) 자산으로 설치 | Docker Desktop이 VM에서 자원을 격리 | 호스트의 docker 데몬 |
| 자원 한계 | VM 설정에 종속 → 2번의 `docker info` 직접 관측 | VM 설정에 종속 | 호스트 직접 |
| localhost 포워드 | Windows 브라우저에서 WSL2 localhost로 대부분 접속 가능하나 미작 시 Windows↔WSL2 포트 우회 확인 | 정상 | 정상 |
| PATH | `$HOME/bin`을 Windows 사용자 PATH에 추가. WSL2 내부 PATH는 별도 | `/usr/local/bin` 기본, 권한 없으면 사용자 디렉터리 지정 | 설치 위치 확인 후 `gpu version` |
| filesystem | Windows 마운트(예 `/mnt/c`)의 IO는 느리므로 데이터 로컬화 권장 | — | — |
| 시그널/종료 | Ctrl-C로 port-forward 종료 확인 필수 | 동일 | 동일 |

- Windows native PowerShell과 WSL2 bash에서 서로 다른 GPU 바이너리를 설치해 혼동한 사례가 강사 Q&A에서 가장 잦다. `gpu version`을 실행한 셸을 기록한다.

## 9. 실제 GPU 증거 초동 수집

lab은 driver/CUDA/RDMA 하드웨어를 재현하지 않는다. 실환경 사고에서는 아래를 교차로 모은다(수집 절차는 [course/templates/real-cluster-evidence-template.md](course/templates/real-cluster-evidence-template.md)).

- kernel의 `NVRM`/Xid 라인과 시각 → `nvidia-smi -q` 출력 → DCGM health/bug report를 교차한다. Xid 값만으로는 root cause를 단정하지 않는다.
- IB는 `ibstat`/`ibstatus`로 State=Active, Physical State=LinkUp, Link layer, expected Rate를 먼저 보되, 이것만으로 NCCL/통신 성공을 증명하지 않는다. 통신성 판정은 rank 간 all-reduce 성공과 retry/timeout counter의 별개 증거가 필요하다.
- GPU reset, drain, 포트 shutdown, firmware reload은 어떤 절차에서도 기본값으로 실행하지 않는다. 변경 승인과 롤백 계획 이후에만 검토한다.
- 공식 절차 참조(실환경 기준, lab과 신뢰 등급이 다름): [NCCL networking troubleshooting](https://docs.nvidia.com/deeplearning/nccl/user-guide/docs/troubleshooting/networking_troubleshooting.html), [Xid 소개](https://docs.nvidia.com/deploy/xid-errors/introduction.html), [Xid 작업 흐름](https://docs.nvidia.com/deploy/xid-errors/working-with-xid-errors.html).

## 10. 그래도 실패하면

증거 번들을 남기고 문의한다: `gpu version`·`gpu doctor`·`gpu status` 원본, 실패한 명령과 에러 원문, `docker info`와 `kind get clusters` 출력, 재현 순서. 개인정보·사내 식별자는 제거한다.
