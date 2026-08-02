# Release Binary

GitHub Release에는 `gpu-lab` CLI의 macOS, Linux, Windows 바이너리가 업로드됩니다. GPU Lab 내부의 `nvidia-device-plugin`과 `dcgm-exporter`는 별도 사용자용 CLI가 아니라 `gpu-lab create`가 cluster에 배포하는 교육용 image입니다.

## 설치

Release tag가 `v0.1.0`이면 archive 안의 버전 문자열은 `0.1.0`입니다. 자신의 OS와 CPU architecture에 맞는 asset을 선택합니다.

| 환경 | OS | architecture | archive |
| --- | --- | --- | --- |
| Apple Silicon Mac | `darwin` | `arm64` | `gpu-lab_0.1.0_darwin_arm64.tar.gz` |
| Intel Mac | `darwin` | `amd64` | `gpu-lab_0.1.0_darwin_amd64.tar.gz` |
| Linux PC/VM | `linux` | `amd64` 또는 `arm64` | `gpu-lab_0.1.0_linux_<arch>.tar.gz` |
| Windows | `windows` | `amd64` 또는 `arm64` | `gpu-lab_0.1.0_windows_<arch>.zip` |

### macOS / Linux

아래 예시는 Apple Silicon Mac입니다. Linux에서는 `OS=linux`, architecture가 다르면 `ARCH=amd64`로 바꿉니다.

```bash
VERSION=0.1.0
OS=darwin
ARCH=arm64
ASSET="gpu-lab_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/gpu-lab/gpu-lab/releases/download/v${VERSION}"

curl -fL -o "$ASSET" "${BASE_URL}/${ASSET}"
curl -fL -o checksums.txt "${BASE_URL}/checksums.txt"
grep "$ASSET" checksums.txt | shasum -a 256 -c -

tar -xzf "$ASSET"
mkdir -p "$HOME/.local/bin"
install -m 0755 gpu-lab "$HOME/.local/bin/gpu-lab"
export PATH="$HOME/.local/bin:$PATH"

gpu-lab version
```

새 shell에서도 사용하려면 `~/.zshrc` 또는 `~/.bashrc`에 다음을 추가합니다.

```bash
export PATH="$HOME/.local/bin:$PATH"
```

### Windows PowerShell

Windows native 환경에서는 `.zip` asset을 사용합니다. WSL2 안에서 실행한다면 Windows asset이 아니라 Linux asset을 설치합니다.

```powershell
$Version = "0.1.0"
$Arch = "amd64"
$Asset = "gpu-lab_${Version}_windows_${Arch}.zip"
$BaseUrl = "https://github.com/gpu-lab/gpu-lab/releases/download/v$Version"

Invoke-WebRequest "$BaseUrl/$Asset" -OutFile $Asset
Invoke-WebRequest "$BaseUrl/checksums.txt" -OutFile checksums.txt
$Expected = (Select-String -Path checksums.txt -Pattern $Asset).Line.Split()[0].ToLower()
$Actual = (Get-FileHash $Asset -Algorithm SHA256).Hash.ToLower()
if ($Expected -ne $Actual) { throw "checksum verification failed" }

Expand-Archive $Asset -DestinationPath .\gpu-lab-$Version -Force
New-Item -ItemType Directory -Force -Path "$HOME\bin" | Out-Null
Copy-Item ".\gpu-lab-$Version\gpu-lab.exe" "$HOME\bin\gpu-lab.exe" -Force

& "$HOME\bin\gpu-lab.exe" version
```

`$HOME\bin`을 Windows 사용자 `PATH`에 한 번 추가하면 이후에는 `gpu-lab`만 입력할 수 있습니다.

## 업그레이드

새 release의 asset을 같은 위치에 다시 설치합니다. CLI 업그레이드는 기존 kind cluster, dedicated kubeconfig, scenario 상태를 삭제하지 않습니다.

업그레이드 후 다음을 확인합니다.

```bash
gpu-lab version
gpu-lab doctor
gpu-lab status
```

강의는 사용하는 프로젝트 버전과 같은 release tag를 고정하는 것을 권장합니다.

## 제거

CLI 바이너리만 제거하려면 다음을 실행합니다. cluster와 kubeconfig는 별도 상태이므로 필요할 때 명시적으로 정리합니다.

```bash
gpu-lab destroy  # cluster까지 제거할 때만 먼저 실행
rm -f "$HOME/.local/bin/gpu-lab"
```

Windows PowerShell에서는 다음을 실행합니다.

```powershell
Remove-Item "$HOME\bin\gpu-lab.exe"
```

## Release 생성

`v*` tag push가 GitHub Actions release workflow를 시작합니다.

```bash
git tag -a v0.1.0 -m "gpu-lab v0.1.0"
git push origin v0.1.0
```

로컬에서 GitHub 업로드 없이 cross-platform artifact를 확인하려면 GoReleaser를 설치한 뒤 실행합니다.

```bash
make release-snapshot
```

결과물은 `dist/`에 생성되며 `checksums.txt`에는 SHA-256 checksum이 포함됩니다.

## Container runtime image

`v1.0.0` tag를 push하면 `.github/workflows/publish-image.yml`이 GHCR에 다음 이미지를 생성합니다.

```text
ghcr.io/<github-owner>/gpu-lab-runtime:1.0.0
```

이미지는 `linux/amd64`와 `linux/arm64` manifest를 함께 제공하며, tag에는 `latest`를 사용하지 않습니다. 강의에서는 CLI release version과 같은 image tag를 고정해야 합니다.

```bash
docker pull ghcr.io/<github-owner>/gpu-lab-runtime:1.0.0
docker buildx imagetools inspect ghcr.io/<github-owner>/gpu-lab-runtime:1.0.0
```

Release binary로 `gpu-lab create`를 실행하면 CLI version과 같은 tag의 runtime image를 자동으로 pull한 뒤 kind node에 load합니다. `go run` 개발 모드는 기존처럼 local `gpu-lab:dev` image를 build합니다.

기본 runtime repository는 `ghcr.io/gpu-lab/gpu-lab-runtime`입니다. 프로젝트를 다른 GitHub owner로 fork했다면 다음 환경 변수를 자신의 GHCR 경로로 바꿉니다.

```bash
GPU_LAB_RUNTIME_IMAGE_REPOSITORY=ghcr.io/<github-owner>/gpu-lab-runtime gpu-lab create
```

개발자가 release binary로 local image를 테스트하려면 다음처럼 지정합니다.

```bash
GPU_LAB_IMAGE_SOURCE=local GPU_LAB_IMAGE=gpu-lab:dev gpu-lab create
```

처음 publish한 뒤 GitHub의 `Packages`에서 `gpu-lab-runtime` package를 repository에 연결하고 visibility를 `Public`으로 설정해야 수강생이 로그인 없이 pull할 수 있습니다. GitHub Container Registry public package는 anonymous pull을 지원합니다.
