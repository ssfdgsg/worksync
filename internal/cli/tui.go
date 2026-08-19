package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// errInitCancelled marks a user abort in the init wizard; callers treat it
// as a clean no-op (exit 0) instead of a failure.
var errInitCancelled = errors.New("init cancelled")

// ANSI color codes for terminal output. Colors are only applied when the
// app runs on a real terminal (App.Interactive); otherwise the raw text is
// returned unchanged so logs and tests stay clean.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiDim    = "\x1b[2m"
)

func (a *App) paint(code, s string) string {
	if !a.Interactive {
		return s
	}
	return code + s + ansiReset
}

func (a *App) bold(s string) string   { return a.paint(ansiBold, s) }
func (a *App) cyan(s string) string   { return a.paint(ansiCyan, s) }
func (a *App) green(s string) string  { return a.paint(ansiGreen, s) }
func (a *App) yellow(s string) string { return a.paint(ansiYellow, s) }
func (a *App) red(s string) string    { return a.paint(ansiRed, s) }
func (a *App) dim(s string) string    { return a.paint(ansiDim, s) }

// ask prints a prompt and reads one line. The returned string is trimmed;
// an empty answer is reported as ok=false so callers can apply defaults.
func (a *App) ask(prompt string) (string, bool) {
	fmt.Fprintf(a.Stdout, "%s ", a.cyan(prompt))
	if a.Stdin == nil {
		return "", false
	}
	var line string
	if _, err := fmt.Fscanln(a.Stdin, &line); err != nil {
		return "", false
	}
	return strings.TrimSpace(line), true
}

// initWizard runs the guided `worksync init` flow: project name, base image
// (menu or any custom reference), optional published port, and a final
// confirmation with a preview. Every step accepts Enter for the default.
// Returns the final (name, image, portYAML) or an error when cancelled.
func (a *App) initWizard(defaultName string) (string, string, string, error) {
	fmt.Fprintln(a.Stdout, a.bold("worksync init — create a new project"))

	// 1. project name
	name := defaultName
	fmt.Fprintf(a.Stdout, "project name %s [%s]: ", a.dim("(Enter=default)"), a.bold(defaultName))
	if n, ok := a.ask(""); ok && n != "" {
		name = sanitizeName(n)
	}

	// 2. base image: number from menu, or a custom image name/URL
	fmt.Fprintln(a.Stdout, a.dim("base image — pick a number, or paste any image URL/name:"))
	for i, c := range imageChoices {
		fmt.Fprintf(a.Stdout, "  %s %-24s %s\n", a.cyan(fmt.Sprintf("%d)", i+1)), c.image, a.dim(c.lang))
	}
	image := ""
	if img, ok := a.ask("choose [" + strconv.Itoa(len(imageChoices)) + "] (or image URL):"); ok && img != "" {
		if sel, err := strconv.Atoi(img); err == nil && sel >= 1 && sel <= len(imageChoices) {
			image = imageChoices[sel-1].image
		} else {
			image = img // custom reference
		}
	}
	if image == "" {
		image = langImage("")
	}

	// 3. published port (optional, custom allowed)
	portYAML := defaultPortYAML()
	fmt.Fprintln(a.Stdout, a.dim("publish a port? (Enter=web:3000 auto, 0=none, or name:target)"))
	if p, ok := a.ask("port [web:3000]:"); ok && p != "" {
		switch p {
		case "0", "none", "-":
			portYAML = ""
		case "web:3000", "default":
			portYAML = defaultPortYAML()
		default:
			if y, err := customPortYAML(p); err == nil {
				portYAML = y
			} else {
				fmt.Fprintf(a.Stdout, "%s ignoring %q (expected name:target)\n", a.yellow("!"), p)
			}
		}
	}

	// 4. preview + confirm
	content := buildInitYAML(name, image, portYAML)
	fmt.Fprintln(a.Stdout, a.dim("preview:"))
	for _, ln := range strings.Split(content, "\n") {
		fmt.Fprintf(a.Stdout, "%s\n", a.dim(ln))
	}
	if ok, _ := a.confirm("write worksync.yaml? [Y/n]:"); !ok {
		return "", "", "", errInitCancelled
	}
	return name, image, content, nil
}

// confirm asks a yes/no question; empty answer means yes.
func (a *App) confirm(prompt string) (bool, bool) {
	ans, ok := a.ask(prompt)
	if !ok || ans == "" || ans == "y" || ans == "Y" || ans == "yes" {
		return true, ok
	}
	return false, ok
}

// defaultPortYAML returns the scaffold's default published-port block.
func defaultPortYAML() string {
	return "ports:\n" +
		"  - name: web\n" +
		"    target: 3000\n" +
		"    published: auto\n" +
		"    listen: 127.0.0.1\n" +
		"    protocol: tcp\n"
}

// customPortYAML renders a single port block from a name:target string.
// A bare number is allowed and becomes name "web".  Returns an error for
// invalid targets so the wizard can warn instead of crashing.
func customPortYAML(spec string) (string, error) {
	name := "web"
	target := spec
	if c, t, ok := strings.Cut(spec, ":"); ok {
		name = strings.TrimSpace(c)
		target = strings.TrimSpace(t)
	}
	n, err := strconv.Atoi(target)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("invalid port %q", spec)
	}
	return "ports:\n" +
		fmt.Sprintf("  - name: %s\n", name) +
		fmt.Sprintf("    target: %d\n", n) +
		"    published: auto\n" +
		"    listen: 127.0.0.1\n" +
		"    protocol: tcp\n", nil
}

// buildInitYAML assembles the scaffold from wizard choices.
func buildInitYAML(name, image, portYAML string) string {
	var b strings.Builder
	b.WriteString("schemaVersion: 1\n")
	b.WriteString("name: " + name + "\n\n")
	b.WriteString("runtime:\n  engine: podman\n  backend: auto\n  rootless: true\n\n")
	b.WriteString("container:\n  image: " + image + "\n")
	b.WriteString("  persistentRoot: true\n  workdir: /workspace\n")
	// no user: use the image's default user. Hardcoding a name (e.g. dev)
	// breaks generic images (debian/ubuntu have no such user) at container
	// start; the image default (root for most, node for node images, etc.)
	// is always present.
	b.WriteString("  command: [\"/opt/worksync/bin/worksync-agent\", \"idle\"]\n")
	b.WriteString("  environment:\n    NODE_ENV: development\n\n")
	b.WriteString(portYAML)
	if portYAML != "" {
		b.WriteString("\n")
	}
	b.WriteString("volumes:\n  workspace:\n    source:\n      type: host\n      path: ./\n    target: /workspace\n    policy: tracked\n\n")
	b.WriteString("  home:\n    target: /home/dev\n    policy: persistent\n\n")
	b.WriteString("commit:\n  environment: true\n  volumes:\n    - workspace\n")
	return b.String()
}
