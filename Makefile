.PHONY: build test fmt docker-build

build:
	go build ./cmd/gpu-lab ./cmd/fake-gpu-device-plugin ./cmd/mock-gpu-exporter

test:
	go test ./...

fmt:
	gofmt -w cmd internal deploy scenarios

docker-build:
	docker build -t gpu-lab:dev .
