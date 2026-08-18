// Package commit defines the immutable Commit Descriptor (design §14).
package commit

import (
	"encoding/json"
	"fmt"
	"time"

	"worksync/internal/digest"
)

// SchemaVersion is the Commit Descriptor schema version (design §14.1).
const SchemaVersion = 1

// Platform identifies the OS/architecture an environment commit was made on
// (design §17.2).
type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

// EnvironmentRef describes the committed environment image (design §14.1).
type EnvironmentRef struct {
	// Base is the original base image reference with digest, e.g.
	// "docker.io/library/node@sha256:...".
	Base string `json:"base"`
	// Image is the digest of the committed environment OCI image.
	Image string `json:"image"`
}

// Descriptor is one immutable worksync commit. It is serialized as canonical
// JSON and identified by its SHA-256 digest (design §14.1).
type Descriptor struct {
	SchemaVersion int               `json:"schemaVersion"`
	Project       string            `json:"project"`
	Platform      Platform          `json:"platform"`
	Environment   EnvironmentRef    `json:"environment"`
	Snapshots     map[string]string `json:"snapshots"` // volume name -> "restic:<id>"
	ConfigDigest  string            `json:"configDigest"`
	Parent        string            `json:"parent,omitempty"`
	Message       string            `json:"message,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
}

// New creates a descriptor with schemaVersion and createdAt filled in.
func New(project string, platform Platform) Descriptor {
	return Descriptor{
		SchemaVersion: SchemaVersion,
		Project:       project,
		Platform:      platform,
		Snapshots:     map[string]string{},
		CreatedAt:     time.Now().UTC(),
	}
}

// Validate checks the descriptor's invariants (design §14.1).
func (d *Descriptor) Validate() error {
	if d.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported descriptor schemaVersion %d", d.SchemaVersion)
	}
	if d.Project == "" {
		return fmt.Errorf("descriptor project is required")
	}
	if d.Platform.OS == "" || d.Platform.Architecture == "" {
		return fmt.Errorf("descriptor platform is required")
	}
	if d.Environment.Image != "" {
		if _, err := digest.Parse(d.Environment.Image); err != nil {
			return fmt.Errorf("environment.image: %w", err)
		}
	}
	if d.ConfigDigest != "" {
		if _, err := digest.Parse(d.ConfigDigest); err != nil {
			return fmt.Errorf("configDigest: %w", err)
		}
	}
	if d.Parent != "" {
		if _, err := digest.Parse(d.Parent); err != nil {
			return fmt.Errorf("parent: %w", err)
		}
	}
	for name, ref := range d.Snapshots {
		if !validSnapshotRef(ref) {
			return fmt.Errorf("snapshot %q has invalid reference %q (want restic:<id>)", name, ref)
		}
	}
	if d.CreatedAt.IsZero() {
		return fmt.Errorf("descriptor createdAt is required")
	}
	return nil
}

func validSnapshotRef(ref string) bool {
	const prefix = "restic:"
	if len(ref) <= len(prefix) {
		return false
	}
	for i, c := range ref {
		if i < len(prefix) {
			if ref[i] != prefix[i] {
				return false
			}
			continue
		}
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// ParseJSON decodes a descriptor from canonical or plain JSON, rejecting
// unknown fields.
func ParseJSON(b []byte) (*Descriptor, error) {
	dec := json.NewDecoder(newBytesReader(b))
	dec.DisallowUnknownFields()
	var d Descriptor
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("parse commit descriptor: %w", err)
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// Digest returns the content digest of the descriptor's canonical JSON.
func (d *Descriptor) Digest() (digest.Digest, error) {
	canon, err := MarshalCanonical(d)
	if err != nil {
		return "", err
	}
	return digest.FromBytes(canon), nil
}
