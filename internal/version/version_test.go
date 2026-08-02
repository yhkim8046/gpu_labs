package version

import "testing"

func TestString(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := Version, Commit, BuildDate
	defer func() {
		Version, Commit, BuildDate = oldVersion, oldCommit, oldBuildDate
	}()

	Version = "0.1.0"
	Commit = "abc1234"
	BuildDate = "2026-08-02T00:00:00Z"

	got := String()
	want := "gpu-lab 0.1.0 (commit abc1234, built 2026-08-02T00:00:00Z)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
