package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"worksync/internal/commit"
	"worksync/internal/oci"
	"worksync/internal/refs"
	"worksync/internal/snapshot/restic"
	"worksync/internal/store"
)

// Store drives push/pull/fetch for one project against one remote endpoint
// (design §16). Object transfer is content-addressed and deduplicated: only
// missing OCI blobs are uploaded, and volume data moves with restic copy's
// native deduplication.
type Store struct {
	EP              *Endpoint
	ProjectID       string
	Layout          *store.Layout
	ResticPassword  restic.PasswordProvider
	Stdout          io.Writer
	RemoteHome      string
	LocalResticRepo string
}

// Option configures a Store.
type Option func(*Store)

func WithStdout(w io.Writer) Option { return func(s *Store) { s.Stdout = w } }

// NewStore prepares a store for a project.
func NewStore(ep *Endpoint, projectID string, layout *store.Layout, pw restic.PasswordProvider) *Store {
	return &Store{
		EP:              ep,
		ProjectID:       projectID,
		Layout:          layout,
		ResticPassword:  pw,
		LocalResticRepo: filepath.Join(layout.ResticDir, "repository"),
	}
}

// home returns the remote home directory, fetching it once via ssh.
func (s *Store) home(ctx context.Context) (string, error) {
	if s.RemoteHome == "" {
		res, err := s.EP.sshRun(ctx, "printf %s \"$HOME\"")
		if err != nil {
			return "", fmt.Errorf("resolve remote home: %w", err)
		}
		s.RemoteHome = strings.TrimSpace(res.Stdout)
		if s.RemoteHome == "" {
			s.RemoteHome = "~"
		}
	}
	return s.RemoteHome, nil
}

// base returns the resolved store root.
func (s *Store) base(ctx context.Context) (string, error) {
	home, err := s.home(ctx)
	if err != nil {
		return "", err
	}
	return s.EP.Base(home), nil
}

// EnsureVersion writes the VERSION marker and remote bases.
func (s *Store) EnsureVersion(ctx context.Context) error {
	base, err := s.base(ctx)
	if err != nil {
		return err
	}
	if err := s.EP.MkdirAll(ctx, s.EP.ProjectBase(base, s.ProjectID)); err != nil {
		return err
	}
	exists, err := s.EP.Exists(ctx, s.EP.VersionFile(base))
	if err != nil {
		return err
	}
	if !exists {
		return s.writeRemoteText(ctx, s.EP.VersionFile(base), Version+"\n")
	}
	return nil
}

func (s *Store) writeRemoteText(ctx context.Context, remotePath, content string) error {
	tmp := s.tmpLocalPath("remote-" + shortRand() + "-" + filepath.Base(remotePath))
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	return s.EP.Upload(ctx, tmp, remotePath)
}

func (s *Store) tmpLocalPath(name string) string {
	_ = os.MkdirAll(s.Layout.StagingDir, 0o755)
	return filepath.Join(s.Layout.StagingDir, name)
}

// ---- refs ----

// RemoteRef reads a remote ref file.
func (s *Store) RemoteRef(ctx context.Context, name string) (refs.Ref, bool, error) {
	base, err := s.base(ctx)
	if err != nil {
		return refs.Ref{}, false, err
	}
	path := s.EP.RefPath(base, s.ProjectID, name)
	exists, err := s.EP.Exists(ctx, path)
	if err != nil {
		return refs.Ref{}, false, err
	}
	if !exists {
		return refs.Ref{}, false, nil
	}
	local := s.tmpLocalPath("remote-ref-" + name + ".json")
	defer os.Remove(local)
	if err := s.EP.Download(ctx, path, local); err != nil {
		return refs.Ref{}, false, err
	}
	b, err := os.ReadFile(local)
	if err != nil {
		return refs.Ref{}, false, err
	}
	var r refs.Ref
	if err := json.Unmarshal(b, &r); err != nil {
		return refs.Ref{}, false, fmt.Errorf("parse remote ref %s: %w", name, err)
	}
	if err := r.Validate(); err != nil {
		return refs.Ref{}, false, err
	}
	return r, true, nil
}

