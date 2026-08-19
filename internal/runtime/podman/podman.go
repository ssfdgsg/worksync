// Package podman wraps the rootless Podman runtime: all podman command
// construction lives here (design §8, §19). Command builders are pure
// functions returning argument slices so they can be unit-tested without a
// running daemon; Client executes them against native or machine Podman.
package podman

import (
	"context"
	"fmt"
	"io"
	"strings"

	"worksync/internal/backend"
	"worksync/internal/executil"
	"worksync/internal/manifest"
	"worksync/internal/ports"
	"worksync/internal/volume"
)

// Client executes podman commands for a selected backend.
type Client struct {
	Bin        string
	GlobalArgs []string // e.g. --remote --connection machine
	Stdout     io.Writer
	Stderr     io.Writer
	// ProbeErr, when set, records a failed machine-connection probe; Run
	// fails fast with it instead of executing podman against a bogus
	// connection.
	ProbeErr error
}

// New creates a client for the given backend. The podman-machine backend
// routes through a named machine connection (design §7.3). When machineName
// is empty the default connection is probed via `podman system connection
// ls`; a probe that finds no connection is surfaced as ProbeErr so the user
// gets a clear diagnostic instead of a raw `connection not found` failure.
func New(b backend.Backend, machineName string) *Client {
	c := &Client{Bin: "podman"}
	if b.Kind == backend.KindMachine {
		conn := machineName
		if conn == "" {
			var err error
			conn, err = DefaultMachineConnection(context.Background(), c.Bin)
			if err != nil {
				c.ProbeErr = err
				conn = "podman-machine-default" // placeholder; Run fails fast
			}
		}
		c.GlobalArgs = []string{"--remote", "--connection", conn}
	}
	return c
}

// DefaultMachineConnection resolves the machine connection used for
// --remote. It prefers the connection marked default in `podman system
// connection ls`, then the first listed name. An empty result (no machine
// configured on this host) returns a descriptive error with the fix.
func DefaultMachineConnection(ctx context.Context, bin string) (string, error) {
	res, err := executil.Run(ctx, bin, []string{"system", "connection", "ls", "--format", "{{.Name}} {{.Default}}"})
	if err != nil {
		return "", fmt.Errorf("cannot list podman connections: %v (is podman installed?) ", err)
	}
	best := ""
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		if best == "" {
			best = fields[0]
		}
		if fields[1] == "true" {
			return fields[0], nil
		}
	}
	if best != "" {
		return best, nil
	}
	return "", fmt.Errorf("no podman machine found: run [podman machine init] then [podman machine start] (see [podman system connection ls])")
}

// Run executes podman with args, returning captured output. A failed
// machine-connection probe fails fast with the probe diagnostic.
func (c *Client) Run(ctx context.Context, args ...string) (executil.Result, error) {
	if c.ProbeErr != nil {
		return executil.Result{}, c.ProbeErr
	}
	all := append(append([]string{}, c.GlobalArgs...), args...)
	res, err := executil.Run(ctx, c.Bin, all, executil.WithStdout(c.Stdout), executil.WithStderr(c.Stderr))
	if err != nil {
		return res, wrapPodmanError(c.Bin, all, err)
	}
	return res, nil
}

// wrapPodmanError turns raw podman exit errors into diagnostics a user can
// act on, while keeping the underlying message available.
func wrapPodmanError(bin string, args []string, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection") && strings.Contains(msg, "not found"):
		return fmt.Errorf("%s: podman connection not found — run [podman machine init] && [podman machine start] first", strings.Join(args, " "))
	case strings.Contains(msg, "cannot connect"), strings.Contains(msg, "connection refused"), strings.Contains(msg, "no such host"):
		return fmt.Errorf("%s: cannot reach podman — is the machine running? ([podman machine start])", strings.Join(args, " "))
	case strings.Contains(msg, "unable to find user"):
		return fmt.Errorf("the image has no such user (check container.user in worksync.yaml): %s", msg)
	default:
		return err
	}
}

// ---- pure command builders (unit-testable) ----

// PullArgs builds `podman pull <image>`.
func PullArgs(image string) []string { return []string{"pull", image} }

// InspectImageArgs returns a create-able immutable image reference via
// inspect. {{.Id}} is used instead of {{.Digest}}: for multi-arch images
// the latter is the manifest-list digest, which podman create rejects with
// "image not known".
func InspectImageArgs(image string) []string {
	return []string{"image", "inspect", "--format", "{{.Id}}", image}
}

// Mount is a bind-mount or volume mount entry.
type Mount struct {
	Host     string // host path or volume name
	Target   string // container path
	ReadOnly bool
}

// CreateSpec is the fully-resolved input to container creation (design §8).
type CreateSpec struct {
	Name       string
	Image      string // resolved digest reference
	Workdir    string
	User       string
	Entrypoint string   // override ENTRYPOINT (agent injection)
	Command    []string // override CMD
	Env        map[string]string
	Mounts     []Mount
	Ports      []ports.Port
	KeepID     bool
	Labels     map[string]string
}

