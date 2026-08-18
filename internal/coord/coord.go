// Package coord implements the commit coordinator (design §14.2): the
// end-to-end flow that freezes the environment and selected volumes into an
// immutable, content-addressed commit.
package coord

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"worksync/internal/commit"
	"worksync/internal/digest"
	"worksync/internal/manifest"
	"worksync/internal/oci"
	"worksync/internal/project"
	"worksync/internal/refs"
	"worksync/internal/runtime/podman"
	"worksync/internal/snapshot/restic"
	"worksync/internal/state"
	"worksync/internal/store"
	"worksync/internal/volume"
)

// Coordinator orchestrates one commit.
type Coordinator struct {
	Layout         *store.Layout
	DB             *state.DB
	Stdout         io.Writer
	Stderr         io.Writer
	ResticPassword restic.PasswordProvider

	// BackendDataRoot hosts managed volume paths (design §9.2).
	BackendDataRoot string
}

// Options controls a commit.
type Options struct {
	Message string
	// Volumes overrides the manifest commit.volumes selection (design §10).
	Volumes []string
}

// RefLatest is the default local ref name (design §15).
const RefLatest = "latest"

// EnvImageName is the stable local image name of a committed environment.
func EnvImageName(projectID string) string { return "worksync-" + projectID + ":latest" }

// localRestic returns a restic client for the local store repository.
func (c *Coordinator) localRestic() *restic.Client {
	return &restic.Client{
		Repo:     c.Layout.ResticDir + "/repository",
		Password: c.ResticPassword,
	}
}

// Commit runs the §14.2 flow. The caller must hold the project lock and have
// started a journaled operation.
func (c *Coordinator) Commit(ctx context.Context, rt *podman.Client, proj *project.Project, opts Options) (digest.Digest, error) {
	m := proj.Manifest
	container, err := c.DB.GetContainer(proj.ID)
	if err != nil {
		return "", fmt.Errorf("no container: %w", err)
	}
	if container.State != state.StateRunning && container.State != state.StateStopped {
		return "", fmt.Errorf("cannot commit while container is %s", container.State)
	}

	// 1. decide what to freeze (design §10).
	envCommit := true
	if m.Commit != nil {
		envCommit = m.Commit.Environment
	}
	volPolicies := map[string]volume.Policy{}
	for name, v := range m.Volumes {
		volPolicies[name] = v.Policy
	}
	var selectedVols []string
	if m.Commit != nil && len(m.Commit.Volumes) > 0 {
		selectedVols = m.Commit.Volumes
	}
	if len(opts.Volumes) > 0 {
		selectedVols = opts.Volumes
	}
	selection, err := volume.SelectCommit(volPolicies, selectedVols)
	if err != nil {
		return "", err
	}

	// 2. snapshot consistency mode (design §10.1).
	wasRunning := container.State == state.StateRunning
	stopBefore := true
	if m.Snapshot != nil {
		switch m.Snapshot.Mode {
		case "none":
			stopBefore = false
		case "command":
			stopBefore = false
			if err := c.runHooks(ctx, rt, container.ContainerID, m.Snapshot.Pre); err != nil {
				return "", err
			}
		}
	}
	if stopBefore && wasRunning {
		fmt.Fprintln(c.Stdout, "stopping container for a consistent snapshot (snapshot.mode=stop)...")
		if err := rt.Stop(ctx, container.ContainerID, 10); err != nil {
			return "", err
		}
	}
	defer func() {
		if stopBefore && wasRunning {
			_ = rt.Start(ctx, container.ContainerID)
		}
		if m.Snapshot != nil && m.Snapshot.Mode == "command" {
			_ = c.runHooks(ctx, rt, container.ContainerID, m.Snapshot.Post)
		}
	}()

	// 3. freeze the environment (design §14.2 steps 6-7).
	stageEnv := ""
	desc := commit.New(proj.ID, commit.Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH})
	desc.Message = opts.Message
	desc.ConfigDigest = string(proj.ManifestHash)
	if parent, err := c.DB.GetRef(proj.ID, RefLatest); err == nil {
		desc.Parent = parent.Commit
	}
	if envCommit {
		imgName := EnvImageName(proj.ID)
		fmt.Fprintln(c.Stdout, "freezing container rootfs...")
		if _, err := rt.CommitNamed(ctx, container.ContainerID, imgName); err != nil {
			return "", fmt.Errorf("podman commit: %w", err)
		}
		dg, err := rt.ResolveDigest(ctx, imgName)
		if err != nil {
			return "", err
		}
		desc.Environment.Base = m.Container.Image
		desc.Environment.Image = dg
		// export + verify + stage blobs
		stageEnv = filepath.Join(c.Layout.StagingDir, proj.ID+".env.tar")
		if err := rt.Save(ctx, imgName, stageEnv); err != nil {
			return "", fmt.Errorf("export environment: %w", err)
		}
		if _, err := oci.VerifyArchive(stageEnv); err != nil {
			return "", err
		}
		stageBlobs := filepath.Join(c.Layout.StagingDir, proj.ID+".blobs")
		_ = os.RemoveAll(stageBlobs)
		if err := oci.ExtractBlobs(stageEnv, stageBlobs); err != nil {
			return "", err
		}
	}

	// 4. freeze selected volumes with restic (design §14.2 step 8).
	if len(selection) > 0 {
		fmt.Fprintln(c.Stdout, "snapshotting volumes with restic...")
		rc := c.localRestic()
		if err := rc.InitIfAbsent(ctx); err != nil {
			return "", fmt.Errorf("restic init: %w", err)
		}
	}
	for _, name := range selection {
		v, ok := m.Volumes[name]
		if !ok {
			continue
		}
		path := volumeHostPath(v, c.BackendDataRoot, proj.ID, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Fprintf(c.Stdout, "  volume %s has no data yet; skipping\n", name)
			continue
		}
		fmt.Fprintf(c.Stdout, "  snapshotting %s...\n", name)
		id, err := c.localRestic().Snapshot(ctx, restic.SnapshotOptions{
			Paths: []string{path},
			Tags:  []string{proj.ID, name},
			Host:  "worksync-local",
		})
		if err != nil {
			return "", err
		}
		desc.Snapshots[name] = "restic:" + id
	}

	// 5. compute digest and stage the descriptor (design §14.2 steps 9-11).
	if err := desc.Validate(); err != nil {
		return "", err
	}
	if desc.Environment.Image == "" && len(desc.Snapshots) == 0 {
		return "", fmt.Errorf("commit has no environment and no volumes; nothing to commit")
	}
	dg, err := desc.Digest()
	if err != nil {
		return "", err
	}
	canon, err := commit.MarshalCanonical(&desc)
	if err != nil {
		return "", err
	}
	stageDesc := filepath.Join(c.Layout.StagingDir, dg.Hex()+".json")
	if err := os.WriteFile(stageDesc, canon, 0o644); err != nil {
		return "", err
	}

	// 6. verify dependencies exist locally (design §14.2 step 10).
	if err := c.verifyDeps(ctx, stageBlobsDir(c.Layout, proj.ID), &desc); err != nil {
		return "", err
	}

	// 7. atomically publish descriptor + move blobs into the store (11-12).
	finalDesc := c.Layout.CommitDescriptorPath(dg.Hex())
	if err := os.Rename(stageDesc, finalDesc); err != nil {
		return "", fmt.Errorf("publish descriptor: %w", err)
	}
	blobDest := filepath.Join(c.Layout.OCIDir, dg.Hex())
	if err := os.RemoveAll(blobDest); err != nil {
		return "", err
	}
	if err := os.Rename(stageBlobsDir(c.Layout, proj.ID), blobDest); err != nil {
		return "", fmt.Errorf("publish blobs: %w", err)
	}
	if envCommit {
		if err := os.Rename(stageEnv, filepath.Join(blobDest, "image.tar")); err != nil {
			return "", fmt.Errorf("publish environment archive: %w", err)
		}
	}

	// 8. persist in DB and update the local ref atomically (design §15).
	if err := c.DB.SaveCommit(state.Commit{
		Digest:         string(dg),
		ProjectID:      proj.ID,
		DescriptorJSON: canon,
		Parent:         desc.Parent,
		Message:        desc.Message,
		CreatedAt:      desc.CreatedAt,
	}); err != nil {
		return "", err
	}
	if err := c.advanceRef(proj.ID, string(dg)); err != nil {
		return "", err
	}
	fmt.Fprintf(c.Stdout, "committed %s\n", dg)
	return dg, nil
}

