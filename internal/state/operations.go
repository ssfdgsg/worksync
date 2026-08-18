package state

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

// NewOperationID generates a random operation ID.
func NewOperationID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("op-%d", time.Now().UnixNano())
	}
	return "op-" + hex.EncodeToString(b)
}

// StartOperation records a running operation in the journal (design §9.4:
// every mutating operation is journaled for crash recovery).
func (d *DB) StartOperation(op Operation) (string, error) {
	if op.ID == "" {
		op.ID = NewOperationID()
	}
	if op.StartedAt.IsZero() {
		op.StartedAt = time.Now().UTC()
	}
	if op.PID == 0 {
		op.PID = os.Getpid()
	}
	_, err := d.sql.Exec(`INSERT INTO operations (id, project_id, kind, state, started_at, finished_at, error, pid)
		VALUES (?, ?, ?, ?, ?, '', '', ?)`,
		op.ID, op.ProjectID, string(op.Kind), string(OpRunning), op.StartedAt.Format(timeLayout), op.PID)
	if err != nil {
		return "", err
	}
	return op.ID, nil
}

// FinishOperation marks a running operation as success or failed.
func (d *DB) FinishOperation(id string, opErr error) error {
	state := OpSuccess
	errMsg := ""
	if opErr != nil {
		state = OpFailed
		errMsg = opErr.Error()
	}
	res, err := d.sql.Exec(`UPDATE operations SET state=?, finished_at=?, error=? WHERE id=? AND state=?`,
		string(state), nowUTC(), errMsg, id, string(OpRunning))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("operation %s is not running", id)
	}
	return nil
}

// FindRunningOperations returns all operations still in running state
// (design §22 recovery scan step 1).
func (d *DB) FindRunningOperations() ([]Operation, error) {
	rows, err := d.sql.Query(`SELECT id, project_id, kind, state, started_at, finished_at, error, pid FROM operations WHERE state=?`, string(OpRunning))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Operation
	for rows.Next() {
		var o Operation
		var kind, st, fin, errMsg string
		var started, finished TimeScanner
		if err := rows.Scan(&o.ID, &o.ProjectID, &kind, &st, &started, &fin, &errMsg, &o.PID); err != nil {
			return nil, err
		}
		o.StartedAt = started.Time
		o.FinishedAt = finished.Time
		o.Kind = OperationKind(kind)
		o.State = OperationState(st)
		o.FinishedAt = parseTime(fin)
		o.Error = errMsg
		out = append(out, o)
	}
	return out, rows.Err()
}

// MarkInterrupted marks a running operation as interrupted (used by the
// recovery scan for operations whose process no longer exists, design §22).
func (d *DB) MarkInterrupted(id, reason string) error {
	_, err := d.sql.Exec(`UPDATE operations SET state=?, finished_at=?, error=? WHERE id=? AND state=?`,
		string(OpInterrupted), nowUTC(), reason, id, string(OpRunning))
	return err
}

// OperationIsRunning reports whether a running operation exists for the
// project (used together with the project lock).
func (d *DB) OperationIsRunning(projectID string) (bool, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM operations WHERE project_id=? AND state=?`, projectID, string(OpRunning)).Scan(&n)
	return n > 0, err
}
