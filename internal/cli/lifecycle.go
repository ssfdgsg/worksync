package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"worksync/internal/backend"
	"worksync/internal/lock"
	"worksync/internal/manifest"
	"worksync/internal/ports"
	"worksync/internal/project"
	"worksync/internal/runtime/podman"
	"worksync/internal/state"
)

// dataRoot returns the backend data root (design §9.2: native backend lives
// under the host data directory).
func (a *App) dataRoot() string { return filepath.Join(a.Layout.Root, "data") }

// withProjectLock runs fn under the project lock with a journaled operation.
// It implements design §21 (exclusive lock) and §9.4 (operations journal).
func (a *App) withProjectLock(ctx context.Context, projID string, kind state.OperationKind, fn func() error) error {
	opID := state.NewOperationID()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	l, err := lock.Acquire(ctx, a.Layout.LockPath(projID), opID, string(kind))
	if err != nil {
		var held *lock.ErrHeld
		if errors.As(err, &held) {
			return &WbError{Code: CodeLocked, Message: held.Error()}
		}
		return err
	}
	defer l.Release()
	// §22 recovery: while holding the lock, mark stale journal entries
	// (interrupted processes) as interrupted so state stays consistent.
	if running, err := a.DB.FindRunningOperations(); err == nil {
		for _, op := range running {
			if op.ProjectID == projID && !lock.ProcessAlive(op.PID) {
				_ = a.DB.MarkInterrupted(op.ID, "recovered by worksync: owner process exited")
			}
		}
	}
	journalID, err := a.DB.StartOperation(state.Operation{ProjectID: projID, Kind: kind})
	if err != nil {
		return err
	}
	if err := fn(); err != nil {
		_ = a.DB.FinishOperation(journalID, err)
		return err
	}
	return a.DB.FinishOperation(journalID, nil)
}

// ensureProjectRow syncs the projects table (derived state, §9.4).
func (a *App) ensureProjectRow(proj *project.Project, b backend.Backend) error {
	return a.DB.UpsertProject(state.Project{
		ID:             proj.ID,
		ManifestPath:   proj.ManifestPath,
		ManifestDigest: string(proj.ManifestHash),
		Backend:        b.Kind,
	})
}

// hostPortFree probes whether listen:port is free on the host.
func defaultListen(l string) string {
	if l == "" {
		return ports.DefaultListen
	}
	return l
}

func hostPortFree(listen string, port uint16) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort(listen, strconv.Itoa(int(port))))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// resolvePorts converts manifest ports into concrete ports, allocating host
// ports for published:auto (design §11.4: record the actual port, reuse
// across restarts, allocate a new port on conflict).
func resolvePorts(m *manifest.Manifest, existing []state.Port) ([]ports.Port, error) {
	byName := map[string]state.Port{}
	for _, p := range existing {
		byName[p.Name] = p
	}
	alloc := ports.NewAllocator(ports.DefaultListen)
	var out []ports.Port
	seen := map[string]bool{}
	for _, p := range m.Ports {
		cp := ports.Port{Name: p.Name, Target: p.Target, Protocol: p.Protocol, Listen: p.Listen}
		if cp.Listen == "" {
			cp.Listen = ports.DefaultListen // §11.3: listen defaults to 127.0.0.1
		}
		if p.Published != ports.AutoPublished {
			cp.Published = p.Published
			out = append(out, cp)
			seen[cp.Name] = true
			continue
		}
		// reuse recorded port if still free
		if rec, ok := byName[p.Name]; ok && hostPortFree(defaultListen(rec.Listen), rec.Published) {
			cp.Published = strconv.Itoa(int(rec.Published))
			out = append(out, cp)
			seen[cp.Name] = true
			continue
		}
		// allocate a new free port in the ephemeral range, excluding host
		// ports already recorded for this project.
		used := map[uint16]bool{}
		for _, e := range existing {
			used[e.Published] = true
		}
		n, err := alloc.Allocate(p.Name, 10240, 65535, func(cand uint16) bool {
			return used[cand] || !hostPortFree(cp.Listen, cand)
		})
		if err != nil {
			return nil, &WbError{Code: CodePortInUse, Message: fmt.Sprintf("port for %s: %v", p.Name, err)}
		}
		cp.Published = strconv.Itoa(int(n))
		out = append(out, cp)
		seen[cp.Name] = true
	}
	// Dynamic ports recorded by `expose` are not in the manifest (E2E-005);
	// they must reach the podman create spec on every (re)provision, otherwise
	// the DB-only entries never materialize on the runtime.
	for _, e := range existing {
		if seen[e.Name] {
			continue
		}
		out = append(out, ports.Port{
			Name:      e.Name,
			Target:    e.Target,
			Published: strconv.Itoa(int(e.Published)),
			Listen:    defaultListen(e.Listen),
			Protocol:  e.Protocol,
		})
	}
	return out, nil
}

