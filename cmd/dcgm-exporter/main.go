package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/gpu-lab/gpu-lab/internal/exporter"
)

func main() {
	nodeName := envOr("NODE_NAME", "unknown-node")
	gpuCount := envInt("GPU_COUNT", exporter.DefaultGPUCount)
	addr := envOr("LISTEN_ADDRESS", ":9400")
	namespace := envOr("NAMESPACE", "gpu-lab-system")

	model := exporter.NewModel(nodeName, gpuCount)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go exporter.WatchConfigMap(ctx, model, namespace)

	log.Printf("dcgm-exporter node=%s gpu_count=%d listen=%s", nodeName, gpuCount, addr)
	if err := exporter.NewServer(model, addr).ListenAndServe(ctx); err != nil {
		log.Fatal(err)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
