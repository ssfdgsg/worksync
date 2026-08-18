package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"worksync/internal/volume"
)

const sampleYAML = `
schemaVersion: 1
name: dsh-dev

runtime:
  engine: podman
  backend: auto
  rootless: true

container:
  image: node:24
  persistentRoot: true
  workdir: /workspace
  user: dev
  command: ["/opt/worksync/bin/worksync-agent", "idle"]
  environment:
    NODE_ENV: development

ports:
  - name: web
    target: 3000
    published: 3000
    listen: 127.0.0.1
    protocol: tcp

  - name: debug
    target: 9229
    published: auto
    listen: 127.0.0.1
    protocol: tcp

volumes:
  workspace:
    source:
      type: host
      path: ./
    target: /workspace
    policy: tracked

  home:
    target: /home/dev
    policy: persistent

  npm-cache:
    target: /home/dev/.npm
    policy: cache

  dsh-config:
    target: /home/dev/.dsh
    policy: tracked

commit:
  environment: true
  volumes:
    - workspace
    - dsh-config

snapshot:
  mode: stop
  services: [db]

remote:
  default: origin
  remotes:
    origin:
      url: ssh://user@example.com/~/worksync-store
`

func TestParseSample(t *testing.T) {
	m, err := Parse(strings.NewReader(sampleYAML), "/tmp/proj")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Name != "dsh-dev" {
		t.Errorf("Name = %q, want dsh-dev", m.Name)
	}
	if m.Container.Image != "node:24" {
		t.Errorf("Image = %q", m.Container.Image)
	}
	if len(m.Ports) != 2 {
		t.Fatalf("len(Ports) = %d, want 2", len(m.Ports))
	}
	if m.Ports[1].Published != "auto" {
		t.Errorf("second port published = %q, want auto", m.Ports[1].Published)
	}
	if got := m.Volumes["workspace"].Policy; got != volume.Tracked {
		t.Errorf("workspace policy = %q", got)
	}
	if m.Volumes["workspace"].Source == nil || m.Volumes["workspace"].Source.Type != "host" {
		t.Errorf("workspace source not host")
	}
	if m.Commit == nil || !m.Commit.Environment || len(m.Commit.Volumes) != 2 {
		t.Errorf("commit spec wrong: %+v", m.Commit)
	}
	if m.Remote == nil || m.Remote.Default != "origin" {
		t.Errorf("remote wrong: %+v", m.Remote)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	bad := strings.Replace(sampleYAML, "persistentRoot: true", "persistentRoot: true\n  bogusField: 1", 1)
	_, err := Parse(strings.NewReader(bad), "/tmp")
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "bogusField") {
		t.Errorf("error should mention field: %v", err)
	}
}

func TestParseRejectsUnknownTopLevel(t *testing.T) {
	bad := sampleYAML + "\nextra: true\n"
	_, err := Parse(strings.NewReader(bad), "/tmp")
	if err == nil {
		t.Fatal("expected error for unknown top-level field")
	}
}

func TestParseRejectsBadSchemaVersion(t *testing.T) {
	bad := strings.Replace(sampleYAML, "schemaVersion: 1", "schemaVersion: 2", 1)
	_, err := Parse(strings.NewReader(bad), "/tmp")
	if err == nil || !strings.Contains(err.Error(), "schemaVersion") {
		t.Fatalf("want schemaVersion error, got %v", err)
	}
}

func TestParseRejectsBadPort(t *testing.T) {
	bad := strings.Replace(sampleYAML, "protocol: tcp", "protocol: udp", 1)
	_, err := Parse(strings.NewReader(bad), "/tmp")
	if err == nil || !strings.Contains(err.Error(), "udp") {
		t.Fatalf("want udp error, got %v", err)
	}
}

func TestParseRejectsPrivilegedPort(t *testing.T) {
	bad := strings.Replace(sampleYAML, "published: 3000", "published: 80", 1)
	_, err := Parse(strings.NewReader(bad), "/tmp")
	if err == nil || !strings.Contains(err.Error(), "1024") {
		t.Fatalf("want privileged-port error, got %v", err)
	}
}

func TestParseRejectsUnknownVolumeInCommit(t *testing.T) {
	bad := strings.Replace(sampleYAML, "- dsh-config", "- nope", 1)
	_, err := Parse(strings.NewReader(bad), "/tmp")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("want unknown-volume error, got %v", err)
	}
}

func TestParseRejectsBadPolicy(t *testing.T) {
	bad := strings.Replace(sampleYAML, "policy: cache", "policy: nope", 1)
	_, err := Parse(strings.NewReader(bad), "/tmp")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("want bad-policy error, got %v", err)
	}
}

func TestParseRejectsMissingName(t *testing.T) {
	bad := strings.Replace(sampleYAML, "name: dsh-dev", "", 1)
	_, err := Parse(strings.NewReader(bad), "/tmp")
	if err == nil {
		t.Fatal("expected missing-name error")
	}
}

func TestRelativeHostPathResolved(t *testing.T) {
	y := `schemaVersion: 1
name: relproj
container:
  image: node:24
volumes:
  workspace:
    source:
      type: host
      path: ./src
    target: /workspace
    policy: tracked
`
	m, err := Parse(strings.NewReader(y), "/base/dir")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Volumes["workspace"].Source.Path; got != filepath.Join("/base/dir", "src") {
		t.Errorf("resolved path = %q, want %q", got, filepath.Join("/base/dir", "src"))
	}
}

func TestEnvExpansion(t *testing.T) {
	y := `schemaVersion: 1
name: envproj
container:
  image: node:24
  environment:
    NODE_ENV: production
    FROM_VAR: "${WBTEST_VAR}"
    LITERAL: "$WBTEST_VAR"
`
	t.Setenv("WBTEST_VAR", "expanded-value")
	m, err := Parse(strings.NewReader(y), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Container.Environment["FROM_VAR"]; got != "expanded-value" {
		t.Errorf("FROM_VAR = %q", got)
	}
	if got := m.Container.Environment["LITERAL"]; got != "$WBTEST_VAR" {
		t.Errorf("LITERAL should stay literal, got %q", got)
	}
}

func TestEnvExpansionUnsetFails(t *testing.T) {
	y := `schemaVersion: 1
name: envproj2
container:
  image: node:24
  environment:
    BAD: "${WBTEST_UNSET_VAR_XYZ}"
`
	_, err := Parse(strings.NewReader(y), "/tmp")
	if err == nil || !strings.Contains(err.Error(), "WBTEST_UNSET_VAR_XYZ") {
		t.Fatalf("want unset-var error, got %v", err)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worksync.yaml")
	if err := os.WriteFile(path, []byte(sampleYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "dsh-dev" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.Dir() != dir {
		t.Errorf("Dir = %q", m.Dir())
	}
}
