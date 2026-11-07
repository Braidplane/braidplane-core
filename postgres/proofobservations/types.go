// Package proofobservations persists the backend-owned Proof observation ledger.
package proofobservations

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

//go:generate -command enumer go tool -modfile=../../tools/go.mod enumer
//go:generate enumer -type=State -json -text -linecomment -output=state_enumer.go
type State int

const (
	StateUnknown  State = iota // unknown
	StatePending               // pending
	StateRunning               // running
	StateFinished              // finished
)

type Policy struct {
	OrganizationID                  int32
	ProjectID, ProjectEnvironmentID int64
	WorkerIdentity                  string
	Value                           proofevidence.Policy
}

type Scope struct{ Organization, Project, Environment slug.Slug }

type Job struct {
	CancellationRequested                bool
	Slug                                 slug.Slug
	State                                State
	RequestDigest                        string
	Request                              proofevidence.Request
	Authorization                        proofevidence.Authorization
	TargetCiphertext, SnapshotCiphertext []byte
	Deadline, StartedAt                  time.Time
	Result                               proofevidence.Result
}

type Budget struct{ ActiveJobs, Requests, Bytes, Rows, Cost int64 }

// MaintenanceScope identifies retained state even after its environment is disabled.
type MaintenanceScope struct {
	Scope
	OrganizationID       int32
	ProjectEnvironmentID int64
	Disabled             bool
}

// JobReference identifies a retention mutation without returning protected payloads.
type JobReference struct {
	Slug          slug.Slug
	RequestDigest string
}
