package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"worksync/internal/backend"
	"worksync/internal/project"
	"worksync/internal/state"
	"worksync/internal/store"
)

// newCheckpointTestApp builds an App with an isolated data root and a fake
// podman binary that logs invocations.
func newCheckpointTestApp(t *testing.T, logPath string) (*App, *project.Project, backend.Backend, string) {
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
	if err := db.UpsertContainer(state.Container{ProjectID: "demo", Name: "worksync-demo", State: state.StateRunning, ContainerID: "container-1"}); err != nil {
		t.Fatal(err)
	}
	app := &App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Layout: layout, DB: db}
	b, err := backend.Detect("darwin", backend.SelectorPodmanMachine)
	if err != nil {
		t.Fatal(err)
	}
	return app, &project.Project{ID: "demo"}, b, filepath.Join(root, "podman")
}

func TestCheckpointAndReplacePreservesWritableLayer(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "podman.log")
	binPath := filepath.Join(dir, "podman")
	script := "#!/bin/sh\n" +
		"[ \"$1\" = --remote ] && shift 3\n" +
		"echo \"$@\" >> " + logPath + "\n" +
		"case \"$1\" in\n" +
		"  system) echo \"podman-machine-default true\"; exit 0 ;;\n" +
		"  stop) exit 0 ;;\n" +
		"  commit) echo \"sha256:checkpoint\"; exit 0 ;;\n" +
		"  rm) exit 0 ;;\n" +
		"esac\n" +
		"exit 1\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	app, proj, b, _ := newCheckpointTestApp(t, logPath)
	defer app.Close()
	// the fake podman must be the one used; patch backend name and env
	_ = os.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	image, err := app.checkpointAndReplace(context.Background(), proj, b, "ports")
	if err != nil {
		t.Fatal(err)
	}
	if image != "sha256:checkpoint" {
		t.Fatalf("checkpoint image = %q", image)
	}
	cp, err := app.DB.LatestCheckpoint(proj.ID)
	if err != nil {
		t.Fatalf("checkpoint not recorded: %v", err)
	}
	if cp.ImageRef != "sha256:checkpoint" || cp.Reason != "ports" {
		t.Fatalf("checkpoint row = %+v", cp)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(logBytes))
	want := []string{"system", "connection", "ls", "--format", "{{.Name}}", "{{.Default}}", "stop", "--time", "10", "container-1", "commit", "container-1", "rm", "container-1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("podman sequence = %q, want %q", got, want)
	}
}

func TestCheckpointAndReplaceKeepsContainerOnCommitFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "podman.log")
	binPath := filepath.Join(dir, "podman")
	script := "#!/bin/sh\n" +
		"[ \"$1\" = --remote ] && shift 3\n" +
		"echo \"$@\" >> " + logPath + "\n" +
		"case \"$1\" in\n" +
		"  system) echo \"podman-machine-default true\"; exit 0 ;;\n" +
		"  stop) exit 0 ;;\n" +
		"  commit) exit 1 ;;\n" +
		"  rm) exit 0 ;;\n" +
		"esac\n" +
		"exit 1\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	app, proj, b, _ := newCheckpointTestApp(t, logPath)
	defer app.Close()
	_ = os.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if _, err := app.checkpointAndReplace(context.Background(), proj, b, "ports"); err == nil {
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
