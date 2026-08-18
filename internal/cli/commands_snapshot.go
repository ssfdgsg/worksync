package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"worksync/internal/commit"
	"worksync/internal/coord"
	"worksync/internal/digest"
	"worksync/internal/manifest"
	"worksync/internal/project"
	"worksync/internal/refs"
	"worksync/internal/runtime/podman"
	"worksync/internal/snapshot/restic"
	"worksync/internal/state"
)

// resticPasswordProvider returns the repository password: WORKSYNC_RESTIC_PASSWORD
// wins; otherwise a 0600 keyring file under the restic dir. With
// createIfAbsent=true (used by commit, which owns the repository) a missing
// local file is created with a random value (design §20.1: repositories are
// always encrypted). The remote store never carries the password, so when a
// fresh clone has neither env nor local password file, restore-oriented
// callers (createIfAbsent=false) get a clear error instead of silently
// generating a wrong password that cannot open the mirrored repository.
func (a *App) resticPasswordProvider(createIfAbsent bool) restic.PasswordProvider {
	return func() (string, error) {
		if pw := os.Getenv("WORKSYNC_RESTIC_PASSWORD"); pw != "" {
			return pw, nil
		}
		key := filepath.Join(a.Layout.ResticDir, "password")
		if b, err := os.ReadFile(key); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b)), nil
		}
		if !createIfAbsent {
			return "", fmt.Errorf("no restic password locally; set WORKSYNC_RESTIC_PASSWORD or copy the password file from the machine that pushed (the password is deliberately not stored on the remote)")
		}
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		pw := hex.EncodeToString(buf)
		if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(key, []byte(pw+"\n"), 0o600); err != nil {
			return "", err
		}
		return pw, nil
	}
}

func (a *App) newCoordinator() *coord.Coordinator {
	return &coord.Coordinator{
		Layout:          a.Layout,
		DB:              a.DB,
		Stdout:          a.Stdout,
		Stderr:          a.Stderr,
		ResticPassword:  a.resticPasswordProvider(true), // commit owns the repository
		BackendDataRoot: a.dataRoot(),
	}
}

func cmdCommit(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	b, err := resolveBackend(proj, "")
	if err != nil {
		return err
	}
	if err := requireTool("podman"); err != nil {
		return err
	}
	msg := flagValue(args, "-m")
	if msg == "" {
		return &WbError{Code: CodeConfig, Message: "commit requires -m MESSAGE"}
	}
	return app.withProjectLock(ctx, proj.ID, state.OpCommit, func() error {
		rt := podman.New(b, "")
		_, err := app.newCoordinator().Commit(ctx, rt, proj, coord.Options{Message: msg})
		return err
	})
}

func cmdLog(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	commits, err := app.DB.ListCommits(proj.ID)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		fmt.Fprintln(app.Stdout, "no commits yet")
		return nil
	}
	type logRow struct {
		Digest    string    `json:"digest"`
		Parent    string    `json:"parent,omitempty"`
		Message   string    `json:"message"`
		CreatedAt time.Time `json:"createdAt"`
	}
	rows := make([]logRow, 0, len(commits))
	for _, c := range commits {
		rows = append(rows, logRow{Digest: c.Digest, Parent: c.Parent, Message: c.Message, CreatedAt: c.CreatedAt})
	}
	return writeJSON(app, rows, func() error {
		for _, c := range commits {
			short := shortDigest(c.Digest)
			line := fmt.Sprintf("%s  %s  %s", c.CreatedAt.Format("2006-01-02 15:04"), short, c.Message)
			if c.Parent != "" {
				line += fmt.Sprintf(" (parent %s)", shortDigest(c.Parent))
			}
			fmt.Fprintln(app.Stdout, line)
		}
		return nil
	})
}

// resolveCommitRef resolves "latest", a ref name, or a commit digest to a
// persisted commit row.
func (a *App) resolveCommitRef(projID, name string) (*state.Commit, error) {
	if name == "" {
		name = "latest"
	}
	// bare digest?
	if _, err := digest.Parse(name); err == nil {
		c, err := a.DB.GetCommit(name)
		if err != nil {
			return nil, &WbError{Code: CodeNotFound, Message: fmt.Sprintf("commit %s not found locally", name)}
		}
		return c, nil
	}
	r, err := a.DB.GetRef(projID, name)
	if err != nil {
		return nil, &WbError{Code: CodeNotFound, Message: fmt.Sprintf("ref %q not found", name)}
	}
	c, err := a.DB.GetCommit(r.Commit)
	if err != nil {
		return nil, &WbError{Code: CodeNotFound, Message: fmt.Sprintf("commit %s (ref %q) not found locally; run worksync fetch", r.Commit, name)}
	}
	return c, nil
}

func cmdTag(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return &WbError{Code: CodeConfig, Message: "usage: worksync tag NAME [COMMIT-OR-REF]"}
	}
	name := args[0]
	target := ""
	if len(args) > 1 {
		target = args[1]
	}
	return app.withProjectLock(ctx, proj.ID, state.OpTag, func() error {
		c, err := app.resolveCommitRef(proj.ID, target)
		if err != nil {
			return err
		}
		cur, err := app.DB.GetRef(proj.ID, name)
		if err != nil && !errors.Is(err, state.ErrNotFound) {
			return err
		}
		expected := ""
		if err == nil {
			expected = cur.Commit
		}
		next, err := refs.Advance(cur, expected, c.Digest, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := app.DB.PutRef(proj.ID, name, next); err != nil {
			return err
		}
		fmt.Fprintf(app.Stdout, "tagged %s -> %s\n", name, shortDigest(c.Digest))
		return nil
	})
}

