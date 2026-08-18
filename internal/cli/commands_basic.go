package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"worksync/internal/backend"
	"worksync/internal/manifest"
	"worksync/internal/project"
	"worksync/internal/state"
)

// initTemplate is the scaffold produced by `worksync init` (design §12.1).
const initTemplate = `schemaVersion: 1
name: %s

runtime:
  engine: podman
  backend: auto
  rootless: true

container:
  image: %s
  persistentRoot: true
  workdir: /workspace
  user: dev
  command: ["/opt/worksync/bin/worksync-agent", "idle"]
  environment:
    NODE_ENV: development

ports:
  - name: web
    target: 3000
    published: auto
    listen: 127.0.0.1
    protocol: tcp

volumes:
  workspace:
    source:
      type: host
      path: ./
    target: /workspace
    policy: tracked

  home:
    target: /home/dev
    policy: persistent

commit:
  environment: true
  volumes:
    - workspace
`

func cmdInit(ctx context.Context, app *App, args []string) error {
	name := ""
	image := "node:24"
	force := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			i++
			if i >= len(args) {
				return &WbError{Code: CodeConfig, Message: "--name requires a value"}
			}
			name = args[i]
		case "--image":
			i++
			if i >= len(args) {
				return &WbError{Code: CodeConfig, Message: "--image requires a value"}
			}
			image = args[i]
		case "--force":
			force = true
		case "--json", "--debug":
			// handled by the dispatcher
		default:
			return &WbError{Code: CodeConfig, Message: fmt.Sprintf("unknown init option %q", args[i])}
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if name == "" {
		name = sanitizeName(filepath.Base(cwd))
	}
	path := filepath.Join(cwd, manifest.DefaultFileName)
	if _, err := os.Stat(path); err == nil && !force {
		return &WbError{Code: CodeConfig, Message: fmt.Sprintf("%s already exists (use --force to overwrite)", path)}
	}
	content := fmt.Sprintf(initTemplate, name, image)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(app.Stdout, "created %s\n", path)
	fmt.Fprintf(app.Stdout, "next: run [worksync up] to create the development container\n")
	return nil
}

// sanitizeName maps an arbitrary directory name to a valid project name.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' && b.Len() > 0:
			b.WriteRune(r)
		default:
			if b.Len() > 0 {
				b.WriteRune('-')
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "project"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func cmdStatus(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	b, err := resolveBackend(proj, flagValue(args, "--backend"))
	if err != nil {
		return err
	}
	container, cerr := app.DB.GetContainer(proj.ID)
	volumes, _ := app.DB.ListVolumes(proj.ID)
	portRows, _ := app.DB.ListPorts(proj.ID)
	refRows, _ := app.DB.ListRefs(proj.ID)
	type statusJSON struct {
		Project   string           `json:"project"`
		Manifest  string           `json:"manifest"`
		Backend   string           `json:"backend"`
		Container *state.Container `json:"container,omitempty"`
		Volumes   []state.Volume   `json:"volumes"`
		Ports     []state.Port     `json:"ports"`
		Refs      []state.RefRow   `json:"refs"`
	}
	sj := statusJSON{
		Project:  proj.ID,
		Manifest: proj.ManifestPath,
		Backend:  string(b.Kind),
		Volumes:  volumes,
		Ports:    portRows,
		Refs:     refRows,
	}
	if cerr == nil {
		sj.Container = container
	}
	return writeJSON(app, sj, func() error {
		fmt.Fprintf(app.Stdout, "project: %s\n", proj.ID)
		fmt.Fprintf(app.Stdout, "manifest: %s\n", proj.ManifestPath)
		fmt.Fprintf(app.Stdout, "backend: %s\n", b.Kind)
		if container != nil {
			fmt.Fprintf(app.Stdout, "container: %s (%s, %s)\n", container.Name, container.State, container.ImageTag)
		} else {
			fmt.Fprintln(app.Stdout, "container: absent")
		}
		if len(volumes) > 0 {
			fmt.Fprintln(app.Stdout, "volumes:")
			for _, v := range volumes {
				fmt.Fprintf(app.Stdout, "  %-12s %s (%s)\n", v.Name, v.Target, v.Policy)
			}
		}
		if len(portRows) > 0 {
			fmt.Fprintln(app.Stdout, "ports:")
			for _, p := range portRows {
				fmt.Fprintf(app.Stdout, "  %-10s %s:%d -> %d/%s\n", p.Name, p.Listen, p.Published, p.Target, p.Protocol)
			}
		}
		return nil
	})
}

func cmdDoctor(ctx context.Context, app *App, args []string) error {
	type check struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail,omitempty"`
	}
	var checks []check
	add := func(name, detail string, ok bool) {
		checks = append(checks, check{Name: name, OK: ok, Detail: detail})
	}
	// backend selection (design §7)
	b, err := backend.AutoDetect("auto")
	if err != nil {
		add("backend", err.Error(), false)
	} else {
		add("backend", string(b.Kind), true)
		caps := make([]string, 0, len(b.Caps))
		for c := range b.Caps {
			caps = append(caps, string(c))
		}
		add("backend capabilities", strings.Join(caps, ", "), true)
	}
	// tools (design §23 doctor)
	for _, tool := range []string{"podman", "restic", "ssh"} {
		if path, err := exec.LookPath(tool); err == nil {
			add(tool, path, true)
		} else {
			add(tool, "not found on PATH (optional until used)", false)
		}
	}
	// store writability (design §9.1)
	probe := filepath.Join(app.Layout.Root, ".doctor-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		add("data dir", app.Layout.Root+": "+err.Error(), false)
	} else {
		os.Remove(probe)
		add("data dir", app.Layout.Root, true)
	}
	return writeJSON(app, checks, func() error {
		ok := true
		for _, c := range checks {
			mark := "ok"
			if !c.OK {
				mark = "FAIL"
				ok = false
			}
			fmt.Fprintf(app.Stdout, "%-10s %-4s %s\n", c.Name, mark, c.Detail)
		}
		if ok {
			fmt.Fprintln(app.Stdout, "\nall checks passed")
		} else {
			fmt.Fprintln(app.Stdout, "\nsome checks failed; see worksync docs for installation steps")
		}
		return nil
	})
}

func cmdHelp(ctx context.Context, app *App, args []string) error {
	if len(args) > 0 {
		for i := range commands {
			if commands[i].Name == strings.TrimPrefix(args[0], "--") {
				fmt.Fprintf(app.Stdout, "usage: worksync %s\n\n%s\n", commands[i].Usage, commands[i].Short)
				return nil
			}
		}
	}
	var b strings.Builder
	printUsage(&b)
	fmt.Fprint(app.Stdout, b.String())
	return nil
}

// flagValue returns the value of --flag in args or "".
func flagValue(args []string, flag string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// Workdir returns the process working directory (kept small to avoid
// re-reading env in every command).
func (a *App) Workdir() string {
	if a.wd == "" {
		a.wd, _ = os.Getwd()
	}
	return a.wd
}
