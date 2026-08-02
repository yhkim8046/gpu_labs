package cluster

import (
	"io"
	"testing"

	"github.com/gpu-lab/gpu-lab/internal/runner"
	"github.com/gpu-lab/gpu-lab/internal/version"
)

func TestNewManagerUsesLocalImageForDevelopmentBuild(t *testing.T) {
	oldVersion := version.Version
	defer func() { version.Version = oldVersion }()
	version.Version = "dev"
	t.Setenv("GPU_LAB_IMAGE", "")
	t.Setenv("GPU_LAB_IMAGE_SOURCE", "auto")

	m := NewManager(runner.New(io.Discard, io.Discard))
	if m.Image != LocalImageName {
		t.Fatalf("image = %q, want %q", m.Image, LocalImageName)
	}
	if m.ImageSource != ImageSourceAuto {
		t.Fatalf("image source = %q, want %q", m.ImageSource, ImageSourceAuto)
	}
	source, err := m.resolveImageSource()
	if err != nil {
		t.Fatal(err)
	}
	if source != ImageSourceLocal {
		t.Fatalf("resolved image source = %q, want %q", source, ImageSourceLocal)
	}
}

func TestNewManagerUsesVersionedRegistryImageForReleaseBuild(t *testing.T) {
	oldVersion := version.Version
	defer func() { version.Version = oldVersion }()
	version.Version = "v1.2.3"
	t.Setenv("GPU_LAB_IMAGE", "")
	t.Setenv("GPU_LAB_IMAGE_SOURCE", "auto")
	t.Setenv("GPU_LAB_RUNTIME_IMAGE_REPOSITORY", "ghcr.io/example/gpu-lab-runtime/")

	m := NewManager(runner.New(io.Discard, io.Discard))
	want := "ghcr.io/example/gpu-lab-runtime:1.2.3"
	if m.Image != want {
		t.Fatalf("image = %q, want %q", m.Image, want)
	}
	source, err := m.resolveImageSource()
	if err != nil {
		t.Fatal(err)
	}
	if source != ImageSourceRegistry {
		t.Fatalf("resolved image source = %q, want %q", source, ImageSourceRegistry)
	}
}

func TestNewManagerHonorsImageOverrideAndSource(t *testing.T) {
	oldVersion := version.Version
	defer func() { version.Version = oldVersion }()
	version.Version = "1.2.3"
	t.Setenv("GPU_LAB_IMAGE", "gpu-lab:course-local")
	t.Setenv("GPU_LAB_IMAGE_SOURCE", "local")

	m := NewManager(runner.New(io.Discard, io.Discard))
	if m.Image != "gpu-lab:course-local" {
		t.Fatalf("image = %q, want override", m.Image)
	}
	source, err := m.resolveImageSource()
	if err != nil {
		t.Fatal(err)
	}
	if source != ImageSourceLocal {
		t.Fatalf("resolved image source = %q, want %q", source, ImageSourceLocal)
	}
}

func TestNewManagerExplicitSourceSelectsMatchingDefaultImage(t *testing.T) {
	oldVersion := version.Version
	defer func() { version.Version = oldVersion }()
	version.Version = "v1.2.3"
	t.Setenv("GPU_LAB_IMAGE", "")
	t.Setenv("GPU_LAB_IMAGE_SOURCE", "local")

	m := NewManager(runner.New(io.Discard, io.Discard))
	if m.Image != LocalImageName {
		t.Fatalf("local image = %q, want %q", m.Image, LocalImageName)
	}

	version.Version = "dev"
	t.Setenv("GPU_LAB_IMAGE_SOURCE", "registry")
	m = NewManager(runner.New(io.Discard, io.Discard))
	want := DefaultImageRepo + ":dev"
	if m.Image != want {
		t.Fatalf("registry image = %q, want %q", m.Image, want)
	}
}

func TestManagerRejectsInvalidImageSource(t *testing.T) {
	m := Manager{Image: "gpu-lab:dev", ImageSource: "mirror"}
	if _, err := m.resolveImageSource(); err == nil {
		t.Fatal("resolveImageSource() succeeded for invalid source")
	}
}

func TestRenderAssetReplacesRuntimeImage(t *testing.T) {
	input := []byte("image: gpu-lab:dev\nimagePullPolicy: IfNotPresent\n")
	got := string(renderAsset(input, "ghcr.io/example/gpu-lab-runtime:1.2.3"))
	want := "image: ghcr.io/example/gpu-lab-runtime:1.2.3\nimagePullPolicy: IfNotPresent\n"
	if got != want {
		t.Fatalf("renderAsset() = %q, want %q", got, want)
	}
}
