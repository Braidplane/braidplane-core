package http

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/apikeyssvc"
	"github.com/Braidplane/braidplane-core/service/patssvc"
)

func NewBearerAuthMiddleware(
	apiKeysService interface {
		Authenticate(ctx context.Context, token string) (*model.APIKeyPrincipal, error)
	},
	patsService interface {
		Authenticate(ctx context.Context, token string) (*model.UserPrincipal, error)
	},
) fiber.Handler {
	return func(c fiber.Ctx) error {
		authHeader := strings.TrimSpace(c.Get(fiber.HeaderAuthorization))
		if authHeader == "" {
			return c.Next()
		}

		parts := strings.Fields(authHeader)
		if len(parts) == 0 || !strings.EqualFold(parts[0], "Bearer") {
			return c.Next()
		}
		if len(parts) != 2 {
			return apierror.Unauthorized()
		}
		token := parts[1]

		if strings.HasPrefix(strings.TrimSpace(token), patssvc.TokenPrefix) {
			principal, err := patsService.Authenticate(c.Context(), token)
			if err != nil {
				if !xerror.Is(err, patssvc.ErrInvalidToken) {
					return apierror.InternalServerError()
				}
			} else if principal != nil {
				reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{
					Slug:                          principal.UserSlug,
					Source:                        reqctx.UserPrincipalSourcePersonalAccessToken,
					PersonalAccessTokenSlug:       principal.PersonalAccessTokenSlug,
					CanManagePersonalAccessTokens: principal.CanManagePersonalAccessTokens,
				})
				return c.Next()
			}
		}

		principal, err := apiKeysService.Authenticate(c.Context(), token)
		if err != nil {
			if xerror.Is(err, apikeyssvc.ErrInvalidToken) {
				return apierror.Unauthorized()
			}
			return apierror.InternalServerError()
		}

		if principal == nil {
			return apierror.Unauthorized()
		}

		reqctx.SetAPIKeyPrincipal(c, reqctx.APIKeyPrincipal{
			Slug:             principal.Slug,
			OrgSlug:          principal.OrgSlug,
			Role:             principal.Role,
			CanManageAPIKeys: principal.CanManageAPIKeys,
		})
		return c.Next()
	}
}

func (s *server) ListAPIKeys(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListAPIKeysParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrgAPIKeyManager(c, orgSlug, model.Read); err != nil {
		return err
	}

	var roleFilter *slug.Slug
	if principal, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		roleFilter = &principal.Role
	}
	response, err := s.apiKeysService.ListAPIKeys(
		c.Context(),
		orgSlug,
		roleFilter,
		toAPIKeysListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	apiKeys := make([]api.APIKey, 0, len(response.Items))
	for _, key := range response.Items {
		apiKeys = append(apiKeys, apiKeyToHTTP(key))
	}

	return c.JSON(api.APIKeyListResponse{
		ApiKeys:    apiKeys,
		HasMore:    response.HasMore,
		NextCursor: response.NextCursor,
		PrevCursor: response.PrevCursor,
	})
}

func (s *server) CreateAPIKey(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrgAPIKeyManager(c, orgSlug, model.Create); err != nil {
		return err
	}

	var req api.NewAPIKey
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	newAPIKey, err := newAPIKeyFromHTTP(req)
	if err != nil {
		return err
	}

	if principal, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		if req.CanManageApiKeys != nil || newAPIKey.Role != principal.Role {
			return apierror.Forbidden(model.ResourceAPIKeys.String(), model.Create.String())
		}
		newAPIKey.CanManageAPIKeys = false
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "credential.mutation", true); err != nil {
		return err
	}

	key, err := s.apiKeysService.CreateAPIKey(
		c.Context(),
		orgSlug,
		s.eventActor(c),
		newAPIKey,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusCreated).JSON(apiKeyWithTokenToHTTP(*key))
}

