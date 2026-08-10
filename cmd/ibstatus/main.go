package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gpu-lab/gpu-lab/internal/rdmacompat"
	"github.com/gpu-lab/gpu-lab/internal/runner"
)

func main() {
	if err := rdmacompat.Run(context.Background(), runner.New(os.Stdout, os.Stderr), rdmacompat.IBStatus, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
