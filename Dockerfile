FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/gpu-lab ./cmd/gpu-lab
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/nvidia-device-plugin ./cmd/nvidia-device-plugin
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/dcgm-exporter ./cmd/dcgm-exporter

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gpu-lab /usr/local/bin/gpu-lab
COPY --from=build /out/nvidia-device-plugin /usr/local/bin/nvidia-device-plugin
COPY --from=build /out/dcgm-exporter /usr/local/bin/dcgm-exporter
