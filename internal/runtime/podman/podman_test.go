package podman

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"worksync/internal/backend"
	"worksync/internal/manifest"
	"worksync/internal/ports"
	"worksync/internal/volume"
)

func TestPullArgs(t *testing.T) {
	if got := PullArgs("node:24"); !reflect.DeepEqual(got, []string{"pull", "node:24"}) {
		t.Errorf("got %v", got)
	}
}

func TestCreateArgs(t *testing.T) {
	spec := CreateSpec{
		Name:    "worksync-demo",
		Image:   "sha256:abc",
		Workdir: "/workspace",
		User:    "dev",
		KeepID:  true,
		Env:     map[string]string{"NODE_ENV": "development"},
		Mounts: []Mount{
			{Host: "/src", Target: "/workspace"},
			{Host: "/data", Target: "/db", ReadOnly: true},
		},
		Ports:   []ports.Port{{Name: "web", Target: 3000, Published: "3000", Listen: "127.0.0.1", Protocol: "tcp"}},
		Command: []string{"/opt/worksync/bin/worksync-agent", "idle"},
	}
	args := CreateArgs(spec)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"create",
		"--name", "worksync-demo",
		"--userns=keep-id",
		"--workdir", "/workspace",
		"--user", "dev",
		"--env", "NODE_ENV=development",
		"--volume", "/src:/workspace",
		"--volume", "/data:/db:ro",
		"--publish", "127.0.0.1:3000:3000/tcp",
		"sha256:abc",
		"/opt/worksync/bin/worksync-agent", "idle",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}
}

func TestMountsFromManifest(t *testing.T) {
	m := &manifest.Manifest{
		Volumes: map[string]manifest.VolumeSpec{
			"workspace":  {Target: "/workspace", Policy: volume.Tracked, Source: &manifest.SourceSpec{Type: "host", Path: "/host/src"}},
			"home":       {Target: "/home/dev", Policy: volume.Persistent},
			"npm-cache":  {Target: "/home/dev/.npm", Policy: volume.Cache},
			"dsh-config": {Target: "/home/dev/.dsh", Policy: volume.Tracked},
			"token":      {Target: "/run/secrets/token", Policy: volume.Secret},
		},
	}
	mounts := MountsFromManifest(m, "demo", "/data-root")
	byTarget := map[string]Mount{}
	for _, mo := range mounts {
		byTarget[mo.Target] = mo
	}
	if byTarget["/workspace"].Host != "/host/src" {
		t.Errorf("workspace host = %q", byTarget["/workspace"].Host)
	}
	if byTarget["/home/dev"].Host != "/data-root/demo/volumes/home" {
		t.Errorf("home host = %q", byTarget["/home/dev"].Host)
	}
	if byTarget["/home/dev/.npm"].Host != "/data-root/demo/caches/npm-cache" {
		t.Errorf("npm-cache host = %q", byTarget["/home/dev/.npm"].Host)
	}
	if byTarget["/run/secrets/token"].Host != "/data-root/demo/secrets/token" {
		t.Errorf("secret host = %q", byTarget["/run/secrets/token"].Host)
	}
}

func TestClientMachineGlobalArgs(t *testing.T) {
	c := New(backend.Backend{Kind: backend.KindMachine}, "myvm")
	if !reflect.DeepEqual(c.GlobalArgs, []string{"--remote", "--connection", "myvm"}) {
		t.Errorf("global args = %v", c.GlobalArgs)
	}
	c2 := New(backend.Backend{Kind: backend.KindNative}, "")
	if len(c2.GlobalArgs) != 0 {
		t.Errorf("native should have no global args, got %v", c2.GlobalArgs)
	}
}

// fakePodman creates a fake podman executable that records args and emits
// canned responses keyed by subcommand.
func fakePodman(t *testing.T) (binDir string, logPath string) {
	t.Helper()
	binDir = t.TempDir()
	logPath = filepath.Join(t.TempDir(), "args.log")
	script := `#!/bin/sh
echo "$@" >> "$LOGFILE"
case "$1" in
  pull) echo "Pulled";;
  image) echo "sha256:"$(printf 'ab%.0s' $(seq 1 32));;
  create) echo "container-abc123";;
  ps) echo "";;
  start) echo "container-abc123";;
  stop) echo "container-abc123";;
  commit) echo "localhost/worksync-demo:latest";;
  port) echo "3000/tcp -> 127.0.0.1:3000";;
  diff) echo "C /usr/bin/newtool";;
  rm) echo "container-abc123";;
  *) echo "ok";;
esac
exit 0
`
	script = strings.ReplaceAll(script, "$LOGFILE", logPath)
	bin := filepath.Join(binDir, "podman")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return binDir, logPath
}

func TestClientEndToEnd(t *testing.T) {
	_, logPath := fakePodman(t)
	c := New(backend.Backend{Kind: backend.KindNative}, "")
	ctx := context.Background()

	if err := c.Pull(ctx, "node:24"); err != nil {
		t.Fatal(err)
	}
	dg, err := c.ResolveDigest(ctx, "node:24")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dg, "sha256:") {
		t.Errorf("digest = %q", dg)
	}
	id, err := c.Create(ctx, CreateSpec{Name: "worksync-demo", Image: dg})
	if err != nil {
		t.Fatal(err)
	}
	if id != "container-abc123" {
		t.Errorf("id = %q", id)
	}
	if err := c.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, id, 10); err != nil {
		t.Fatal(err)
	}
	img, err := c.Commit(ctx, id)
	if err != nil || img == "" {
		t.Fatalf("commit: %v %q", err, img)
	}
	portMap, err := c.ListPublishedPorts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if portMap["3000/tcp"] != "127.0.0.1:3000" {
		t.Errorf("ports = %v", portMap)
	}
	diffs, err := c.Diff(ctx, id)
	if err != nil || len(diffs) != 1 {
		t.Fatalf("diff: %v %v", diffs, err)
	}
	// verify recorded args
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pull node:24", "image inspect", "create --name worksync-demo", "start container-abc123", "stop --time 10 container-abc123", "commit container-abc123"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("missing %q in log: %s", want, log)
		}
	}
}

func TestExistsContainer(t *testing.T) {
	_, _ = fakePodman(t)
	c := New(backend.Backend{Kind: backend.KindNative}, "")
	ok, err := c.ExistsContainer(context.Background(), "worksync-demo")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("fake ps returns empty; container should not exist")
	}
}
