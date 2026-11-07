package patssvc

import (
	"context"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pats"
)

func (s *service) ListPersonalAccessTokens(
	ctx context.Context,
	userSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.PersonalAccessToken], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var ret []model.PersonalAccessToken
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		tokens, err := s.patsRepo.ListByUser(ctx, tx, userSlug, queryReq)
		if err != nil {
			return xerror.Wrapf("list personal access tokens: %w", err)
		}
		ret = personalAccessTokensToModel(tokens)
		return nil
	})
	if err != nil {
		return model.ListResponse[model.PersonalAccessToken]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item model.PersonalAccessToken, sortBy string) (string, string) {
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

	return model.ListResponse[model.PersonalAccessToken]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

func (s *service) CreatePersonalAccessToken(
	ctx context.Context,
	userSlug slug.Slug,
	newPAT model.NewPersonalAccessToken,
) (*model.PersonalAccessTokenWithToken, error) {
	if err := validate.Default().Struct(newPAT); err != nil {
		return nil, ErrInvalidInput.With(err)
	}

	patSlug, err := newPAT.Slug.ValidateOrGenerate(newPAT.Name)
	if err != nil {
		return nil, xerror.InvalidInput(xerror.Slug).With(err)
	}

	token, tokenHash, safePrefix, err := generateToken()
	if err != nil {
		return nil, xerror.Wrapf("generate personal access token: %w", err)
	}

	var ret *model.PersonalAccessTokenWithToken
	create := func() error {
		return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
			pat, err := s.patsRepo.Create(ctx, tx, pats.CreateParams{
				UserSlug:                      userSlug,
				Slug:                          patSlug,
				Name:                          newPAT.Name,
				Description:                   newPAT.Description,
				TokenHash:                     tokenHash,
				TokenPrefix:                   safePrefix,
				ExpiresAt:                     newPAT.ExpiresAt,
				CanManagePersonalAccessTokens: newPAT.CanManagePersonalAccessTokens,
			})
			if err != nil {
				return xerror.Wrapf("create personal access token: %w", err)
			}

			ret = &model.PersonalAccessTokenWithToken{
				PersonalAccessToken: personalAccessTokenToModel(pat),
				Token:               token,
			}
			return nil
		})
	}
	err = create()
	if newPAT.Slug == "" && xerror.Is(err, pats.ErrSlugAlreadyExists) {
		patSlug, err = slug.WithRandomSuffix(patSlug, 6)
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

func (s *service) GetPersonalAccessToken(
	ctx context.Context,
	userSlug, patSlug slug.Slug,
) (*model.PersonalAccessToken, error) {
	var ret model.PersonalAccessToken
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		pat, err := s.patsRepo.FindBySlug(ctx, tx, userSlug, patSlug)
		if err != nil {
			return xerror.Wrapf("find personal access token: %w", err)
		}
		ret = personalAccessTokenToModel(pat)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &ret, nil
}

// UpdatePersonalAccessToken validates and persists safe PAT metadata without changing token material.
func (s *service) UpdatePersonalAccessToken(
	ctx context.Context,
	userSlug, patSlug slug.Slug,
	update model.UpdatePersonalAccessToken,
) (*model.PersonalAccessToken, error) {
	if err := validate.Default().Struct(update); err != nil {
		return nil, ErrInvalidInput.With(err)
	}

	var ret model.PersonalAccessToken
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		token, err := s.patsRepo.FindBySlug(ctx, tx, userSlug, patSlug)
		if err != nil {
			return xerror.Wrapf("find personal access token before update: %w", err)
		}
		if token.RevokedAt != nil {
			return ErrPersonalAccessTokenRevoked
		}
		canManagePersonalAccessTokens := false
		if update.CanManagePersonalAccessTokens != nil {
			canManagePersonalAccessTokens = *update.CanManagePersonalAccessTokens
		}
		if err := s.patsRepo.Update(ctx, tx, pats.UpdateParams{
			UserSlug:                         userSlug,
			Slug:                             patSlug,
			Name:                             update.Name,
			DescriptionSet:                   update.Description.Present,
			Description:                      update.Description.Value,
			ExpiresAtSet:                     update.ExpiresAt.Present,
			ExpiresAt:                        update.ExpiresAt.Value,
			CanManagePersonalAccessTokensSet: update.CanManagePersonalAccessTokens != nil,
			CanManagePersonalAccessTokens:    canManagePersonalAccessTokens,
		}); err != nil {
			return xerror.Wrapf("update personal access token: %w", err)
		}
		updated, err := s.patsRepo.FindBySlug(ctx, tx, userSlug, patSlug)
		if err != nil {
			return xerror.Wrapf("find personal access token after update: %w", err)
		}
		ret = personalAccessTokenToModel(updated)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &ret, nil
}

func (s *service) RevokePersonalAccessToken(ctx context.Context, userSlug, patSlug slug.Slug) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.patsRepo.Revoke(ctx, tx, userSlug, patSlug); err != nil {
			return xerror.Wrapf("revoke personal access token: %w", err)
		}
		return nil
	})
}

