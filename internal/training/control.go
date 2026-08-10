package training

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Control is the operator-controlled fault injection document. The projected
// ConfigMap file is read before every step, so updates do not require a new
// image. The CLI's flat schema is supported: {"generation":1,
// "straggler_rank":2,"straggler_delay":"2s","crash_rank":1,
// "crash_token":"crash-1"}. The nested schema remains supported for
// compatibility. CrashGeneration (or Token) identifies a one-shot crash.
type Control struct {
	Straggler        *StragglerControl `json:"straggler,omitempty"`
	Crash            *CrashControl     `json:"crash,omitempty"`
	FabricMode       string            `json:"fabric_mode,omitempty"`
	FabricTargetNode string            `json:"fabric_target_node,omitempty"`
	FabricDelay      time.Duration     `json:"-"`
}

type StragglerControl struct {
	Rank  int           `json:"rank"`
	Delay time.Duration `json:"-"`
}

type CrashControl struct {
	Rank       int    `json:"rank"`
	Enabled    bool   `json:"enabled"`
	Generation string `json:"generation,omitempty"`
	Token      string `json:"token,omitempty"`
}

func (s *StragglerControl) UnmarshalJSON(b []byte) error {
	var raw struct {
		Rank  int             `json:"rank"`
		Delay json.RawMessage `json:"delay"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	s.Rank = raw.Rank
	if len(raw.Delay) == 0 {
		return nil
	}
	var text string
	if json.Unmarshal(raw.Delay, &text) == nil {
		d, err := time.ParseDuration(text)
		s.Delay = d
		return err
	}
	var seconds float64
	if err := json.Unmarshal(raw.Delay, &seconds); err != nil {
		return err
	}
	s.Delay = time.Duration(seconds * float64(time.Second))
	return nil
}

func ReadControl(path string) (Control, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Control{}, nil
		}
		return Control{}, err
	}
	if len(b) == 0 {
		return Control{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return Control{}, err
	}
	var c Control
	if _, nested := fields["straggler"]; nested {
		if err := json.Unmarshal(b, &c); err != nil {
			return Control{}, err
		}
	}
	if _, nested := fields["crash"]; nested {
		if err := json.Unmarshal(b, &c); err != nil {
			return Control{}, err
		}
	}
	if raw, ok := fields["straggler_rank"]; ok && string(raw) != "null" {
		var rank int
		if err := json.Unmarshal(raw, &rank); err != nil {
			return Control{}, fmt.Errorf("invalid straggler_rank: %w", err)
		}
		delay := time.Duration(0)
		if rawDelay, exists := fields["straggler_delay"]; exists {
			var text string
			if err := json.Unmarshal(rawDelay, &text); err != nil {
				return Control{}, fmt.Errorf("invalid straggler_delay: %w", err)
			}
			delay, err = time.ParseDuration(text)
			if err != nil {
				return Control{}, fmt.Errorf("invalid straggler_delay: %w", err)
			}
		}
		c.Straggler = &StragglerControl{Rank: rank, Delay: delay}
	} else if _, ok := fields["straggler_rank"]; ok {
		c.Straggler = nil
	}
	if raw, ok := fields["crash_rank"]; ok && string(raw) != "null" {
		var rank int
		if err := json.Unmarshal(raw, &rank); err != nil {
			return Control{}, fmt.Errorf("invalid crash_rank: %w", err)
		}
		crash := &CrashControl{Rank: rank, Enabled: false}
		if rawToken, exists := fields["crash_token"]; exists && string(rawToken) != "null" {
			if err := json.Unmarshal(rawToken, &crash.Token); err != nil {
				return Control{}, fmt.Errorf("invalid crash_token: %w", err)
			}
		}
		if crash.Token != "" {
			crash.Enabled = true
		}
		if rawGeneration, exists := fields["generation"]; exists && string(rawGeneration) != "null" {
			crash.Generation = rawScalarString(rawGeneration)
		}
		c.Crash = crash
	} else if _, ok := fields["crash_rank"]; ok {
		c.Crash = nil
	}
	if c.Straggler != nil && (c.Straggler.Rank < 0 || c.Straggler.Delay < 0) {
		return Control{}, errors.New("invalid straggler control")
	}
	if c.Crash != nil && c.Crash.Rank < 0 {
		return Control{}, errors.New("invalid crash control")
	}
	if raw, ok := fields["fabric_mode"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &c.FabricMode); err != nil {
			return Control{}, fmt.Errorf("invalid fabric_mode: %w", err)
		}
		c.FabricMode = strings.ToLower(strings.TrimSpace(c.FabricMode))
	}
	if c.FabricMode == "" {
		c.FabricMode = "normal"
	}
	if raw, ok := fields["fabric_target_node"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &c.FabricTargetNode); err != nil {
			return Control{}, fmt.Errorf("invalid fabric_target_node: %w", err)
		}
		c.FabricTargetNode = strings.TrimSpace(c.FabricTargetNode)
	}
	if raw, ok := fields["fabric_delay"]; ok && string(raw) != "null" {
		delay, err := parseDuration(raw)
		if err != nil {
			return Control{}, fmt.Errorf("invalid fabric_delay: %w", err)
		}
		if delay < 0 {
			return Control{}, errors.New("invalid fabric_delay: must not be negative")
		}
		c.FabricDelay = delay
	}
	return c, nil
}

func parseDuration(raw json.RawMessage) (time.Duration, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return time.ParseDuration(strings.TrimSpace(text))
	}
	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err != nil {
		return 0, err
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func rawScalarString(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func (c Control) CrashID() string {
	if c.Crash == nil || !c.Crash.Enabled {
		return ""
	}
	if c.Crash.Generation != "" {
		return c.Crash.Generation
	}
	return c.Crash.Token
}

func (c Control) EffectiveFabricMode() string {
	switch strings.ToLower(strings.TrimSpace(c.FabricMode)) {
	case "down", "degraded", "errors", "retries", "congestion":
		return strings.ToLower(strings.TrimSpace(c.FabricMode))
	default:
		return "normal"
	}
}

var syntheticFabricNodes = map[string]string{
	"gpu-node-01": "gpu-lab-worker",
	"gpu-node-02": "gpu-lab-worker2",
	"gpu-node-03": "gpu-lab-worker3",
}

// FabricTargetMatches accepts both the user-facing synthetic node ID and the
// Kubernetes NODE_NAME exposed to a training pod. Direct matches are accepted
// as well, which keeps the helper useful for custom lab node names.
func FabricTargetMatches(target, node string) bool {
	target = strings.TrimSpace(target)
	node = strings.TrimSpace(node)
	if target == "" || node == "" {
		return false
	}
	if target == node {
		return true
	}
	if mapped, ok := syntheticFabricNodes[target]; ok {
		return mapped == node
	}
	for syntheticID, mapped := range syntheticFabricNodes {
		if mapped == target && syntheticID == node {
			return true
		}
	}
	return false
}

func FabricTargetNode(target string) string {
	return syntheticFabricNodes[strings.TrimSpace(target)]
}
