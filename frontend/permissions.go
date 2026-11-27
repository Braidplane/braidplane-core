package frontend

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
)

func (r *Router) resolvePagePermissions(c fiber.Ctx, org string) views.OrganizationPermissions {
	params := api.GetEffectivePermissionsParams{}
	project, env, source := c.Params("projectSlug"), c.Params("environmentSlug"), c.Params("dsSlug")
	kind, id := "", ""
	switch {
	case c.Params("roleSlug") != "":
		kind, id = "roles", c.Params("roleSlug")
		if strings.HasSuffix(c.Route().Path, "/restore") {
			kind = "deleted_roles"
		}
	case c.Params("apiKeySlug") != "":
		kind, id = "api_keys", c.Params("apiKeySlug")
	case c.Params("userSlug") != "":
		kind, id = "members", c.Params("userSlug")
	case c.Params("invitationSlug") != "" && strings.Contains(c.Route().Path, "/member-invitations/"):
		kind, id = "member_invitations", c.Params("invitationSlug")
	case c.Params("destinationSlug") != "":
		kind, id = "alert_destinations", c.Params("destinationSlug")
	case c.Params("policySlug") != "":
		kind, id = "alert_policies", c.Params("policySlug")
	case c.Params("notification_connection_slug") != "":
		kind, id = "notification_connections", c.Params("notification_connection_slug")
	case c.Params("notificationSlug") != "":
		kind, id = "notifications", c.Params("notificationSlug")
	case c.Params("querySlug") != "":
		kind, id = "queries", c.Params("querySlug")
		params.DatasourceSlug = &source
	case source != "":
		kind, id = "datasources", source
	case c.Params("pipelineSlug") != "":
		kind, id = "pipelines", c.Params("pipelineSlug")
		if env != "" {
			kind = "deployed_pipelines"
			if strings.HasSuffix(c.Route().Path, "/execution-control") {
				kind = "deployment_targets"
			}
			params.EnvironmentSlug = &env
		}
	case c.Params("runSlug") != "":
		kind, id = "runs", c.Params("runSlug")
		env = requestRuntimeEnvironmentSlug(c)
		if env != "" {
			params.EnvironmentSlug = &env
		}
	case c.Params("triggerSlug") != "":
		kind, id = "pipeline_triggers", c.Params("triggerSlug")
		env = requestRuntimeEnvironmentSlug(c)
		if env != "" {
			params.EnvironmentSlug = &env
		}
	case c.Params("creationSlug") != "":
		kind, id = "pipeline_creations", c.Params("creationSlug")
	case project != "" && env != "":
		kind = "project_environments"
		params.EnvironmentSlug = &env
	case env != "":
		kind, id = "environments", env
	case project != "":
		kind, id = "projects", project
	}
	if project != "" {
		params.ProjectSlug = &project
	}
	if kind != "" {
		params.TargetKind = &kind
	}
	if id != "" {
		params.TargetSlug = &id
	}
	if strings.Contains(c.Route().Path, "/bindings") || strings.Contains(c.Route().Path, "/data-source-bindings") {
		enabled := true
		params.ProjectDatasourceBindings = &enabled
	}
	return r.targetPermissions(c, org, params)
}

func (r *Router) targetPermissions(c fiber.Ctx, org string, params api.GetEffectivePermissionsParams) views.OrganizationPermissions {
	return organizationPermissions(r.effectivePermissions(c.Context(), c.Get(fiber.HeaderCookie), org, &params))
}

// rowAllows checks the stored target only when the page-wide grant does not cover it.
func (r *Router) rowAllows(c fiber.Ctx, org, kind, target, resource, action string, page views.OrganizationPermissions) bool {
	if page.Can(resource, action) {
		return true
	}
	return r.targetPermissions(c, org, api.GetEffectivePermissionsParams{TargetKind: &kind, TargetSlug: &target}).Can(resource, action)
}

func (r *Router) resolveOrganizationPermissions(ctx context.Context, cookieHeader, org string) views.OrganizationPermissions {
	return organizationPermissions(r.effectivePermissions(ctx, cookieHeader, org, nil))
}

func (r *Router) effectivePermissions(ctx context.Context, cookieHeader, org string, params *api.GetEffectivePermissionsParams) []api.Permission {
	response, err := r.apiClient.GetEffectivePermissionsWithResponse(ctx, org, params, frontendRequestEditor(ctx, cookieHeader))
	if err != nil || response.JSON200 == nil {
		return nil
	}
	return response.JSON200.Permissions
}

func organizationPermissions(permissions []api.Permission) views.OrganizationPermissions {
	values := make([]string, 0, len(permissions))
	for _, permission := range permissions {
		resource := strings.TrimSpace(permission.Resource)
		action := strings.TrimSpace(string(permission.Action))
		if resource == "" || action == "" {
			continue
		}
		values = append(values, resource+":"+action)
	}

	return views.NewOrganizationPermissions(values...)
}
