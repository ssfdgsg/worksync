package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"worksync/internal/commit"
	"worksync/internal/project"
	"worksync/internal/refs"
	"worksync/internal/state"
	"worksync/internal/transport/remote"
	"worksync/internal/transport/sshurl"
)

// coordRefLatest mirrors coord.RefLatest (avoids an import cycle).
const coordRefLatest = "latest"

// resolveRemote returns the Store for a named remote (defaulting to the
// manifest's remote.default, then "origin").
func (a *App) resolveRemote(ctx context.Context, proj *project.Project, name string) (*remote.Store, string, error) {
	remotes, err := loadRemotes(a, proj.ID)
	if err != nil {
		return nil, "", err
	}
	if name == "" {
		if proj.Manifest.Remote != nil && proj.Manifest.Remote.Default != "" {
			name = proj.Manifest.Remote.Default
		} else {
			name = "origin"
		}
	}
	r, ok := remotes[name]
	if !ok {
		return nil, "", &WbError{Code: CodeNotFound, Message: fmt.Sprintf("remote %q not configured (worksync remote add %s URL)", name, name)}
	}
	u, err := sshurl.Parse(r.URL)
	if err != nil {
		return nil, "", err
	}
	st := remote.NewStore(remote.NewEndpoint(u), proj.ID, a.Layout, a.resticPasswordProvider(false))
	remote.WithStdout(a.Stdout)(st)
	return st, name, nil
}

