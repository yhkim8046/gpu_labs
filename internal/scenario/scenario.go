package scenario

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	builtins "github.com/gpu-lab/gpu-lab/scenarios"
	"gopkg.in/yaml.v3"
)

const (
	APIVersion        = "gpu-lab.io/v1alpha1"
	Kind              = "Scenario"
	Namespace         = "gpu-lab-system"
	ConfigName        = "gpu-lab-scenario"
	ScenarioDataKey   = "scenario.yaml"
	GenerationDataKey = "generation"
	StartedAtDataKey  = "started_at"
)

// Parse decodes a strict YAML or JSON scenario document and rejects any
// document that fails Validate.
func Parse(data []byte) (Scenario, error) {
	var s Scenario
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&s); err != nil {
		return Scenario{}, err
	}
	if err := s.Validate(); err != nil {
		return Scenario{}, err
	}
	return s, nil
}

// LoadBuiltin reads and validates a scenario embedded from the scenarios package.
func LoadBuiltin(name string) (Scenario, error) {
	data, err := fs.ReadFile(builtins.FS, name+".yaml")
	if err != nil {
		return Scenario{}, fmt.Errorf("builtin scenario %q: %w", name, err)
	}
	return Parse(data)
}

// LoadFile reads and validates a scenario document from disk.
func LoadFile(filename string) (Scenario, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Scenario{}, err
	}
	return Parse(data)
}

// ListBuiltin returns the sorted names of all embedded scenario documents.
func ListBuiltin() ([]string, error) {
	entries, err := fs.ReadDir(builtins.FS, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".yaml" {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".yaml"))
	}
	sort.Strings(names)
	return names, nil
}

// Marshal validates the scenario and renders its canonical YAML document.
func Marshal(s Scenario) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(s)
}

// HasAction returns the first action with the requested type.
func HasAction(s Scenario, actionType string) (ScenarioAction, bool) {
	for _, action := range s.Spec.Actions {
		if action.Type == actionType {
			return action, true
		}
	}
	return ScenarioAction{}, false
}
