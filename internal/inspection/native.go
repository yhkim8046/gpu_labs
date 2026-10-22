package inspection

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	probeTimeout = 3 * time.Second
	waitDelay    = 300 * time.Millisecond
	labContext   = "gpu-lab"
)

var ErrToolNotFound = errors.New("tool not found")

type Dependencies struct {
	LookupEnv  func(string) string
	HomeDir    func() (string, error)
	Executable func() (string, error)
	RunCommand func(context.Context, string, ...string) ([]byte, []byte, error)
	HTTPClient *http.Client
	Now        func() time.Time
}

func DefaultDependencies() Dependencies {
	return Dependencies{
		LookupEnv:  os.Getenv,
		HomeDir:    os.UserHomeDir,
		Executable: os.Executable,
		RunCommand: runBoundedCommand,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		Now:        time.Now,
	}
}

func (d Dependencies) normalized() Dependencies {
	defaults := DefaultDependencies()
	if d.LookupEnv == nil {
		d.LookupEnv = defaults.LookupEnv
	}
	if d.HomeDir == nil {
		d.HomeDir = defaults.HomeDir
	}
	if d.Executable == nil {
		d.Executable = defaults.Executable
	}
	if d.RunCommand == nil {
		d.RunCommand = defaults.RunCommand
	}
	if d.HTTPClient == nil {
		d.HTTPClient = defaults.HTTPClient
	}
	if d.Now == nil {
		d.Now = defaults.Now
	}
	return d
}

func runBoundedCommand(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func runProbe(ctx context.Context, d Dependencies, name string, args ...string) ([]byte, []byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return d.RunCommand(probeCtx, name, args...)
}

type NvidiaSMI struct {
	Path    string
	Version string
}

func FindNvidiaSMI(ctx context.Context, dependencies Dependencies) (NvidiaSMI, error) {
	d := dependencies.normalized()
	if override := strings.TrimSpace(d.LookupEnv("GPU_LAB_NVIDIA_SMI")); override != "" {
		if !filepath.IsAbs(override) {
			return NvidiaSMI{}, errors.New("GPU_LAB_NVIDIA_SMI must be an absolute path")
		}
		if err := executableFile(override); err != nil {
			return NvidiaSMI{}, fmt.Errorf("GPU_LAB_NVIDIA_SMI: %w", err)
		}
		if isSelf(override, d) {
			return NvidiaSMI{}, errors.New("GPU_LAB_NVIDIA_SMI resolves to the gpu executable")
		}
		if isKnownGPUlabShim(override, "nvidia-smi") {
			return NvidiaSMI{}, fmt.Errorf("GPU_LAB_NVIDIA_SMI points to a GPU Lab shim: %w", errNvidiaSMIShim)
		}
		return inspectNvidiaSMICandidate(ctx, d, override, true)
	}

	var seen []os.FileInfo
	for _, directory := range filepath.SplitList(d.LookupEnv("PATH")) {
		if directory == "" {
			continue
		}
		for _, candidate := range executableCandidates(directory, "nvidia-smi", d) {
			info, err := os.Stat(candidate)
			if err != nil || !isExecutable(info) || isSelf(candidate, d) || alreadySeen(info, seen) || isKnownGPUlabShim(candidate, "nvidia-smi") {
				continue
			}
			seen = append(seen, info)
			tool, err := inspectNvidiaSMICandidate(ctx, d, candidate, false)
			if errors.Is(err, errNvidiaSMIShim) {
				continue
			}
			if err != nil {
				return NvidiaSMI{}, err
			}
			return tool, nil
		}
	}
	return NvidiaSMI{}, ErrToolNotFound
}

var errNvidiaSMIShim = errors.New("GPU Lab nvidia-smi shim")

func inspectNvidiaSMICandidate(ctx context.Context, d Dependencies, path string, override bool) (NvidiaSMI, error) {
	stdout, stderr, err := runProbe(ctx, d, path, "--version")
	if err != nil {
		return NvidiaSMI{}, fmt.Errorf("nvidia-smi at %q failed --version: %w", path, err)
	}
	version := strings.TrimSpace(string(stdout))
	if version == "" {
		version = strings.TrimSpace(string(stderr))
	}
	if isShimVersion(string(stdout) + "\n" + string(stderr)) {
		if override {
			return NvidiaSMI{}, fmt.Errorf("GPU_LAB_NVIDIA_SMI points to a GPU Lab shim: %w", errNvidiaSMIShim)
		}
		return NvidiaSMI{}, errNvidiaSMIShim
	}
	return NvidiaSMI{Path: path, Version: version}, nil
}

func isShimVersion(version string) bool {
	value := strings.ToLower(version)
	return strings.Contains(value, "gpu lab") || strings.Contains(value, "synthetic")
}

func FindExecutable(name string, dependencies Dependencies) (string, error) {
	d := dependencies.normalized()
	if filepath.IsAbs(name) {
		if err := executableFile(name); err != nil {
			return "", err
		}
		if isSelf(name, d) {
			return "", fmt.Errorf("%s resolves to the gpu executable", name)
		}
		if isKnownGPUlabShim(name, filepath.Base(name)) {
			return "", fmt.Errorf("%s resolves to a GPU Lab shim", name)
		}
		return name, nil
	}
	var seen []os.FileInfo
	for _, directory := range filepath.SplitList(d.LookupEnv("PATH")) {
		if directory == "" {
			continue
		}
		for _, candidate := range executableCandidates(directory, name, d) {
			info, err := os.Stat(candidate)
			if err != nil || !isExecutable(info) || isSelf(candidate, d) || alreadySeen(info, seen) || isKnownGPUlabShim(candidate, name) {
				continue
			}
			seen = append(seen, info)
			return candidate, nil
		}
	}
	return "", ErrToolNotFound
}

func isKnownGPUlabShim(path, command string) bool {
	if isGPUlabCompiledBinary(path) {
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024))
	if err != nil {
		return false
	}
	text := strings.ToLower(string(data))
	if !strings.HasPrefix(text, "#!") {
		return false
	}
	if strings.Contains(text, "gpu lab") && strings.Contains(text, "synthetic") {
		return true
	}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		for i := 0; i < len(fields); i++ {
			token := cleanShellToken(fields[i])
			base := filepath.Base(token)
			if base != "gpu" && base != "gpu-lab" {
				continue
			}
			for j := i + 1; j < len(fields); j++ {
				arg := cleanShellToken(fields[j])
				if arg == "--mode" {
					j++
					continue
				}
				if strings.HasPrefix(arg, "--mode=") {
					continue
				}
				if arg == command {
					return true
				}
				break
			}
		}
	}
	return false
}

