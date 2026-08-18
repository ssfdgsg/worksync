// Package restic wraps the restic binary for local snapshots, restore and
// repository copy (design §10, §14, §16). All invocation happens through the
// restic CLI; repositories are always encrypted (design §20.1) and the
// password never appears on the command line.
package restic

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"worksync/internal/executil"
)

// PasswordProvider supplies the repository encryption password.
type PasswordProvider func() (string, error)

// Client talks to one restic repository. repo is a restic location (empty
// means use RESTIC_REPOSITORY from the environment).
type Client struct {
	Repo     string
	Password PasswordProvider
	Env      []string
}

func (c *Client) env() ([]string, error) {
	env := append([]string{}, os.Environ()...)
	env = append(env, c.Env...)
	if c.Repo != "" {
		env = append(env, "RESTIC_REPOSITORY="+c.Repo)
	}
	if c.Password != nil {
		pw, err := c.Password()
		if err != nil {
			return nil, err
		}
		if pw != "" {
			env = append(env, "RESTIC_PASSWORD="+pw)
		}
	}
	return env, nil
}

func (c *Client) run(ctx context.Context, args ...string) (executil.Result, error) {
	return c.Cmd(ctx, args...)
}

// Cmd runs an arbitrary restic command with repository/password applied.
func (c *Client) Cmd(ctx context.Context, args ...string) (executil.Result, error) {
	env, err := c.env()
	if err != nil {
		return executil.Result{}, err
	}
	return executil.Run(ctx, "restic", args, executil.WithEnv(env))
}

// RunCatConfig probes whether the repository exists (design §16.3).
func (c *Client) RunCatConfig(ctx context.Context) (executil.Result, error) {
	return c.Cmd(ctx, "cat", "config")
}

// Init initializes an encrypted repository.
func (c *Client) Init(ctx context.Context) error {
	_, err := c.run(ctx, "init")
	return err
}

// InitIfAbsent initializes the repository only when it does not exist yet.
func (c *Client) InitIfAbsent(ctx context.Context) error {
	res, _ := c.run(ctx, "cat", "config")
	if res.ExitCode == 0 {
		return nil
	}
	return c.Init(ctx)
}

// SnapshotOptions controls a snapshot call.
type SnapshotOptions struct {
	Paths []string
	Tags  []string
	Host  string
}

// Snapshot creates a snapshot of the given paths and returns its ID.
func (c *Client) Snapshot(ctx context.Context, opts SnapshotOptions) (string, error) {
	args := []string{"backup", "--json"}
	for _, t := range opts.Tags {
		args = append(args, "--tag", t)
	}
	if opts.Host != "" {
		args = append(args, "--host", opts.Host)
	}
	args = append(args, opts.Paths...)
	res, err := c.run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("restic backup: %w", err)
	}
	// restic's non-JSON output prints only a short snapshot id; the --json
	// summary carries the full 64-hex id the descriptor requires (§14.2).
	if id := snapshotIDFromJSON(res.Stdout); id != "" {
		return id, nil
	}
	// fallback for fakes / older restic: a full-length id anywhere in output.
	if id := snapshotIDFromOutput(res.Combined()); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("restic backup produced no snapshot id")
}

// snapshotIDFromJSON extracts the 64-hex snapshot id from restic backup
// --json output (the "summary" message line).
func snapshotIDFromJSON(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		mt, _ := msg["message_type"].(string)
		sid, _ := msg["snapshot_id"].(string)
		if mt == "summary" && len(sid) == 64 && isHex(sid) {
			return sid
		}
	}
	return ""
}

// snapshotIDFromOutput extracts the 64-hex snapshot ID from restic output.
func snapshotIDFromOutput(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 64 && isHex(line) {
			return line
		}
		fields := strings.Fields(line)
		for _, f := range fields {
			if len(f) == 64 && isHex(f) {
				return f
			}
		}
	}
	return ""
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// SnapshotInfo is a row of the restic snapshot list.
type SnapshotInfo struct {
	ID       string   `json:"id"`
	Time     string   `json:"time"`
	Paths    []string `json:"paths"`
	Tags     []string `json:"tags"`
	Hostname string   `json:"hostname"`
}

// List returns snapshots, optionally filtered by tag, newest first.
func (c *Client) List(ctx context.Context, tag string) ([]SnapshotInfo, error) {
	args := []string{"snapshots", "--json"}
	if tag != "" {
		args = append(args, "--tag", tag)
	}
	res, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var out []SnapshotInfo
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return nil, fmt.Errorf("parse restic snapshots: %w", err)
	}
	return out, nil
}

