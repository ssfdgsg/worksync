package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"worksync/internal/store"
	"worksync/internal/transport/sshurl"
)

func mustURL(t *testing.T, s string) *sshurl.URL {
	t.Helper()
	u, err := sshurl.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"simple": "'simple'",
		"a b":    "'a b'",
		"":       "''",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestBaseResolution(t *testing.T) {
	ep := NewEndpoint(mustURL(t, "ssh://dev@host/~/store"))
	if got := ep.Base("/home/dev"); got != "/home/dev/store" {
		t.Errorf("tilde base = %s", got)
	}
	ep2 := NewEndpoint(mustURL(t, "ssh://host/abs/store"))
	if got := ep2.Base("/home/x"); got != "/abs/store" {
		t.Errorf("absolute base = %s", got)
	}
}

func TestStorePaths(t *testing.T) {
	ep := NewEndpoint(mustURL(t, "ssh://dev@host/~/store"))
	const base = "/home/dev/store"
	if got := ep.RefPath(base, "demo", "latest"); got != "/home/dev/store/projects/demo/refs/latest.json" {
		t.Errorf("ref = %s", got)
	}
	if got := ep.BlobPath(base, "demo", "abc"); got != "/home/dev/store/projects/demo/objects/oci/blobs/sha256/abc" {
		t.Errorf("blob = %s", got)
	}
	if got := ep.ResticRepo(base, "demo"); got != "/home/dev/store/projects/demo/objects/restic" {
		t.Errorf("restic = %s", got)
	}
}

// TestFindRecursive enumerates remote files under a dir via the fake ssh
// (E2E-002B: restic objects are mirrored file-by-file).
func TestFindRecursive(t *testing.T) {
	root := t.TempDir()
	fakeRemote(t, root)
	ep := NewEndpoint(mustURL(t, "ssh://dev@host/~/store"))
	ctx := context.Background()
	dir := ep.ResticRepo(root, "demo")
	if err := ep.MkdirAll(ctx, dir); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "snapshot-abc")
	if err := os.WriteFile(local, []byte("snap"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ep.Upload(ctx, local, dir+"/snapshots/abc"); err != nil {
		t.Fatal(err)
	}
	if err := ep.Upload(ctx, local, dir+"/config"); err != nil {
		t.Fatal(err)
	}
	files, err := ep.FindRecursive(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f] = true
	}
	if !got["snapshots/abc"] || !got["config"] {
		t.Errorf("FindRecursive = %v, want snapshots/abc and config", files)
	}
}

// fakeRemote installs fake ssh and sftp binaries that mirror remote state
// into a local directory.
func fakeRemote(t *testing.T, root string) {
	t.Helper()
	dir := t.TempDir()
	sshScript := `#!/bin/sh
# drop options and the host argument, keep the remote command
while [ "$1" != "" ]; do
  case "$1" in
    -p) shift; shift;;
    -*) shift;;
    *) break;;
  esac
done
[ "$1" != "" ] && shift
cmd="$*"
case "$cmd" in
  *HOME*) echo "$WB_FAKE_HOME"; exit 0;;
esac
eval "$(echo "$cmd" | sed "s/'//g")"
exit $?
`
	out := filepath.Join(dir, "ssh")
	if err := os.WriteFile(out, []byte(sshScript), 0o755); err != nil {
		t.Fatal(err)
	}
	sftpScript := `#!/bin/sh
resolve() { p="$1"; case "$p" in /*) echo "$p";; *) echo "$WB_FAKE_REMOTE/$p";; esac; }
while IFS= read -r line; do
  set -- $line
  op="$1"; shift
  case "$op" in
    ls) shift; p=$(resolve "$(echo "$1" | tr -d "'")"); if [ -e "$p" ]; then echo ok; else exit 1; fi;;
    put) l=$(echo "$1" | tr -d "'"); r=$(resolve "$(echo "$2" | tr -d "'")"); mkdir -p "$(dirname "$r")"; cp "$l" "$r";;
    rename) a=$(resolve "$(echo "$1" | tr -d "'")"); b=$(resolve "$(echo "$2" | tr -d "'")"); mkdir -p "$(dirname "$b")"; mv "$a" "$b";;
    get) r=$(resolve "$(echo "$1" | tr -d "'")"); l=$(echo "$2" | tr -d "'"); mkdir -p "$(dirname "$l")"; cp "$r" "$l";;
  esac
done
exit 0
`
	out2 := filepath.Join(dir, "sftp")
	if err := os.WriteFile(out2, []byte(sftpScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("WB_FAKE_HOME", root)
	t.Setenv("WB_FAKE_REMOTE", root)
}

func TestEndpointUploadRoundTrip(t *testing.T) {
	root := t.TempDir()
	fakeRemote(t, root)
	ep := NewEndpoint(mustURL(t, "ssh://dev@host/~/store"))
	ctx := context.Background()
	local := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(local, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	remotePath := ep.RefPath(root, "demo", "latest")
	if err := ep.Upload(ctx, local, remotePath); err != nil {
		t.Fatal(err)
	}
	exists, err := ep.Exists(ctx, remotePath)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("uploaded file not visible")
	}
	out := filepath.Join(t.TempDir(), "out.bin")
	if err := ep.Download(ctx, remotePath, out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Errorf("round trip = %q", b)
	}
}

// TestResticObjectMirror pins the E2E-002B contract: restic objects are
// mirrored file-by-file (not `restic copy`), so the snapshot files —
// whose names ARE the snapshot IDs — survive a push then pull unchanged.
func TestResticObjectMirror(t *testing.T) {
	root := t.TempDir()
	fakeRemote(t, root)
	ep := NewEndpoint(mustURL(t, "ssh://dev@host/~/store"))
	ctx := context.Background()
	layout := &store.Layout{ResticDir: t.TempDir() + "/restic"}
	repo := filepath.Join(layout.ResticDir, "repository")
	// a fake restic repository with one snapshot file whose name is the ID
	snapID := strings.Repeat("ab", 32) // 64 hex chars
	dirs := []string{
		filepath.Join(repo, "keys"),
		filepath.Join(repo, "data", "aa"),
		filepath.Join(repo, "index"),
		filepath.Join(repo, "snapshots"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"config":              "{\"version\":2}",
		"keys/0001":           "key-bytes",
		"data/aa/pack01":      "pack-bytes",
		"index/index01":       "index-bytes",
		"snapshots/" + snapID: "{\"id\":\"" + snapID + "\"}",
	}
	for rel, content := range files {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pw := func() (string, error) { return "pw", nil }
	st := NewStore(ep, "demo", layout, pw)
	base, err := st.base(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.pushResticObjects(ctx, base)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if n != len(files) {
		t.Errorf("push uploaded %d, want %d", n, len(files))
	}
	// second push must be a no-op (dedup by filename)
	n2, err := st.pushResticObjects(ctx, base)
	if err != nil {
		t.Fatalf("push2: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second push uploaded %d, want 0", n2)
	}
	// fresh clone: empty local repo, pull everything back
	cloneRepo := filepath.Join(t.TempDir(), "repository")
	cloneLayout := &store.Layout{ResticDir: filepath.Dir(cloneRepo)}
	st2 := NewStore(ep, "demo", cloneLayout, pw)
	base2, err := st2.base(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n3, err := st2.pullResticObjects(ctx, base2)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if n3 != len(files) {
		t.Errorf("pull downloaded %d, want %d", n3, len(files))
	}
	// the snapshot ID file must exist unchanged on the clone (E2E-002B)
	got, err := os.ReadFile(filepath.Join(cloneRepo, "snapshots", snapID))
	if err != nil {
		t.Fatalf("snapshot id not preserved: %v", err)
	}
	if string(got) != files["snapshots/"+snapID] {
		t.Errorf("snapshot content drift: %q", got)
	}
}

func TestExistsNegative(t *testing.T) {
	root := t.TempDir()
	fakeRemote(t, root)
	ep := NewEndpoint(mustURL(t, "ssh://dev@host/~/store"))
	exists, err := ep.Exists(context.Background(), "/nope/missing")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("missing path reported as existing")
	}
}
