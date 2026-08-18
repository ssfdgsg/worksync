package lock

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proj.lock")
	l, err := Acquire(context.Background(), path, "op-1", "up")
	if err != nil {
		t.Fatal(err)
	}
	info := l.Info()
	if info.PID == 0 || info.OperationID != "op-1" || info.Command != "up" {
		t.Errorf("info = %+v", info)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	// reacquire must succeed after release
	l2, err := Acquire(context.Background(), path, "op-2", "status")
	if err != nil {
		t.Fatal(err)
	}
	l2.Release()
}

func TestLockHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proj.lock")
	l, err := Acquire(context.Background(), path, "op-1", "commit")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	// A second acquisition (different fd) must report ErrHeld with holder info.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = Acquire(ctx, path, "op-2", "push")
	if err == nil {
		t.Fatal("second acquire should fail")
	}
	var held *ErrHeld
	if !errors.As(err, &held) {
		t.Fatalf("want ErrHeld, got %T: %v", err, err)
	}
	if held.Holder.OperationID != "op-1" || held.Holder.PID == 0 {
		t.Errorf("holder = %+v", held.Holder)
	}
	if !held.Alive {
		t.Errorf("holder should be alive")
	}
}

func TestReadInfoRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proj.lock")
	l, err := Acquire(context.Background(), path, "op-9", "stop")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	info, err := ReadInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.OperationID != "op-9" || info.Command != "stop" {
		t.Errorf("info = %+v", info)
	}
}

func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(1) { // init is always alive on unix
		t.Log("pid 1 not considered alive (unexpected on unix, but not fatal)")
	}
	if ProcessAlive(99999999) {
		t.Error("nonexistent pid considered alive")
	}
}
