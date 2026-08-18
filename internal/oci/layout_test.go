package oci

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// digestOf returns the sha256:<hex> of the given bytes.
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// buildArchive constructs an in-memory OCI-style tar with blob files whose
// names are the real SHA-256 digests of their padded content.
func buildArchive(t *testing.T, contents ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, c := range contents {
		pad := make([]byte, 32)
		copy(pad, c)
		d := digestOf(pad)
		hdr := &tar.Header{Name: "blobs/sha256/" + strings.TrimPrefix(d, "sha256:"), Mode: 0o644, Size: int64(len(pad))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(pad); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestVerifyArchiveOK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.tar")
	data := buildArchive(t, "hello world", "second blob content")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := VerifyArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Errorf("found %d blobs", len(found))
	}
}

func TestVerifyArchiveMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.tar")
	// content "a" but filename claims the digest of "b"
	padA := make([]byte, 32)
	padA[0] = 'a'
	padB := make([]byte, 32)
	padB[0] = 'b'
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	d := digestOf(padB)
	hdr := &tar.Header{Name: "blobs/sha256/" + strings.TrimPrefix(d, "sha256:"), Mode: 0o644, Size: 32}
	tw.WriteHeader(hdr)
	tw.Write(padA)
	tw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyArchive(path); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("want digest mismatch, got %v", err)
	}
}

func TestVerifyArchiveNoBlobs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.tar")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyArchive(path); err == nil {
		t.Fatal("expected no-blobs error")
	}
}

func TestExtractBlobs(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "env.tar")
	blob := "payload"
	pad := make([]byte, 32)
	copy(pad, blob)
	data := buildArchive(t, blob)
	if err := os.WriteFile(archive, data, 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out")
	if err := ExtractBlobs(archive, dest); err != nil {
		t.Fatal(err)
	}
	extracted := filepath.Join(dest, "blobs", "sha256", strings.TrimPrefix(digestOf(pad), "sha256:"))
	b, err := os.ReadFile(extracted)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(pad) {
		t.Errorf("content = %q", b)
	}
}
