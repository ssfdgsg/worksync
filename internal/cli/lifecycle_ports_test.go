package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"worksync/internal/runtime/podman"
	"worksync/internal/state"
)

func TestCheckpointAndRemoveForPortsPreservesWritableLayer(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "podman.log")
	binPath := filepath.Join(dir, "podman")
	script := `#!/bin/sh
echo "$@" >> "` + logPath + `"
case "$1" in
  stop) exit 0 ;;
  commit) echo "sha256:checkpoint"; exit 0 ;;
  rm) exit 0 ;;
esac
exit 1
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := &podman.Client{Bin: binPath}
	c := &state.Container{ContainerID: "container-1", State: state.StateRunning}

	image, err := checkpointAndRemoveForPorts(context.Background(), rt, c)
	if err != nil {
		t.Fatal(err)
	}
	if image != "sha256:checkpoint" {
		t.Fatalf("checkpoint image = %q", image)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(logBytes))
	want := []string{"stop", "--time", "10", "container-1", "commit", "container-1", "rm", "container-1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("podman sequence = %q, want %q", got, want)
	}
}

func TestCheckpointAndRemoveForPortsKeepsContainerOnCommitFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "podman.log")
	binPath := filepath.Join(dir, "podman")
	script := `#!/bin/sh
echo "$@" >> "` + logPath + `"
case "$1" in
  stop) exit 0 ;;
  commit) exit 1 ;;
  rm) exit 0 ;;
esac
exit 1
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := &podman.Client{Bin: binPath}
	c := &state.Container{ContainerID: "container-1", State: state.StateRunning}

	if _, err := checkpointAndRemoveForPorts(context.Background(), rt, c); err == nil {
		t.Fatal("expected commit failure")
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logBytes), "rm container-1") {
		t.Fatalf("container removed after failed checkpoint: %s", logBytes)
	}
}
