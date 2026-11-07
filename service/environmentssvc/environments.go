package environmentssvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
)

func (s *service) CreateEnvironment(
	ctx context.Context,
	orgSlug slug.Slug,
	newEnvironment model.NewEnvironment,
	actor model.EventActor,
) (*model.Environment, error) {
	if err := validate.Default().Struct(newEnvironment); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	environmentSlug, err := newEnvironment.Slug.ValidateOrGenerate(newEnvironment.Name)
	if err != nil {
		return nil, ErrInvalidEnvironmentSlug.With(err)
	}

	var ret *model.Environment
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if existing, findErr := s.environments.FindAnyBySlug(
			ctx,
			tx,
			orgSlug,
			environmentSlug,
		); findErr == nil &&
			existing != nil &&
			newEnvironment.Slug == "" {
			nextSlug, genErr := slug.WithRandomSuffix(environmentSlug, 6)
			if genErr != nil {
				return xerror.Unexpected().With(genErr)
			}
			environmentSlug = nextSlug
		} else if findErr != nil && !xerror.Is(findErr, environments.ErrNotFound) {
			return xerror.Wrapf("check environment slug collision: %w", findErr)
		}

		created, createErr := s.environments.Create(ctx, tx, orgSlug, &environments.CreateParams{
			Slug:        environmentSlug,
			Name:        newEnvironment.Name,
			Description: newEnvironment.Description,
			Color:       newEnvironment.Color,
			Protected:   newEnvironment.Protected,
		})
		if createErr != nil {
			return xerror.Wrapf("create environment: %w", createErr)
		}

		ret = toEnvironment(created)
		if err := s.recordEnvironmentEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			environmentSlug,
			model.EventActionCreate,
			false,
		); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *service) CreateDefaultEnvironments(ctx context.Context, orgSlug slug.Slug) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		for _, defaultEnvironment := range model.DefaultEnvironmentDefinitions() {
			if err := s.createDefaultEnvironment(ctx, tx, orgSlug, defaultEnvironment); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *service) createDefaultEnvironment(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	defaultEnvironment model.NewEnvironment,
) error {
	if _, err := s.environments.FindAnyBySlug(ctx, tx, orgSlug, defaultEnvironment.Slug); err == nil {
		return nil
	} else if !xerror.Is(err, environments.ErrNotFound) {
		return xerror.Wrapf("check default environment %s: %w", defaultEnvironment.Slug, err)
	}

	if _, err := s.environments.Create(ctx, tx, orgSlug, &environments.CreateParams{
		Slug:        defaultEnvironment.Slug,
		Name:        defaultEnvironment.Name,
		Description: defaultEnvironment.Description,
		Color:       defaultEnvironment.Color,
		Protected:   defaultEnvironment.Protected,
	}); err != nil && !xerror.Is(err, environments.ErrSlugTaken) {
		return xerror.Wrapf("create default environment %s: %w", defaultEnvironment.Slug, err)
	}

	return nil
}

func (s *service) GetEnvironment(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
) (*model.Environment, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.Environment, error) {
		environment, err := s.environments.FindBySlug(ctx, tx, orgSlug, environmentSlug)
		if err != nil {
			return nil, xerror.Wrapf("get environment: %w", err)
		}
		return toEnvironment(environment), nil
	})
}

