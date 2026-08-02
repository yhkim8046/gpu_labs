.PHONY: build test fmt docker-build e2e release-snapshot

VERSION_PACKAGE := github.com/gpu-lab/gpu-lab/internal/version
VERSION ?= dev
COMMIT ?= unknown
BUILD_DATE ?= unknown
LDFLAGS ?= -s -w -X $(VERSION_PACKAGE).Version=$(VERSION) -X $(VERSION_PACKAGE).Commit=$(COMMIT) -X $(VERSION_PACKAGE).BuildDate=$(BUILD_DATE)

build:
	go build -trimpath -ldflags="$(LDFLAGS)" ./cmd/gpu-lab ./cmd/nvidia-device-plugin ./cmd/dcgm-exporter

test:
	go test ./...

fmt:
	gofmt -w cmd internal deploy scenarios

docker-build:
	docker build -t gpu-lab:dev .

e2e:
	./test/e2e/run.sh

release-snapshot:
	goreleaser release --snapshot --clean
