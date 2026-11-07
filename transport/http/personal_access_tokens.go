package http

import (
	"encoding/json"
	"fmt"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func (s *server) ListPersonalAccessTokens(
	c fiber.Ctx,
	params api.ListPersonalAccessTokensParams,
) error {
	principal, err := s.canPersonalAccessTokenManager(c, model.Read)
	if err != nil {
		return err
	}

	response, err := s.patsService.ListPersonalAccessTokens(
		c.Context(),
		principal.Slug,
		toPersonalAccessTokensListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	tokens := make([]api.PersonalAccessToken, 0, len(response.Items))
	for _, token := range response.Items {
		tokens = append(tokens, personalAccessTokenToHTTP(token))
	}

	return c.JSON(api.PersonalAccessTokenListResponse{
		PersonalAccessTokens: tokens,
		HasMore:              response.HasMore,
		NextCursor:           response.NextCursor,
		PrevCursor:           response.PrevCursor,
	})
}

func (s *server) CreatePersonalAccessToken(c fiber.Ctx) error {
	principal, err := s.canPersonalAccessTokenManager(c, model.Update)
	if err != nil {
		return err
	}

	var req api.NewPersonalAccessToken
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	if principal.Source == reqctx.UserPrincipalSourcePersonalAccessToken &&
		req.CanManagePersonalAccessTokens != nil {
		return apierror.Forbidden(model.ResourceProfile.String(), model.Update.String())
	}

	newPAT, err := newPersonalAccessTokenFromHTTP(req)
	if err != nil {
		return err
	}
	if err := chargeAuthorizedUserAbuseControl(c, principal.Slug.String()); err != nil {
		return err
	}

	token, err := s.patsService.CreatePersonalAccessToken(c.Context(), principal.Slug, newPAT)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusCreated).JSON(personalAccessTokenWithTokenToHTTP(*token))
}

func (s *server) GetPersonalAccessToken(
	c fiber.Ctx,
	patSlugParam api.PATSlugParam,
) error {
	userSlug, patSlug, err := s.personalAccessTokenPathSlugs(c, patSlugParam, model.Read)
	if err != nil {
		return err
	}

	token, err := s.patsService.GetPersonalAccessToken(c.Context(), userSlug, patSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(personalAccessTokenToHTTP(*token))
}

// UpdatePersonalAccessToken updates safe PAT metadata for the authenticated session user.
func (s *server) UpdatePersonalAccessToken(
	c fiber.Ctx,
	patSlugParam api.PATSlugParam,
) error {
	userSlug, patSlug, err := s.personalAccessTokenPathSlugs(c, patSlugParam, model.Update)
	if err != nil {
		return err
	}

	update, err := updatePersonalAccessTokenFromHTTP(c.Body())
	if err != nil {
		return err
	}
	principal, ok := reqctx.GetUserPrincipal(c, s.sessionStore)
	if !ok || (principal.Source == reqctx.UserPrincipalSourcePersonalAccessToken &&
		update.CanManagePersonalAccessTokens != nil) {
		return apierror.Forbidden(model.ResourceProfile.String(), model.Update.String())
	}

	token, err := s.patsService.UpdatePersonalAccessToken(
		c.Context(),
		userSlug,
		patSlug,
		update,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(personalAccessTokenToHTTP(*token))
}

// updatePersonalAccessTokenFromHTTP decodes PAT metadata updates while preserving omitted-vs-null fields.
func updatePersonalAccessTokenFromHTTP(body []byte) (model.UpdatePersonalAccessToken, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return model.UpdatePersonalAccessToken{}, apierror.InvalidRequestBody()
	}
	if raw == nil {
		return model.UpdatePersonalAccessToken{}, apierror.InvalidRequestBody()
	}

	var req api.UpdatePersonalAccessToken
	if err := json.Unmarshal(body, &req); err != nil {
		return model.UpdatePersonalAccessToken{}, apierror.InvalidRequestBody()
	}

	_, hasDescription := raw["description"]
	_, hasExpiresAt := raw["expires_at"]
	return model.UpdatePersonalAccessToken{
		Name: req.Name,
		Description: model.NullableStringUpdate{
			Present: hasDescription,
			Value:   req.Description,
		},
		ExpiresAt: model.NullableTimeUpdate{
			Present: hasExpiresAt,
			Value:   req.ExpiresAt,
		},
		CanManagePersonalAccessTokens: req.CanManagePersonalAccessTokens,
	}, nil
}

func (s *server) DeletePersonalAccessToken(
	c fiber.Ctx,
	patSlugParam api.PATSlugParam,
) error {
	userSlug, patSlug, err := s.personalAccessTokenPathSlugs(c, patSlugParam, model.Update)
	if err != nil {
		return err
	}

	if err := s.patsService.RevokePersonalAccessToken(c.Context(), userSlug, patSlug); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// DeleteRevokedPersonalAccessToken permanently deletes revoked PAT metadata for the session user.
func (s *server) DeleteRevokedPersonalAccessToken(
	c fiber.Ctx,
	patSlugParam api.PATSlugParam,
) error {
	userSlug, err := s.canSessionProfile(c, model.Update)
	if err != nil {
		return err
	}
	patSlug, err := slug.New(patSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid PAT slug: %v", err))
	}

	if err := s.patsService.DeleteRevokedPersonalAccessToken(c.Context(), userSlug, patSlug); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) RotatePersonalAccessToken(
	c fiber.Ctx,
	patSlugParam api.PATSlugParam,
) error {
	userSlug, patSlug, err := s.personalAccessTokenPathSlugs(c, patSlugParam, model.Update)
	if err != nil {
		return err
	}
	if err := chargeAuthorizedUserAbuseControl(c, userSlug.String()); err != nil {
		return err
	}

	token, err := s.patsService.RotatePersonalAccessToken(c.Context(), userSlug, patSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(personalAccessTokenWithTokenToHTTP(*token))
}

func newPersonalAccessTokenFromHTTP(req api.NewPersonalAccessToken) (model.NewPersonalAccessToken, error) {
	var patSlug slug.Slug
	if req.Slug != nil {
		parsedSlug, err := slug.New(*req.Slug)
		if err != nil {
			return model.NewPersonalAccessToken{}, apierror.ValidationFailed(fmt.Sprintf("Invalid PAT slug: %v", err))
		}
		patSlug = parsedSlug
	}

	return model.NewPersonalAccessToken{
		Name:                          req.Name,
		Slug:                          patSlug,
		Description:                   req.Description,
		ExpiresAt:                     req.ExpiresAt,
		CanManagePersonalAccessTokens: req.CanManagePersonalAccessTokens != nil && *req.CanManagePersonalAccessTokens,
	}, nil
}

func (s *server) personalAccessTokenPathSlugs(
	c fiber.Ctx,
	patSlugParam api.PATSlugParam,
	action model.Action,
) (slug.Slug, slug.Slug, error) {
	principal, err := s.canPersonalAccessTokenManager(c, action)
	if err != nil {
		return "", "", err
	}

	patSlug, err := slug.New(patSlugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid PAT slug: %v", err))
	}

	return principal.Slug, patSlug, nil
}

func personalAccessTokenWithTokenToHTTP(token model.PersonalAccessTokenWithToken) api.PersonalAccessTokenWithToken {
	base := personalAccessTokenToHTTP(token.PersonalAccessToken)
	return api.PersonalAccessTokenWithToken{
		Slug:                          base.Slug,
		Name:                          base.Name,
		Description:                   base.Description,
		TokenPrefix:                   base.TokenPrefix,
		UserSlug:                      base.UserSlug,
		CanManagePersonalAccessTokens: base.CanManagePersonalAccessTokens,
		ExpiresAt:                     base.ExpiresAt,
		RevokedAt:                     base.RevokedAt,
		LastUsedAt:                    base.LastUsedAt,
		CreatedAt:                     base.CreatedAt,
		UpdatedAt:                     base.UpdatedAt,
		Token:                         token.Token,
	}
}

func personalAccessTokenToHTTP(token model.PersonalAccessToken) api.PersonalAccessToken {
	return api.PersonalAccessToken{
		Slug:                          token.Slug.String(),
		Name:                          token.Name,
		Description:                   token.Description,
		TokenPrefix:                   token.TokenPrefix,
		UserSlug:                      token.UserSlug.String(),
		CanManagePersonalAccessTokens: token.CanManagePersonalAccessTokens,
		ExpiresAt:                     nullableTimestamp(token.ExpiresAt),
		RevokedAt:                     nullableTimestamp(token.RevokedAt),
		LastUsedAt:                    nullableTimestamp(token.LastUsedAt),
		CreatedAt:                     token.CreatedAt,
		UpdatedAt:                     token.UpdatedAt,
	}
}
