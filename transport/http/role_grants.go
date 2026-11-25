package http

import (
	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func (s *server) ListRoleGrants(c fiber.Ctx, slugParam api.SlugParam) error {
	org, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed("Invalid organization slug")
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceRoles); err != nil {
		return err
	}
	grants, err := s.rolesService.ListRoleGrants(c.Context(), org)
	if err != nil {
		return mapServiceError(err)
	}
	result := api.RoleGrantList{Grants: make([]api.RoleGrant, 0, len(grants))}
	for _, grant := range grants {
		result.Grants = append(result.Grants, roleGrantToHTTP(grant))
	}
	return c.JSON(result)
}

func (s *server) CreateRoleGrant(c fiber.Ctx, slugParam api.SlugParam) error {
	org, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed("Invalid organization slug")
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceRoles); err != nil {
		return err
	}
	var body api.NewRoleGrant
	if err := c.Bind().Body(&body); err != nil {
		return apierror.InvalidRequestBody()
	}
	grant, err := parseRoleGrant(org, body)
	if err != nil {
		return err
	}
	created, err := s.rolesService.CreateRoleGrant(c.Context(), org, s.eventActor(c), grant)
	if err != nil {
		return mapServiceError(err)
	}
	return c.Status(fiber.StatusCreated).JSON(roleGrantToHTTP(*created))
}

func (s *server) RevokeRoleGrant(c fiber.Ctx, slugParam api.SlugParam, grantParam api.Slug) error {
	org, grant, err := parseOrgAndResourceSlug(slugParam, grantParam, "grant")
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceRoles); err != nil {
		return err
	}
	if err := s.rolesService.RevokeRoleGrant(c.Context(), org, grant, s.eventActor(c)); err != nil {
		return mapServiceError(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func parseRoleGrant(org slug.Slug, body api.NewRoleGrant) (model.NewRoleGrant, error) {
	var grant model.NewRoleGrant
	if !body.SubjectKind.Valid() || !body.Scope.Valid() {
		return grant, apierror.ValidationFailed("Invalid subject kind or grant scope")
	}
	var err error
	grant.SubjectKind, grant.Scope = string(body.SubjectKind), string(body.Scope)
	if grant.SubjectSlug, err = slug.New(body.SubjectSlug); err != nil {
		return grant, apierror.ValidationFailed("Invalid subject slug")
	}
	if grant.RoleSlug, err = slug.New(body.RoleSlug); err != nil {
		return grant, apierror.ValidationFailed("Invalid role slug")
	}
	if body.Scope != "resource" && (body.ParentSlug != nil || body.ProjectSlug != nil || body.EnvironmentSlug != nil || body.DatasourceSlug != nil) {
		return grant, apierror.ValidationFailed("Target qualifiers apply only to individual resources")
	}
	kind := ""
	switch body.Scope {
	case "organization":
		kind = "organizations"
		if body.TargetSlug != nil && *body.TargetSlug != org.String() {
			return grant, apierror.ValidationFailed("Organization target must match the requested organization")
		}
		body.TargetSlug = new(org.String())
	case "project":
		kind = "projects"
	case "environment":
		kind = "environments"
	case "resource":
		if body.TargetKind == nil || *body.TargetKind == "" {
			return grant, apierror.ValidationFailed("Resource grant requires a target kind")
		}
		kind = *body.TargetKind
	}
	if body.TargetSlug == nil || (body.TargetKind != nil && *body.TargetKind != kind) {
		return grant, apierror.ValidationFailed("Supply a target of the requested scope")
	}
	grant.Target, err = parseAuthorizationTarget(api.GetEffectivePermissionsParams{
		TargetKind: &kind, TargetSlug: body.TargetSlug, ProjectSlug: body.ProjectSlug,
		EnvironmentSlug: body.EnvironmentSlug, DatasourceSlug: body.DatasourceSlug, ParentSlug: body.ParentSlug,
	})
	return grant, err
}

func roleGrantToHTTP(grant model.RoleGrant) api.RoleGrant {
	return api.RoleGrant{
		Slug: grant.Slug.String(), SubjectKind: api.RoleGrantSubjectKind(grant.SubjectKind), SubjectSlug: grant.SubjectSlug.String(),
		RoleSlug: grant.RoleSlug.String(), Scope: api.RoleGrantScope(grant.Scope),
		TargetKind: optionalString(grant.Target.Kind), TargetSlug: optionalString(grant.Target.Slug.String()),
		ProjectSlug: optionalString(grant.Target.ProjectSlug.String()), EnvironmentSlug: optionalString(grant.Target.EnvironmentSlug.String()),
		DatasourceSlug: optionalString(grant.Target.DataSourceSlug.String()), ParentSlug: optionalString(grant.Target.ParentSlug.String()),
	}
}