// agentDir ensures the static worksync-agent exists (design §8.3) and returns
// its host path for read-only mounting at /opt/worksync.
func (a *App) agentDir() (string, error) {
	dir := filepath.Join(a.Layout.Root, "agent")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		return "", err
	}
	idle := filepath.Join(dir, "bin", "worksync-agent")
	if _, err := os.Stat(idle); os.IsNotExist(err) {
		script := "#!/bin/sh\n# worksync-agent idle: keep the development container alive (design §8.3)\nwhile true; do sleep 3600; done\n"
		if err := os.WriteFile(idle, []byte(script), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// buildCreateSpec converts the manifest into a podman create spec.
func buildCreateSpec(proj *project.Project, b backend.Backend, a *App, concretePorts []ports.Port) (podman.CreateSpec, error) {
	m := proj.Manifest
	agent, err := a.agentDir()
	if err != nil {
		return podman.CreateSpec{}, err
	}
	if err := os.MkdirAll(a.dataRoot(), 0o755); err != nil {
		return podman.CreateSpec{}, err
	}
	// host workspace must exist and be visible (design §9.3).
	if ws, ok := m.Volumes["workspace"]; ok && ws.Source != nil && ws.Source.Type == "host" {
		if fi, err := os.Stat(ws.Source.Path); err != nil || !fi.IsDir() {
			return podman.CreateSpec{}, &WbError{Code: CodeConfig, Message: fmt.Sprintf("workspace path %s is not a readable directory: %v", ws.Source.Path, err)}
		}
	}
	mounts := podman.MountsFromManifest(m, proj.ID, a.dataRoot())
	// E2E-008: managed (non-host) volume host dirs must exist before create;
	// statfs on a missing dir makes podman create fail on a clean data root.
	for name, v := range m.Volumes {
		if v.Source != nil && v.Source.Type == "host" {
			continue
		}
		host := podman.VolumeHostPath(a.dataRoot(), proj.ID, name, v.Policy)
		if err := os.MkdirAll(host, 0o755); err != nil {
			return podman.CreateSpec{}, fmt.Errorf("create managed volume dir %s: %w", host, err)
		}
	}
	mounts = append(mounts, podman.Mount{Host: agent, Target: "/opt/worksync", ReadOnly: true})
	spec := podman.CreateSpec{
		Name:    podman.ContainerName(proj.ID),
		Image:   m.Container.Image, // resolved to digest before create
		Workdir: m.Container.Workdir,
		User:    m.Container.User,
		Env:     m.Container.Environment,
		Mounts:  mounts,
		Ports:   concretePorts,
		KeepID:  b.Caps.Has(backend.CapKeepID),
		Labels: map[string]string{
			"worksync.project": proj.ID,
			"worksync.managed": "true",
		},
	}
	if len(m.Container.Command) > 0 {
		spec.Command = m.Container.Command
	} else {
		// fallback idle command (design §8.3 agent injection)
		spec.Command = []string{"/opt/worksync/bin/worksync-agent", "idle"}
	}
	return spec, nil
}

func cmdUp(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	b, err := resolveBackend(proj, flagValue(args, "--backend"))
	if err != nil {
		return err
	}
	if err := b.Require(backend.CapRootless); err != nil {
		return &WbError{Code: CodeUnsupported, Message: err.Error()}
	}
	if err := requireTool("podman"); err != nil {
		return err
	}
	return app.withProjectLock(ctx, proj.ID, state.OpUp, func() error {
		if err := app.ensureProjectRow(proj, b); err != nil {
			return err
		}
		// persist volumes (derived state); HostPath is resolved per volume
		// (map iteration order is random, so [0] would be wrong).
		for name, v := range proj.Manifest.Volumes {
			vv := state.Volume{ProjectID: proj.ID, Name: name, Target: v.Target, Policy: v.Policy}
			if v.Source != nil {
				vv.SourceType = v.Source.Type
				vv.SourcePath = v.Source.Path
			}
			vv.HostPath = volumeHostPathFor(app, v, proj.ID, name)
			if err := app.DB.UpsertVolume(vv); err != nil {
				return err
			}
		}
		rt := podman.New(b, "")
		name := podman.ContainerName(proj.ID)
		exists, err := rt.ExistsContainer(ctx, name)
		if err != nil {
			return err
		}
		if exists {
			// check drift (design §8.2: rebuild on config drift). A container
			// present in podman but unknown to the DB is treated as drift and
			// recreated.
			c, cerr := app.DB.GetContainer(proj.ID)
			if cerr == nil && c.ConfigDigest == string(proj.ManifestHash) {
				// E2E-006: the DB can hold a stale container id (e.g. renamed
				// projects); resolve the real container by its deterministic
				// name and repair the row before starting it.
				actualID, ierr := rt.InspectID(ctx, name)
				if ierr != nil {
					return ierr
				}
				// idempotent up: start the existing container (design §13.1)
				if err := rt.Start(ctx, actualID); err != nil {
					return err
				}
				c.ContainerID = actualID
				c.State = state.StateRunning
				if err := app.DB.UpsertContainer(*c); err != nil {
					return err
				}
				fmt.Fprintf(app.Stdout, "container %s is running\n", name)
				return nil
			}
			fmt.Fprintf(app.Stdout, "container config changed or unknown; rebuilding %s\n", name)
			// remove the podman container whether or not a DB row exists: a
			// container present in podman but unknown to the DB must also be
			// removed, otherwise the create below fails on the name.
			target := name
			if c != nil && c.ContainerID != "" {
				target = c.ContainerID
			}
			if err := rt.Rm(ctx, target); err != nil {
				return err
			}
			_ = app.DB.DeleteContainer(proj.ID)
		}
		// resolve ports (reuse/allocate) and persist — only for (re)provision,
		// never in the idempotent up path above (the DB already records the
		// ports the running container was created with).
		existing, _ := app.DB.ListPorts(proj.ID)
		concretePorts, err := resolvePorts(proj.Manifest, existing)
		if err != nil {
			return err
		}
		for _, p := range concretePorts {
			pub, _ := strconv.Atoi(p.Published)
			if err := app.DB.UpsertPort(state.Port{ProjectID: proj.ID, Name: p.Name, Target: p.Target, Published: uint16(pub), Listen: p.Listen, Protocol: p.Protocol}); err != nil {
				return err
			}
		}
		// E2E-001: when the project is checked out (rolled back to a commit),
		// provision from the committed environment image. A manifest change
		// since the commit invalidates the checkout.
		envImage, err := app.checkoutImage(proj)
		if err != nil {
			return err
		}
		// pull, create, start and record (design §12.2)
		if err := app.provisionContainer(ctx, proj, b, rt, concretePorts, envImage); err != nil {
			return err
		}
		for _, p := range concretePorts {
			fmt.Fprintf(app.Stdout, "  %s -> %s:%s\n", p.Name, defaultListen(p.Listen), p.Published)
		}
		return nil
	})
}

func cmdStop(ctx context.Context, app *App, args []string) error {
	return lifecycleSimple(ctx, app, "stop", state.OpStop, func(rt *podman.Client, c *state.Container) error {
		if c.State == state.StateStopped {
			fmt.Fprintln(app.Stdout, "container already stopped")
			return nil
		}
		if err := rt.Stop(ctx, c.ContainerID, 10); err != nil {
			return err
		}
		c.State = state.StateStopped
		return app.DB.UpsertContainer(*c)
	})
}

func cmdStart(ctx context.Context, app *App, args []string) error {
	return lifecycleSimple(ctx, app, "start", state.OpStart, func(rt *podman.Client, c *state.Container) error {
		if c.State == state.StateRunning {
			fmt.Fprintln(app.Stdout, "container already running")
			return nil
		}
		if err := rt.Start(ctx, c.ContainerID); err != nil {
			return err
		}
		c.State = state.StateRunning
		return app.DB.UpsertContainer(*c)
	})
}

// lifecycleSimple loads the project, acquires the lock and runs fn with the
// container row; missing containers produce a clear error.
func lifecycleSimple(ctx context.Context, app *App, verb string, kind state.OperationKind, fn func(*podman.Client, *state.Container) error) error {
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
	return app.withProjectLock(ctx, proj.ID, kind, func() error {
		c, err := app.DB.GetContainer(proj.ID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return &WbError{Code: CodeNotFound, Message: fmt.Sprintf("no container for project %s (run worksync up first)", proj.ID)}
			}
			return err
		}
		rt := podman.New(b, "")
		return fn(rt, c)
	})
}

func cmdRm(ctx context.Context, app *App, args []string) error {
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
	removeVolumes := false
	yes := false
	for _, a := range args {
		switch a {
		case "--volumes":
			removeVolumes = true
		case "--yes":
			yes = true
		}
	}
	if removeVolumes && !yes {
		return &WbError{Code: CodeConfig, Message: "volume removal is destructive; confirm with: worksync rm --volumes --yes"}
	}
	return app.withProjectLock(ctx, proj.ID, state.OpRm, func() error {
		c, err := app.DB.GetContainer(proj.ID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return &WbError{Code: CodeNotFound, Message: "no container to remove"}
			}
			return err
		}
		rt := podman.New(b, "")
		// E2E-007: podman rm refuses running containers; stop first
		// (best-effort: the container may already be stopped).
		_ = rt.Stop(ctx, c.ContainerID, 10)
		if err := rt.Rm(ctx, c.ContainerID); err != nil {
			return err
		}
		if err := app.DB.DeleteContainer(proj.ID); err != nil {
			return err
		}
		msg := "removed container " + c.Name
		if removeVolumes {
			// delete managed volume data (host-sourced volumes are never
			// touched; they are the user's own files).
			dataDir := app.dataRoot()
			for name, v := range proj.Manifest.Volumes {
				if v.Source != nil && v.Source.Type == "host" {
					continue
				}
				host := podman.VolumeHostPath(dataDir, proj.ID, name, v.Policy)
				if err := os.RemoveAll(host); err != nil {
					return err
				}
				if err := app.DB.DeleteVolume(proj.ID, name); err != nil {
					return err
				}
			}
			msg += " and managed volumes"
		}
		fmt.Fprintf(app.Stdout, "%s\n", msg)
		return nil
	})
}

