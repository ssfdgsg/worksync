package state

import (
	"time"

	"worksync/internal/backend"
	"worksync/internal/volume"
)

// ContainerState is one state of the lifecycle machine (design §13).
type ContainerState string

const (
	StateAbsent       ContainerState = "absent"
	StateProvisioning ContainerState = "provisioning"
	StateRunning      ContainerState = "running"
	StateStopped      ContainerState = "stopped"
	StateCommitting   ContainerState = "committing"
	StateRemoving     ContainerState = "removing"
	StateError        ContainerState = "error"
)

// Project is a row of the projects table.
type Project struct {
	ID             string
	ManifestPath   string
	ManifestDigest string
	Backend        backend.Kind
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Container is a row of the containers table.
type Container struct {
	ProjectID    string
	Name         string
	ImageTag     string
	ImageRef     string
	ConfigDigest string // manifest digest the container was provisioned from
	State        ContainerState
	ContainerID  string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Volume is a row of the volumes table.
type Volume struct {
	ProjectID  string
	Name       string
	Target     string
	Policy     volume.Policy
	SourceType string // "host" | "managed"
	SourcePath string // manifest-declared path (host volumes)
	HostPath   string // resolved absolute path on the backend
}

// Port is a row of the ports table; Published is the concrete host port
// after auto-allocation (design §11.4).
type Port struct {
	ProjectID string
	Name      string
	Target    uint16
	Published uint16
	Listen    string
	Protocol  string
}

// Commit is a row of the commits table; DescriptorJSON is the canonical
// descriptor bytes.
type Commit struct {
	Digest         string
	ProjectID      string
	DescriptorJSON []byte
	Parent         string
	Message        string
	CreatedAt      time.Time
}

// RefRow is a row of the refs table.
type RefRow struct {
	ProjectID string
	Name      string
	Commit    string
	Previous  string
	UpdatedAt time.Time
}

// Checkout records the environment image frozen by the last rollback (or a
// pulled commit applied via rollback), so `up` recreates the container from
// the committed rootfs instead of the manifest base image (E2E-001, §17).
type Checkout struct {
	ProjectID    string
	CommitDigest string // the rolled-back commit digest
	ImageRef     string // resolved environment image digest to create from
	ConfigDigest string // manifest digest the commit was made from ("" = any)
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Checkpoint records an internal writable-layer freeze taken before a
// container replacement (ports, drift, rollback). It is NOT part of the
// user-visible commit graph and is never pushed to remotes; it exists so an
// automatic rebuild can never silently fall back to the manifest base image
// (design M7, dsh-deployment-target P0).
type Checkpoint struct {
	ProjectID       string
	ImageRef        string // internal `podman commit` image digest
	SourceContainer string // container id the checkpoint was taken from
	Platform        string // OCI platform (e.g. linux/arm64)
	Reason          string // ports | drift | rollback | export | manual
	CreatedAt       time.Time
	RestoredAt      time.Time // empty when not yet used to rebuild
}

// OperationKind is a mutating command kind recorded in the journal.
type OperationKind string

const (
	OpUp       OperationKind = "up"
	OpStop     OperationKind = "stop"
	OpStart    OperationKind = "start"
	OpRm       OperationKind = "rm"
	OpCommit   OperationKind = "commit"
	OpPush     OperationKind = "push"
	OpPull     OperationKind = "pull"
	OpTag      OperationKind = "tag"
	OpRollback OperationKind = "rollback"
	OpExpose   OperationKind = "expose"
	OpUnexpose OperationKind = "unexpose"
)

// OperationState is the journal lifecycle (design §22 recovery scan uses
// running operations).
type OperationState string

const (
	OpRunning     OperationState = "running"
	OpSuccess     OperationState = "success"
	OpFailed      OperationState = "failed"
	OpInterrupted OperationState = "interrupted"
)

// Operation is a row of the operations journal (design §9.4, §22).
type Operation struct {
	ID         string
	ProjectID  string
	Kind       OperationKind
	State      OperationState
	StartedAt  time.Time
	FinishedAt time.Time
	Error      string
	PID        int
}
