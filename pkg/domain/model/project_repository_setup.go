//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=ProjectRepositorySetupStatus -json -text -linecomment -output=project_repository_setup_status_enumer.go
package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type ProjectRepositorySetupStatus int

const (
	ProjectRepositorySetupStatusUnknown           ProjectRepositorySetupStatus = iota // unknown
	ProjectRepositorySetupStatusPending                                               // pending
	ProjectRepositorySetupStatusRepositoryCreated                                     // repository_created
	ProjectRepositorySetupStatusSourceCreated                                         // source_created
	ProjectRepositorySetupStatusAttached                                              // attached
)

type CreateProjectRepository struct {
	Provider                DataSourceType `json:"provider"`
	Owner                   string         `json:"owner"`
	RepositoryName          string         `json:"repository_name"`
	TrackedBranch           string         `json:"tracked_branch"`
	PipelineDir             string         `json:"pipeline_dir"`
	CreatePrivateRepository bool           `json:"create_private_repository"`
}

// GitHubRepositoryAuthorization is request-only and must never be persisted.
type GitHubRepositoryAuthorization struct {
	State          string
	Code           string
	InstallationID int64
}

// GitLabRepositoryAuthorization is request-only and must never be persisted.
type GitLabRepositoryAuthorization struct {
	State  string
	Code   string
	Issuer string
}

type ProjectRepositorySetup struct {
	AuthorizationURL string
	Slug             slug.Slug
	ProjectSlug      slug.Slug
	Request          CreateProjectRepository
	Status           ProjectRepositorySetupStatus
	DataSourceSlug   slug.Slug
	RepositoryID     string
	RepositoryURL    string
	FirstCommitSHA   string
	FailureCode      string
	AbandonedAt      *time.Time
}
