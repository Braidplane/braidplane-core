package http

import (
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// Route parameters locate objects. The store verifies their relationships before
// granting access. Cross-resource actions pass their own target explicitly.
func routeAuthorizationTarget(c fiber.Ctx, resource model.Resource) model.AuthorizationTarget {
	project := slug.Slug(c.Params("project_slug"))
	env := slug.Slug(c.Params("environment_slug"))
	source := slug.Slug(c.Params("ds_slug"))
	target := model.AuthorizationTarget{ProjectSlug: project, EnvironmentSlug: env}
	object := func(kind, param string) model.AuthorizationTarget {
		target.Kind, target.Slug = kind, slug.Slug(c.Params(param))
		return target
	}
	empty := model.AuthorizationTarget{}
	path := c.Route().Path
	switch resource {
	case model.ResourceOrganizations:
		return empty
	case model.ResourceDataSources:
		if c.Params("data_source_ref_slug") != "" {
			target.ParentSlug = slug.Slug(c.Params("pipeline_slug"))
			kind := "data_source_refs"
			if env != "" {
				kind = "data_source_bindings"
				if c.Method() == fiber.MethodPut {
					kind += "_upsert"
				}
			}
			return object(kind, "data_source_ref_slug")
		}
		if source != "" {
			return object("datasources", "ds_slug")
		}
		if !isProjectBindingRoute(path) {
			// A project can reference a shared source without owning it.
			return empty
		}
		if c.Params("pipeline_slug") != "" {
			target.EnvironmentSlug = ""
			return object("pipelines", "pipeline_slug")
		}
	case model.ResourceQueries:
		if source == "" {
			return empty
		}
		if c.Params("query_slug") != "" {
			target.DataSourceSlug = source
			if c.Params("execution_slug") != "" {
				target.ParentSlug = slug.Slug(c.Params("query_slug"))
				return object("query_executions", "execution_slug")
			}
			return object("queries", "query_slug")
		}
		return object("datasources", "ds_slug")
	case model.ResourceProjects:
		if c.Params("contract_slug") != "" {
			return object("contracts", "contract_slug")
		}
		if c.Params("setup_slug") != "" {
			return object("project_repository_setups", "setup_slug")
		}
	case model.ResourcePipelines:
		if c.Params("pipeline_slug") != "" && env != "" {
			if strings.HasSuffix(path, "deploy") || strings.HasSuffix(path, "revalidate") || strings.HasSuffix(path, "/deployment") || strings.HasSuffix(path, "/execution-control") {
				return object("deployment_targets", "pipeline_slug")
			}
			return object("deployed_pipelines", "pipeline_slug")
		}
		for _, field := range []struct{ param, kind string }{
			{"run_slug", "runs"},
			{"pipeline_slug", "pipelines"},
			{"validation_slug", "pipeline_validation_results"},
			{"creation_slug", "pipeline_creations"},
			{"observation_slug", "proof_observations"},
			{"receipt_slug", "proof_receipts"},
			{"report_slug", "drift_impact_reports"},
		} {
			if c.Params(field.param) != "" {
				return object(field.kind, field.param)
			}
		}
	case model.ResourcePipelineTriggers:
		if c.Params("delivery_id") != "" {
			target.ParentSlug = slug.Slug(c.Params("pipeline_trigger_slug"))
			return object("webhook_deliveries", "delivery_id")
		}
		if c.Params("pipeline_trigger_slug") != "" {
			return object("pipeline_triggers", "pipeline_trigger_slug")
		}
	case model.ResourceEnvironments:
		key, kind := "runtime_variable_key", "runtime_variable_definitions"
		if env != "" {
			kind = "environment_runtime_variables"
		}
		if c.Params("environment_secret_key") != "" {
			key, kind = "environment_secret_key", "environment_secrets"
		}
		if c.Params(key) != "" {
			if c.Method() == fiber.MethodPut {
				kind += "_upsert"
			}
			return object(kind, key)
		}
		if env != "" && project == "" {
			return object("environments", "environment_slug")
		}
	case model.ResourceMembers:
		if c.Params("invitation_slug") != "" {
			return object("member_invitations", "invitation_slug")
		}
		if c.Params("user_slug") != "" {
			return object("members", "user_slug")
		}
	case model.ResourceAPIKeys:
		if c.Params("api_key_slug") != "" {
			return object("api_keys", "api_key_slug")
		}
	case model.ResourceRoles:
		if c.Params("role_slug") != "" {
			if strings.HasSuffix(path, "/restore") {
				return object("deleted_roles", "role_slug")
			}
			return object("roles", "role_slug")
		}
	case model.ResourceAlertPolicies:
		if c.Params("alert_policy_slug") != "" {
			return object("alert_policies", "alert_policy_slug")
		}
	case model.ResourceAlertDestinations:
		if c.Params("notification_connection_slug") != "" {
			return object("notification_connections", "notification_connection_slug")
		}
		if c.Params("alert_destination_slug") != "" {
			return object("alert_destinations", "alert_destination_slug")
		}
	case model.ResourceNotifications:
		if c.Params("notification_slug") != "" {
			return object("notifications", "notification_slug")
		}
	default:
		return empty
	}
	if project != "" && env != "" {
		target.Kind = "project_environments"
		return target
	}
	if project != "" {
		return object("projects", "project_slug")
	}
	return empty
}

func (s *server) canTarget(c fiber.Ctx, org slug.Slug, action model.Action, resource model.Resource, target model.AuthorizationTarget) error {
	return s.authorizeOrgTarget(c, org, action, resource, target, true)
}

func (s *server) canPipeline(c fiber.Ctx, org, project, env, pipeline slug.Slug, action model.Action) error {
	kind := "pipelines"
	if env != "" {
		kind = "deployed_pipelines"
	}
	return s.canTarget(c, org, action, model.ResourcePipelines, model.AuthorizationTarget{Kind: kind, Slug: pipeline, ProjectSlug: project, EnvironmentSlug: env})
}

func isProjectBindingRoute(path string) bool {
	return strings.Contains(path, "/data-source-refs") || strings.Contains(path, "/data-source-bindings")
}
