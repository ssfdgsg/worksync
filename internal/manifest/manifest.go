// Package manifest implements parsing, validation and expansion of the
// Project Spec (worksync.yaml), the source of truth for a worksync project.
//
// Parsing is strict: unknown fields are rejected so that typos are never
// silently ignored (design §12.2).
package manifest

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"worksync/internal/ports"
	"worksync/internal/volume"
)

// SchemaVersion is the only Project Spec schema version accepted in v0.
const SchemaVersion = 1

// DefaultFileName is the conventional Project Spec file name.
const DefaultFileName = "worksync.yaml"

// Manifest is the parsed Project Spec. Every field maps 1:1 to the YAML
// schema of design §12.1.
type Manifest struct {
	SchemaVersion int                   `yaml:"schemaVersion"`
	Name          string                `yaml:"name"`
	Runtime       RuntimeSpec           `yaml:"runtime"`
	Container     ContainerSpec         `yaml:"container"`
	Ports         []ports.Port          `yaml:"ports"`
	Volumes       map[string]VolumeSpec `yaml:"volumes"`
	Commit        *CommitSpec           `yaml:"commit"`
	Snapshot      *SnapshotSpec         `yaml:"snapshot"`
	Remote        *RemoteSpec           `yaml:"remote"`

	// dir is the directory containing the manifest file; used to resolve
	// relative host paths. Not part of the schema.
	dir string
}

// RuntimeSpec selects the runtime engine and backend (design §7, §12).
type RuntimeSpec struct {
	Engine   string `yaml:"engine"`
	Backend  string `yaml:"backend"`
	Rootless bool   `yaml:"rootless"`
}

// ContainerSpec describes the persistent development container.
type ContainerSpec struct {
	Image          string            `yaml:"image"`
	PersistentRoot bool              `yaml:"persistentRoot"`
	Workdir        string            `yaml:"workdir"`
	User           string            `yaml:"user"`
	Command        []string          `yaml:"command"`
	Environment    map[string]string `yaml:"environment"`
}

// VolumeSpec is one entry of the volumes map (design §9.3, §10, §12.1).
type VolumeSpec struct {
	Source *SourceSpec   `yaml:"source"`
	Target string        `yaml:"target"`
	Policy volume.Policy `yaml:"policy"`
}

// SourceSpec declares where a volume's data physically lives.
type SourceSpec struct {
	Type string `yaml:"type"` // "host" | "managed"
	Path string `yaml:"path"`
}

// CommitSpec controls which components enter a commit (design §10, §12.1).
type CommitSpec struct {
	Environment bool     `yaml:"environment"`
	Volumes     []string `yaml:"volumes"`
}

// SnapshotSpec configures consistency hooks for snapshotting (design §10.1).
type SnapshotSpec struct {
	Mode     string   `yaml:"mode"` // "stop" | "command" | "none"
	Services []string `yaml:"services"`
	Pre      []string `yaml:"pre"`
	Post     []string `yaml:"post"`
}

// RemoteSpec lists remote stores (design §12.1, §16).
type RemoteSpec struct {
	Default string                     `yaml:"default"`
	Remotes map[string]RemoteSpecEntry `yaml:"remotes"`
}

// RemoteSpecEntry is a single named remote.
type RemoteSpecEntry struct {
	URL string `yaml:"url"`
}

// Parse reads a Project Spec from r. Relative host paths are resolved
// against dir.
func Parse(r io.Reader, dir string) (*Manifest, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse worksync.yaml: %w", err)
	}
	m.dir = dir
	if err := m.Validate(); err != nil {
		return nil, err
	}
	// Environment expansion happens after validation so that referenced
	// variables must exist (design §12.2).
	if err := m.Expand(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Load reads and parses the Project Spec at path.
func Load(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f, filepath.Dir(path))
}

// Dir returns the directory the manifest was loaded from ("" when parsed
// from memory without a base directory).
func (m *Manifest) Dir() string { return m.dir }