// Restore restores snapshot id into target, making target exactly match the
// snapshot (E2E-003): paths not present in the snapshot are removed, so a
// rolled-back workspace does not keep post-commit drift.
//
// restic restore nests the snapshot's original absolute path under --target
// (restore --target /T of a snapshot of /a/b yields /T/a/b). To make the
// target directory itself receive the snapshot root, restore into a staging
// dir on the same filesystem and relocate the content (§14.2 rollback).
func (c *Client) Restore(ctx context.Context, id, target string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	// Staging on the same filesystem keeps the relocation atomic; the system
	// temp dir is a fallback when the target parent is not writable.
	stage, err := os.MkdirTemp(filepath.Dir(target), ".worksync-restore-")
	if err != nil {
		stage, err = os.MkdirTemp("", "worksync-restore-")
		if err != nil {
			return err
		}
	}
	defer os.RemoveAll(stage)
	if _, err := c.run(ctx, "restore", id, "--target", stage); err != nil {
		return fmt.Errorf("restic restore: %w", err)
	}
	paths, err := c.snapshotPaths(ctx, id)
	if err != nil {
		return err
	}
	for _, p := range paths {
		rel := strings.TrimPrefix(filepath.Clean(p), "/")
		src := filepath.Join(stage, rel)
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("restored content %s not found: %w", src, err)
		}
		if err := moveContents(src, target); err != nil {
			return err
		}
	}
	// Exact rollback: remove everything under target that is not part of the
	// snapshot.
	keep, err := c.snapshotFileSet(ctx, id)
	if err != nil {
		return err
	}
	removed, err := removeNotInSnapshot(target, "", keep)
	if err != nil {
		return err
	}
	if removed > 0 {
		fmt.Fprintf(os.Stderr, "restore: removed %d path(s) not present in snapshot\n", removed)
	}
	return nil
}

// snapshotFileSet returns the set of snapshot-relative paths stored in
// snapshot id ("/" for the snapshot root, "/a/b" for children), collected
// from `restic ls`.
func (c *Client) snapshotFileSet(ctx context.Context, id string) (map[string]bool, error) {
	paths, err := c.snapshotPaths(ctx, id)
	if err != nil {
		return nil, err
	}
	roots := make([]string, 0, len(paths))
	for _, p := range paths {
		roots = append(roots, filepath.Clean(p))
	}
	res, err := c.run(ctx, "ls", id)
	if err != nil {
		return nil, fmt.Errorf("restic ls: %w", err)
	}
	keep := map[string]bool{"/": true}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		abs := filepath.Clean(line)
		for _, root := range roots {
			switch {
			case abs == root:
				keep["/"] = true
			case strings.HasPrefix(abs, root+"/"):
				keep[strings.TrimPrefix(abs, root)] = true
			default:
				continue
			}
			break
		}
	}
	return keep, nil
}

// removeNotInSnapshot deletes every entry under dir whose snapshot-relative
// path (baseRel + "/" + name) is not in keep, recursing into kept
// directories. Returns the number of removed entries.
func removeNotInSnapshot(dir, baseRel string, keep map[string]bool) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		rel := baseRel + "/" + e.Name()
		full := filepath.Join(dir, e.Name())
		if keep[rel] {
			if e.IsDir() {
				n, err := removeNotInSnapshot(full, rel, keep)
				removed += n
				if err != nil {
					return removed, err
				}
			}
			continue
		}
		if err := os.RemoveAll(full); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// snapshotPaths returns the paths captured in snapshot id.
func (c *Client) snapshotPaths(ctx context.Context, id string) ([]string, error) {
	res, err := c.run(ctx, "snapshots", "--json", id)
	if err != nil {
		return nil, err
	}
	var out []SnapshotInfo
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return nil, fmt.Errorf("parse restic snapshot %s: %w", id, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("restic snapshot %s not found", id)
	}
	return out[0].Paths, nil
}

// moveContents moves every entry of src into dst, merging directories
// (existing files in dst are left untouched, matching restic restore).
func moveContents(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			if err := moveContents(s, d); err != nil {
				return err
			}
			continue
		}
		if err := os.Rename(s, d); err != nil {
			return err
		}
	}
	return nil
}

// Check verifies the repository integrity.
func (c *Client) Check(ctx context.Context) error {
	_, err := c.run(ctx, "check")
	return err
}

// KeyFileProvider returns a PasswordProvider reading from a 0600 file.
func KeyFileProvider(path string) PasswordProvider {
	return func() (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read restic password file %s: %w", path, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
}

// EnsureRepoDir creates the repository directory.
func EnsureRepoDir(repoRoot string) error {
	return os.MkdirAll(filepath.Join(repoRoot, "repository"), 0o700)
}
