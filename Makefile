.PHONY: build test fmt docker-build e2e

build:
	go build ./cmd/gpu-lab ./cmd/nvidia-device-plugin ./cmd/dcgm-exporter

test:
	go test ./...

fmt:
	gofmt -w cmd internal deploy scenarios

docker-build:
	docker build -t gpu-lab:dev .

e2e:
	./test/e2e/run.sh
