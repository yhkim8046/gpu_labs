.PHONY: build test fmt docker-build dev-create e2e e2e-training e2e-ib release-snapshot

VERSION_PACKAGE := github.com/gpu-lab/gpu-lab/internal/version
VERSION ?= dev
COMMIT ?= unknown
BUILD_DATE ?= unknown
LDFLAGS ?= -s -w -X $(VERSION_PACKAGE).Version=$(VERSION) -X $(VERSION_PACKAGE).Commit=$(COMMIT) -X $(VERSION_PACKAGE).BuildDate=$(BUILD_DATE)

build:
	go build -trimpath -ldflags="$(LDFLAGS)" ./cmd/gpu-lab ./cmd/nvidia-smi ./cmd/ibstat ./cmd/ibstatus ./cmd/ibv-devinfo ./cmd/nvidia-device-plugin ./cmd/dcgm-exporter ./cmd/training-worker

test:
	go test ./...

fmt:
	gofmt -w cmd internal deploy scenarios

docker-build:
	docker build -t gpu-lab:dev .

dev-create:
	GPU_LAB_IMAGE_SOURCE=local go run ./cmd/gpu-lab create --local

e2e:
	./test/e2e/run.sh

e2e-training:
	./test/e2e/training.sh

e2e-ib:
	./test/e2e/ib.sh

release-snapshot:
	goreleaser release --snapshot --clean
