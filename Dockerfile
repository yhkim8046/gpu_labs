FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/gpu-lab ./cmd/gpu-lab
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/fake-gpu-device-plugin ./cmd/fake-gpu-device-plugin
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/mock-gpu-exporter ./cmd/mock-gpu-exporter

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gpu-lab /usr/local/bin/gpu-lab
COPY --from=build /out/fake-gpu-device-plugin /usr/local/bin/fake-gpu-device-plugin
COPY --from=build /out/mock-gpu-exporter /usr/local/bin/mock-gpu-exporter