// advanceRef updates the local "latest" ref with CAS semantics. The project
// lock serializes local writers, so a DB read-compare-write is safe.
func (c *Coordinator) advanceRef(projectID, commitDigest string) error {
	cur, err := c.DB.GetRef(projectID, RefLatest)
	if err != nil && !errorsIs(err) {
		return err
	}
	expected := ""
	if err == nil {
		expected = cur.Commit
	}
	next, err := refs.Advance(cur, expected, commitDigest, time.Now().UTC())
	if err != nil {
		return err
	}
	return c.DB.PutRef(projectID, RefLatest, next)
}

func errorsIs(err error) bool { return err == state.ErrNotFound }

// verifyDeps confirms blob files and restic snapshot references exist.
func (c *Coordinator) verifyDeps(ctx context.Context, blobsDir string, desc *commit.Descriptor) error {
	if desc.Environment.Image != "" {
		entries, err := ociBlobFiles(blobsDir)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return fmt.Errorf("no OCI blobs staged for environment")
		}
	}
	for name, ref := range desc.Snapshots {
		id := strings.TrimPrefix(ref, "restic:")
		list, err := c.localRestic().List(ctx, name)
		if err != nil {
			return fmt.Errorf("verify restic snapshot %s: %w", ref, err)
		}
		found := false
		for _, s := range list {
			if s.ID == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("restic snapshot %s not found in local repository", ref)
		}
	}
	return nil
}

func stageBlobsDir(l *store.Layout, projectID string) string {
	return filepath.Join(l.StagingDir, projectID+".blobs")
}

func ociBlobFiles(dir string) ([]string, error) {
	var out []string
	blobDir := filepath.Join(dir, "blobs", "sha256")
	err := filepath.WalkDir(blobDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func volumeHostPath(v manifest.VolumeSpec, root, projectID, name string) string {
	if v.Source != nil && v.Source.Type == "host" {
		return v.Source.Path
	}
	return podman.VolumeHostPath(root, projectID, name, v.Policy)
}

func (c *Coordinator) runHooks(ctx context.Context, rt *podman.Client, id string, hooks []string) error {
	for _, hook := range hooks {
		if strings.TrimSpace(hook) == "" {
			continue
		}
		parts := strings.Fields(hook)
		if len(parts) == 0 {
			continue
		}
		fmt.Fprintf(c.Stdout, "  hook: %s\n", hook)
		if _, err := rt.Exec(ctx, id, parts); err != nil {
			return fmt.Errorf("hook %q: %w", hook, err)
		}
	}
	return nil
}
