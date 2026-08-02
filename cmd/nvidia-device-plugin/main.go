package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/gpu-lab/gpu-lab/internal/deviceplugin"
)

func main() {
	resourceName := envOr("RESOURCE_NAME", "nvidia.com/gpu")
	nodeName := envOr("NODE_NAME", "unknown-node")
	gpuCount := envInt("GPU_COUNT", 8)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("nvidia-device-plugin node=%s resource=%s gpu_count=%d", nodeName, resourceName, gpuCount)
	if err := deviceplugin.New(resourceName, nodeName, gpuCount).Serve(ctx); err != nil {
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
