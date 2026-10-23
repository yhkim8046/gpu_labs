package inspection

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const Disclaimer = "GPU Lab — unofficial; not affiliated with or endorsed by NVIDIA"

type Mode string

const (
	ModeAuto      Mode = "auto"
	ModeReal      Mode = "real"
	ModeSynthetic Mode = "synthetic"
)

func ParseMode(value string) (Mode, error) {
	mode := Mode(strings.TrimSpace(value))
	switch mode {
	case ModeAuto, ModeReal, ModeSynthetic:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid mode %q; use auto, real, or synthetic", value)
	}
}

type Status string

const (
	StatusPass        Status = "pass"
	StatusWarn        Status = "warn"
	StatusFail        Status = "fail"
	StatusUnavailable Status = "unavailable"
	StatusUnsupported Status = "unsupported"
)

type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Value  string `json:"value,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type Report struct {
	Mode          Mode    `json:"mode"`
	RequestedMode Mode    `json:"requested_mode,omitempty"`
	Source        string  `json:"source"`
	Unofficial    bool    `json:"unofficial"`
	Disclaimer    string  `json:"disclaimer"`
	Checks        []Check `json:"checks"`
}

func NewReport(mode, requested Mode, source string) Report {
	return Report{
		Mode:          mode,
		RequestedMode: requested,
		Source:        source,
		Unofficial:    true,
		Disclaimer:    Disclaimer,
		Checks:        []Check{},
	}
}

func (r *Report) Add(name string, status Status, value, detail string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Value: value, Detail: detail})
}

func (r Report) WriteJSON(w io.Writer) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func (r Report) WriteText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "%s\nMode: %s", r.Disclaimer, r.Mode); err != nil {
		return err
	}
	if r.RequestedMode != "" && r.RequestedMode != r.Mode {
		if _, err := fmt.Fprintf(w, " (requested %s)", r.RequestedMode); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "\nSource: %s\n", r.Source); err != nil {
		return err
	}
	for _, check := range r.Checks {
		if _, err := fmt.Fprintf(w, "[%s] %s", check.Status, check.Name); err != nil {
			return err
		}
		if check.Value != "" {
			if _, err := fmt.Fprintf(w, ": %s", check.Value); err != nil {
				return err
			}
		}
		if check.Detail != "" {
			if _, err := fmt.Fprintf(w, " — %s", check.Detail); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func WriteReport(w io.Writer, report Report, asJSON bool) error {
	if w == nil {
		return errors.New("inspection report writer is nil")
	}
	if asJSON {
		return report.WriteJSON(w)
	}
	return report.WriteText(w)
}
