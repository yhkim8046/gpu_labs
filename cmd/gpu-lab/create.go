package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gpu-lab/gpu-lab/internal/cluster"
)

type createOptions struct {
	image       string
	imageSource string
	installAll  bool
}

func createCommand(ctx context.Context, m cluster.Manager, args []string, stdout io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printCreateHelp(stdout)
		return nil
	}
	options, err := parseCreateArgs(args)
	if err != nil {
		return err
	}
	if options.imageSource != "" {
		m.ImageSource = options.imageSource
		switch options.imageSource {
		case cluster.ImageSourceLocal:
			if options.image == "" {
				m.Image = cluster.LocalImageName
			}
		case cluster.ImageSourceRegistry:
			if options.image == "" && m.Image == cluster.LocalImageName {
				m.Image = cluster.RuntimeImageForVersion()
			}
		}
	}
	if options.image != "" {
		m.Image = options.image
	}
	createCtx, cancel := context.WithTimeout(ctx, cluster.DefaultTimeout())
	defer cancel()
	if err := m.Create(createCtx); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "gpu-lab base cluster is ready")
	if options.installAll {
		if err := m.InstallAll(createCtx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "all GPU Lab Helm components are installed")
		return nil
	}
	fmt.Fprintln(stdout, "next: gpu helm install nvidia-device-plugin")
	return nil
}

func parseCreateArgs(args []string) (createOptions, error) {
	options := createOptions{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--local":
			if options.imageSource != "" && options.imageSource != cluster.ImageSourceLocal {
				return createOptions{}, errors.New("cannot combine --local with another image source")
			}
			options.imageSource = cluster.ImageSourceLocal
		case "--registry":
			if options.imageSource != "" && options.imageSource != cluster.ImageSourceRegistry {
				return createOptions{}, errors.New("cannot combine --registry with another image source")
			}
			options.imageSource = cluster.ImageSourceRegistry
		case "--image-source":
			if i+1 >= len(args) {
				return createOptions{}, errors.New("usage: gpu create [--local|--registry] [--image <image>] [--all]")
			}
			i++
			source := args[i]
			if source != cluster.ImageSourceAuto && source != cluster.ImageSourceLocal && source != cluster.ImageSourceRegistry {
				return createOptions{}, fmt.Errorf("invalid image source %q; expected auto, local, or registry", source)
			}
			if options.imageSource != "" && options.imageSource != source {
				return createOptions{}, errors.New("cannot combine multiple image sources")
			}
			options.imageSource = source
		case "--image":
			if i+1 >= len(args) || args[i+1] == "" {
				return createOptions{}, errors.New("usage: gpu create [--local|--registry] [--image <image>] [--all]")
			}
			i++
			options.image = args[i]
		case "--all":
			options.installAll = true
		default:
			return createOptions{}, fmt.Errorf("unknown create option %q; run gpu create --help", args[i])
		}
	}
	return options, nil
}

func printCreateHelp(w io.Writer) {
	_, _ = io.WriteString(w, `gpu create — create or reuse the base GPU lab cluster

Usage:
  gpu create
  gpu create --local
  gpu create --registry
  gpu create --image ghcr.io/<owner>/gpu-lab-runtime:1.0.0
  gpu create --all

Options:
  --local                  build and load the local gpu-lab:dev image
  --registry               pull and load the versioned runtime image
  --image <image>          override the runtime image reference
  --image-source <source> choose auto, local, or registry
  --all                    install all components after cluster bootstrap
`)
}
