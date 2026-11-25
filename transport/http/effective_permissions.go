package http

import (
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

var (
	authorizationKeyPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,99}$`)
	authorizationDeliveryPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

func (s *server) GetEffectivePermissions(c fiber.Ctx, orgParam api.SlugParam, params api.GetEffectivePermissionsParams) error {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	org, err := slug.New(orgParam)
	if err != nil {
		return apierror.ValidationFailed("Invalid organization slug")
	}
	input := authorization.Input{OrgSlug: &org, Source: s.authenticatedSource(c)}
	if key, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		if key.OrgSlug != org {
			return c.JSON(fiber.Map{"permissions": []api.Permission{}})
		}
		input.APIKeySlug = key.Slug
		input.APIKeyRoleSlug = key.Role
	} else if user, ok := reqctx.GetUserPrincipal(c, s.sessionStore); ok {
		input.UserSlug = user.Slug
	} else {
		return apierror.Unauthorized()
	}

	input.ProjectDataSourceBindings = params.ProjectDatasourceBindings != nil && *params.ProjectDatasourceBindings
	if params.TargetKind != nil {
		input.Target.Kind = *params.TargetKind
	}
	for _, field := range []struct {
		raw   *string
		value *slug.Slug
	}{
		{params.ParentSlug, &input.Target.ParentSlug},
		{params.ProjectSlug, &input.Target.ProjectSlug},
		{params.EnvironmentSlug, &input.Target.EnvironmentSlug},
		{params.DatasourceSlug, &input.Target.DataSourceSlug},
	} {
		if field.raw == nil {
			continue
		}
		*field.value, err = slug.New(*field.raw)
		if err != nil {
			return apierror.ValidationFailed("Invalid authorization target slug")
		}
	}
	if params.TargetSlug != nil {
		value := *params.TargetSlug
		switch strings.TrimSuffix(input.Target.Kind, "_upsert") {
		case "environment_secrets", "environment_runtime_variables", "runtime_variable_definitions":
			if !authorizationKeyPattern.MatchString(value) {
				return apierror.ValidationFailed("Invalid runtime key")
			}
		case "webhook_deliveries":
			if !authorizationDeliveryPattern.MatchString(value) {
				return apierror.ValidationFailed("Invalid delivery identifier")
			}
		default:
			if _, err = slug.New(value); err != nil {
				return apierror.ValidationFailed("Invalid authorization target slug")
			}
		}
		input.Target.Slug = slug.Slug(value)
	}
	projectEnvironment := input.Target.Kind == "project_environments" && input.Target.ProjectSlug != "" && input.Target.EnvironmentSlug != ""
	if !projectEnvironment && ((input.Target.Kind == "") != (input.Target.Slug == "") || (input.Target.Kind == "" && input.Target != (model.AuthorizationTarget{}))) {
		return apierror.ValidationFailed("A target requires a kind and slug")
	}
	permissions, err := s.authorizationService.EffectivePermissions(c.Context(), input)
	if err != nil {
		return mapServiceError(err)
	}
	allowed := make([]api.Permission, 0, len(permissions))
	for _, permission := range permissions {
		allowed = append(allowed, permissionToHTTP(permission))
	}
	return c.JSON(fiber.Map{"permissions": allowed})
}
