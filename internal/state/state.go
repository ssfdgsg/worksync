// Package state persists derived worksync state in a SQLite database with
// WAL mode, using a pure-Go driver to avoid CGO (design §9.4). It stores
// derived state only; worksync.yaml remains the source of truth.
package state

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite connection and schema.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if needed) the state database at path and applies the
// schema with WAL mode (design §9.4).
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}
	d := &DB{sql: sqlDB}
	if err := d.init(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) init() error {
	stmts := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
		schema,
	}
	for _, s := range stmts {
		if _, err := d.sql.Exec(s); err != nil {
			return fmt.Errorf("init state db: %w", err)
		}
	}
	return nil
}

// Close closes the underlying database.
func (d *DB) Close() error { return d.sql.Close() }

// schema is the minimal table set of design §9.4: projects, containers,
// volumes, ports, commits, refs, operations.
const schema = `
CREATE TABLE IF NOT EXISTS projects (
	id            TEXT PRIMARY KEY,
	manifest_path TEXT NOT NULL,
	manifest_digest TEXT NOT NULL,
	backend       TEXT NOT NULL,
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS containers (
	project_id    TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
	name          TEXT NOT NULL,
	image_tag     TEXT NOT NULL,
	image_ref     TEXT NOT NULL,
	config_digest TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL,
	container_id  TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS volumes (
	project_id   TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	name         TEXT NOT NULL,
	target       TEXT NOT NULL,
	policy       TEXT NOT NULL,
	source_type  TEXT NOT NULL DEFAULT 'managed',
	source_path  TEXT NOT NULL DEFAULT '',
	host_path    TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, name)
);

CREATE TABLE IF NOT EXISTS ports (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	target     INTEGER NOT NULL,
	published  INTEGER NOT NULL,
	listen     TEXT NOT NULL,
	protocol   TEXT NOT NULL,
	PRIMARY KEY (project_id, name)
);

CREATE TABLE IF NOT EXISTS commits (
	digest         TEXT PRIMARY KEY,
	project_id     TEXT NOT NULL,
	descriptor_json TEXT NOT NULL,
	parent         TEXT NOT NULL DEFAULT '',
	message        TEXT NOT NULL DEFAULT '',
	created_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS refs (
	project_id    TEXT NOT NULL,
	name          TEXT NOT NULL,
	commit_digest TEXT NOT NULL,
	previous      TEXT NOT NULL DEFAULT '',
	updated_at    TEXT NOT NULL,
	PRIMARY KEY (project_id, name)
);

CREATE TABLE IF NOT EXISTS checkouts (
	project_id    TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
	commit_digest TEXT NOT NULL,
	image_ref     TEXT NOT NULL DEFAULT '',
	config_digest TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS checkpoints (
	project_id        TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	image_ref         TEXT NOT NULL,
	source_container  TEXT NOT NULL DEFAULT '',
	platform          TEXT NOT NULL DEFAULT '',
	reason            TEXT NOT NULL DEFAULT '',
	created_at        TEXT NOT NULL,
	restored_at       TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, image_ref)
);

CREATE TABLE IF NOT EXISTS operations (
	id          TEXT PRIMARY KEY,
	project_id  TEXT NOT NULL,
	kind        TEXT NOT NULL,
	state       TEXT NOT NULL,
	started_at  TEXT NOT NULL,
	finished_at TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT '',
	pid         INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_commits_project ON commits(project_id);
CREATE INDEX IF NOT EXISTS idx_operations_project ON operations(project_id);
CREATE INDEX IF NOT EXISTS idx_operations_state ON operations(state);
`

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// TimeScanner scans SQLite TEXT timestamps (RFC3339Nano) into time.Time;
// the pure-Go driver returns TEXT columns as strings.
type TimeScanner struct {
	Time time.Time
}

func (s *TimeScanner) Scan(v interface{}) error {
	switch x := v.(type) {
	case nil:
		s.Time = time.Time{}
	case string:
		if x == "" {
			s.Time = time.Time{}
			return nil
		}
		t, err := time.Parse(time.RFC3339Nano, x)
		if err != nil {
			return fmt.Errorf("parse time %q: %w", x, err)
		}
		s.Time = t
	case time.Time:
		s.Time = x
	default:
		return fmt.Errorf("unsupported time value %T", v)
	}
	return nil
}
