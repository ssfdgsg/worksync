package state

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"worksync/internal/refs"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestProjectCRUD(t *testing.T) {
	d := openTestDB(t)
	p := Project{ID: "proj-a", ManifestPath: "/x/worksync.yaml", ManifestDigest: "sha256:aa", Backend: "native-podman"}
	if err := d.UpsertProject(p); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetProject("proj-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.ManifestPath != "/x/worksync.yaml" || string(got.Backend) != "native-podman" {
		t.Errorf("got %+v", got)
	}
	if _, err := d.GetProject("missing"); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestContainerLifecycleTransitions(t *testing.T) {
	d := openTestDB(t)
	if err := d.UpsertProject(Project{ID: "p", ManifestPath: "/x", ManifestDigest: "sha256:bb"}); err != nil {
		t.Fatal(err)
	}
	st, err := d.TransitionWithLock("p", "up")
	if err != nil {
		t.Fatal(err)
	}
	if st != StateProvisioning {
		t.Errorf("state = %s", st)
	}
	// seed a container row and run start/stop/commit cycle
	c := Container{ProjectID: "p", Name: "wb-p", ImageTag: "node:24", ImageRef: "sha256:cc", State: StateProvisioning}
	if err := d.UpsertContainer(c); err != nil {
		t.Fatal(err)
	}
	if st, _ = d.TransitionWithLock("p", "provision"); st != StateRunning {
		t.Errorf("provision -> %s", st)
	}
	if st, _ = d.TransitionWithLock("p", "stop"); st != StateStopped {
		t.Errorf("stop -> %s", st)
	}
	if st, _ = d.TransitionWithLock("p", "stop"); st != StateStopped {
		t.Errorf("idempotent stop -> %s", st)
	}
	if st, _ = d.TransitionWithLock("p", "start"); st != StateRunning {
		t.Errorf("start -> %s", st)
	}
	if st, _ = d.TransitionWithLock("p", "commit"); st != StateCommitting {
		t.Errorf("commit -> %s", st)
	}
	if st, _ = d.TransitionWithLock("p", "commit-done"); st != StateRunning {
		t.Errorf("commit-done(recover running) -> %s", st)
	}
}

func TestIllegalTransition(t *testing.T) {
	if _, err := Transition(StateAbsent, "stop"); err == nil {
		t.Fatal("expected illegal transition")
	}
	if st, err := Transition(StateRunning, "start"); err != nil || st != StateRunning {
		t.Fatalf("start from running should be idempotent (design §13.1), got %s/%v", st, err)
	}
	if _, err := Transition(StateAbsent, "stop"); err == nil {
		t.Fatal("stop from absent should error")
	}
	if _, err := Transition(StateStopped, "rm"); err != nil {
		t.Fatal("rm from stopped should be allowed")
	}
}

func TestOperationsJournal(t *testing.T) {
	d := openTestDB(t)
	_, err := d.StartOperation(Operation{ProjectID: "p", Kind: OpUp})
	if err != nil {
		t.Fatal(err)
	}
	running, err := d.FindRunningOperations()
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0].Kind != OpUp || running[0].PID == 0 {
		t.Errorf("running = %+v", running)
	}
	if err := d.FinishOperation(running[0].ID, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.FindRunningOperations(); len(n) != 0 {
		t.Errorf("still running: %+v", n)
	}
	// failed operation
	id2, _ := d.StartOperation(Operation{ProjectID: "p", Kind: OpCommit})
	if err := d.FinishOperation(id2, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	ops, err := d.FindRunningOperations()
	if err != nil || len(ops) != 0 {
		t.Errorf("ops = %+v err = %v", ops, err)
	}
}

func TestRefsStore(t *testing.T) {
	d := openTestDB(t)
	r, err := refs.New("sha256:"+strings.Repeat("ab", 32), time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.PutRef("proj", "latest", r); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetRef("proj", "latest")
	if err != nil {
		t.Fatal(err)
	}
	if got.Commit != r.Commit {
		t.Errorf("commit = %s", got.Commit)
	}
	if _, err := d.GetRef("proj", "nope"); err != ErrNotFound {
		t.Errorf("want ErrNotFound got %v", err)
	}
}

func TestWALMode(t *testing.T) {
	d := openTestDB(t)
	var mode string
	if err := d.sql.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal mode = %q, want wal", mode)
	}
}
