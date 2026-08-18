// Package oci works with OCI archives produced by podman save (design §14.2
// step 7: export the environment as an OCI layout and validate every blob
// digest).
package oci

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"worksync/internal/digest"
)

// blobPathInTar is the standard OCI layout blob path.
func blobPathInTar(hexDigest string) string {
	return filepath.ToSlash(filepath.Join("blobs", "sha256", hexDigest))
}

// VerifyArchive streams an OCI archive (tar) and validates that every blob
// under blobs/sha256/ matches its filename digest. It returns the list of
// blob digests found, in file order.
func VerifyArchive(archivePath string) ([]digest.Digest, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var found []digest.Digest
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read oci archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		name := filepath.ToSlash(hdr.Name)
		prefix := "blobs/sha256/"
		if !strings.HasPrefix(name, prefix) || strings.Contains(name[len(prefix):], "/") {
			continue
		}
		hexDigest := name[len(prefix):]
		if err := validateHex(hexDigest); err != nil {
			return nil, fmt.Errorf("oci archive %s: %w", name, err)
		}
		h := sha256.New()
		if _, err := io.Copy(h, tr); err != nil {
			return nil, fmt.Errorf("hash blob %s: %w", name, err)
		}
		got := hex.EncodeToString(h.Sum(nil))
		if got != hexDigest {
			return nil, fmt.Errorf("oci archive blob %s digest mismatch: got %s", name, got)
		}
		found = append(found, digest.Digest("sha256:"+hexDigest))
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("oci archive %s contains no blobs/sha256 entries", archivePath)
	}
	return found, nil
}

// ExtractBlobs extracts every blobs/sha256/<hex> file from the archive into
// dest/blobs/sha256/ so push can enumerate and upload only missing blobs
// (design §16.3).
func ExtractBlobs(archivePath, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		name := filepath.ToSlash(hdr.Name)
		prefix := "blobs/sha256/"
		if !strings.HasPrefix(name, prefix) || strings.Contains(name[len(prefix):], "/") {
			continue
		}
		hexDigest := name[len(prefix):]
		out := filepath.Join(dest, prefix, hexDigest)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, tr); err != nil {
			w.Close()
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	}
	return nil
}

// ValidateHex checks a 64-char lowercase hex string.
func validateHex(s string) error {
	if len(s) != sha256.Size*2 {
		return fmt.Errorf("bad digest length %d", len(s))
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("bad hex character %q", string(c))
		}
	}
	return nil
}
