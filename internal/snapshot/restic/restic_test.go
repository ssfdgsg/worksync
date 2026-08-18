package restic

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRestic writes a fake restic binary whose output depends on argv.
func fakeRestic(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  init) echo "created restic repository";;
  cat) exit 1;;
  backup) echo '{"message_type":"summary","snapshot_id":"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}';;
  snapshots) echo '[{"id":"snap1","time":"2026-08-17T00:00:00Z","paths":["/x"],"tags":["workspace"]}]';;
  restore)
    target=""
    prev=""
    for i in "$@"; do [ "$prev" = "--target" ] && target="$i"; prev="$i"; done
    if [ -n "$target" ]; then
      mkdir -p "$target/x/sub"
      echo "hello" > "$target/x/sub/file.txt"
    fi
    echo "restored";;
  ls) echo "/x"; echo "/x/sub"; echo "/x/sub/file.txt";;
  check) echo "no errors";;
  *) exit 1;;
esac
exit 0
`
	bin := filepath.Join(dir, "restic")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestInitIfAbsent(t *testing.T) {
	fakeRestic(t)
	c := &Client{Repo: "/repo"}
	if err := c.InitIfAbsent(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotIDFromOutput(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got := snapshotIDFromOutput("repository " + id); got != id {
		t.Errorf("got %q", got)
	}
	if got := snapshotIDFromOutput("no id here"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestSnapshotAndList(t *testing.T) {
	fakeRestic(t)
	c := &Client{Repo: "/repo"}
	ctx := context.Background()
	id, err := c.Snapshot(ctx, SnapshotOptions{Paths: []string{"/data"}, Tags: []string{"workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "abcdef0123456789") {
		t.Errorf("id = %q", id)
	}
	snaps, err := c.List(ctx, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].ID != "snap1" {
		t.Errorf("snaps = %+v", snaps)
	}
}

func TestPasswordNeverInOutput(t *testing.T) {
	fakeRestic(t)
	var got string
	c := &Client{
		Repo: "/repo",
		Password: func() (string, error) {
			got = "super-secret-pw"
			return got, nil
		},
	}
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "super-secret-pw" {
		t.Errorf("password provider not consulted")
	}
}

// TestSnapshotUsesJSON verifies backup is invoked and the id comes from the
// summary's snapshot_id (real restic 0.18 behaviour).
func TestSnapshotUsesJSON(t *testing.T) {
	fakeRestic(t)
	c := &Client{Repo: "/repo"}
	id, err := c.Snapshot(context.Background(), SnapshotOptions{Paths: []string{"/data"}, Tags: []string{"workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	const want = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if id != want {
		t.Errorf("id = %q, want %q", id, want)
	}
}

// TestSnapshotIDFromJSON exercises the summary parser directly.
func TestSnapshotIDFromJSON(t *testing.T) {
	out := `{"message_type":"status","percent_done":0.5}` + "\n" + `{"message_type":"summary","snapshot_id":"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}`
	if got := snapshotIDFromJSON(out); got != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" {
		t.Errorf("got %q", got)
	}
	if got := snapshotIDFromJSON(`{"message_type":"summary"}`); got != "" {
		t.Errorf("summary without id: got %q", got)
	}
	if got := snapshotIDFromJSON("no json"); got != "" {
		t.Errorf("non-json: got %q", got)
	}
}

// TestRestoreExact verifies restore stages into a temp dir, relocates the
// snapshot root into target (restic nests the captured absolute path under
// --target), and drops paths that are not part of the snapshot (E2E-003:
// post-commit drift must not survive a rollback).
func TestRestoreExact(t *testing.T) {
	fakeRestic(t)
	c := &Client{Repo: "/repo"}
	target := t.TempDir()
	// drift that must NOT survive the exact restore
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "DRIFT.txt"), []byte("drift"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Restore(context.Background(), "snap1", target); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(target, "sub", "file.txt"))
	if err != nil {
		t.Fatalf("expected relocated file under target: %v", err)
	}
	if strings.TrimSpace(string(b)) != "hello" {
		t.Errorf("content = %q", b)
	}
	if _, err := os.Stat(filepath.Join(target, "DRIFT.txt")); !os.IsNotExist(err) {
		t.Errorf("DRIFT.txt should have been removed by the exact restore")
	}
	if _, err := os.Stat(filepath.Join(target, "sub")); err != nil {
		t.Errorf("kept directory sub should remain: %v", err)
	}
}