func cmdPush(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	remoteName := ""
	target := ""
	if len(args) > 0 {
		remoteName = args[0]
	}
	if len(args) > 1 {
		target = args[1]
	}
	st, _, err := app.resolveRemote(ctx, proj, remoteName)
	if err != nil {
		return err
	}
	return app.withProjectLock(ctx, proj.ID, state.OpPush, func() error {
		head, err := app.resolveCommitRef(proj.ID, target)
		if err != nil {
			return err
		}
		if err := st.EnsureVersion(ctx); err != nil {
			return err
		}
		// fast-forward check against the remote ref (design §16.3).
		remoteRef, exists, err := st.RemoteRef(ctx, coordRefLatest)
		if err != nil {
			return err
		}
		localRef, lerr := app.DB.GetRef(proj.ID, coordRefLatest)
		if lerr != nil && !errors.Is(lerr, state.ErrNotFound) {
			return lerr
		}
		if exists && lerr == nil {
			if remoteRef.Commit != localRef.Commit && !isDescendant(app, remoteRef.Commit, localRef.Commit) {
				return &WbError{Code: CodeConflict, Message: fmt.Sprintf("remote %s has diverged (remote %s, local %s); pull first", coordRefLatest, shortDigest(remoteRef.Commit), shortDigest(localRef.Commit))}
			}
		}
		uploads, err := st.PushObjects(ctx, head.Digest)
		if err != nil {
			return err
		}
		// CAS double-check (design §16.3): re-read the remote ref after the
		// upload; if another push landed meanwhile, fail instead of silently
		// overwriting it.
		if exists {
			fresh, ok2, err2 := st.RemoteRef(ctx, coordRefLatest)
			if err2 != nil {
				return err2
			}
			if ok2 && fresh.Commit != remoteRef.Commit {
				return &WbError{Code: CodeConflict, Message: fmt.Sprintf("remote %s moved during push (now %s); pull and retry", coordRefLatest, shortDigest(fresh.Commit))}
			}
		}
		// the pushed ref points at the pushed head: for `push origin` that is
		// local latest; for `push origin <commit>` it is the explicit target.
		// Advance keeps the Previous chain for traceability.
		next, err := refs.Advance(localRef, localRef.Commit, head.Digest, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := st.PutRemoteRef(ctx, coordRefLatest, next); err != nil {
			return err
		}
		fmt.Fprintf(app.Stdout, "pushed %s (%d objects) -> %s\n", shortDigest(head.Digest), uploads, coordRefLatest)
		return nil
	})
}

// isDescendant reports whether ancestor is reached by walking head's parent
// chain (true when equal). It walks the on-disk descriptor files (not the
// DB) so commits freshly downloaded by fetch/pull are visible too; a missing
// descriptor means the chain cannot be proven and the walk stops (false).
func isDescendant(app *App, ancestor, head string) bool {
	cur := head
	seen := map[string]bool{}
	for cur != "" && !seen[cur] {
		if cur == ancestor {
			return true
		}
		seen[cur] = true
		b, err := os.ReadFile(app.Layout.CommitDescriptorPath(strings.TrimPrefix(cur, "sha256:")))
		if err != nil {
			return false
		}
		d, err := commit.ParseJSON(b)
		if err != nil {
			return false
		}
		cur = d.Parent
	}
	return false
}

func cmdPull(ctx context.Context, app *App, args []string) error {
	return pullFetch(ctx, app, args, true)
}

func cmdFetch(ctx context.Context, app *App, args []string) error {
	return pullFetch(ctx, app, args, false)
}

// pullFetch implements both pull (apply the ref) and fetch (objects only).
func pullFetch(ctx context.Context, app *App, args []string, apply bool) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	remoteName := ""
	refName := "latest"
	if len(args) > 0 {
		remoteName = args[0]
	}
	if len(args) > 1 {
		refName = args[1]
	}
	st, _, err := app.resolveRemote(ctx, proj, remoteName)
	if err != nil {
		return err
	}
	return app.withProjectLock(ctx, proj.ID, state.OpPull, func() error {
		// E2E-010: a fresh clone has no projects row yet; register it so
		// follow-up rollback (which writes checkout/volume rows) does not trip
		// the FK constraint.
		bk, berr := resolveBackend(proj, "")
		if berr != nil {
			return berr
		}
		_ = app.ensureProjectRow(proj, bk)
		remoteRef, exists, err := st.RemoteRef(ctx, refName)
		if err != nil {
			return err
		}
		if !exists {
			return &WbError{Code: CodeNotFound, Message: fmt.Sprintf("remote ref %q does not exist yet", refName)}
		}
		downloaded, err := st.PullDownloads(ctx, remoteRef.Commit)
		if err != nil {
			return err
		}
		// persist downloaded descriptors in the local DB so log/rollback/tag
		// can resolve them without re-fetching.
		for _, dg := range downloaded {
			if err := app.persistPulledCommit(proj.ID, dg); err != nil {
				return err
			}
		}
		if apply {
			// fast-forward check AFTER downloads: the ancestry walk needs remote
			// commits to be present locally. Walking the local descriptor files
			// (not the DB) so freshly downloaded commits are visible too.
			localRef, lerr := app.DB.GetRef(proj.ID, coordRefLatest)
			if lerr == nil && localRef.Commit != remoteRef.Commit {
				if !isDescendant(app, localRef.Commit, remoteRef.Commit) {
					return &WbError{Code: CodeConflict, Message: fmt.Sprintf("local %s is not an ancestor of remote %s; commit locally first", shortDigest(localRef.Commit), shortDigest(remoteRef.Commit))}
				}
			}
		}
		verb := "fetched"
		if apply {
			cur, cerr := app.DB.GetRef(proj.ID, refName)
			expected := ""
			if cerr == nil {
				expected = cur.Commit
			}
			next, err := refs.Advance(cur, expected, remoteRef.Commit, time.Now().UTC())
			if err != nil {
				return err
			}
			if err := app.DB.PutRef(proj.ID, refName, next); err != nil {
				return err
			}
			verb = "pulled"
		}
		fmt.Fprintf(app.Stdout, "%s %s (%d commits) from remote\n", verb, shortDigest(remoteRef.Commit), len(downloaded))
		return nil
	})
}

// persistPulledCommit reads a pulled descriptor file and records it in DB.
func (a *App) persistPulledCommit(projectID, dg string) error {
	hexDg := strings.TrimPrefix(dg, "sha256:")
	b, err := os.ReadFile(a.Layout.CommitDescriptorPath(hexDg))
	if err != nil {
		return err
	}
	d, err := commit.ParseJSON(b)
	if err != nil {
		return err
	}
	return a.DB.SaveCommit(state.Commit{
		Digest:         dg,
		ProjectID:      projectID,
		DescriptorJSON: b,
		Parent:         d.Parent,
		Message:        d.Message,
		CreatedAt:      d.CreatedAt,
	})
}
