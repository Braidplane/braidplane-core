package fakes

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// ProjectsService is a test double for projectssvc.Service.
type ProjectsService struct {
	CreateProjectFn             func(ctx context.Context, orgSlug slug.Slug, newProject model.NewProject, actorSlug slug.Slug) (*model.Project, error)
	GetProjectFn                func(ctx context.Context, orgSlug, projectSlug slug.Slug) (*model.Project, error)
	ListProjectsFn              func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.Project], error)
	UpdateProjectFn             func(ctx context.Context, orgSlug, projectSlug slug.Slug, update model.UpdateProject, actorSlug slug.Slug) (*model.Project, error)
	SetProjectPipelineBackendFn func(ctx context.Context, orgSlug, projectSlug slug.Slug, params model.SetProjectPipelineBackend) (*model.Project, error)
	DeleteProjectFn             func(ctx context.Context, orgSlug, projectSlug slug.Slug, options model.DeleteProjectOptions, actorSlug slug.Slug) error
}

func (f *ProjectsService) CreateProject(
	ctx context.Context,
	orgSlug slug.Slug,
	newProject model.NewProject,
	actorSlug slug.Slug,
) (*model.Project, error) {
	if f.CreateProjectFn != nil {
		return f.CreateProjectFn(ctx, orgSlug, newProject, actorSlug)
	}
	return &model.Project{Slug: newProject.Slug, Name: newProject.Name}, nil
}

func (f *ProjectsService) GetProject(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
) (*model.Project, error) {
	if f.GetProjectFn != nil {
		return f.GetProjectFn(ctx, orgSlug, projectSlug)
	}
	return &model.Project{Slug: projectSlug}, nil
}

func (f *ProjectsService) ListProjects(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.Project], error) {
	if f.ListProjectsFn != nil {
		return f.ListProjectsFn(ctx, orgSlug, req)
	}
	return model.ListResponse[model.Project]{Items: []model.Project{}}, nil
}

func (f *ProjectsService) UpdateProject(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	update model.UpdateProject,
	actorSlug slug.Slug,
) (*model.Project, error) {
	if f.UpdateProjectFn != nil {
		return f.UpdateProjectFn(ctx, orgSlug, projectSlug, update, actorSlug)
	}
	return &model.Project{Slug: projectSlug, Name: update.Name}, nil
}

func (f *ProjectsService) DeleteProject(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	options model.DeleteProjectOptions,
	actorSlug slug.Slug,
) error {
	if f.DeleteProjectFn != nil {
		return f.DeleteProjectFn(ctx, orgSlug, projectSlug, options, actorSlug)
	}
	return nil
}

func (f *ProjectsService) SetProjectPipelineBackend(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	params model.SetProjectPipelineBackend,
) (*model.Project, error) {
	if f.SetProjectPipelineBackendFn != nil {
		return f.SetProjectPipelineBackendFn(ctx, orgSlug, projectSlug, params)
	}
	return &model.Project{
		Slug: projectSlug,
		PipelineBackend: &model.ProjectPipelineBackend{
			DataSourceSlug: params.DataSourceSlug,
			TrackedBranch:  params.TrackedBranch,
		},
	}, nil
}