// exec runs a command in the container (used by shell and exec).
func containerExec(ctx context.Context, app *App, b backend.Backend, cmd []string) error {
	c, err := app.DB.GetContainer(projIDFromCwd(app))
	if err != nil {
		return &WbError{Code: CodeNotFound, Message: "no container; run worksync up first"}
	}
	if c.State != state.StateRunning {
		return &WbError{Code: CodeNotFound, Message: fmt.Sprintf("container is %s; run worksync start", c.State)}
	}
	rt := podman.New(b, "")
	res, err := rt.Exec(ctx, c.ContainerID, cmd)
	if err != nil {
		return err
	}
	fmt.Fprint(app.Stdout, res.Stdout)
	fmt.Fprint(app.Stderr, res.Stderr)
	if res.ExitCode != 0 {
		return &WbError{Code: CodeInternal, Message: fmt.Sprintf("command exited %d", res.ExitCode)}
	}
	return nil
}

func projIDFromCwd(app *App) string {
	p, err := project.Load(app.Workdir())
	if err != nil {
		return ""
	}
	return p.ID
}

// cmdExec runs a command in the running container (design §18.1).
func cmdExec(ctx context.Context, app *App, args []string) error {
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
	// strip a leading "--" separator
	cmd := args
	if len(cmd) > 0 && cmd[0] == "--" {
		cmd = cmd[1:]
	}
	if len(cmd) == 0 {
		return &WbError{Code: CodeConfig, Message: "usage: worksync exec -- command..."}
	}
	return containerExec(ctx, app, b, cmd)
}