// CreateArgs builds `podman create ...` for a persistent dev container.
func CreateArgs(spec CreateSpec) []string {
	args := []string{"create"}
	args = append(args, "--name", spec.Name)
	for k, v := range spec.Labels {
		args = append(args, "--label", k+"="+v)
	}
	if spec.KeepID {
		args = append(args, "--userns=keep-id")
	}
	if spec.Workdir != "" {
		args = append(args, "--workdir", spec.Workdir)
	}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	for k, v := range spec.Env {
		args = append(args, "--env", k+"="+v)
	}
	for _, m := range spec.Mounts {
		mount := m.Host + ":" + m.Target
		if m.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "--volume", mount)
	}
	for _, p := range spec.Ports {
		// -p [listen:]published:target/proto (design §11). Published stays a
		// string: "auto" lets podman assign, a number is used verbatim.
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		args = append(args, "--publish", fmt.Sprintf("%s:%s:%d/%s", p.Listen, p.Published, p.Target, proto))
	}
	if spec.Entrypoint != "" {
		args = append(args, "--entrypoint", spec.Entrypoint)
	}
	args = append(args, spec.Image)
	if len(spec.Command) > 0 {
		args = append(args, spec.Command...)
	}
	return args
}

// StartArgs builds `podman start <id>`.
func StartArgs(id string) []string { return []string{"start", id} }

// StopArgs builds `podman stop --time <s> <id>` (design §8.2: stop keeps
// rootfs and volumes).
func StopArgs(id string, timeoutSeconds uint) []string {
	return []string{"stop", "--time", fmt.Sprintf("%d", timeoutSeconds), id}
}

// ExecArgs builds `podman exec <flags> <id> <cmd...>`. -i keeps the container
// stdin attached to the CLI; -t additionally allocates a pseudo-TTY so
// interactive shells behave like a real terminal (design §18.1).
func ExecArgs(id string, cmd []string, tty bool) []string {
	flags := "-i"
	if tty {
		flags = "-it"
	}
	return append([]string{"exec", flags, id}, cmd...)
}

// CommitArgs builds `podman commit <id>`; the output is the new image ref.
func CommitArgs(id string) []string { return []string{"commit", id} }

// CommitNamedArgs builds `podman commit --format oci <id> <name>`, producing
// an image with the stable environment name (design §14.2 step 6).
func CommitNamedArgs(id, name string) []string {
	return []string{"commit", "--format", "oci", id, name}
}

// TagArgs builds `podman tag <source> <target>`.
func TagArgs(source, target string) []string { return []string{"tag", source, target} }

// SaveArgs builds `podman save --format oci-archive -o <dest> <image>`.
func SaveArgs(image, dest string) []string {
	return []string{"save", "--format", "oci-archive", "-o", dest, image}
}

// LoadArgs builds `podman load -i <src>`.
func LoadArgs(src string) []string { return []string{"load", "-i", src} }

// RmArgs builds `podman rm <id>` (design §8.2: rm deletes the container).
func RmArgs(id string) []string { return []string{"rm", id} }

// DiffArgs builds `podman diff <id>` for change reporting.
func DiffArgs(id string) []string { return []string{"diff", id} }

// InspectContainerArgs builds `podman inspect --format <fmt> <id>`.
func InspectContainerArgs(id, format string) []string {
	return []string{"inspect", "--format", format, id}
}

// PortArgs builds `podman port <id>`.
func PortArgs(id string) []string { return []string{"port", id} }

// ContainerName returns the deterministic container name for a project.
func ContainerName(projectID string) string { return "worksync-" + projectID }

// --- derived helpers ---

// MountsFromManifest converts manifest volumes into runtime mounts using the
// backend data root for managed volumes (design §9.2). Host-sourced volumes
// bind-mount their resolved host path; secret volumes are recreated empty
// per run (design §10).
func MountsFromManifest(m *manifest.Manifest, projectID, backendDataRoot string) []Mount {
	var out []Mount
	for name, v := range m.Volumes {
		if v.Source != nil && v.Source.Type == "host" {
			out = append(out, Mount{Host: v.Source.Path, Target: v.Target})
			continue
		}
		host := managedPath(backendDataRoot, projectID, name, v.Policy)
		out = append(out, Mount{Host: host, Target: v.Target})
	}
	return out
}

// VolumeHostPath maps a managed volume to its backend directory (design
// §9.2). Exported so the commit coordinator can locate volume data.
func VolumeHostPath(root, projectID, name string, policy volume.Policy) string {
	return managedPath(root, projectID, name, policy)
}

// managedPath maps a managed volume to its backend directory (design §9.2).
func managedPath(root, projectID, name string, policy volume.Policy) string {
	base := root + "/" + projectID
	switch policy {
	case volume.Tracked:
		return base + "/workspaces/" + name
	case volume.Persistent:
		return base + "/volumes/" + name
	case volume.Cache:
		return base + "/caches/" + name
	case volume.Secret:
		return base + "/secrets/" + name
	default:
		return base + "/ephemeral/" + name
	}
}

// String renders a command line for logs with redaction applied by caller.
func String(args []string) string { return strings.Join(args, " ") }
