package apikeyssvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/apikeys"
)

func (s *service) ListAPIKeys(
	ctx context.Context,
	orgSlug slug.Slug,
	roleFilter *slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.APIKey], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var ret []model.APIKey
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		keys, err := s.apiKeysRepo.ListByOrganization(ctx, tx, orgSlug, roleFilter, queryReq)
		if err != nil {
			return xerror.Wrapf("list api keys: %w", err)
		}
		ret = apiKeysToModel(keys)
		return nil
	})
	if err != nil {
		return model.ListResponse[model.APIKey]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item model.APIKey, sortBy string) (string, string) {
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

	return model.ListResponse[model.APIKey]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) CreateAPIKey(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	newAPIKey model.NewAPIKey,
) (*model.APIKeyWithToken, error) {
	if err := validate.Default().Struct(newAPIKey); err != nil {
		return nil, ErrInvalidInput.With(err)
	}

	apiKeySlug, err := newAPIKey.Slug.ValidateOrGenerate(newAPIKey.Name)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.Slug).With(err)
	}

	if _, err := s.rolesSvc.GetRole(ctx, orgSlug, newAPIKey.Role); err != nil {
		return nil, xerror.Wrapf("get api key role: %w", err)
	}
	if actor.Slug == nil || (actor.Type != model.EventActorTypeUser && actor.Type != model.EventActorTypeAPIKey) {
		return nil, ErrInvalidInput
	}
	var createdByUserSlug *slug.Slug
	if actor.Type == model.EventActorTypeUser {
		createdByUserSlug = actor.Slug
	}

	token, tokenHash, safePrefix, err := generateToken()
	if err != nil {
		return nil, xerror.Wrapf("generate api key token: %w", err)
	}

	var ret *model.APIKeyWithToken
	create := func() error {
		return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
			key, err := s.apiKeysRepo.Create(ctx, tx, apikeys.CreateParams{
				OrgSlug:           orgSlug,
				Slug:              apiKeySlug,
				Name:              newAPIKey.Name,
				Description:       newAPIKey.Description,
				TokenHash:         tokenHash,
				TokenPrefix:       safePrefix,
				CreatedByUserSlug: createdByUserSlug,
				RoleSlug:          newAPIKey.Role,
				ExpiresAt:         newAPIKey.ExpiresAt,
				CanManageAPIKeys:  newAPIKey.CanManageAPIKeys,
			})
			if err != nil {
				return xerror.Wrapf("create api key: %w", err)
			}

			apiKey := apiKeyToModel(key)
			apiKey.Role = newAPIKey.Role
			apiKey.CreatedByUserSlug = createdByUserSlug
			ret = &model.APIKeyWithToken{APIKey: apiKey, Token: token}
			if err := s.recordAPIKeyEvent(ctx, tx, orgSlug, actor, apiKeyCreatedEvent{
				apiKey: apiKey,
			}); err != nil {
				return err
			}
			return nil
		})
	}
	err = create()
	if newAPIKey.Slug == "" && xerror.Is(err, apikeys.ErrSlugAlreadyExists) {
		apiKeySlug, err = slug.WithRandomSuffix(apiKeySlug, 6)
		if err != nil {
			return nil, err
		}
		err = create()
	}
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *service) GetAPIKey(
	ctx context.Context,
	orgSlug, apiKeySlug slug.Slug,
) (*model.APIKey, error) {
	var ret model.APIKey
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		key, err := s.apiKeysRepo.FindBySlug(ctx, tx, orgSlug, apiKeySlug)
		if err != nil {
			return xerror.Wrapf("find api key: %w", err)
		}
		ret = apiKeyToModel(key)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &ret, nil
}

func (s *service) UpdateAPIKey(
	ctx context.Context,
	orgSlug, apiKeySlug slug.Slug,
	actor model.EventActor,
	update model.UpdateAPIKey,
) (*model.APIKey, error) {
	if err := validate.Default().Struct(update); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	canManageAPIKeys := false
	if update.CanManageAPIKeys != nil {
		canManageAPIKeys = *update.CanManageAPIKeys
	}

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if _, err := s.apiKeysRepo.FindBySlug(ctx, tx, orgSlug, apiKeySlug); err != nil {
			return xerror.Wrapf("find api key before update: %w", err)
		}
		if err := s.apiKeysRepo.Update(ctx, tx, apikeys.UpdateParams{
			OrgSlug:             orgSlug,
			Slug:                apiKeySlug,
			Name:                update.Name,
			Description:         update.Description,
			ExpiresAt:           update.ExpiresAt,
			CanManageAPIKeysSet: update.CanManageAPIKeys != nil,
			CanManageAPIKeys:    canManageAPIKeys,
		}); err != nil {
			return xerror.Wrapf("update api key: %w", err)
		}
		if err := s.recordAPIKeyEvent(ctx, tx, orgSlug, actor, apiKeyUpdatedEvent{
			apiKeySlug: apiKeySlug,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return s.GetAPIKey(ctx, orgSlug, apiKeySlug)
}

func (s *service) RevokeAPIKey(
	ctx context.Context,
	orgSlug, apiKeySlug slug.Slug,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if _, err := s.apiKeysRepo.FindBySlug(ctx, tx, orgSlug, apiKeySlug); err != nil {
			return xerror.Wrapf("find api key before revoke: %w", err)
		}
		if err := s.apiKeysRepo.Revoke(ctx, tx, orgSlug, apiKeySlug); err != nil {
			return xerror.Wrapf("revoke api key: %w", err)
		}
		return s.recordAPIKeyEvent(ctx, tx, orgSlug, actor, apiKeyRevokedEvent{
			apiKeySlug: apiKeySlug,
		})
	})
}