// cmdShell opens an interactive shell in the container (design §18.1). For
// distroless images without a shell this errors clearly (design §8.3).
func cmdShell(ctx context.Context, app *App, args []string) error {
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
	// a custom command after -- overrides the default shell
	cmd := []string{"/bin/sh", "-i"}
	if len(args) > 0 && args[0] == "--" && len(args) > 1 {
		cmd = args[1:]
	}
	return containerExec(ctx, app, b, cmd)
}

// provisionContainer pulls (unless creating from a checked-out environment
// image), creates, starts and records the container from the given concrete
// ports. envImage is the digest of a committed environment image to create
// from (E2E-001); empty means the manifest base image. Used by up and by the
// dynamic port commands (expose/unexpose, design §11.4).
func (a *App) provisionContainer(ctx context.Context, proj *project.Project, b backend.Backend, rt *podman.Client, concretePorts []ports.Port, envImage string) error {
	name := podman.ContainerName(proj.ID)
	imageRef := ""
	if envImage != "" {
		// the committed rootfs is already present locally (loaded by
		// rollback/pull); no pull, use the digest directly.
		fmt.Fprintf(a.Stdout, "creating from committed environment image...\n")
		imageRef = envImage
	} else {
		fmt.Fprintf(a.Stdout, "pulling %s...\n", proj.Manifest.Container.Image)
		if err := rt.Pull(ctx, proj.Manifest.Container.Image); err != nil {
			return err
		}
		dg, err := rt.ResolveDigest(ctx, proj.Manifest.Container.Image)
		if err != nil {
			return err
		}
		imageRef = dg
	}
	spec, err := buildCreateSpec(proj, b, a, concretePorts)
	if err != nil {
		return err
	}
	spec.Image = imageRef
	id, err := rt.Create(ctx, spec)
	if err != nil {
		return err
	}
	if err := rt.Start(ctx, id); err != nil {
		return err
	}
	if err := a.DB.UpsertContainer(state.Container{
		ProjectID:    proj.ID,
		Name:         name,
		ImageTag:     proj.Manifest.Container.Image,
		ImageRef:     imageRef,
		ConfigDigest: string(proj.ManifestHash),
		State:        state.StateRunning,
		ContainerID:  id,
	}); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "container %s is running\n", name)
	return nil
}

