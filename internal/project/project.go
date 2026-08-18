// Package project resolves the Project Spec from the filesystem and ties it
// to a stable project identity (design §12).
package project

import (
	"fmt"
	"os"
	"path/filepath"

	"worksync/internal/digest"
	"worksync/internal/manifest"
)

// ErrNotFound is returned when no worksync.yaml can be found.
var ErrNotFound = fmt.Errorf("no %s found (run worksync init?)", manifest.DefaultFileName)

// Project is a resolved project: identity + parsed manifest + digest.
type Project struct {
	ID           string
	ManifestPath string
	Manifest     *manifest.Manifest
	ManifestHash digest.Digest
}

// FindManifest walks up from startDir looking for worksync.yaml.
func FindManifest(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, manifest.DefaultFileName)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNotFound
		}
		dir = parent
	}
}

// Load resolves the project from the current working directory.
func Load(cwd string) (*Project, error) {
	path, err := FindManifest(cwd)
	if err != nil {
		return nil, err
	}
	return LoadFrom(path)
}

// LoadFrom parses the manifest at path and builds the project identity.
func LoadFrom(path string) (*Project, error) {
	m, err := manifest.Load(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &Project{
		ID:           m.Name,
		ManifestPath: path,
		Manifest:     m,
		ManifestHash: digest.FromBytes(raw),
	}, nil
}