func isGPUlabCompiledBinary(path string) bool {
	info, err := buildinfo.ReadFile(path)
	if err != nil || info == nil || info.Main.Path != "github.com/gpu-lab/gpu-lab" {
		return false
	}
	switch info.Path {
	case "github.com/gpu-lab/gpu-lab/cmd/gpu-lab",
		"github.com/gpu-lab/gpu-lab/cmd/nvidia-smi",
		"github.com/gpu-lab/gpu-lab/cmd/ibstat",
		"github.com/gpu-lab/gpu-lab/cmd/ibstatus",
		"github.com/gpu-lab/gpu-lab/cmd/ibv-devinfo",
		"github.com/gpu-lab/gpu-lab/cmd/ibv_devinfo":
		return true
	default:
		return false
	}
}

func cleanShellToken(token string) string {
	return strings.Trim(token, "\"'`;,(){}[]")
}

func executableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !isExecutable(info) {
		return fmt.Errorf("%q is not an executable file", path)
	}
	return nil
}

func isExecutable(info os.FileInfo) bool {
	if runtime.GOOS == "windows" {
		return info != nil && !info.IsDir()
	}
	return info != nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func executableCandidates(directory, name string, d Dependencies) []string {
	if runtime.GOOS != "windows" || filepath.Ext(name) != "" {
		return []string{filepath.Join(directory, name)}
	}
	extensions := strings.Split(d.LookupEnv("PATHEXT"), ";")
	if len(extensions) == 1 && extensions[0] == "" {
		extensions = []string{".COM", ".EXE", ".BAT", ".CMD"}
	}
	paths := make([]string, 0, len(extensions))
	for _, extension := range extensions {
		if extension != "" {
			paths = append(paths, filepath.Join(directory, name+strings.ToLower(extension)))
		}
	}
	return paths
}

func isSelf(candidate string, d Dependencies) bool {
	current, err := d.Executable()
	if err != nil || current == "" {
		return false
	}
	candidateInfo, candidateErr := os.Stat(candidate)
	currentInfo, currentErr := os.Stat(current)
	return candidateErr == nil && currentErr == nil && os.SameFile(candidateInfo, currentInfo)
}

func alreadySeen(info os.FileInfo, seen []os.FileInfo) bool {
	for _, previous := range seen {
		if os.SameFile(info, previous) {
			return true
		}
	}
	return false
}

type syntheticEvidence struct {
	Source     string
	StateURL   string
	Kubeconfig string
}

func detectSyntheticEvidence(ctx context.Context, dependencies Dependencies) (syntheticEvidence, error) {
	d := dependencies.normalized()
	endpoint := strings.TrimSpace(d.LookupEnv("GPU_LAB_STATE_URL"))
	if endpoint != "" {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return syntheticEvidence{}, fmt.Errorf("invalid GPU_LAB_STATE_URL %q", endpoint)
		}
		return syntheticEvidence{Source: "GPU_LAB_STATE_URL", StateURL: endpoint}, nil
	}

	home, err := d.HomeDir()
	if err != nil {
		return syntheticEvidence{}, fmt.Errorf("locate dedicated GPU Lab kubeconfig: %w", err)
	}
	path := filepath.Join(home, ".kube", "gpu-lab.config")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return syntheticEvidence{}, nil
		}
		return syntheticEvidence{}, fmt.Errorf("inspect dedicated GPU Lab kubeconfig: %w", err)
	}
	stdout, _, err := d.RunCommand(ctx, "kubectl", "--kubeconfig", path, "--context", labContext, "get", "nodes", "-o", "json")
	if err != nil {
		return syntheticEvidence{}, fmt.Errorf("read GPU Lab node labels and annotations: %w", err)
	}
	hasMetadata, err := hasGPUlabMetadata(stdout)
	if err != nil {
		return syntheticEvidence{}, fmt.Errorf("decode GPU Lab node metadata: %w", err)
	}
	if !hasMetadata {
		return syntheticEvidence{}, nil
	}
	return syntheticEvidence{Source: "dedicated gpu-lab kubeconfig", Kubeconfig: path}, nil
}

