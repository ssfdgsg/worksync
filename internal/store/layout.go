// Package store defines the local data layout (design §9.1): host metadata,
// state DB, locks, projects, commits, refs, OCI blobs and the local restic
// repository.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ObjectsVersion is the local store layout version.
const ObjectsVersion = 1

// Layout is the resolved local store layout. All paths are absolute.
type Layout struct {
	// Root is the data root (~/.local/share/worksync or equivalent).
	Root string
	// ConfigFile is the host metadata config file (~/.config/worksync/config.yaml).
	ConfigFile string
	// StateDB is the SQLite database path.
	StateDB     string
	LocksDir    string
	ProjectsDir string
	CommitsDir  string
	RefsDir     string
	OCIDir      string
	ResticDir   string
	StagingDir  string
}

// DefaultLayout resolves the layout from environment overrides or platform
// user directories (design §9.1: macOS uses equivalent app-data dirs; CLI
// prints the real paths).
func DefaultLayout() (*Layout, error) {
	dataRoot := os.Getenv("WORKSYNC_DATA_DIR")
	if dataRoot == "" {
		base, err := userDataDir()
		if err != nil {
			return nil, fmt.Errorf("resolve user data dir: %w", err)
		}
		dataRoot = filepath.Join(base, "worksync")
	}
	confRoot := os.Getenv("WORKSYNC_CONFIG_DIR")
	if confRoot == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("resolve user config dir: %w", err)
		}
		confRoot = filepath.Join(base, "worksync")
	}
	return &Layout{
		Root:        dataRoot,
		ConfigFile:  filepath.Join(confRoot, "config.yaml"),
		StateDB:     filepath.Join(dataRoot, "state.db"),
		LocksDir:    filepath.Join(dataRoot, "locks"),
		ProjectsDir: filepath.Join(dataRoot, "projects"),
		CommitsDir:  filepath.Join(dataRoot, "commits"),
		RefsDir:     filepath.Join(dataRoot, "refs"),
		OCIDir:      filepath.Join(dataRoot, "oci"),
		ResticDir:   filepath.Join(dataRoot, "restic"),
		StagingDir:  filepath.Join(dataRoot, "staging"),
	}, nil
}

// Ensure creates the directory layout and writes the store version file.
func (l *Layout) Ensure() error {
	dirs := []string{l.Root, l.LocksDir, l.ProjectsDir, l.CommitsDir, l.RefsDir, l.OCIDir, l.ResticDir, l.StagingDir}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	ver := filepath.Join(l.Root, "version")
	if _, err := os.Stat(ver); os.IsNotExist(err) {
		if err := os.WriteFile(ver, []byte(fmt.Sprintf("%d\n", ObjectsVersion)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ProjectDir returns the per-project data directory (design §9.2 workspaces
// live under the backend data root; projects/ holds lightweight local state).
func (l *Layout) ProjectDir(projectID string) string {
	return filepath.Join(l.ProjectsDir, projectID)
}

// LockPath returns the lock file for a project.
func (l *Layout) LockPath(projectID string) string {
	return filepath.Join(l.LocksDir, projectID+".lock")
}

// CommitDescriptorPath returns the on-disk path for a commit descriptor.
func (l *Layout) CommitDescriptorPath(digestHex string) string {
	return filepath.Join(l.CommitsDir, digestHex+".json")
}

// RefPath returns the on-disk ref file for project/name (design §15).
func (l *Layout) RefPath(projectID, name string) string {
	return filepath.Join(l.RefsDir, projectID, name+".json")
}

// OCIBlobPath returns the path for an OCI blob by digest hex.
func (l *Layout) OCIBlobPath(digestHex string) string {
	return filepath.Join(l.OCIDir, digestHex+".blob")
}

// Staging path helpers for atomic commits (design §14.2 step 10-11).
func (l *Layout) StagingPath(name string) string {
	return filepath.Join(l.StagingDir, name)
}

// userDataDir returns the per-user application data directory by platform
// (design §9.1: macOS uses equivalent app-data dirs; output shows real paths).
func userDataDir() (string, error) {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return x, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support"), nil
	default:
		return filepath.Join(home, ".local", "share"), nil
	}
}
