package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gpu-lab/gpu-lab/internal/nvidiasmi"
	"github.com/gpu-lab/gpu-lab/internal/runner"
)

func main() {
	if err := nvidiasmi.Run(context.Background(), runner.New(os.Stdout, os.Stderr), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
