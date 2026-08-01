package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Runner struct {
	Stdout io.Writer
	Stderr io.Writer
}

func New(stdout, stderr io.Writer) Runner {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return Runner{Stdout: stdout, Stderr: stderr}
}

func Exists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (r Runner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		return commandError(name, args, err)
	}
	return nil
}

func (r Runner) RunInput(ctx context.Context, input []byte, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		return commandError(name, args, err)
	}
	return nil
}

func (r Runner) Output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message != "" {
			return stdout.String(), fmt.Errorf("%w: %s", commandError(name, args, err), message)
		}
		return stdout.String(), commandError(name, args, err)
	}
	return stdout.String(), nil
}

func commandError(name string, args []string, err error) error {
	return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
}
