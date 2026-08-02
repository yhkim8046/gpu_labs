# Compile on the native BuildKit platform and cross-compile the static Go
# binaries. This keeps arm64 builds fast on amd64 GitHub runners and avoids
# running the Go compiler through QEMU.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -X github.com/gpu-lab/gpu-lab/internal/version.Version=${VERSION} -X github.com/gpu-lab/gpu-lab/internal/version.Commit=${COMMIT} -X github.com/gpu-lab/gpu-lab/internal/version.BuildDate=${BUILD_DATE}" -o /out/gpu-lab ./cmd/gpu-lab
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/nvidia-device-plugin ./cmd/nvidia-device-plugin
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/dcgm-exporter ./cmd/dcgm-exporter

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="gpu-lab runtime" \
      org.opencontainers.image.description="Synthetic Kubernetes GPU infrastructure lab runtime" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/gpu-lab /usr/local/bin/gpu-lab
COPY --from=build /out/nvidia-device-plugin /usr/local/bin/nvidia-device-plugin
COPY --from=build /out/dcgm-exporter /usr/local/bin/dcgm-exporter
COPY LICENSE /licenses/gpu-lab/LICENSE
