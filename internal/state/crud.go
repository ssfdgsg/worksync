package state

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"worksync/internal/backend"
	"worksync/internal/refs"
)

// ErrNotFound is returned when a queried row does not exist.
var ErrNotFound = errors.New("not found")

const timeLayout = time.RFC3339Nano

// formatTime formats a time for storage; the zero value becomes an empty
// string so optional timestamps (e.g. restored_at) stay readable.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(timeLayout)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(timeLayout, s)
	return t
}

// ---- projects ----

// UpsertProject inserts or replaces a project row.
func (d *DB) UpsertProject(p Project) error {
	now := nowUTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	_, err := d.sql.Exec(`INSERT INTO projects (id, manifest_path, manifest_digest, backend, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			manifest_path=excluded.manifest_path,
			manifest_digest=excluded.manifest_digest,
			backend=excluded.backend,
			updated_at=excluded.updated_at`,
		p.ID, p.ManifestPath, p.ManifestDigest, string(p.Backend),
		p.CreatedAt.Format(timeLayout), now)
	return err
}

// GetProject returns a project by id.
func (d *DB) GetProject(id string) (*Project, error) {
	row := d.sql.QueryRow(`SELECT id, manifest_path, manifest_digest, backend, created_at, updated_at FROM projects WHERE id=?`, id)
	var p Project
	var backendStr string
	var created, updated TimeScanner
	if err := row.Scan(&p.ID, &p.ManifestPath, &p.ManifestDigest, &backendStr, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Backend = backend.Kind(backendStr)
	p.CreatedAt = created.Time
	p.UpdatedAt = updated.Time
	return &p, nil
}

// ListProjects returns all projects ordered by id.
func (d *DB) ListProjects() ([]Project, error) {
	rows, err := d.sql.Query(`SELECT id, manifest_path, manifest_digest, backend, created_at, updated_at FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var b string
		var created, updated TimeScanner
		if err := rows.Scan(&p.ID, &p.ManifestPath, &p.ManifestDigest, &b, &created, &updated); err != nil {
			return nil, err
		}
		p.Backend = backend.Kind(b)
		p.CreatedAt = created.Time
		p.UpdatedAt = updated.Time
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- containers ----

// UpsertContainer inserts or replaces a container row.
func (d *DB) UpsertContainer(c Container) error {
	now := nowUTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	_, err := d.sql.Exec(`INSERT INTO containers (project_id, name, image_tag, image_ref, config_digest, state, container_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			name=excluded.name, image_tag=excluded.image_tag, image_ref=excluded.image_ref,
			config_digest=excluded.config_digest, state=excluded.state, container_id=excluded.container_id,
			updated_at=excluded.updated_at`,
		c.ProjectID, c.Name, c.ImageTag, c.ImageRef, c.ConfigDigest, string(c.State), c.ContainerID,
		c.CreatedAt.Format(timeLayout), now)
	return err
}

// GetContainer returns the container row for a project.
func (d *DB) GetContainer(projectID string) (*Container, error) {
	row := d.sql.QueryRow(`SELECT project_id, name, image_tag, image_ref, config_digest, state, container_id, created_at, updated_at FROM containers WHERE project_id=?`, projectID)
	var c Container
	var st string
	var created, updated TimeScanner
	if err := row.Scan(&c.ProjectID, &c.Name, &c.ImageTag, &c.ImageRef, &c.ConfigDigest, &st, &c.ContainerID, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.State = ContainerState(st)
	c.CreatedAt = created.Time
	c.UpdatedAt = updated.Time
	return &c, nil
}

// DeleteContainer removes the container row.
func (d *DB) DeleteContainer(projectID string) error {
	_, err := d.sql.Exec(`DELETE FROM containers WHERE project_id=?`, projectID)
	return err
}

// ---- checkouts ----

// UpsertCheckout records the checked-out (rolled-back) environment image.
func (d *DB) UpsertCheckout(c Checkout) error {
	now := nowUTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	_, err := d.sql.Exec(`INSERT INTO checkouts (project_id, commit_digest, image_ref, config_digest, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			commit_digest=excluded.commit_digest, image_ref=excluded.image_ref,
			config_digest=excluded.config_digest, updated_at=excluded.updated_at`,
		c.ProjectID, c.CommitDigest, c.ImageRef, c.ConfigDigest, c.CreatedAt.Format(timeLayout), now)
	return err
}

// GetCheckout returns the project's checkout row, or ErrNotFound.
func (d *DB) GetCheckout(projectID string) (*Checkout, error) {
	row := d.sql.QueryRow(`SELECT project_id, commit_digest, image_ref, config_digest, created_at, updated_at FROM checkouts WHERE project_id=?`, projectID)
	var c Checkout
	var created, updated TimeScanner
	if err := row.Scan(&c.ProjectID, &c.CommitDigest, &c.ImageRef, &c.ConfigDigest, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.CreatedAt = created.Time
	c.UpdatedAt = updated.Time
	return &c, nil
}

// DeleteCheckout removes the project's checkout row.
func (d *DB) DeleteCheckout(projectID string) error {
	_, err := d.sql.Exec(`DELETE FROM checkouts WHERE project_id=?`, projectID)
	return err
}

// ---- checkpoints ----

// UpsertCheckpoint inserts or replaces an internal checkpoint row.
func (d *DB) UpsertCheckpoint(c Checkpoint) error {
	_, err := d.sql.Exec(`INSERT INTO checkpoints (project_id, image_ref, source_container, platform, reason, created_at, restored_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, image_ref) DO UPDATE SET
			source_container=excluded.source_container, platform=excluded.platform,
			reason=excluded.reason, restored_at=excluded.restored_at`,
		c.ProjectID, c.ImageRef, c.SourceContainer, c.Platform, c.Reason,
		formatTime(c.CreatedAt), formatTime(c.RestoredAt))
	return err
}

// LatestCheckpoint returns the most recent checkpoint row for a project,
// or ErrNotFound.
func (d *DB) LatestCheckpoint(projectID string) (*Checkpoint, error) {
	row := d.sql.QueryRow(`SELECT project_id, image_ref, source_container, platform, reason, created_at, restored_at
		FROM checkpoints WHERE project_id=? ORDER BY created_at DESC LIMIT 1`, projectID)
	var c Checkpoint
	var created, restored TimeScanner
	if err := row.Scan(&c.ProjectID, &c.ImageRef, &c.SourceContainer, &c.Platform, &c.Reason, &created, &restored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.CreatedAt = created.Time
	c.RestoredAt = restored.Time
	return &c, nil
}

// ListCheckpoints returns all checkpoint rows for a project, newest first.
func (d *DB) ListCheckpoints(projectID string) ([]Checkpoint, error) {
	rows, err := d.sql.Query(`SELECT project_id, image_ref, source_container, platform, reason, created_at, restored_at
		FROM checkpoints WHERE project_id=? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		var created, restored TimeScanner
		if err := rows.Scan(&c.ProjectID, &c.ImageRef, &c.SourceContainer, &c.Platform, &c.Reason, &created, &restored); err != nil {
			return nil, err
		}
		c.CreatedAt = created.Time
		c.RestoredAt = restored.Time
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkCheckpointRestored sets restored_at for a checkpoint row.
func (d *DB) MarkCheckpointRestored(projectID, imageRef string) error {
	_, err := d.sql.Exec(`UPDATE checkpoints SET restored_at=? WHERE project_id=? AND image_ref=?`, nowUTC(), projectID, imageRef)
	return err
}

// DeleteCheckpoint removes a checkpoint row.
func (d *DB) DeleteCheckpoint(projectID, imageRef string) error {
	_, err := d.sql.Exec(`DELETE FROM checkpoints WHERE project_id=? AND image_ref=?`, projectID, imageRef)
	return err
}

// ---- volumes ----

// UpsertVolume inserts or replaces a volume row.
func (d *DB) UpsertVolume(v Volume) error {
	_, err := d.sql.Exec(`INSERT INTO volumes (project_id, name, target, policy, source_type, source_path, host_path)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, name) DO UPDATE SET
			target=excluded.target, policy=excluded.policy, source_type=excluded.source_type,
			source_path=excluded.source_path, host_path=excluded.host_path`,
		v.ProjectID, v.Name, v.Target, string(v.Policy), v.SourceType, v.SourcePath, v.HostPath)
	return err
}

// ListVolumes returns volume rows for a project.
func (d *DB) ListVolumes(projectID string) ([]Volume, error) {
	rows, err := d.sql.Query(`SELECT project_id, name, target, policy, source_type, source_path, host_path FROM volumes WHERE project_id=? ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Volume
	for rows.Next() {
		var v Volume
		if err := rows.Scan(&v.ProjectID, &v.Name, &v.Target, &v.Policy, &v.SourceType, &v.SourcePath, &v.HostPath); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- ports ----

// UpsertPort inserts or replaces a port row.
func (d *DB) UpsertPort(p Port) error {
	_, err := d.sql.Exec(`INSERT INTO ports (project_id, name, target, published, listen, protocol)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, name) DO UPDATE SET
			target=excluded.target, published=excluded.published, listen=excluded.listen, protocol=excluded.protocol`,
		p.ProjectID, p.Name, p.Target, p.Published, p.Listen, p.Protocol)
	return err
}

// ListPorts returns port rows for a project.
func (d *DB) ListPorts(projectID string) ([]Port, error) {
	rows, err := d.sql.Query(`SELECT project_id, name, target, published, listen, protocol FROM ports WHERE project_id=? ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Port
	for rows.Next() {
		var p Port
		if err := rows.Scan(&p.ProjectID, &p.Name, &p.Target, &p.Published, &p.Listen, &p.Protocol); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- commits ----

// SaveCommit inserts or replaces a commit row.
func (d *DB) SaveCommit(c Commit) error {
	_, err := d.sql.Exec(`INSERT INTO commits (digest, project_id, descriptor_json, parent, message, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(digest) DO NOTHING`,
		c.Digest, c.ProjectID, c.DescriptorJSON, c.Parent, c.Message, c.CreatedAt.Format(timeLayout))
	return err
}

// GetCommit returns a commit by digest.
func (d *DB) GetCommit(dg string) (*Commit, error) {
	row := d.sql.QueryRow(`SELECT digest, project_id, descriptor_json, parent, message, created_at FROM commits WHERE digest=?`, dg)
	var c Commit
	var created TimeScanner
	if err := row.Scan(&c.Digest, &c.ProjectID, &c.DescriptorJSON, &c.Parent, &c.Message, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.CreatedAt = created.Time
	return &c, nil
}

// ListCommits returns commits of a project, newest first.
func (d *DB) ListCommits(projectID string) ([]Commit, error) {
	rows, err := d.sql.Query(`SELECT digest, project_id, descriptor_json, parent, message, created_at FROM commits WHERE project_id=? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Commit
	for rows.Next() {
		var c Commit
		var created TimeScanner
		if err := rows.Scan(&c.Digest, &c.ProjectID, &c.DescriptorJSON, &c.Parent, &c.Message, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = created.Time
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- refs ----

// PutRef stores a ref row. The ref must already be validated.
func (d *DB) PutRef(projectID, name string, r refs.Ref) error {
	_, err := d.sql.Exec(`INSERT INTO refs (project_id, name, commit_digest, previous, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, name) DO UPDATE SET
			commit_digest=excluded.commit_digest, previous=excluded.previous, updated_at=excluded.updated_at`,
		projectID, name, r.Commit, r.Previous, r.UpdatedAt.Format(timeLayout))
	return err
}

// GetRef returns a ref row, or ErrNotFound.
func (d *DB) GetRef(projectID, name string) (refs.Ref, error) {
	row := d.sql.QueryRow(`SELECT commit_digest, previous, updated_at FROM refs WHERE project_id=? AND name=?`, projectID, name)
	var r refs.Ref
	var updated TimeScanner
	if err := row.Scan(&r.Commit, &r.Previous, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return refs.Ref{}, ErrNotFound
		}
		return refs.Ref{}, err
	}
	r.UpdatedAt = updated.Time
	if err := r.Validate(); err != nil {
		return refs.Ref{}, fmt.Errorf("ref %s/%s: %w", projectID, name, err)
	}
	return r, nil
}

// DeleteRef removes a ref row.
func (d *DB) DeleteRef(projectID, name string) error {
	_, err := d.sql.Exec(`DELETE FROM refs WHERE project_id=? AND name=?`, projectID, name)
	return err
}

// ListRefs returns all ref rows of a project.
func (d *DB) ListRefs(projectID string) ([]RefRow, error) {
	rows, err := d.sql.Query(`SELECT project_id, name, commit_digest, previous, updated_at FROM refs WHERE project_id=? ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RefRow
	for rows.Next() {
		var r RefRow
		var updated TimeScanner
		if err := rows.Scan(&r.ProjectID, &r.Name, &r.Commit, &r.Previous, &updated); err != nil {
			return nil, err
		}
		r.UpdatedAt = updated.Time
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetPort returns one port row by name.
func (d *DB) GetPort(projectID, name string) (*Port, error) {
	row := d.sql.QueryRow(`SELECT project_id, name, target, published, listen, protocol FROM ports WHERE project_id=? AND name=?`, projectID, name)
	var p Port
	if err := row.Scan(&p.ProjectID, &p.Name, &p.Target, &p.Published, &p.Listen, &p.Protocol); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// DeletePort removes a port row.
func (d *DB) DeletePort(projectID, name string) error {
	res, err := d.sql.Exec(`DELETE FROM ports WHERE project_id=? AND name=?`, projectID, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteVolume removes one volume row.
func (d *DB) DeleteVolume(projectID, name string) error {
	res, err := d.sql.Exec(`DELETE FROM volumes WHERE project_id=? AND name=?`, projectID, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
