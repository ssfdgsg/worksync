// Package digest provides content-addressed digests (sha256:...) used to
// identify immutable worksync objects: commit descriptors, OCI blobs and
// image references.
package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Digest is a content digest in the form "<algorithm>:<hex>", e.g.
// "sha256:ab12...".
type Digest string

// Algorithm is the hash algorithm prefix of a digest.
type Algorithm string

const (
	SHA256 Algorithm = "sha256"
)

var (
	ErrInvalidDigest = errors.New("invalid digest")
	ErrUnsupported   = errors.New("unsupported digest algorithm")
)

// FromBytes computes a SHA-256 digest of the given bytes.
func FromBytes(b []byte) Digest {
	sum := sha256.Sum256(b)
	return Digest(string(SHA256) + ":" + hex.EncodeToString(sum[:]))
}

// FromReader computes a SHA-256 digest of everything read from r.
func FromReader(r io.Reader) (Digest, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return Digest(string(SHA256) + ":" + hex.EncodeToString(h.Sum(nil))), nil
}

// Parse validates and returns an opaque digest string.
func Parse(s string) (Digest, error) {
	algo, hexPart, ok := strings.Cut(s, ":")
	if !ok {
		return "", fmt.Errorf("%w: missing algorithm separator", ErrInvalidDigest)
	}
	if algo != string(SHA256) {
		return "", fmt.Errorf("%w: %q", ErrUnsupported, algo)
	}
	if len(hexPart) != sha256.Size*2 {
		return "", fmt.Errorf("%w: bad hex length", ErrInvalidDigest)
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidDigest, err)
	}
	return Digest(s), nil
}

// Algorithm returns the algorithm prefix.
func (d Digest) Algorithm() Algorithm {
	s := string(d)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return Algorithm(s[:i])
	}
	return ""
}

// Hex returns the hex payload after the colon.
func (d Digest) Hex() string {
	s := string(d)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[i+1:]
	}
	return ""
}

// Validate verifies the digest format.
func (d Digest) Validate() error {
	_, err := Parse(string(d))
	return err
}

func (d Digest) String() string { return string(d) }
