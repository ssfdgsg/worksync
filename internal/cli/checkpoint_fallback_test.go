package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"worksync/internal/backend"
	"worksync/internal/manifest"
	"worksync/internal/project"
	"worksync/internal/runtime/podman"
	"worksync/internal/state"
	"worksync/internal/store"
)

// newCheckpointFallbackApp builds an App with an isolated data root and a
// fake podman binary whose behavior is driven by the script.
func newCheckpointFallbackApp(t *testing.T, script string) (*App, *project.Project, backend.Backend) {
	t.Helper()
	root := t.TempDir()
	layout := &store.Layout{
		Root:        root,
		StateDB:     filepath.Join(root, "state.db"),
		LocksDir:    filepath.Join(root, "locks"),
		ProjectsDir: filepath.Join(root, "projects"),
		CommitsDir:  filepath.Join(root, "commits"),
		RefsDir:     filepath.Join(root, "refs"),
		OCIDir:      filepath.Join(root, "oci"),
		ResticDir:   filepath.Join(root, "restic"),
		StagingDir:  filepath.Join(root, "staging"),
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	db, err := state.Open(layout.StateDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertProject(state.Project{ID: "demo"}); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(root, "podman")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PATH", root+":"+os.Getenv("PATH")); err != nil {
		t.Fatal(err)
	}
	app := &App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Layout: layout, DB: db}
	b, err := backend.Detect("darwin", backend.SelectorPodmanMachine)
	if err != nil {
		t.Fatal(err)
	}
	proj := &project.Project{ID: "demo", Manifest: &manifest.Manifest{
		Container: manifest.ContainerSpec{Image: "busybox:latest", Workdir: "/workspace"},
		Volumes:   map[string]manifest.VolumeSpec{},
	}}
	return app, proj, b
}

// fakeScript builds a podman fake that logs invocations and fails start.
func fakeScript(logPath string) string {
	return "#!/bin/sh\n" +
		"[ \"$1\" = --remote ] && shift 3\n" +
		"echo \"$@\" >> " + logPath + "\n" +
		"case \"$1\" in\n" +
		"  system) echo \"podman-machine-default true\"; exit 0 ;;\n" +
		"  create) echo container-9; exit 0 ;;\n" +
		"  start) exit 1 ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
}

// TestCheckpointSurvivesStartFailure: when a rebuild from an internal
// checkpoint fails at container start, the checkpoint must stay unused so a
// later `up` retries from it instead of silently falling back to the base
// image (P0 acceptance: never lose the writable layer on create/start fail).
func TestCheckpointSurvivesStartFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "podman.log")
	app, proj, b := newCheckpointFallbackApp(t, fakeScript(logPath))
	defer app.Close()
	if err := app.DB.UpsertCheckpoint(state.Checkpoint{
		ProjectID: proj.ID,
		ImageRef:  "sha256:ckpt1",
		Platform:  platformOf(b),
		Reason:    "drift",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	// rebuild from the checkpoint; start fails
	rt := podman.New(b, "")
	err := app.provisionContainer(context.Background(), proj, b, rt, nil, "")
	if err == nil || !strings.Contains(err.Error(), "start") {
		t.Fatalf("expected start failure, got %v", err)
	}
	// the checkpoint must still be usable for the next attempt
	cp, cerr := app.DB.LatestCheckpoint(proj.ID)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if !cp.RestoredAt.IsZero() {
		t.Fatalf("checkpoint consumed on failed start: restored_at=%s", cp.RestoredAt)
	}
}

// TestCheckpointConsumedOnSuccessfulStart: on the happy path the checkpoint
// is marked restored only after create+start+record all succeed.
func TestCheckpointConsumedOnSuccessfulStart(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "podman.log")
	script := "#!/bin/sh\n" +
		"[ \"$1\" = --remote ] && shift 3\n" +
		"echo \"$@\" >> " + logPath + "\n" +
		"case \"$1\" in\n" +
		"  system) echo \"podman-machine-default true\"; exit 0 ;;\n" +
		"  create) echo container-9; exit 0 ;;\n" +
		"  start) exit 0 ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
	app, proj, b := newCheckpointFallbackApp(t, script)
	defer app.Close()
	if err := app.DB.UpsertCheckpoint(state.Checkpoint{
		ProjectID: proj.ID,
		ImageRef:  "sha256:ckpt1",
		Platform:  platformOf(b),
		Reason:    "drift",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	rt := podman.New(b, "")
	if err := app.provisionContainer(context.Background(), proj, b, rt, nil, ""); err != nil {
		t.Fatal(err)
	}
	cp, cerr := app.DB.LatestCheckpoint(proj.ID)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if cp.RestoredAt.IsZero() {
		t.Fatalf("checkpoint not consumed after successful start")
	}
}
