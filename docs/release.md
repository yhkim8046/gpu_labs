# Release Binary

GitHub Release에는 수강생용 `gpu`, 호환용 `gpu-lab`, 그리고 synthetic telemetry를 보여주는 `nvidia-smi` 호환 명령의 macOS, Linux, Windows 바이너리가 함께 업로드됩니다. GPU Lab 내부의 `nvidia-device-plugin`과 `dcgm-exporter`는 `gpu helm install`이 실제 Helm release로 배포하는 교육용 runtime image입니다.

## 설치

Release tag가 `v0.2.0`이면 archive 안의 버전 문자열은 `0.2.0`입니다. 자신의 OS와 CPU architecture에 맞는 asset을 선택합니다.

수강생에게는 수동 archive 선택보다 공식 설치 스크립트를 권장합니다. 스크립트가 최신 Release, OS, CPU architecture를 자동으로 선택하고 checksum 및 필수 바이너리를 검증합니다.

```bash
curl --fail --silent --show-error --location \
  --output gpu-lab-install.sh \
  https://raw.githubusercontent.com/yhkim8046/gpu_labs/main/scripts/install.sh
bash gpu-lab-install.sh
```

기본 설치 위치는 `/usr/local/bin`입니다. 권한이 없으면 `GPU_LAB_INSTALL_DIR="$HOME/.local/bin"`을 지정할 수 있습니다.

| 환경 | OS | architecture | archive |
| --- | --- | --- | --- |
| Apple Silicon Mac | `darwin` | `arm64` | `gpu-lab_0.2.0_darwin_arm64.tar.gz` |
| Intel Mac | `darwin` | `amd64` | `gpu-lab_0.2.0_darwin_amd64.tar.gz` |
| Linux PC/VM | `linux` | `amd64` 또는 `arm64` | `gpu-lab_0.2.0_linux_<arch>.tar.gz` |
| Windows | `windows` | `amd64` 또는 `arm64` | `gpu-lab_0.2.0_windows_<arch>.zip` |

### macOS / Linux

아래 예시는 Apple Silicon Mac입니다. Linux에서는 `OS=linux`, architecture가 다르면 `ARCH=amd64`로 바꿉니다.

```bash
VERSION=0.2.0
OS=darwin
ARCH=arm64
ASSET="gpu-lab_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/yhkim8046/gpu_labs/releases/download/v${VERSION}"

curl -fL -o "$ASSET" "${BASE_URL}/${ASSET}"
curl -fL -o checksums.txt "${BASE_URL}/checksums.txt"
grep "$ASSET" checksums.txt | shasum -a 256 -c -

tar -xzf "$ASSET"
mkdir -p "$HOME/.local/bin"
install -m 0755 gpu "$HOME/.local/bin/gpu"
install -m 0755 nvidia-smi "$HOME/.local/bin/nvidia-smi"
export PATH="$HOME/.local/bin:$PATH"

gpu version
```

새 shell에서도 사용하려면 `~/.zshrc` 또는 `~/.bashrc`에 다음을 추가합니다.

```bash
export PATH="$HOME/.local/bin:$PATH"
```

### Windows PowerShell

Windows native 환경에서는 `.zip` asset을 사용합니다. WSL2 안에서 실행한다면 Windows asset이 아니라 Linux asset을 설치합니다.

```powershell
$Version = "0.2.0"
$Arch = "amd64"
$Asset = "gpu-lab_${Version}_windows_${Arch}.zip"
$BaseUrl = "https://github.com/yhkim8046/gpu_labs/releases/download/v$Version"

Invoke-WebRequest "$BaseUrl/$Asset" -OutFile $Asset
Invoke-WebRequest "$BaseUrl/checksums.txt" -OutFile checksums.txt
$Expected = (Select-String -Path checksums.txt -Pattern $Asset).Line.Split()[0].ToLower()
$Actual = (Get-FileHash $Asset -Algorithm SHA256).Hash.ToLower()
if ($Expected -ne $Actual) { throw "checksum verification failed" }

Expand-Archive $Asset -DestinationPath .\gpu-lab-$Version -Force
New-Item -ItemType Directory -Force -Path "$HOME\bin" | Out-Null
Copy-Item ".\gpu-lab-$Version\gpu.exe" "$HOME\bin\gpu.exe" -Force
Copy-Item ".\gpu-lab-$Version\nvidia-smi.exe" "$HOME\bin\nvidia-smi.exe" -Force

& "$HOME\bin\gpu.exe" version
```

`$HOME\bin`을 Windows 사용자 `PATH`에 한 번 추가하면 이후에는 `gpu`만 입력할 수 있습니다.

