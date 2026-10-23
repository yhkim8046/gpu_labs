package deploy

import (
	"io"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type devicePluginResources struct {
	Requests map[string]string `yaml:"requests"`
	Limits   map[string]string `yaml:"limits"`
}

type devicePluginManifest struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name      string                `yaml:"name"`
					Resources devicePluginResources `yaml:"resources"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type devicePluginChartValues struct {
	Resources devicePluginResources `yaml:"resources"`
}

func TestDevicePluginResourcesMatchChartDefaults(t *testing.T) {
	manifestData, err := FS.ReadFile("device-plugin/device-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest devicePluginManifest
	decoder := yaml.NewDecoder(strings.NewReader(string(manifestData)))
	for {
		var document devicePluginManifest
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parse default device plugin manifest: %v", err)
		}
		if document.Kind == "DaemonSet" {
			manifest = document
			break
		}
	}
	if manifest.Kind != "DaemonSet" {
		t.Fatalf("manifest kind = %q, want DaemonSet", manifest.Kind)
	}
	if len(manifest.Spec.Template.Spec.Containers) != 1 || manifest.Spec.Template.Spec.Containers[0].Name != "device-plugin" {
		t.Fatalf("manifest containers = %#v, want the device-plugin container", manifest.Spec.Template.Spec.Containers)
	}
	manifestResources := manifest.Spec.Template.Spec.Containers[0].Resources

	valuesData, err := FS.ReadFile("charts/nvidia-device-plugin/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values devicePluginChartValues
	if err := yaml.Unmarshal(valuesData, &values); err != nil {
		t.Fatalf("parse Helm values: %v", err)
	}
	assertDevicePluginResources(t, "raw manifest", manifestResources)
	assertDevicePluginResources(t, "Helm defaults", values.Resources)
	if manifestResources.Requests["cpu"] != values.Resources.Requests["cpu"] ||
		manifestResources.Requests["memory"] != values.Resources.Requests["memory"] ||
		manifestResources.Limits["cpu"] != values.Resources.Limits["cpu"] ||
		manifestResources.Limits["memory"] != values.Resources.Limits["memory"] {
		t.Fatalf("raw manifest resources %#v do not match Helm defaults %#v", manifestResources, values.Resources)
	}

	templateData, err := FS.ReadFile("charts/nvidia-device-plugin/templates/daemonset.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(templateData), "toYaml .Values.resources | indent 12") {
		t.Fatal("Helm DaemonSet template does not render .Values.resources")
	}
}

func assertDevicePluginResources(t *testing.T, source string, resources devicePluginResources) {
	t.Helper()
	if resources.Requests["cpu"] != "10m" || resources.Requests["memory"] != "32Mi" {
		t.Errorf("%s requests = %#v, want cpu=10m and memory=32Mi", source, resources.Requests)
	}
	if resources.Limits["cpu"] != "100m" || resources.Limits["memory"] != "256Mi" {
		t.Errorf("%s limits = %#v, want cpu=100m and memory=256Mi", source, resources.Limits)
	}
}