func (s *service) ListEnvironments(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListEnvironmentsRequest,
) (model.ListResponse[model.Environment], error) {
	normalized, queryReq := model.PrepareListQuery(req.ListRequest, model.ListSortCreatedAt)

	var ret []model.Environment
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		environmentsList, listErr := s.environments.ListByOrg(
			ctx,
			tx,
			orgSlug,
			model.ListEnvironmentsRequest{
				ListRequest:    queryReq,
				IncludeDeleted: req.IncludeDeleted,
			},
		)
		if listErr != nil {
			return xerror.Wrapf("list environments: %w", listErr)
		}

		ret = make([]model.Environment, 0, len(environmentsList))
		for _, environment := range environmentsList {
			ret = append(ret, *toEnvironment(environment))
		}
		return nil
	})
	if err != nil {
		return model.ListResponse[model.Environment]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item model.Environment, sortBy string) (string, string) {
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

	return model.ListResponse[model.Environment]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) UpdateEnvironment(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	update model.UpdateEnvironment,
	actor model.EventActor,
) (*model.Environment, error) {
	if err := validate.Default().Struct(update); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	var ret *model.Environment
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		existing, findErr := s.environments.FindBySlug(ctx, tx, orgSlug, environmentSlug)
		if findErr != nil {
			return xerror.Wrapf("find environment before update: %w", findErr)
		}
		if existing.Protected {
			if err := protectedenv.ConfirmEnvironment(toEnvironment(existing), update.ProtectedEnvironmentConfirmation); err != nil {
				return err
			}
		}
		updated, updateErr := s.environments.Update(
			ctx,
			tx,
			orgSlug,
			environmentSlug,
			&environments.UpdateParams{
				Name:        update.Name,
				Description: update.Description,
				Color:       update.Color,
				Protected:   update.Protected,
			},
		)
		if updateErr != nil {
			return xerror.Wrapf("update environment: %w", updateErr)
		}

		ret = toEnvironment(updated)
		if err := s.recordEnvironmentEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			environmentSlug,
			model.EventActionUpdate,
			existing.Protected,
		); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *service) DeleteEnvironment(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	deleted model.DeleteEnvironment,
	actor model.EventActor,
) error {
	if err := validate.Default().Struct(deleted); err != nil {
		return xerror.ValidationFailed().With(err)
	}

	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		environment, findErr := s.environments.FindBySlug(ctx, tx, orgSlug, environmentSlug)
		if findErr != nil {
			return xerror.Wrapf("find environment before delete: %w", findErr)
		}
		if err := protectedenv.ConfirmEnvironment(toEnvironment(environment), deleted.ProtectedEnvironmentConfirmation); err != nil {
			return err
		}
		secrets, err := s.environments.ListAllEnvironmentSecrets(ctx, tx, orgSlug, environmentSlug)
		if err != nil {
			return xerror.Wrapf("list environment secrets before environment delete: %w", err)
		}
		for _, secret := range secrets {
			if err := s.revokeEnvironmentSecretCredential(ctx, orgSlug, environmentSlug, secret.Key, actor); err != nil {
				return xerror.Wrapf("revoke environment secret %q before environment delete: %w", secret.Key, err)
			}
		}
		if err := s.environments.DeleteEnvironmentSecretsForEnvironment(ctx, tx, orgSlug, environmentSlug); err != nil {
			return xerror.Wrapf("delete environment secrets before environment delete: %w", err)
		}
		if err := s.environments.Delete(
			ctx,
			tx,
			orgSlug,
			environmentSlug,
			&environments.DeleteParams{Reason: deleted.Reason},
		); err != nil {
			return xerror.Wrapf("delete environment: %w", err)
		}
		return s.recordEnvironmentEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			environmentSlug,
			model.EventActionDelete,
			environment.Protected,
		)
	})
}

