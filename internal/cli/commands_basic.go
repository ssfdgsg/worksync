package cli

import (
	"context"
	"errors"
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

// initDefaultPortYAML is the scaffold's default published-port block.
// (kept in sync with defaultPortYAML in tui.go)

func cmdInit(ctx context.Context, app *App, args []string) error {
	name := ""
	image := ""
	lang := ""
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
		case "--lang":
			i++
			if i >= len(args) {
				return &WbError{Code: CodeConfig, Message: "--lang requires a value"}
			}
			lang = args[i]
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
	// Guided wizard unless the image is already pinned by a flag: the wizard
	// runs on any terminal (not only TTYs) so interactive customizing works
	// from wrapped/GUI terminals too; a closed/piped stdin falls back to the
	// scaffold defaults and --name/--image/--lang skip the matching step.
	var content string
	if image == "" && lang == "" {
		var portYAML string
		name, image, portYAML, err = app.initWizard(name)
		if errors.Is(err, errInitCancelled) {
			fmt.Fprintf(app.Stdout, "%s\n", app.yellow("init cancelled — nothing written"))
			return nil
		}
		if err != nil {
			return err
		}
		content = portYAML
	} else {
		if image == "" {
			if lang != "" {
				image = langImage(lang)
			} else {
				image = langImage("")
			}
		}
		content = buildInitYAML(name, image, defaultPortYAML())
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(app.Stdout, "%s\n", app.green("created "+path))
	fmt.Fprintf(app.Stdout, "next: run %s to create the development container\n", app.bold("[worksync up]"))
	return nil
}

// langImage returns the recommended base image for a language stack. The
// exact tag follows the project's supported toolchains; keep it in sync
// with detectLang's hints.
func langImage(lang string) string {
	switch strings.ToLower(lang) {
	case "go", "golang":
		return "golang:1.24"
	case "node", "nodejs", "js", "ts", "typescript":
		return "node:24"
	case "python", "py":
		return "python:3.13"
	case "rust", "rs":
		return "rust:1.83"
	case "java", "jvm":
		return "eclipse-temurin:21-jdk"
	default:
		// generic development base: full toolchain (apt, git, curl) instead
		// of a busybox-style minimal rootfs that lacks package managers.
		return "debian:bookworm-slim"
	}
}

// imageChoices lists the base images offered by the interactive init menu.
// Keep in sync with langImage so --lang names match menu entries.
var imageChoices = []struct{ lang, image string }{
	{"go", "golang:1.24"},
	{"node", "node:24"},
	{"python", "python:3.13"},
	{"rust", "rust:1.83"},
	{"java", "eclipse-temurin:21-jdk"},
	{"generic (apt/git/curl)", "debian:bookworm-slim"},
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
