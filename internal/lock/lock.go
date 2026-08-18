// Package lock implements per-project mutual exclusion with crash-safe
// semantics (design §21): a lock file records PID, start time and operation
// ID; stale locks are only cleared after checking the owning process.
package lock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Info is the metadata written into a lock file.
type Info struct {
	PID         int       `json:"pid"`
	StartedAt   time.Time `json:"startedAt"`
	OperationID string    `json:"operationID"`
	Host        string    `json:"host"`
	Command     string    `json:"command,omitempty"`
}

// Lock is a held exclusive lock.
type Lock struct {
	path string
	f    *os.File
	info Info
}

// ErrHeld reports that another process holds the lock; the holder's Info is
// attached for user-facing diagnostics.
type ErrHeld struct {
	Path   string
	Holder Info
	Alive  bool
}

func (e *ErrHeld) Error() string {
	state := "alive"
	if !e.Alive {
		state = "not running (stale)"
	}
	return fmt.Sprintf("project is locked by operation %s (pid %d, started %s, %s) in %s",
		e.Holder.OperationID, e.Holder.PID, e.Holder.StartedAt.Format(time.RFC3339), state, e.Path)
}

// Acquire takes the exclusive flock on path, retrying until ctx is done.
// operationID and command describe the mutating operation for diagnostics.
func Acquire(ctx context.Context, path, operationID, command string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	for {
		err = flock(f, false)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, fmt.Errorf("flock %s: %w", path, err)
		}
		// Someone holds it: report who (design §21 diagnostics).
		if info, rerr := ReadInfo(path); rerr == nil {
			return nil, &ErrHeld{Path: path, Holder: info, Alive: ProcessAlive(info.PID)}
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("timed out waiting for lock %s: %w", path, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	info := Info{
		PID:         os.Getpid(),
		StartedAt:   time.Now().UTC(),
		OperationID: operationID,
		Host:        hostname(),
		Command:     command,
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		f.Close()
		return nil, err
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(info); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{path: path, f: f, info: info}, nil
}

// Info returns the holder metadata of this lock.
func (l *Lock) Info() Info { return l.info }

// Release drops the lock. Safe to call multiple times.
func (l *Lock) Release() error {
	if l.f == nil {
		return nil
	}
	flock(l.f, true)
	err := l.f.Close()
	l.f = nil
	return err
}

// ReadInfo reads the metadata of a lock file without taking the lock.
func ReadInfo(path string) (Info, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Info{}, err
	}
	var info Info
	if err := json.Unmarshal(b, &info); err != nil {
		return Info{}, err
	}
	return info, nil
}

// ProcessAlive reports whether a process with the given pid exists.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