// checkoutImage returns the committed environment image digest to create the
// container from when the project is checked out to a commit (E2E-001). It
// returns "" when there is no checkout, and clears the checkout when the
// manifest has drifted since the checked-out commit so the base image path
// takes over.
func (a *App) checkoutImage(proj *project.Project) (string, error) {
	co, err := a.DB.GetCheckout(proj.ID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	if co.ConfigDigest != "" && co.ConfigDigest != string(proj.ManifestHash) {
		_ = a.DB.DeleteCheckout(proj.ID)
		return "", nil
	}
	return co.ImageRef, nil
}

// restartForPorts removes the container and re-provisions it so dynamic
// port changes take effect (rootless podman cannot republish live).
func (a *App) restartForPorts(ctx context.Context, proj *project.Project, b backend.Backend) error {
	if err := requireTool("podman"); err != nil {
		return err
	}
	rt := podman.New(b, "")
	c, err := a.DB.GetContainer(proj.ID)
	if err == nil && c.ContainerID != "" {
		_ = rt.Stop(ctx, c.ContainerID, 10)
		if err := rt.Rm(ctx, c.ContainerID); err != nil {
			return err
		}
		if err := a.DB.DeleteContainer(proj.ID); err != nil {
			return err
		}
	}
	existing, _ := a.DB.ListPorts(proj.ID)
	concretePorts, err := resolvePorts(proj.Manifest, existing)
	if err != nil {
		return err
	}
	// keep creating from a checked-out environment image across the
	// re-provision (E2E-001).
	envImage, err := a.checkoutImage(proj)
	if err != nil {
		return err
	}
	return a.provisionContainer(ctx, proj, b, rt, concretePorts, envImage)
}

func cmdPorts(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	portsList, err := app.DB.ListPorts(proj.ID)
	if err != nil {
		return err
	}
	return writeJSON(app, portsList, func() error {
		if len(portsList) == 0 {
			fmt.Fprintln(app.Stdout, "no published ports")
			return nil
		}
		for _, p := range portsList {
			fmt.Fprintf(app.Stdout, "%s\t%s:%d -> %d/%s\n", p.Name, defaultListen(p.Listen), p.Published, p.Target, p.Protocol)
		}
		return nil
	})
}

// cmdExpose publishes an additional port: TARGET[:HOST]. The host port is
// allocated (auto) or given explicitly; the container is re-provisioned.
func cmdExpose(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	b, err := resolveBackend(proj, "")
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return &WbError{Code: CodeConfig, Message: "usage: worksync expose TARGET[:HOST]"}
	}
	targetStr := args[0]
	targetPort := uint16(0)
	hostPort := "auto"
	if c, h, ok := strings.Cut(targetStr, ":"); ok {
		targetStr = c
		hostPort = h
	}
	n, err := strconv.Atoi(targetStr)
	if err != nil || n < 1 || n > 65535 {
		return &WbError{Code: CodeConfig, Message: fmt.Sprintf("invalid target port %q", targetStr)}
	}
	targetPort = uint16(n)
	name := "port-" + targetStr
	return app.withProjectLock(ctx, proj.ID, state.OpExpose, func() error {
		if _, err := app.DB.GetPort(proj.ID, name); err == nil {
			return &WbError{Code: CodeConflict, Message: fmt.Sprintf("port %s already exposed", targetStr)}
		}
		pub := uint16(0)
		if hostPort != "auto" {
			hp, err := strconv.Atoi(hostPort)
			if err != nil || hp < 1024 || hp > 65535 {
				return &WbError{Code: CodeConfig, Message: "host port must be an integer >= 1024 (rootless)"}
			}
			pub = uint16(hp)
		}
		if pub == 0 {
			// avoid host ports already recorded for this project even if the
			// container is not currently listening (e.g. after rm).
			used := map[uint16]bool{}
			if existing, err := app.DB.ListPorts(proj.ID); err == nil {
				for _, p := range existing {
					used[p.Published] = true
				}
			}
			alloc := ports.NewAllocator(ports.DefaultListen)
			free, err := alloc.Allocate(name, 10240, 65535, func(cand uint16) bool {
				return used[cand] || !hostPortFree(defaultListen(""), cand)
			})
			if err != nil {
				return err
			}
			pub = free
		}
		if err := app.DB.UpsertPort(state.Port{ProjectID: proj.ID, Name: name, Target: targetPort, Published: pub, Listen: ports.DefaultListen, Protocol: "tcp"}); err != nil {
			return err
		}
		fmt.Fprintf(app.Stdout, "exposed %d -> %s:%d (re-provisioning container)\n", targetPort, ports.DefaultListen, pub)
		return app.restartForPorts(ctx, proj, b)
	})
}

func cmdUnexpose(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	b, err := resolveBackend(proj, "")
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return &WbError{Code: CodeConfig, Message: "usage: worksync unexpose PORT"}
	}
	name := "port-" + strings.TrimPrefix(args[0], "port-")
	return app.withProjectLock(ctx, proj.ID, state.OpUnexpose, func() error {
		if err := app.DB.DeletePort(proj.ID, name); err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return &WbError{Code: CodeNotFound, Message: fmt.Sprintf("port %s not exposed", args[0])}
			}
			return err
		}
		fmt.Fprintf(app.Stdout, "unexposed %s (re-provisioning container)\n", args[0])
		return app.restartForPorts(ctx, proj, b)
	})
}