// DeleteRevokedAPIKey permanently removes revoked API key metadata after confirming the key is already revoked.
func (s *service) DeleteRevokedAPIKey(
	ctx context.Context,
	orgSlug, apiKeySlug slug.Slug,
	actor model.EventActor,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		key, err := s.apiKeysRepo.FindBySlug(ctx, tx, orgSlug, apiKeySlug)
		if err != nil {
			return xerror.Wrapf("find api key before delete: %w", err)
		}
		if key.RevokedAt == nil {
			return ErrAPIKeyNotRevoked
		}
		if err := s.apiKeysRepo.DeleteRevoked(ctx, tx, orgSlug, apiKeySlug); err != nil {
			return xerror.Wrapf("delete revoked api key: %w", err)
		}
		return s.recordAPIKeyEvent(ctx, tx, orgSlug, actor, apiKeyDeletedEvent{
			apiKeySlug: apiKeySlug,
		})
	})
}

func (s *service) RotateAPIKey(
	ctx context.Context,
	orgSlug, apiKeySlug slug.Slug,
	actor model.EventActor,
) (*model.APIKeyWithToken, error) {
	token, tokenHash, safePrefix, err := generateToken()
	if err != nil {
		return nil, xerror.Wrapf("generate api key token: %w", err)
	}

	var ret *model.APIKeyWithToken
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		key, err := s.apiKeysRepo.Rotate(ctx, tx, apikeys.RotateParams{
			OrgSlug:     orgSlug,
			Slug:        apiKeySlug,
			TokenHash:   tokenHash,
			TokenPrefix: safePrefix,
		})
		if err != nil {
			return xerror.Wrapf("rotate api key: %w", err)
		}

		ret = &model.APIKeyWithToken{APIKey: apiKeyToModel(key), Token: token}
		if err := s.recordAPIKeyEvent(ctx, tx, orgSlug, actor, apiKeyRotatedEvent{
			apiKeySlug: apiKeySlug,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *service) Authenticate(ctx context.Context, token string) (*model.APIKeyPrincipal, error) {
	if token == "" {
		return nil, ErrInvalidToken
	}

	var ret *model.APIKeyPrincipal
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		principal, err := s.apiKeysRepo.FindActivePrincipalByTokenHash(ctx, tx, hashToken(token))
		if err != nil {
			if xerror.Is(err, apikeys.ErrNotFound) {
				return ErrInvalidToken
			}
			return xerror.Wrapf("find api key principal: %w", err)
		}
		if err := s.apiKeysRepo.MarkUsed(ctx, tx, principal.OrgSlug, principal.Slug); err != nil {
			if xerror.Is(err, apikeys.ErrNotFound) {
				return ErrInvalidToken
			}
			return xerror.Wrapf("mark api key used: %w", err)
		}
		ret = &model.APIKeyPrincipal{
			Slug:             principal.Slug,
			OrgSlug:          principal.OrgSlug,
			Role:             principal.Role,
			CanManageAPIKeys: principal.CanManageAPIKeys,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func apiKeysToModel(keys []*apikeys.APIKey) []model.APIKey {
	ret := make([]model.APIKey, 0, len(keys))
	for _, key := range keys {
		ret = append(ret, apiKeyToModel(key))
	}
	return ret
}

func apiKeyToModel(key *apikeys.APIKey) model.APIKey {
	if key == nil {
		return model.APIKey{}
	}
	return model.APIKey{
		Slug:              key.Slug,
		Name:              key.Name,
		Description:       key.Description,
		TokenPrefix:       key.TokenPrefix,
		Role:              key.RoleSlug,
		CanManageAPIKeys:  key.CanManageAPIKeys,
		CreatedByUserSlug: key.CreatedByUserSlug,
		ExpiresAt:         key.ExpiresAt,
		RevokedAt:         key.RevokedAt,
		LastUsedAt:        key.LastUsedAt,
		Timestamps: model.Timestamps{
			CreatedAt: key.CreatedAt,
			UpdatedAt: key.UpdatedAt,
		},
	}
}
