package runrecency

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type Scope struct {
	Org, Project, Environment, Pipeline slug.Slug
}

type Expectation struct {
	ScopeDisabled bool
	ID            int64
	Scope
	EnvironmentID                               int64
	Revision                                    int64
	MaximumAge                                  time.Duration
	Enabled                                     bool
	EnabledAt                                   time.Time
	DeploymentID, DeploymentRevision, VersionID int64
	ResolutionDigest                            *string
	VersionSpecHash                             *string
	DeployedAt                                  time.Time
	SuccessRun                                  *string
	SucceededAt                                 *time.Time
}

type Episode struct {
	Slug                                            slug.Slug
	ExpectationID, DeploymentID, DeploymentRevision int64
	Deadline, BreachedAt                            time.Time
	LastSuccessAt                                   *time.Time
	LastSuccessRun                                  *string
}

// EpisodeEvidence retains the identities recorded when a breach was observed.
type EpisodeEvidence struct {
	Episode
	ExpectationRevision           int64
	MaximumAge                    time.Duration
	EnabledAt                     time.Time
	VersionSpecHash               string
	ResolutionDigest              *string
	DeployedAt                    time.Time
	ResolvedAt                    *time.Time
	ResolutionReason, RecoveryRun *string
	RecoverySucceededAt           *time.Time
}