// PutRemoteRef uploads a ref with CAS semantics via one atomic batch
// (put .partial-cas + rename), the commit point of design §16.3.
func (s *Store) PutRemoteRef(ctx context.Context, name string, r refs.Ref) error {
	base, err := s.base(ctx)
	if err != nil {
		return err
	}
	path := s.EP.RefPath(base, s.ProjectID, name)
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := s.tmpLocalPath("ref-upload-" + name + ".json")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := s.EP.MkdirAll(ctx, filepathDir(path)); err != nil {
		return err
	}
	partial := path + ".partial-cas"
	batch := []string{
		"put " + shellQuote(tmp) + " " + shellQuote(partial),
		"rename " + shellQuote(partial) + " " + shellQuote(path),
	}
	if _, err := s.EP.sftpBatch(ctx, batch); err != nil {
		return fmt.Errorf("write remote ref %s: %w", name, err)
	}
	return nil
}

// ---- object transfer ----

// loadLocalCommit reads a local descriptor.
func (s *Store) loadLocalCommit(dg string) (*commit.Descriptor, error) {
	path := s.Layout.CommitDescriptorPath(strings.TrimPrefix(dg, "sha256:"))
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return commit.ParseJSON(b)
}

// CommitMissing returns the chain of commit digests (oldest first) the
// remote does not yet have, walking parents from head.
func (s *Store) CommitMissing(ctx context.Context, head string) ([]string, error) {
	base, err := s.base(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var missing []string
	cur := head
	for cur != "" {
		if seen[cur] {
			break
		}
		seen[cur] = true
		exists, err := s.EP.Exists(ctx, s.EP.CommitPath(base, s.ProjectID, strings.TrimPrefix(cur, "sha256:")))
		if err != nil {
			return nil, err
		}
		if exists {
			break
		}
		missing = append(missing, cur)
		desc, err := s.loadLocalCommit(cur)
		if err != nil {
			return nil, err
		}
		cur = desc.Parent
	}
	reverse(missing)
	return missing, nil
}

func reverse(ss []string) {
	for i, j := 0, len(ss)-1; i < j; i, j = i+1, j-1 {
		ss[i], ss[j] = ss[j], ss[i]
	}
}

// PushObjects uploads the missing commits' descriptors, blobs, archives and
// volume snapshots (restic copy); returns the number of uploads.
func (s *Store) PushObjects(ctx context.Context, head string) (int, error) {
	base, err := s.base(ctx)
	if err != nil {
		return 0, err
	}
	missing, err := s.CommitMissing(ctx, head)
	if err != nil {
		return 0, err
	}
	uploads := 0
	for _, dg := range missing {
		hexDg := strings.TrimPrefix(dg, "sha256:")
		desc, err := s.loadLocalCommit(dg)
		if err != nil {
			return uploads, err
		}
		descLocal := s.Layout.CommitDescriptorPath(hexDg)
		if err := s.EP.Upload(ctx, descLocal, s.EP.CommitPath(base, s.ProjectID, hexDg)); err != nil {
			return uploads, err
		}
		uploads++
		if desc.Environment.Image != "" {
			n, err := s.pushBlobs(ctx, base, dg)
			if err != nil {
				return uploads, err
			}
			uploads += n
			remoteArchive := s.EP.ArchivePath(base, s.ProjectID, hexDg)
			has, err := s.EP.Exists(ctx, remoteArchive)
			if err != nil {
				return uploads, err
			}
			if !has {
				archiveLocal := filepath.Join(s.Layout.OCIDir, hexDg, "image.tar")
				if _, err := os.Stat(archiveLocal); err == nil {
					if err := s.EP.Upload(ctx, archiveLocal, remoteArchive); err != nil {
						return uploads, err
					}
					uploads++
				}
			}
		}
		if len(desc.Snapshots) > 0 {
			n, err := s.pushResticObjects(ctx, base)
			if err != nil {
				return uploads, err
			}
			uploads += n
		}
	}
	return uploads, nil
}

func (s *Store) pushBlobs(ctx context.Context, base, dg string) (int, error) {
	blobDir := filepath.Join(s.Layout.OCIDir, strings.TrimPrefix(dg, "sha256:"), "blobs", "sha256")
	entries, err := os.ReadDir(blobDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		remote := s.EP.BlobPath(base, s.ProjectID, e.Name())
		exists, err := s.EP.Exists(ctx, remote)
		if err != nil {
			return n, err
		}
		if exists {
			continue
		}
		if err := s.EP.Upload(ctx, filepath.Join(blobDir, e.Name()), remote); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// pushResticObjects mirrors the local restic repository to the remote one
// file-by-file, so both sides share the same encrypted repository and every
// snapshot ID stays identical (E2E-002B: `restic copy` rewrites
// snapshot IDs because snapshots carry repository-local metadata; copying the
// underlying object files keeps IDs stable, so the `restic:<id>`
// references inside commit descriptors stay valid on the remote).
func (s *Store) pushResticObjects(ctx context.Context, base string) (int, error) {
	remoteRepo := s.EP.ResticRepo(base, s.ProjectID)
	remoteFiles, err := s.EP.FindRecursive(ctx, remoteRepo)
	if err != nil {
		// repository may not exist yet; treat as empty
		remoteFiles = nil
	}
	have := map[string]bool{}
	for _, f := range remoteFiles {
		have[f] = true
	}
	uploads := 0
	err = filepath.WalkDir(s.LocalResticRepo, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.LocalResticRepo, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if have[rel] {
			return nil
		}
		remote := remoteRepo + "/" + rel
		if err := s.EP.Upload(ctx, path, remote); err != nil {
			return err
		}
		have[rel] = true
		uploads++
		return nil
	})
	return uploads, err
}

// LocalHasCommit reports whether a commit is fully present locally
// (E2E-011): the descriptor plus every dependency it references — OCI
// archive/blobs for the environment, and each restic snapshot object inside
// the local repository.
func (s *Store) LocalHasCommit(dg string) bool {
	hexDg := strings.TrimPrefix(dg, "sha256:")
	b, err := os.ReadFile(s.Layout.CommitDescriptorPath(hexDg))
	if err != nil {
		return false
	}
	d, err := commit.ParseJSON(b)
	if err != nil {
		return false
	}
	if d.Environment.Image != "" {
		ociDir := filepath.Join(s.Layout.OCIDir, hexDg)
		if _, err := os.Stat(filepath.Join(ociDir, "image.tar")); err != nil {
			return false
		}
		if fi, err := os.Stat(filepath.Join(ociDir, "blobs", "sha256")); err != nil || !fi.IsDir() {
			return false
		}
	}
	for _, snapRef := range d.Snapshots {
		id := strings.TrimPrefix(snapRef, "restic:")
		snap := filepath.Join(s.LocalResticRepo, "snapshots", id)
		if _, err := os.Stat(snap); err != nil {
			return false
		}
	}
	return true
}

// PullDownloads fetches all commits from remoteHead (inclusive) not present
// locally: descriptors, OCI archives/blobs, and volume snapshots mirrored
// from the remote restic repository. Returns the downloaded commit digests
// (newest first) so callers can persist them in higher layers.
func (s *Store) PullDownloads(ctx context.Context, remoteHead string) ([]string, error) {
	base, err := s.base(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var need []string
	cur := remoteHead
	for cur != "" {
		if seen[cur] {
			break
		}
		seen[cur] = true
		if s.LocalHasCommit(cur) {
			break
		}
		need = append(need, cur)
		desc, ok, err := s.downloadCommit(ctx, base, cur)
		if err != nil {
			return nil, err
		}
		if ok {
			cur = desc.Parent
		} else {
			break
		}
	}
	// need is newest-first (head first). Descriptors are downloaded in the
	// walk above, so parent references are already available; the remaining
	// downloads are order-independent (archives/blobs/restic objects).
	for _, dg := range need {
		desc, err := s.loadLocalCommit(dg)
		if err != nil {
			return nil, err
		}
		if desc.Environment.Image != "" {
			if err := s.pullEnv(ctx, base, dg); err != nil {
				return nil, err
			}
		}
	}
	needRestic := false
	for _, dg := range need {
		desc, err := s.loadLocalCommit(dg)
		if err != nil {
			return nil, err
		}
		if len(desc.Snapshots) > 0 {
			needRestic = true
			break
		}
	}
	if needRestic {
		if _, err := s.pullResticObjects(ctx, base); err != nil {
			return nil, err
		}
	}
	// final completeness gate (E2E-011): every downloaded commit must now be
	// fully present, or the pull would silently proceed with broken deps.
	for _, dg := range need {
		if !s.LocalHasCommit(dg) {
			return nil, fmt.Errorf("commit %s incomplete after pull (missing OCI or restic objects)", shortDigestOf(dg))
		}
	}
	return need, nil // already newest-first: head was appended first
}

func shortDigestOf(dg string) string {
	if len(dg) > 12 {
		return dg[:12]
	}
	return dg
}

// downloadCommit fetches a remote descriptor into the local store.
func (s *Store) downloadCommit(ctx context.Context, base, dg string) (*commit.Descriptor, bool, error) {
	hexDg := strings.TrimPrefix(dg, "sha256:")
	remote := s.EP.CommitPath(base, s.ProjectID, hexDg)
	exists, err := s.EP.Exists(ctx, remote)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}
	local := s.Layout.CommitDescriptorPath(hexDg)
	if _, err := os.Stat(local); err == nil {
		b, err := os.ReadFile(local)
		if err != nil {
			return nil, false, err
		}
		d, err := commit.ParseJSON(b)
		return d, true, err
	}
	// E2E-011: stage the descriptor first; only atomically publish it after a
	// successful download+parse, so a partial fetch never leaves a descriptor
	// that later looks complete.
	stage := s.tmpLocalPath("commit-" + hexDg + ".json")
	if err := s.EP.Download(ctx, remote, stage); err != nil {
		return nil, false, err
	}
	b, err := os.ReadFile(stage)
	if err != nil {
		return nil, false, err
	}
	d, err := commit.ParseJSON(b)
	if err != nil {
		_ = os.Remove(stage)
		return nil, false, fmt.Errorf("parse staged descriptor %s: %w", hexDg, err)
	}
	if err := os.Rename(stage, local); err != nil {
		return nil, false, fmt.Errorf("publish descriptor %s: %w", hexDg, err)
	}
	return d, true, nil
}

// pullEnv fetches the environment archive for a commit, then extracts blobs.
func (s *Store) pullEnv(ctx context.Context, base, dg string) error {
	hexDg := strings.TrimPrefix(dg, "sha256:")
	dest := filepath.Join(s.Layout.OCIDir, hexDg)
	if err := os.MkdirAll(filepath.Join(dest, "blobs", "sha256"), 0o755); err != nil {
		return err
	}
	archive := filepath.Join(dest, "image.tar")
	if _, err := os.Stat(archive); err == nil {
		return nil
	}
	remoteArchive := s.EP.ArchivePath(base, s.ProjectID, hexDg)
	has, err := s.EP.Exists(ctx, remoteArchive)
	if err != nil {
		return err
	}
	if !has {
		return fmt.Errorf("remote environment archive for %s missing", hexDg)
	}
	if err := s.EP.Download(ctx, remoteArchive, archive); err != nil {
		return err
	}
	return oci.ExtractBlobs(archive, dest)
}

// pullResticObjects mirrors the remote restic repository into the local one
// file-by-file (E2E-009: a fresh clone ends up with a complete, usable local
// repository — config/keys included — so no local `restic init` is needed
// before the first restore).
//
// Security: the repository password is NEVER uploaded to the remote. restic's
// threat model assumes the encrypted repository may be public; keeping the
// password in the same trust domain as the ciphertext defeats encryption.
// Callers must ensure a local password exists (WORKSYNC_RESTIC_PASSWORD or
// the local password file) before restoring.
func (s *Store) pullResticObjects(ctx context.Context, base string) (int, error) {
	remoteRepo := s.EP.ResticRepo(base, s.ProjectID)
	remoteFiles, err := s.EP.FindRecursive(ctx, remoteRepo)
	if err != nil {
		return 0, fmt.Errorf("remote restic repository not found: %w", err)
	}
	downloads := 0
	for _, rel := range remoteFiles {
		local := filepath.Join(s.LocalResticRepo, filepath.FromSlash(rel))
		if _, err := os.Stat(local); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			return downloads, err
		}
		if err := s.EP.Download(ctx, remoteRepo+"/"+rel, local); err != nil {
			return downloads, err
		}
		downloads++
	}
	return downloads, nil
}

// ListRemoteCommits returns remote commit hex digests.
func (s *Store) ListRemoteCommits(ctx context.Context) ([]string, error) {
	base, err := s.base(ctx)
	if err != nil {
		return nil, err
	}
	dir := s.EP.ProjectBase(base, s.ProjectID) + "/commits"
	entries, err := s.EP.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	sort.Strings(entries)
	return entries, nil
}
