package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"worksync/internal/project"
	"worksync/internal/runtime/podman"
	"worksync/internal/state"
)

// checkpointDir returns where exported checkpoint archives are written by
// default.
func (a *App) checkpointDir() string { return filepath.Join(a.Layout.Root, "checkpoints") }

// cmdCheckpoint implements the checkpoint subcommand family:
//
//	checkpoint export [-o FILE.tar]   freeze current writable layer to archive
//	checkpoint import FILE.tar        load an archive and record a checkpoint
//	checkpoint list                   show recorded checkpoints
//
// Manual export/import lets a user carry a writable layer between machines
// out-of-band (design P0/M7), like a git bundle.
func cmdCheckpoint(ctx context.Context, app *App, args []string) error {
	if len(args) == 0 {
		return &WbError{Code: CodeConfig, Message: "usage: worksync checkpoint export|import|list"}
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "export":
		return cmdCheckpointExport(ctx, app, rest)
	case "import":
		return cmdCheckpointImport(ctx, app, rest)
	case "list":
		return cmdCheckpointList(ctx, app, rest)
	default:
		return &WbError{Code: CodeConfig, Message: fmt.Sprintf("unknown checkpoint subcommand %q", sub)}
	}
}

// checkpointExportDest extracts the -o/--output destination from args,
// or "" when absent.
func checkpointExportDest(args []string) string {
	dest := ""
	for i := 0; i < len(args); i++ {
		if (args[i] == "-o" || args[i] == "--output") && i+1 < len(args) {
			dest = args[i+1]
			i++
		}
	}
	return dest
}

func cmdCheckpointExport(ctx context.Context, app *App, args []string) error {
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
	dest := checkpointExportDest(args)
	return app.withProjectLock(ctx, proj.ID, state.OpUp, func() error {
		// Prefer an unused checkpoint; otherwise freeze the live container.
		imageRef := ""
		cp, cperr := app.DB.LatestCheckpoint(proj.ID)
		if cperr == nil && cp.RestoredAt.IsZero() {
			imageRef = cp.ImageRef
		} else if c, cerr := app.DB.GetContainer(proj.ID); cerr == nil && c.ContainerID != "" {
			fmt.Fprintln(app.Stdout, "freezing current container writable layer for export...")
			imageRef, err = app.checkpointAndReplace(ctx, proj, b, "export")
			if err != nil {
				return err
			}
		}
		if imageRef == "" {
			return &WbError{Code: CodeNotFound, Message: "no container or checkpoint to export; run worksync up first or commit a checkpoint"}
		}
		rt := podman.New(b, "")
		if dest == "" {
			if err := os.MkdirAll(app.checkpointDir(), 0o755); err != nil {
				return err
			}
			dest = filepath.Join(app.checkpointDir(), strings.TrimPrefix(imageRef, "sha256:")+".tar")
		}
		if err := rt.Save(ctx, imageRef, dest); err != nil {
			return err
		}
		fmt.Fprintf(app.Stdout, "checkpoint %s exported to %s\n", shortDigest(imageRef), dest)
		return nil
	})
}

func cmdCheckpointImport(ctx context.Context, app *App, args []string) error {
	if len(args) == 0 {
		return &WbError{Code: CodeConfig, Message: "usage: worksync checkpoint import FILE.tar"}
	}
	src := args[0]
	if _, err := os.Stat(src); err != nil {
		return &WbError{Code: CodeNotFound, Message: fmt.Sprintf("archive %s: %v", src, err)}
	}
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
	return app.withProjectLock(ctx, proj.ID, state.OpUp, func() error {
		// a fresh data root has no projects row yet; checkpoints FK to it
		if err := app.ensureProjectRow(proj, b); err != nil {
			return err
		}
		rt := podman.New(b, "")
		fmt.Fprintf(app.Stdout, "loading %s...\n", src)
		out, err := rt.LoadOut(ctx, src)
		if err != nil {
			return err
		}
		ref := loadedImageRef(out)
		if ref == "" {
			return fmt.Errorf("load %s: could not determine the loaded image", src)
		}
		dg, err := rt.ResolveDigest(ctx, ref)
		if err != nil {
			return err
		}
		if err := app.DB.UpsertCheckpoint(state.Checkpoint{
			ProjectID: proj.ID,
			ImageRef:  dg,
			Platform:  platformOf(b),
			Reason:    "import",
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			return err
		}
		fmt.Fprintf(app.Stdout, "imported checkpoint %s; run worksync up to rebuild the container\n", shortDigest(dg))
		return nil
	})
}

// loadedImageRef extracts an image reference from `podman load` output which
// prints lines like "Loaded image: docker.io/library/alpine:latest".
func loadedImageRef(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		// podman prints "Loaded image: <ref>"; docker variants may print
		// "Loaded image(s): <ref>".
		for _, prefix := range []string{"Loaded image(s):", "Loaded image:"} {
			if strings.HasPrefix(line, prefix) {
				ref := strings.TrimSpace(strings.TrimPrefix(line, prefix))
				if ref != "" {
					return ref
				}
			}
		}
	}
	return ""
}

func cmdCheckpointList(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	cps, err := app.DB.ListCheckpoints(proj.ID)
	if err != nil {
		return err
	}
	return writeJSON(app, cps, func() error {
		if len(cps) == 0 {
			fmt.Fprintln(app.Stdout, "no checkpoints")
			return nil
		}
		for _, cp := range cps {
			status := "unused"
			if !cp.RestoredAt.IsZero() {
				status = "restored"
			}
			fmt.Fprintf(app.Stdout, "%s  %-9s %-9s %s\n", shortDigest(cp.ImageRef), cp.Reason, status, cp.CreatedAt.Format("2006-01-02 15:04"))
		}
		return nil
	})
}
