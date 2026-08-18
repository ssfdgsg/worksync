// Package remote implements the remote store over SSH/SFTP (design §16.3):
// versioned layout, content-addressed OCI blob objects, restic repositories
// for volume data (restic's native SFTP support), and ref files with
// compare-and-swap uploads. All SSH session setup is delegated to the ssh
// and sftp binaries so the user's SSH agent provides credentials; worksync
// never stores keys (design §20.1).
package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"worksync/internal/executil"
	"worksync/internal/transport/sshurl"
)

// Version is the remote store layout version (design §16.3).
const Version = "1"

// Endpoint is a parsed remote location.
type Endpoint struct {
	URL *sshurl.URL
}

// NewEndpoint builds an Endpoint from an ssh:// URL.
func NewEndpoint(u *sshurl.URL) *Endpoint { return &Endpoint{URL: u} }

// shellQuote wraps s in single quotes for remote shell command lines.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// userHost renders user@host for ssh/sftp invocation.
func (e *Endpoint) userHost() string {
	if e.URL.User != "" {
		return e.URL.User + "@" + e.URL.Host
	}
	return e.URL.Host
}

// sshRun runs a remote command via the ssh binary.
func (e *Endpoint) sshRun(ctx context.Context, cmd string) (executil.Result, error) {
	args := []string{}
	if e.URL.Port != "" {
		args = append(args, "-p", e.URL.Port)
	}
	args = append(args, e.userHost(), cmd)
	return executil.Run(ctx, "ssh", args, executil.WithRedact(cmd))
}

// sftpBatch runs a batch of sftp commands via stdin (-b -).
func (e *Endpoint) sftpBatch(ctx context.Context, batch []string) (executil.Result, error) {
	args := []string{"-q", "-b", "-"}
	if e.URL.Port != "" {
		args = append(args, "-P", e.URL.Port)
	}
	args = append(args, e.userHost())
	script := strings.Join(batch, "\n") + "\n"
	return executil.Run(ctx, "sftp", args, executil.WithStdin(strings.NewReader(script)))
}

// Exists reports whether a remote path exists (single ls in a batch).
func (e *Endpoint) Exists(ctx context.Context, remotePath string) (bool, error) {
	res, err := e.sftpBatch(ctx, []string{"ls -l " + shellQuote(remotePath)})
	if err != nil {
		if _, ok := err.(*executil.Error); ok {
			return false, nil // sftp ls failure == not found
		}
		return false, err
	}
	return res.ExitCode == 0, nil
}

// MkdirAll creates remote directories via a remote shell.
func (e *Endpoint) MkdirAll(ctx context.Context, remoteDir string) error {
	_, err := e.sshRun(ctx, "mkdir -p "+shellQuote(remoteDir))
	return err
}

// Upload copies a local file to a remote path atomically (.partial + rename).
func (e *Endpoint) Upload(ctx context.Context, localPath, remotePath string) error {
	partial := remotePath + ".partial-" + shortRand()
	if err := e.MkdirAll(ctx, filepathDir(remotePath)); err != nil {
		return err
	}
	batch := []string{
		"put " + shellQuote(localPath) + " " + shellQuote(partial),
		"rename " + shellQuote(partial) + " " + shellQuote(remotePath),
	}
	if _, err := e.sftpBatch(ctx, batch); err != nil {
		return fmt.Errorf("upload %s: %w", remotePath, err)
	}
	return nil
}

// Download fetches a remote file to a local destination.
func (e *Endpoint) Download(ctx context.Context, remotePath, localPath string) error {
	batch := []string{"get " + shellQuote(remotePath) + " " + shellQuote(localPath)}
	if _, err := e.sftpBatch(ctx, batch); err != nil {
		return fmt.Errorf("download %s: %w", remotePath, err)
	}
	return nil
}

// List returns the entry names of a remote directory via a remote shell.
func (e *Endpoint) List(ctx context.Context, remoteDir string) ([]string, error) {
	res, err := e.sshRun(ctx, "ls -1 "+shellQuote(remoteDir))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.Contains(line, " ") {
			out = append(out, line)
		}
	}
	return out, nil
}

// FindRecursive returns the file paths (relative to dir) of every file under
// the remote dir, used to mirror a restic repository object-by-object.
func (e *Endpoint) FindRecursive(ctx context.Context, dir string) ([]string, error) {
	res, err := e.sshRun(ctx, "find "+shellQuote(dir)+" -type f")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rel := strings.TrimPrefix(line, strings.TrimSuffix(dir, "/")+"/")
		if rel != line {
			out = append(out, rel)
		}
	}
	return out, nil
}

func filepathDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "."
	}
	return p[:i]
}

func shortRand() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "0"
	}
	return hex.EncodeToString(b)
}

// --- store layout helpers (design §16.3) ---

// Base returns the resolved remote root: an absolute path, or a ~-relative
// path resolved against the remote home.
func (e *Endpoint) Base(home string) string {
	if strings.HasPrefix(e.URL.Path, "/") {
		return e.URL.Path
	}
	return home + "/" + strings.TrimPrefix(e.URL.Path, "~/")
}

// VersionFile is the store marker path.
func (e *Endpoint) VersionFile(base string) string { return base + "/VERSION" }

// ProjectBase is the per-project remote root.
func (e *Endpoint) ProjectBase(base, projectID string) string {
	return base + "/projects/" + projectID
}

// RefPath returns the remote ref file path.
func (e *Endpoint) RefPath(base, projectID, refName string) string {
	return e.ProjectBase(base, projectID) + "/refs/" + refName + ".json"
}

// CommitPath returns the remote descriptor path.
func (e *Endpoint) CommitPath(base, projectID, hexDigest string) string {
	return e.ProjectBase(base, projectID) + "/commits/" + hexDigest + ".json"
}

// BlobPath returns the remote OCI blob path (deduplicated across commits).
func (e *Endpoint) BlobPath(base, projectID, hexDigest string) string {
	return e.ProjectBase(base, projectID) + "/objects/oci/blobs/sha256/" + hexDigest
}

// ResticRepo returns the remote restic repository location.
func (e *Endpoint) ResticRepo(base, projectID string) string {
	return e.ProjectBase(base, projectID) + "/objects/restic"
}

// ArchivePath returns the remote whole-environment archive path.
func (e *Endpoint) ArchivePath(base, projectID, hexDigest string) string {
	return e.ProjectBase(base, projectID) + "/objects/oci/archives/" + hexDigest + ".tar"
}
