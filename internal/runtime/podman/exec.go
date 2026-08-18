package podman

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"worksync/internal/executil"
)

// ErrNotExists is returned when a container or image does not exist.
var ErrNotExists = errors.New("podman object does not exist")

// ExistsContainer reports whether a container exists (podman ps -a).
// The name filter is anchored (^name$) because podman matches prefixes
// otherwise: "name=worksync-app" would also match "worksync-app2".
func (c *Client) ExistsContainer(ctx context.Context, id string) (bool, error) {
	res, err := c.Run(ctx, "ps", "-a", "--filter", "name=^"+id+"$", "--format", "{{.Names}}")
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(res.Stdout) != "", nil
}

// Pull pulls an image (idempotent: podman reuses local images).
func (c *Client) Pull(ctx context.Context, image string) error {
	_, err := c.Run(ctx, PullArgs(image)...)
	return err
}

// ResolveDigest returns the digest of a local image (design §12.2: tag is
// resolved to a digest at create time and recorded). {{.Id}} is used (see
// InspectImageArgs) and normalized to the sha256:<hex> form: real podman
// prints a bare hex here, and callers (descriptor, tests) expect the prefix.
func (c *Client) ResolveDigest(ctx context.Context, image string) (string, error) {
	res, err := c.Run(ctx, InspectImageArgs(image)...)
	if err != nil {
		return "", fmt.Errorf("resolve image digest %s: %w", image, err)
	}
	d := strings.TrimSpace(res.Stdout)
	if d == "" {
		return "", fmt.Errorf("resolve image digest %s: empty output", image)
	}
	if !strings.HasPrefix(d, "sha256:") {
		d = "sha256:" + d
	}
	return d, nil
}

// Create creates a container from a spec and returns its ID.
func (c *Client) Create(ctx context.Context, spec CreateSpec) (string, error) {
	res, err := c.Run(ctx, CreateArgs(spec)...)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(res.Stdout)
	if id == "" {
		return "", fmt.Errorf("podman create returned no container id")
	}
	return id, nil
}

// Start starts a container. A missing container is reported as ErrNotExists.
func (c *Client) Start(ctx context.Context, id string) error {
	_, err := c.Run(ctx, StartArgs(id)...)
	if err != nil {
		if strings.Contains(err.Error(), "no such container") {
			return fmt.Errorf("%w: %s", ErrNotExists, id)
		}
		return err
	}
	return nil
}

// Stop stops a container, keeping rootfs and volumes (design §8.2).
func (c *Client) Stop(ctx context.Context, id string, timeoutSeconds uint) error {
	_, err := c.Run(ctx, StopArgs(id, timeoutSeconds)...)
	if err != nil && strings.Contains(err.Error(), "no such container") {
		return fmt.Errorf("%w: %s", ErrNotExists, id)
	}
	return err
}

// Exec runs a command in a running container, streaming output.
func (c *Client) Exec(ctx context.Context, id string, cmd []string) (executil.Result, error) {
	return c.Run(ctx, ExecArgs(id, cmd)...)
}

// Commit creates an image from the container rootfs (design §14.2 step 6).
func (c *Client) Commit(ctx context.Context, id string) (string, error) {
	res, err := c.Run(ctx, CommitArgs(id)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// CommitNamed commits the rootfs directly to a stable image name.
func (c *Client) CommitNamed(ctx context.Context, id, name string) (string, error) {
	res, err := c.Run(ctx, CommitNamedArgs(id, name)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// Tag retags an image (design §14.2 step 6).
func (c *Client) Tag(ctx context.Context, source, target string) error {
	_, err := c.Run(ctx, TagArgs(source, target)...)
	return err
}

// Save exports an image as an OCI archive (design §14.2 step 7).
func (c *Client) Save(ctx context.Context, image, dest string) error {
	_, err := c.Run(ctx, SaveArgs(image, dest)...)
	return err
}

// Load imports an OCI archive (design §16.4 pull step 4).
func (c *Client) Load(ctx context.Context, src string) error {
	_, err := c.Run(ctx, LoadArgs(src)...)
	return err
}

// Rm removes a container.
func (c *Client) Rm(ctx context.Context, id string) error {
	_, err := c.Run(ctx, RmArgs(id)...)
	return err
}

// Diff reports rootfs changes (podman diff), one path per line.
func (c *Client) Diff(ctx context.Context, id string) ([]string, error) {
	res, err := c.Run(ctx, DiffArgs(id)...)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// ListPublishedPorts returns the container's published ports as parsed from
// "podman port" output lines like "3000/tcp -> 127.0.0.1:3000".
func (c *Client) ListPublishedPorts(ctx context.Context, id string) (map[string]string, error) {
	res, err := c.Run(ctx, PortArgs(id)...)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		left, right, ok := strings.Cut(line, " -> ")
		if !ok {
			continue
		}
		out[strings.TrimSpace(left)] = strings.TrimSpace(right)
	}
	return out, nil
}

// InspectID returns the container's actual id, resolving a name (or id) via
// podman inspect. E2E-006: the DB can hold a stale container id while a
// container with the deterministic name still exists on the runtime; `up`
// must start the real container, not the stale id.
func (c *Client) InspectID(ctx context.Context, id string) (string, error) {
	res, err := c.Run(ctx, InspectContainerArgs(id, "{{.Id}}")...)
	if err != nil {
		if strings.Contains(err.Error(), "no such object") {
			return "", fmt.Errorf("%w: %s", ErrNotExists, id)
		}
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// InspectState returns the container state string via podman inspect.
func (c *Client) InspectState(ctx context.Context, id string) (string, error) {
	res, err := c.Run(ctx, InspectContainerArgs(id, "{{.State.Status}}")...)
	if err != nil {
		if strings.Contains(err.Error(), "no such object") {
			return "", fmt.Errorf("%w: %s", ErrNotExists, id)
		}
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}