// Validate checks semantic rules of the Project Spec (design §12.2).
func (m *Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %d (want %d)", m.SchemaVersion, SchemaVersion)
	}
	if err := validateProjectName(m.Name); err != nil {
		return err
	}
	if m.Runtime.Engine == "" {
		m.Runtime.Engine = "podman"
	}
	if m.Runtime.Engine != "podman" {
		return fmt.Errorf("runtime.engine %q is not supported in v0 (only podman)", m.Runtime.Engine)
	}
	if m.Runtime.Backend == "" {
		m.Runtime.Backend = "auto"
	}
	switch m.Runtime.Backend {
	case "auto", "native-podman", "podman-machine":
	default:
		return fmt.Errorf("runtime.backend %q is not supported in v0", m.Runtime.Backend)
	}
	if m.Container.Image == "" {
		return fmt.Errorf("container.image is required")
	}
	seenPorts := map[string]bool{}
	for i := range m.Ports {
		p := &m.Ports[i] // Validate defaults (listen/protocol) must write back
		if err := p.Validate(); err != nil {
			return fmt.Errorf("ports[%d] %q: %w", i, p.Name, err)
		}
		if seenPorts[p.Name] {
			return fmt.Errorf("duplicate port name %q", p.Name)
		}
		seenPorts[p.Name] = true
	}
	for name, v := range m.Volumes {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("volume %q: %w", name, err)
		}
		if v.Source != nil && v.Source.Type == "host" && !filepath.IsAbs(v.Source.Path) && m.dir != "" {
			v.Source.Path = filepath.Join(m.dir, v.Source.Path)
		}
	}
	if m.Commit != nil {
		for _, v := range m.Commit.Volumes {
			if _, ok := m.Volumes[v]; !ok {
				return fmt.Errorf("commit.volumes references unknown volume %q", v)
			}
		}
	}
	if m.Snapshot != nil {
		switch m.Snapshot.Mode {
		case "", "stop", "command", "none":
		default:
			return fmt.Errorf("snapshot.mode %q is not valid (stop|command|none)", m.Snapshot.Mode)
		}
		if m.Snapshot.Mode == "command" && len(m.Snapshot.Pre) == 0 && len(m.Snapshot.Post) == 0 {
			return fmt.Errorf("snapshot.mode command requires pre and/or post commands")
		}
	}
	if m.Remote != nil {
		if m.Remote.Default != "" {
			if _, ok := m.Remote.Remotes[m.Remote.Default]; !ok {
				return fmt.Errorf("remote.default %q does not match any declared remote", m.Remote.Default)
			}
		}
		for name, r := range m.Remote.Remotes {
			if strings.TrimSpace(r.URL) == "" {
				return fmt.Errorf("remote %q has empty url", name)
			}
		}
	}
	if m.Container.Workdir == "" {
		if ws, ok := m.Volumes["workspace"]; ok {
			// workdir defaults to the workspace target when present.
			m.Container.Workdir = ws.Target
		}
	}
	return nil
}

// Expand performs explicit ${VAR} expansion on environment values.
// Bare $VAR is left untouched (design §12.2: expansion must be explicit).
func (m *Manifest) Expand() error {
	lookup := func(k string) (string, bool) { return os.LookupEnv(k) }
	for key, val := range m.Container.Environment {
		expanded, missing, err := ExpandVars(val, lookup)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			return fmt.Errorf("environment %s references unset variable(s): %s", key, strings.Join(missing, ", "))
		}
		m.Container.Environment[key] = expanded
	}
	return nil
}

// VolumeNames returns the declared volume names in deterministic order.
func (m *Manifest) VolumeNames() []string {
	names := make([]string, 0, len(m.Volumes))
	for n := range m.Volumes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DefaultBackend returns the backend selector from the manifest ("auto" when
// unset).
func (m *Manifest) DefaultBackend() string { return m.Runtime.Backend }

// validateProjectName enforces the project-ID charset used for directories,
// refs and container names.
func validateProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("name must be at most 64 characters")
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return fmt.Errorf("name %q contains invalid character %q (allowed: lowercase letters, digits, dashes; must not start with a dash)", name, string(r))
		}
	}
	return nil
}