func (s *server) GetAPIKey(
	c fiber.Ctx,
	slugParam api.SlugParam,
	apiKeySlugParam api.APIKeySlugParam,
) error {
	orgSlug, apiKeySlug, err := apiKeyPathSlugs(slugParam, apiKeySlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrgAPIKeyManager(c, orgSlug, model.Read); err != nil {
		return err
	}

	key, err := s.apiKeysService.GetAPIKey(c.Context(), orgSlug, apiKeySlug)
	if err != nil {
		return mapServiceError(err)
	}
	if err := canManageAPIKey(c, *key, model.Read); err != nil {
		return err
	}

	return c.JSON(apiKeyToHTTP(*key))
}

func (s *server) UpdateAPIKey(
	c fiber.Ctx,
	slugParam api.SlugParam,
	apiKeySlugParam api.APIKeySlugParam,
) error {
	orgSlug, apiKeySlug, err := apiKeyPathSlugs(slugParam, apiKeySlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrgAPIKeyManager(c, orgSlug, model.Update); err != nil {
		return err
	}
	if err := s.canManageAPIKeyTarget(c, orgSlug, apiKeySlug, model.Update); err != nil {
		return err
	}

	var req api.UpdateAPIKey
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	if _, ok := reqctx.GetAPIKeyPrincipal(c); ok && req.CanManageApiKeys != nil {
		return apierror.Forbidden(model.ResourceAPIKeys.String(), model.Update.String())
	}

	key, err := s.apiKeysService.UpdateAPIKey(
		c.Context(),
		orgSlug,
		apiKeySlug,
		s.eventActor(c),
		model.UpdateAPIKey{
			Name:             req.Name,
			Description:      req.Description,
			ExpiresAt:        req.ExpiresAt,
			CanManageAPIKeys: req.CanManageApiKeys,
		},
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(apiKeyToHTTP(*key))
}

func (s *server) DeleteAPIKey(
	c fiber.Ctx,
	slugParam api.SlugParam,
	apiKeySlugParam api.APIKeySlugParam,
) error {
	orgSlug, apiKeySlug, err := apiKeyPathSlugs(slugParam, apiKeySlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrgAPIKeyManager(c, orgSlug, model.Delete); err != nil {
		return err
	}
	if err := s.canManageAPIKeyTarget(c, orgSlug, apiKeySlug, model.Delete); err != nil {
		return err
	}

	if err := s.apiKeysService.RevokeAPIKey(
		c.Context(),
		orgSlug,
		apiKeySlug,
		s.eventActor(c),
	); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// DeleteRevokedAPIKey permanently deletes revoked API key metadata for an organization.
func (s *server) DeleteRevokedAPIKey(
	c fiber.Ctx,
	slugParam api.SlugParam,
	apiKeySlugParam api.APIKeySlugParam,
) error {
	orgSlug, apiKeySlug, err := apiKeyPathSlugs(slugParam, apiKeySlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrgUserSession(c, orgSlug, model.Delete, model.ResourceAPIKeys); err != nil {
		return err
	}

	if err := s.apiKeysService.DeleteRevokedAPIKey(
		c.Context(),
		orgSlug,
		apiKeySlug,
		s.eventActor(c),
	); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) RotateAPIKey(
	c fiber.Ctx,
	slugParam api.SlugParam,
	apiKeySlugParam api.APIKeySlugParam,
) error {
	orgSlug, apiKeySlug, err := apiKeyPathSlugs(slugParam, apiKeySlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrgAPIKeyManager(c, orgSlug, model.Update); err != nil {
		return err
	}
	if err := s.canManageAPIKeyTarget(c, orgSlug, apiKeySlug, model.Update); err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "credential.mutation", true); err != nil {
		return err
	}

	key, err := s.apiKeysService.RotateAPIKey(
		c.Context(),
		orgSlug,
		apiKeySlug,
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(apiKeyWithTokenToHTTP(*key))
}

func newAPIKeyFromHTTP(req api.NewAPIKey) (model.NewAPIKey, error) {
	roleSlug, err := slug.New(req.Role)
	if err != nil {
		return model.NewAPIKey{}, apierror.ValidationFailed(fmt.Sprintf("Invalid role slug: %v", err))
	}

	var apiKeySlug slug.Slug
	if req.Slug != nil {
		apiKeySlug, err = slug.New(*req.Slug)
		if err != nil {
			return model.NewAPIKey{}, apierror.ValidationFailed(fmt.Sprintf("Invalid API key slug: %v", err))
		}
	}

	return model.NewAPIKey{
		Name:             req.Name,
		Slug:             apiKeySlug,
		Description:      req.Description,
		Role:             roleSlug,
		ExpiresAt:        req.ExpiresAt,
		CanManageAPIKeys: req.CanManageApiKeys != nil && *req.CanManageApiKeys,
	}, nil
}

func apiKeyPathSlugs(
	slugParam api.SlugParam,
	apiKeySlugParam api.APIKeySlugParam,
) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	apiKeySlug, err := slug.New(apiKeySlugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid API key slug: %v", err))
	}

	return orgSlug, apiKeySlug, nil
}

func apiKeyWithTokenToHTTP(key model.APIKeyWithToken) api.APIKeyWithToken {
	base := apiKeyToHTTP(key.APIKey)
	return api.APIKeyWithToken{
		Slug:              base.Slug,
		Name:              base.Name,
		Description:       base.Description,
		TokenPrefix:       base.TokenPrefix,
		Role:              base.Role,
		CanManageApiKeys:  base.CanManageApiKeys,
		CreatedByUserSlug: base.CreatedByUserSlug,
		ExpiresAt:         base.ExpiresAt,
		RevokedAt:         base.RevokedAt,
		LastUsedAt:        base.LastUsedAt,
		CreatedAt:         base.CreatedAt,
		UpdatedAt:         base.UpdatedAt,
		Token:             key.Token,
	}
}

func apiKeyToHTTP(key model.APIKey) api.APIKey {
	return api.APIKey{
		Slug:              api.Slug(key.Slug),
		Name:              key.Name,
		Description:       key.Description,
		TokenPrefix:       key.TokenPrefix,
		Role:              api.MemberRole(key.Role),
		CanManageApiKeys:  key.CanManageAPIKeys,
		CreatedByUserSlug: nullableSlug(key.CreatedByUserSlug),
		ExpiresAt:         nullableTimestamp(key.ExpiresAt),
		RevokedAt:         nullableTimestamp(key.RevokedAt),
		LastUsedAt:        nullableTimestamp(key.LastUsedAt),
		CreatedAt:         key.CreatedAt,
		UpdatedAt:         key.UpdatedAt,
	}
}

// canManageAPIKeyTarget prevents a management-enabled API key from managing a
// key with a different role and obtaining or disrupting a more privileged principal.
func (s *server) canManageAPIKeyTarget(
	c fiber.Ctx,
	orgSlug, apiKeySlug slug.Slug,
	action model.Action,
) error {
	_, ok := reqctx.GetAPIKeyPrincipal(c)
	if !ok {
		return nil
	}

	target, err := s.apiKeysService.GetAPIKey(c.Context(), orgSlug, apiKeySlug)
	if err != nil {
		return mapServiceError(err)
	}
	return canManageAPIKey(c, *target, action)
}

func canManageAPIKey(c fiber.Ctx, target model.APIKey, action model.Action) error {
	principal, ok := reqctx.GetAPIKeyPrincipal(c)
	if ok && target.Role != principal.Role {
		return apierror.Forbidden(model.ResourceAPIKeys.String(), action.String())
	}
	return nil
}

func nullableSlug(value *slug.Slug) *api.NullableSlug {
	if value == nil {
		return nil
	}
	s := api.NullableSlug(*value)
	return &s
}

func nullableTimestamp(value *time.Time) *api.NullableTimestamp {
	if value == nil {
		return nil
	}
	ts := *value
	return &ts
}