func hasGPUlabMetadata(data []byte) (bool, error) {
	var response struct {
		Items []struct {
			Metadata struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return false, err
	}
	for _, item := range response.Items {
		for key, value := range item.Metadata.Labels {
			if strings.HasPrefix(key, "gpu.lab/") && value != "" {
				return true, nil
			}
		}
		for key, value := range item.Metadata.Annotations {
			if strings.HasPrefix(key, "gpu.lab/") && value != "" {
				return true, nil
			}
		}
	}
	return false, nil
}

func ForwardNative(ctx context.Context, path string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = waitDelay
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func IsNotFound(err error) bool { return errors.Is(err, ErrToolNotFound) }

type NativeSelection struct {
	Mode   Mode
	Path   string
	Source string
}

func SelectNative(ctx context.Context, command string, mode Mode, dependencies Dependencies) (NativeSelection, error) {
	d := dependencies.normalized()
	if mode != ModeAuto && mode != ModeReal && mode != ModeSynthetic {
		return NativeSelection{}, fmt.Errorf("invalid mode %q", mode)
	}
	if mode == ModeSynthetic {
		return NativeSelection{Mode: ModeSynthetic, Source: "GPU Lab synthetic compatibility"}, nil
	}

	var path string
	var err error
	if command == "nvidia-smi" {
		var tool NvidiaSMI
		tool, err = FindNvidiaSMI(ctx, d)
		path = tool.Path
	} else {
		path, err = FindExecutable(command, d)
	}
	if err == nil {
		return NativeSelection{Mode: ModeReal, Path: path, Source: path}, nil
	}
	if mode == ModeReal || !errors.Is(err, ErrToolNotFound) {
		return NativeSelection{Mode: ModeReal, Source: "local " + command}, fmt.Errorf("real %s unavailable: %w", command, err)
	}

	evidence, evidenceErr := detectSyntheticEvidence(ctx, d)
	if evidenceErr != nil {
		return NativeSelection{Mode: ModeAuto, Source: "mode auto"}, fmt.Errorf("cannot determine whether GPU Lab is available: %w", evidenceErr)
	}
	if evidence.Source == "" {
		return NativeSelection{Mode: ModeAuto, Source: "no local tool or GPU Lab evidence"}, fmt.Errorf("no real %s executable or positive GPU Lab evidence", command)
	}
	return NativeSelection{Mode: ModeSynthetic, Source: evidence.Source}, nil
}
