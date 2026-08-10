package deploy

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRuntimeYAMLAssetsParse(t *testing.T) {
	assets := []string{
		"kind/cluster.yaml",
		"device-plugin/device-plugin.yaml",
		"exporter/exporter.yaml",
		"demo/namespace.yaml",
		"monitoring/values.yaml",
		"monitoring/servicemonitor.yaml",
		"monitoring/alerts.yaml",
		"monitoring/dashboard-configmap.yaml",
		"monitoring/training-servicemonitor.yaml",
		"monitoring/training-alerts.yaml",
		"monitoring/training-dashboard-configmap.yaml",
		"monitoring/fabric-alerts.yaml",
		"monitoring/fabric-dashboard-configmap.yaml",
	}

	for _, asset := range assets {
		t.Run(asset, func(t *testing.T) {
			data, err := FS.ReadFile(asset)
			if err != nil {
				t.Fatal(err)
			}
			decoder := yaml.NewDecoder(strings.NewReader(string(data)))
			documents := 0
			for {
				var document any
				if err := decoder.Decode(&document); err != nil {
					if errors.Is(err, io.EOF) {
						break
					}
					t.Fatalf("parse YAML document %d: %v", documents+1, err)
				}
				if document != nil {
					documents++
				}
			}
			if documents == 0 {
				t.Fatal("asset contains no YAML documents")
			}
		})
	}
}

func TestGrafanaDashboardConfigMapContainsValidDashboard(t *testing.T) {
	assets := map[string]string{
		"monitoring/dashboard-configmap.yaml":          "gpu-lab-overview.json",
		"monitoring/training-dashboard-configmap.yaml": "gpu-lab-distributed-training.json",
		"monitoring/fabric-dashboard-configmap.yaml":   "gpu-lab-ib-fabric.json",
	}
	for asset, key := range assets {
		t.Run(asset, func(t *testing.T) {
			data, err := FS.ReadFile(asset)
			if err != nil {
				t.Fatal(err)
			}
			var configMap struct {
				Kind string            `yaml:"kind"`
				Data map[string]string `yaml:"data"`
			}
			if err := yaml.Unmarshal(data, &configMap); err != nil {
				t.Fatal(err)
			}
			if configMap.Kind != "ConfigMap" {
				t.Fatalf("kind = %q, want ConfigMap", configMap.Kind)
			}
			payload := configMap.Data[key]
			if payload == "" {
				t.Fatalf("%s is missing", key)
			}
			var dashboard struct {
				Title  string `json:"title"`
				Panels []struct {
					Title   string `json:"title"`
					Targets []struct {
						Expr string `json:"expr"`
					} `json:"targets"`
				} `json:"panels"`
			}
			if err := json.Unmarshal([]byte(payload), &dashboard); err != nil {
				t.Fatalf("parse embedded Grafana dashboard: %v", err)
			}
			if dashboard.Title == "" || len(dashboard.Panels) == 0 {
				t.Fatal("dashboard must have a title and at least one panel")
			}
			for i, panel := range dashboard.Panels {
				if panel.Title == "" {
					t.Errorf("panel %d has no title", i)
				}
				for j, target := range panel.Targets {
					if strings.TrimSpace(target.Expr) == "" {
						t.Errorf("panel %q target %d has an empty PromQL expression", panel.Title, j)
					}
				}
			}
		})
	}
}

func TestPrometheusRuleContainsNamedRulesWithExpressions(t *testing.T) {
	for _, asset := range []string{"monitoring/alerts.yaml", "monitoring/training-alerts.yaml", "monitoring/fabric-alerts.yaml"} {
		t.Run(asset, func(t *testing.T) {
			data, err := FS.ReadFile(asset)
			if err != nil {
				t.Fatal(err)
			}
			var manifest struct {
				Kind string `yaml:"kind"`
				Spec struct {
					Groups []struct {
						Name  string `yaml:"name"`
						Rules []struct {
							Alert string `yaml:"alert"`
							Expr  string `yaml:"expr"`
						} `yaml:"rules"`
					} `yaml:"groups"`
				} `yaml:"spec"`
			}
			if err := yaml.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.Kind != "PrometheusRule" {
				t.Fatalf("kind = %q, want PrometheusRule", manifest.Kind)
			}
			if len(manifest.Spec.Groups) == 0 {
				t.Fatal("PrometheusRule must contain at least one group")
			}
			for i, group := range manifest.Spec.Groups {
				if group.Name == "" || len(group.Rules) == 0 {
					t.Errorf("group %d must have a name and at least one rule", i)
				}
				for j, rule := range group.Rules {
					if rule.Alert == "" || strings.TrimSpace(rule.Expr) == "" {
						t.Errorf("group %q rule %d must have alert and expr", group.Name, j)
					}
				}
			}
		})
	}
}
