package projectssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/postgres/projects"
)

func toProject(project *projects.Project) *model.Project {
	var backend *model.ProjectPipelineBackend
	if project.PipelineBackendDataSourceSlug != nil &&
		project.PipelineBackendRepo != nil &&
		project.PipelineBackendTrackedBranch != nil &&
		project.PipelineBackendPipelineDir != nil {
		backend = &model.ProjectPipelineBackend{
			DataSourceSlug: *project.PipelineBackendDataSourceSlug,
			Repo:           *project.PipelineBackendRepo,
			TrackedBranch:  *project.PipelineBackendTrackedBranch,
			PipelineDir:    *project.PipelineBackendPipelineDir,
			FirstCommitSHA: project.PipelineBackendFirstCommitSHA,
		}
	}

	return &model.Project{
		Slug:            project.Slug,
		Name:            project.Name,
		PipelineBackend: backend,
		Timestamps: model.Timestamps{
			CreatedAt: project.CreatedAt,
			UpdatedAt: project.UpdatedAt,
		},
	}
}