func cmdRollback(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	b, err := resolveBackend(proj, "")
	if err != nil {
		return err
	}
	if err := requireTool("podman"); err != nil {
		return err
	}
	if len(args) == 0 {
		return &WbError{Code: CodeConfig, Message: "usage: worksync rollback COMMIT-OR-TAG"}
	}
	return app.withProjectLock(ctx, proj.ID, state.OpRollback, func() error {
		target, err := app.resolveCommitRef(proj.ID, args[0])
		if err != nil {
			return err
		}
		desc, err := commit.ParseJSON(target.DescriptorJSON)
		if err != nil {
			return err
		}
		container, cerr := app.DB.GetContainer(proj.ID)
		if cerr != nil && !errors.Is(cerr, state.ErrNotFound) {
			return cerr
		}
		rt := podman.New(b, "")
		// stop and remove the current container so the rolled-back
		// environment replaces it (design §17).
		if cerr == nil && container.ContainerID != "" {
			fmt.Fprintf(app.Stdout, "stopping container %s...\n", container.Name)
			_ = rt.Stop(ctx, container.ContainerID, 10)
			if err := rt.Rm(ctx, container.ContainerID); err != nil {
				return err
			}
			// E2E-004: the container is gone; drop the derived row so status
			// no longer reports a stale running container.
			if err := app.DB.DeleteContainer(proj.ID); err != nil {
				return err
			}
		}
		// restore the environment image (design §16.4 pull step 4).
		if desc.Environment.Image != "" {
			ociPath := filepath.Join(app.Layout.OCIDir, strings.TrimPrefix(target.Digest, "sha256:"), "image.tar")
			if _, err := os.Stat(ociPath); err != nil {
				fmt.Fprintf(app.Stdout, "environment archive missing locally; image restore skipped\n")
			} else {
				fmt.Fprintln(app.Stdout, "loading environment image...")
				if err := rt.Load(ctx, ociPath); err != nil {
					return err
				}
				// E2E-001: record the checked-out environment image so the
				// following `up` provisions from the committed rootfs instead
				// of the manifest base image.
				if err := app.DB.UpsertCheckout(state.Checkout{
					ProjectID:    proj.ID,
					CommitDigest: target.Digest,
					ImageRef:     desc.Environment.Image,
					ConfigDigest: desc.ConfigDigest,
				}); err != nil {
					return err
				}
			}
		} else {
			// a commit without an environment image cannot feed `up`; clear
			// any older checkout.
			_ = app.DB.DeleteCheckout(proj.ID)
		}
		// restore volumes from restic snapshots.
		rc := &restic.Client{
			Repo:     app.Layout.ResticDir + "/repository",
			Password: app.resticPasswordProvider(false), // restore must use the pushed machine's password
		}
		for name, snapRef := range desc.Snapshots {
			id := strings.TrimPrefix(snapRef, "restic:")
			v, ok := proj.Manifest.Volumes[name]
			if !ok {
				fmt.Fprintf(app.Stdout, "  skipping snapshot for unknown volume %s\n", name)
				continue
			}
			host := volumeHostPathFor(app, v, proj.ID, name)
			fmt.Fprintf(app.Stdout, "  restoring %s -> %s\n", name, host)
			if err := rc.Restore(ctx, id, host); err != nil {
				return err
			}
		}
		fmt.Fprintf(app.Stdout, "rolled back to %s; run worksync up to recreate the container\n", shortDigest(target.Digest))
		return nil
	})
}

func cmdDiff(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	b, err := resolveBackend(proj, "")
	if err != nil {
		return err
	}
	if err := requireTool("podman"); err != nil {
		return err
	}
	c, err := app.DB.GetContainer(proj.ID)
	if err != nil {
		return &WbError{Code: CodeNotFound, Message: "no container; run worksync up first"}
	}
	rt := podman.New(b, "")
	changes, err := rt.Diff(ctx, c.ContainerID)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		fmt.Fprintln(app.Stdout, "no changes since the image was created")
	} else {
		fmt.Fprintf(app.Stdout, "%d paths changed in the rootfs:\n", len(changes))
		for _, ch := range changes {
			fmt.Fprintln(app.Stdout, "  "+ch)
		}
	}
	if c.State == state.StateRunning {
		fmt.Fprintln(app.Stdout, "(workspace volume diff requires restic diff; run worksync commit to freeze)")
	}
	return nil
}

func volumeHostPathFor(app *App, v manifest.VolumeSpec, projectID, name string) string {
	if v.Source != nil && v.Source.Type == "host" {
		return v.Source.Path
	}
	return podman.VolumeHostPath(app.dataRoot(), projectID, name, v.Policy)
}

func shortDigest(d string) string {
	if len(d) > 12 && strings.HasPrefix(d, "sha256:") {
		return d[:12]
	}
	return d
}
