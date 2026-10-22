package exporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gpu-lab/gpu-lab/internal/scenario"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type Server struct {
	model *Model
	addr  string
}

func NewServer(model *Model, addr string) *Server {
	if addr == "" {
		addr = ":9400"
	}
	return &Server{model: model, addr: addr}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.model.IsUnavailable() {
			http.Error(w, "scenario fault: exporter unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		if s.model.IsUnavailable() {
			http.Error(w, "scenario fault: exporter unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(s.model.Metrics()))
	})
	mux.HandleFunc("/api/v1/state", func(w http.ResponseWriter, _ *http.Request) {
		data, err := s.model.StateJSON()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})
	return mux
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	server := &http.Server{Addr: s.addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func WatchConfigMap(ctx context.Context, model *Model, namespace string) {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("scenario watcher disabled: %v", err)
		return
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("scenario watcher disabled: %v", err)
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var lastResourceVersion string
	for {
		if err := syncScenario(ctx, client, model, namespace, &lastResourceVersion); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("scenario sync: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func syncScenario(ctx context.Context, client kubernetes.Interface, model *Model, namespace string, lastResourceVersion *string) error {
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, scenario.ConfigName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if cm.ResourceVersion == *lastResourceVersion {
		return nil
	}
	name, err := applyScenarioConfig(model, cm.Data, cm.ResourceVersion)
	if err != nil {
		return err
	}
	*lastResourceVersion = cm.ResourceVersion
	log.Printf("applied scenario=%s generation=%s", name, model.Snapshot().Generation)
	return nil
}

func applyScenarioConfig(model *Model, config map[string]string, resourceVersion string) (string, error) {
	data := config[scenario.ScenarioDataKey]
	if data == "" {
		return "", errors.New("scenario ConfigMap has no scenario.yaml")
	}
	parsed, err := scenario.Parse([]byte(data))
	if err != nil {
		return "", err
	}
	generation := config[scenario.GenerationDataKey]
	if generation == "" {
		generation = resourceVersion
	}
	startedAt := model.currentTime()
	if raw := config[scenario.StartedAtDataKey]; raw != "" {
		startedAt, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return "", fmt.Errorf("invalid scenario started_at: %w", err)
		}
	} else if len(parsed.Spec.Timeline) > 0 {
		return "", errors.New("timeline scenario has no started_at; reapply it with gpu scenario run")
	}
	model.ApplyAt(parsed, generation, startedAt)
	return parsed.Name(), nil
}

func EncodeState(model *Model) ([]byte, error) {
	return json.Marshal(model.Snapshot())
}
