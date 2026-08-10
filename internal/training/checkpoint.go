package training

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Checkpoint struct {
	Step                    int64           `json:"step"`
	Restarts                int64           `json:"restarts"`
	AppliedCrashGenerations map[string]bool `json:"applied_crash_generations,omitempty"`
}

func LoadCheckpoint(path string) (Checkpoint, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Checkpoint{AppliedCrashGenerations: map[string]bool{}}, nil
	}
	if err != nil {
		return Checkpoint{}, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return Checkpoint{}, err
	}
	if cp.AppliedCrashGenerations == nil {
		cp.AppliedCrashGenerations = map[string]bool{}
	}
	return cp, nil
}

func SaveCheckpoint(path string, cp Checkpoint) error {
	if cp.AppliedCrashGenerations == nil {
		cp.AppliedCrashGenerations = map[string]bool{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	b, err := json.MarshalIndent(cp, "", "  ")
	if err == nil {
		_, err = tmp.Write(b)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