func (s *service) EnableProjectEnvironment(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params model.EnableProjectEnvironment,
	actor model.EventActor,
) (*model.ProjectEnvironment, error) {
	var ret *model.ProjectEnvironment
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		protection, protectionErr := s.environments.FindProjectEnvironmentProtection(ctx, tx, orgSlug, projectSlug, environmentSlug)
		if protectionErr != nil {
			if !xerror.Is(protectionErr, environments.ErrProjectEnvironmentNotFound) {
				return xerror.Wrapf("check project environment protection: %w", protectionErr)
			}
			catalogProtection, catalogProtectionErr := s.environments.FindProjectEnvironmentCatalogProtection(ctx, tx, orgSlug, projectSlug, environmentSlug)
			if catalogProtectionErr != nil {
				return xerror.Wrapf("check catalog environment protection: %w", catalogProtectionErr)
			}
			protection = catalogProtection
		}
		if err := protectedenv.Confirm(protection, params.ProtectedEnvironmentConfirmation); err != nil {
			return err
		}
		protectedConfirmed := protection.Protected
		projectEnvironment, enableErr := s.environments.EnableProjectEnvironment(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			environmentSlug,
			&environments.EnableProjectParams{IsDefault: params.IsDefault},
		)
		if enableErr != nil {
			return xerror.Wrapf("enable project environment: %w", enableErr)
		}

		ret = toProjectEnvironment(projectEnvironment)
		if err := s.recordProjectEnvironmentEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			projectSlug,
			environmentSlug,
			model.EventActionEnable,
			protectedConfirmed,
		); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *service) ListProjectEnvironments(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.ProjectEnvironment], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var ret []model.ProjectEnvironment
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		projectEnvironments, listErr := s.environments.ListProjectEnvironments(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			queryReq,
		)
		if listErr != nil {
			return xerror.Wrapf("list project environments: %w", listErr)
		}

		ret = make([]model.ProjectEnvironment, 0, len(projectEnvironments))
		for _, projectEnvironment := range projectEnvironments {
			ret = append(ret, *toProjectEnvironment(projectEnvironment))
		}
		return nil
	})
	if err != nil {
		return model.ListResponse[model.ProjectEnvironment]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item model.ProjectEnvironment, sortBy string) (string, string) {
			cursorValue := item.Environment.CreatedAt.Format(time.RFC3339Nano)
			switch sortBy {
			case model.ListSortUpdatedAt:
				cursorValue = item.Environment.UpdatedAt.Format(time.RFC3339Nano)
			case model.ListSortName:
				cursorValue = item.Environment.Name
			}
			return cursorValue, item.Environment.Slug.String()
		},
	)

	return model.ListResponse[model.ProjectEnvironment]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) DisableProjectEnvironment(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	confirmation *slug.Slug,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		protection, protectionErr := s.environments.FindProjectEnvironmentProtection(ctx, tx, orgSlug, projectSlug, environmentSlug)
		if protectionErr != nil {
			return xerror.Wrapf("check project environment protection: %w", protectionErr)
		}
		if err := protectedenv.Confirm(protection, confirmation); err != nil {
			return err
		}
		if err := s.environments.DisableProjectEnvironment(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			environmentSlug,
		); err != nil {
			return xerror.Wrapf("disable project environment: %w", err)
		}
		return s.recordProjectEnvironmentEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			projectSlug,
			environmentSlug,
			model.EventActionDisable,
			protection.Protected,
		)
	})
}

func (s *service) recordEnvironmentEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	environmentSlug slug.Slug,
	action model.EventAction,
	protectedEnvironment bool,
) error {
	if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, environmentEvent{
		environmentSlug:      environmentSlug,
		action:               action,
		protectedEnvironment: protectedEnvironment,
	}); err != nil {
		return xerror.Wrapf("enqueue required environment audit event: %w", err)
	}
	return nil
}

// recordEnvironmentSecretEvent records safe secret metadata without including plaintext values.
func (s *service) recordEnvironmentSecretEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	environmentSlug slug.Slug,
	secretKey string,
	action model.EventAction,
	protectedEnvironment bool,
) error {
	if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, environmentEvent{
		environmentSlug:      environmentSlug,
		action:               action,
		protectedEnvironment: protectedEnvironment,
		metadata: map[string]any{
			"key_name": secretKey,
		},
	}); err != nil {
		return xerror.Wrapf("enqueue required environment secret audit event: %w", err)
	}
	return nil
}

func (s *service) recordProjectEnvironmentEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	projectSlug, environmentSlug slug.Slug,
	action model.EventAction,
	protectedEnvironment bool,
) error {
	if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, projectEnvironmentEvent{
		projectSlug:          projectSlug,
		environmentSlug:      environmentSlug,
		action:               action,
		protectedEnvironment: protectedEnvironment,
	}); err != nil {
		return xerror.Wrapf("enqueue required project environment audit event: %w", err)
	}
	return nil
}

func toEnvironment(environment *environments.Environment) *model.Environment {
	if environment == nil {
		return nil
	}

	return &model.Environment{
		Slug:         environment.Slug,
		Name:         environment.Name,
		Description:  environment.Description,
		Color:        environment.Color,
		Protected:    environment.Protected,
		DeletedAt:    environment.DeletedAt,
		DeleteReason: environment.DeleteReason,
		Timestamps: model.Timestamps{
			CreatedAt: environment.CreatedAt,
			UpdatedAt: environment.UpdatedAt,
		},
	}
}

func toProjectEnvironment(
	projectEnvironment *environments.ProjectEnvironment,
) *model.ProjectEnvironment {
	if projectEnvironment == nil {
		return nil
	}

	return &model.ProjectEnvironment{
		Environment: *toEnvironment(&projectEnvironment.Environment),
		IsDefault:   projectEnvironment.IsDefault,
		EnabledAt:   projectEnvironment.EnabledAt,
		UpdatedAt:   projectEnvironment.UpdatedAt,
	}
}