// DeleteRevokedPersonalAccessToken permanently removes revoked PAT metadata after confirming the token is already revoked.
func (s *service) DeleteRevokedPersonalAccessToken(ctx context.Context, userSlug, patSlug slug.Slug) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		pat, err := s.patsRepo.FindBySlug(ctx, tx, userSlug, patSlug)
		if err != nil {
			return xerror.Wrapf("find personal access token before delete: %w", err)
		}
		if pat.RevokedAt == nil {
			return ErrPersonalAccessTokenNotRevoked
		}
		if err := s.patsRepo.DeleteRevoked(ctx, tx, userSlug, patSlug); err != nil {
			return xerror.Wrapf("delete revoked personal access token: %w", err)
		}
		return nil
	})
}

func (s *service) RotatePersonalAccessToken(
	ctx context.Context,
	userSlug, patSlug slug.Slug,
) (*model.PersonalAccessTokenWithToken, error) {
	token, tokenHash, safePrefix, err := generateToken()
	if err != nil {
		return nil, xerror.Wrapf("generate personal access token: %w", err)
	}

	var ret *model.PersonalAccessTokenWithToken
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		pat, err := s.patsRepo.Rotate(ctx, tx, pats.RotateParams{
			UserSlug:    userSlug,
			Slug:        patSlug,
			TokenHash:   tokenHash,
			TokenPrefix: safePrefix,
		})
		if err != nil {
			return xerror.Wrapf("rotate personal access token: %w", err)
		}

		ret = &model.PersonalAccessTokenWithToken{
			PersonalAccessToken: personalAccessTokenToModel(pat),
			Token:               token,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *service) Authenticate(ctx context.Context, token string) (*model.UserPrincipal, error) {
	if token == "" || !strings.HasPrefix(strings.TrimSpace(token), TokenPrefix) {
		return nil, ErrInvalidToken
	}

	var ret *model.UserPrincipal
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		principal, err := s.patsRepo.FindActivePrincipalByTokenHash(ctx, tx, hashToken(token))
		if err != nil {
			if xerror.Is(err, pats.ErrNotFound) {
				return ErrInvalidToken
			}
			return xerror.Wrapf("find personal access token principal: %w", err)
		}
		if err := s.patsRepo.MarkUsed(ctx, tx, principal.UserSlug, principal.TokenSlug); err != nil {
			if xerror.Is(err, pats.ErrNotFound) {
				return ErrInvalidToken
			}
			return xerror.Wrapf("mark personal access token used: %w", err)
		}
		ret = &model.UserPrincipal{
			UserSlug:                      principal.UserSlug,
			PersonalAccessTokenSlug:       principal.TokenSlug,
			CanManagePersonalAccessTokens: principal.CanManagePersonalAccessTokens,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func personalAccessTokensToModel(tokens []*pats.PersonalAccessToken) []model.PersonalAccessToken {
	ret := make([]model.PersonalAccessToken, 0, len(tokens))
	for _, token := range tokens {
		ret = append(ret, personalAccessTokenToModel(token))
	}
	return ret
}

func personalAccessTokenToModel(token *pats.PersonalAccessToken) model.PersonalAccessToken {
	if token == nil {
		return model.PersonalAccessToken{}
	}
	return model.PersonalAccessToken{
		Slug:                          token.Slug,
		Name:                          token.Name,
		Description:                   token.Description,
		TokenPrefix:                   token.TokenPrefix,
		UserSlug:                      token.UserSlug,
		CanManagePersonalAccessTokens: token.CanManagePersonalAccessTokens,
		ExpiresAt:                     token.ExpiresAt,
		RevokedAt:                     token.RevokedAt,
		LastUsedAt:                    token.LastUsedAt,
		Timestamps: model.Timestamps{
			CreatedAt: token.CreatedAt,
			UpdatedAt: token.UpdatedAt,
		},
	}
}
