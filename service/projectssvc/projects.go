package projectssvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	"github.com/Braidplane/braidplane-core/postgres/projects"
)

func (s *service) CreateProject(
	ctx context.Context,
	orgSlug slug.Slug,
	newProject model.NewProject,
	actor model.EventActor,
) (*model.Project, error) {
	if err := validate.Default().Struct(newProject); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	projectSlug, err := newProject.Slug.ValidateOrGenerate(newProject.Name)
	if err != nil {
		return nil, ErrInvalidProjectSlug.With(err)
	}

	var ret *model.Project
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if existing, findErr := s.projectsRepo.FindBySlug(
			ctx,
			tx,
			orgSlug,
			projectSlug,
		); findErr == nil &&
			existing != nil &&
			newProject.Slug == "" {
			nextSlug, genErr := slug.WithRandomSuffix(projectSlug, 6)
			if genErr != nil {
				return xerror.Unexpected().With(genErr)
			}
			projectSlug = nextSlug
		} else if findErr != nil && !xerror.Is(findErr, projects.ErrNotFound) {
			return xerror.Wrapf("check project slug collision: %w", findErr)
		}

		project, createErr := s.projectsRepo.Create(ctx, tx, &projects.CreateParams{
			OrgSlug: orgSlug,
			Slug:    projectSlug,
			Name:    newProject.Name,
		})
		if createErr != nil {
			return xerror.Wrapf("create project: %w", createErr)
		}

		ret = toProject(project)
		if err := s.enableDefaultProjectEnvironment(ctx, tx, orgSlug, projectSlug); err != nil {
			return err
		}

		if err := s.recordProjectEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			projectSlug,
			model.EventActionCreate,
		); err != nil {
			return xerror.Wrapf("record project create event: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *service) enableDefaultProjectEnvironment(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
) error {
	if s.environmentsRepo == nil {
		return nil
	}

	defaultEnvironmentSlug := model.DefaultDevelopmentEnvironmentSlug
	if _, err := s.environmentsRepo.FindBySlug(ctx, tx, orgSlug, defaultEnvironmentSlug); err != nil {
		if xerror.Is(err, environments.ErrNotFound) {
			return nil
		}
		return xerror.Wrapf("find default project environment: %w", err)
	}

	if _, err := s.environmentsRepo.EnableProjectEnvironment(
		ctx,
		tx,
		orgSlug,
		projectSlug,
		defaultEnvironmentSlug,
		&environments.EnableProjectParams{IsDefault: true},
	); err != nil {
		if xerror.Is(err, environments.ErrProjectEnvironmentNotFound) {
			return nil
		}
		return xerror.Wrapf("enable default project environment: %w", err)
	}

	return nil
}

func (s *service) GetProject(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
) (*model.Project, error) {
	var ret *model.Project

	err := s.txm.RunRead(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		project, findErr := s.projectsRepo.FindBySlug(ctx, tx, orgSlug, projectSlug)
		if findErr != nil {
			return xerror.Wrapf("get project: %w", findErr)
		}

		ret = toProject(project)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *service) ListProjects(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.Project], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var ret []model.Project
	err := s.txm.RunRead(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		projectsList, listErr := s.projectsRepo.ListByOrg(ctx, tx, orgSlug, queryReq)
		if listErr != nil {
			return xerror.Wrapf("list projects: %w", listErr)
		}

		ret = make([]model.Project, 0, len(projectsList))
		for _, project := range projectsList {
			ret = append(ret, *toProject(project))
		}

		return nil
	})
	if err != nil {
		return model.ListResponse[model.Project]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item model.Project, sortBy string) (string, string) {
			cursorValue := item.CreatedAt.Format(time.RFC3339Nano)
			switch sortBy {
			case model.ListSortUpdatedAt:
				cursorValue = item.UpdatedAt.Format(time.RFC3339Nano)
			case model.ListSortName:
				cursorValue = item.Name
			}
			return cursorValue, item.Slug.String()
		},
	)

	return model.ListResponse[model.Project]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) UpdateProject(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	update model.UpdateProject,
	actor model.EventActor,
) (*model.Project, error) {
	if err := validate.Default().Struct(update); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	var ret *model.Project
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if updateErr := s.projectsRepo.Update(ctx, tx, &projects.UpdateParams{
			OrgSlug: orgSlug,
			Slug:    projectSlug,
			Name:    update.Name,
		}); updateErr != nil {
			return xerror.Wrapf("update project: %w", updateErr)
		}

		project, findErr := s.projectsRepo.FindBySlug(ctx, tx, orgSlug, projectSlug)
		if findErr != nil {
			return xerror.Wrapf("fetch project: %w", findErr)
		}

		ret = toProject(project)
		if err := s.recordProjectEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			projectSlug,
			model.EventActionUpdate,
		); err != nil {
			return xerror.Wrapf("record project update event: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *service) DeleteProject(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	options model.DeleteProjectOptions,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if options.CleanupRemote {
			if err := s.enqueueProjectRemoteCleanup(ctx, tx, orgSlug, projectSlug); err != nil {
				return err
			}
		}
		if err := s.projectsRepo.Delete(ctx, tx, orgSlug, projectSlug); err != nil {
			return xerror.Wrapf("delete project: %w", err)
		}

		if err := s.recordProjectEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			projectSlug,
			model.EventActionDelete,
		); err != nil {
			return xerror.Wrapf("record project delete event: %w", err)
		}

		return nil
	})
}

func (s *service) recordProjectEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	projectSlug slug.Slug,
	action model.EventAction,
) error {
	switch action {
	case model.EventActionCreate,
		model.EventActionUpdate,
		model.EventActionDelete:
	default:
		return xerror.InvalidInput(xerror.Event).Detail("unsupported project event action")
	}

	if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, projectEvent{
		projectSlug: projectSlug,
		action:      action,
	}); err != nil {
		return xerror.Wrapf("enqueue required project audit event: %w", err)
	}
	return nil
}
