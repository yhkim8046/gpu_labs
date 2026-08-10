package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/gpu-lab/gpu-lab/internal/training"
)

func main() {
	c, err := training.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	w := training.NewWorker(c)
	mux := http.NewServeMux()
	mux.Handle("/metrics", w.Metrics.Handler(map[string]string{"rank": fmt.Sprint(c.Rank), "job": c.Job, "node": c.Node, "pod": c.Pod}))
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(rw http.ResponseWriter, _ *http.Request) {
		if !w.Metrics.IsUp() {
			http.Error(rw, "worker is initializing", http.StatusServiceUnavailable)
			return
		}
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ready\n"))
	})
	go func() {
		if err := http.ListenAndServe(fmt.Sprintf(":%d", c.MetricsPort), mux); err != nil {
			log.Printf("metrics server: %v", err)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := w.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
