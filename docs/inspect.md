# GPU 점검 (gpu inspect)

> 이 기능은 사용자가 설치한 도구를 호출하는 비공식 GPU Lab 기능입니다. NVIDIA와 제휴하거나 인증받은 도구가 아닙니다.

NVIDIA 소스나 바이너리를 복제하지 않습니다. GPU 점검은 명령을 실행하는 호스트의 로컬 GPU를 대상으로 하며, Kubernetes worker의 원격 하드웨어를 점검하는 기능이 아닙니다.

## 전제 조건

- `real` 모드는 `nvidia-smi`가 반드시 설치되어 있어야 합니다.
- `dcgmi`는 선택 항목이며, 없어도 GPU 점검을 계속할 수 있습니다.
- `synthetic` 모드는 두 제조사 도구가 없어도 실행할 수 있습니다.

개발용 Mac에는 GPU와 드라이버가 없습니다. 드라이버가 없으면 `unavailable`로 보고하고 `pass`로 판정하지 않습니다. 예제로 만든 샘플 출력은 하드웨어 검증이 아닙니다.

## 기본 사용법

```bash
gpu inspect                  # 기본값 auto
gpu inspect --mode real      # 설치된 nvidia-smi만 사용, 없으면 unavailable
gpu inspect --mode synthetic # 실험실 근거만 사용
gpu inspect --json           # 기계 판독 출력
gpu inspect --timeout 10s    # 호출 시간 제한
```

## 모드 동작

- `auto`(기본값): 번들된 `nvidia-smi` shim을 제외하고 PATH의 실제 `nvidia-smi`를 찾습니다. 있으면 `nvidia-smi -q -x`를 호출합니다. 없으면 실험실의 긍정적 근거를 사용하고, 둘 다 없으면 `unavailable`입니다. 실제 `nvidia-smi` 탐색이나 호출이 실패하면 synthetic 결과로 대체하지 않습니다.
- `real`: 설치된 `nvidia-smi`를 조회하고, 선택적으로 `dcgmi`의 장치 목록도 확인합니다. 필수 도구가 없거나 조회가 실패하면 `unavailable`이며 synthetic으로 대체하지 않습니다.
- `synthetic`: 실험실 근거만 사용하며 하드웨어 검증을 주장하지 않습니다.

모드 선택은 전역 옵션과 환경 변수로 NVIDIA-SMI 호환 명령에도 같은 방식으로 적용됩니다.

```bash
gpu --mode real nvidia-smi -q
GPU_LAB_MODE=real gpu nvidia-smi -q
gpu --mode real dcgmi discovery -l
```

`GPU_LAB_NVIDIA_SMI=/absolute/path/nvidia-smi`를 지정하면 PATH 검색 대신 공식 `nvidia-smi` 경로를 직접 선택할 수 있습니다.

## real 경로

- 고정 호출: `nvidia-smi -q -x`
- `real`로 선택된 `gpu nvidia-smi`는 NVIDIA-SMI 인자를 변경하지 않고 설치된 `nvidia-smi`에 그대로 전달합니다. `gpu dcgmi`, `gpu ibstat`, `gpu ibstatus`, `gpu ibv_devinfo`도 실제 모드에서 설치된 해당 도구에 인자를 그대로 전달합니다.
- `dcgmi`는 선택적으로 포함되며 `dcgmi discovery -l`만 호출합니다. `reset`, `config`, `diag`를 자동으로 실행하지 않습니다.
- `dcgmi`가 없어도 `nvidia-smi` 기반 점검을 계속합니다.

```bash
gpu nvidia-smi --query-gpu=temperature.gpu,memory.used,utilization.gpu --format=csv,noheader,nounits
```

## synthetic 경로

- GPU Lab의 독립 `nvidia-smi` 바이너리는 항상 모의 명령입니다. `gpu nvidia-smi`는 선택된 모드에 따라 공식 도구를 호출하거나 내부 모의 명령을 사용합니다. `auto`에서도 공식 도구가 없고 실험실 근거가 확인되면 모의 경로를 사용합니다.
- 모의 환경에서도 `gpu` 뒤에 같은 명령과 지원 옵션을 붙입니다. `dcgmi`는 모의 환경에서 지원하지 않으며 실제 DCGM 진단 결과를 만들어 내지 않습니다.
- synthetic은 `--query-gpu`, `--format=csv,noheader,nounits`, `--list-gpus`, `--node` 부분집합만 지원합니다. 모든 제조사 옵션을 동일하게 지원하지는 않습니다.
- 값은 결정적인 synthetic 실험실 근거이며 실제 드라이버, CUDA, RDMA 측정이 아닙니다.

```bash
GPU_LAB_MODE=synthetic gpu nvidia-smi --list-gpus
```

## 상태 의미

판정 상태는 `pass`, `warn`, `fail`, `unavailable`, `unsupported`입니다.

- 전체 집계 ECC 수치가 양(+)의 값이어도 과거 이력의 근거일 뿐 즉각적인 장애 판정이 아닙니다.
- `N/A` 또는 미지원 필드는 `unsupported`이고, 값이 없거나 오래된 값은 `unavailable`입니다. 없는 값을 `0`이나 `pass`로 대체하지 않습니다.

## 출처 표시와 비공식 안내

- 모든 점검 결과에 값의 출처(`real` 또는 `synthetic`)와 비공식 표시를 포함합니다.
- 명령 전달 안내는 stderr에 출력해 stdout의 CSV를 보존합니다. 점검 JSON은 비공식 정보를 메타데이터로 포함하고, 점검 텍스트는 머리말에 표시합니다.
- 검증을 실패한 결과를 `pass`로 보고하지 않고, synthetic 결과를 real처럼 가장하지 않습니다.

## 공식 참고 문서

- [NVIDIA-SMI 사용자 가이드](https://docs.nvidia.com/deploy/nvidia-smi/index.html)
- [DCGM 사용자 가이드 기능 개요](https://docs.nvidia.com/datacenter/dcgm/latest/user-guide/feature-overview.html)
