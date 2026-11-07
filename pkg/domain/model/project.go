//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=SetProjectPipelineBackendMode -json -text -linecomment -output=project_pipeline_backend_mode_enumer.go
package model

import "github.com/Braidplane/braidplane-core/pkg/slug"

const PipelineRepositoryTemplateVersion = "v1"

type NewProject struct {
	Name string `validate:"required,min=1,max=255"`
	Slug slug.Slug
}

type UpdateProject struct {
	Name string `validate:"required,min=1,max=255"`
}

type ProjectPipelineBackend struct {
	DataSourceSlug slug.Slug
	// Repo is normalized external git identity. The credential owner is the data
	// source FK; the repo itself is not a local database resource.
	Repo           string
	TrackedBranch  string
	PipelineDir    string
	FirstCommitSHA *string
}

type SetProjectPipelineBackendMode int

const (
	SetProjectPipelineBackendModeUnknown SetProjectPipelineBackendMode = iota // unknown
	// AdoptExisting switches the project to the target backend, then imports the
	// pipeline files already present there.
	SetProjectPipelineBackendModeAdoptExisting // adopt_existing
	// CreateFromTemplate creates a private provider repository, then imports its
	// exact first commit through the same adoption path.
	SetProjectPipelineBackendModeCreateFromTemplate // create_from_template
)

type SetProjectPipelineBackend struct {
	// Setup must not replace a backend configured while provider creation was in flight.
	RequireUnconfigured bool
	ExpectedRepository  string
	DataSourceSlug      slug.Slug `validate:"required"`
	TrackedBranch       string    `validate:"required"`
	PipelineDir         string    `validate:"required"`
	ProviderOwner       string
	RepositoryName      string
	Mode                SetProjectPipelineBackendMode
}

type DeleteProjectOptions struct {
	CleanupRemote bool
}

type Project struct {
	Slug            slug.Slug
	Name            string
	PipelineBackend *ProjectPipelineBackend
	Timestamps
}