## 업그레이드

새 release의 asset을 같은 위치에 다시 설치합니다. CLI 업그레이드는 기존 kind cluster, dedicated kubeconfig, scenario 상태를 삭제하지 않습니다.

업그레이드 후 다음을 확인합니다.

```bash
gpu version
gpu doctor
gpu status
```

강의는 사용하는 프로젝트 버전과 같은 release tag를 고정하는 것을 권장합니다.

### v0.1.x에서 v0.2.x로 업그레이드

v0.2.0부터 `create`는 component를 자동 설치하지 않습니다. 기존 v0.1.x cluster에는 자동 설치된 리소스가 남아 있으므로 단계별 설치 강의를 시작하기 전에 disposable cluster를 한 번 다시 만듭니다.

```bash
gpu destroy
gpu create
gpu helm install nvidia-device-plugin
gpu helm install dcgm-exporter
gpu helm install monitoring
```

## 제거

CLI 바이너리만 제거하려면 다음을 실행합니다. cluster와 kubeconfig는 별도 상태이므로 필요할 때 명시적으로 정리합니다.

```bash
gpu destroy  # cluster까지 제거할 때만 먼저 실행
rm -f "$HOME/.local/bin/gpu" "$HOME/.local/bin/gpu-lab" "$HOME/.local/bin/nvidia-smi"
```

Windows PowerShell에서는 다음을 실행합니다.

```powershell
Remove-Item "$HOME\bin\gpu.exe", "$HOME\bin\nvidia-smi.exe"
```

## Release 생성

`v*` tag push가 GitHub Actions release workflow를 시작합니다.

```bash
git tag -a v0.2.0 -m "gpu-lab v0.2.0"
git push origin v0.2.0
```

로컬에서 GitHub 업로드 없이 cross-platform artifact를 확인하려면 GoReleaser를 설치한 뒤 실행합니다.

```bash
make release-snapshot
```

결과물은 `dist/`에 생성되며 `checksums.txt`에는 SHA-256 checksum이 포함됩니다.

## Container runtime image

`v1.0.0` tag를 push하면 `.github/workflows/publish-image.yml`이 GHCR에 다음 이미지를 생성합니다.

```text
ghcr.io/yhkim8046/gpu-lab-runtime:1.0.0
```

이미지는 `linux/amd64`와 `linux/arm64` manifest를 함께 제공하며, tag에는 `latest`를 사용하지 않습니다. 강의에서는 CLI release version과 같은 image tag를 고정해야 합니다.

```bash
docker pull ghcr.io/yhkim8046/gpu-lab-runtime:1.0.0
docker buildx imagetools inspect ghcr.io/yhkim8046/gpu-lab-runtime:1.0.0
```

Release binary로 `gpu create`를 실행하면 CLI version과 같은 tag의 runtime image를 자동으로 pull한 뒤 kind node에 load합니다. component는 이후 `gpu helm install ...`로 설치합니다. `go run` 개발 모드는 기존처럼 local `gpu-lab:dev` image를 build합니다.

같은 tag에서 Helm chart publish workflow는 다음 OCI chart를 동일한 version으로 배포합니다.

```text
oci://ghcr.io/yhkim8046/gpu-lab-charts/nvidia-device-plugin
oci://ghcr.io/yhkim8046/gpu-lab-charts/dcgm-exporter
```

따라서 `gpu helm install dcgm-exporter`는 release에 내장된 manifest를 바로 apply하는 명령이 아니라, 공식 Helm이 versioned OCI chart를 pull하고 release를 생성하는 명령입니다. 개발자는 `GPU_LAB_COMPONENT_CHART_SOURCE=embedded`로 repository source의 chart를 직접 검증할 수 있습니다.

기본 runtime repository는 `ghcr.io/yhkim8046/gpu-lab-runtime`입니다. 프로젝트를 다른 GitHub owner로 fork했다면 다음 환경 변수를 자신의 GHCR 경로로 바꿉니다.

```bash
GPU_LAB_RUNTIME_IMAGE_REPOSITORY=ghcr.io/<github-owner>/gpu-lab-runtime gpu create
```

개발자가 release binary로 local image를 테스트하려면 다음처럼 지정합니다.

```bash
gpu create --local
```

처음 publish한 뒤 GitHub의 `Packages`에서 `gpu-lab-runtime` package를 repository에 연결하고 visibility를 `Public`으로 설정해야 수강생이 로그인 없이 pull할 수 있습니다. GitHub Container Registry public package는 anonymous pull을 지원합니다.
