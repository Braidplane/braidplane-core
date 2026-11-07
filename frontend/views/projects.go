package views

import (
	"context"
	"net/url"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func projectListHref(ctx context.Context, orgSlug, selected string) string {
	return entityListHref(ctx, projectRuntimeHref(ctx, organizationProjectSelectionHref(orgSlug, "")), "project_slug", selected)
}

func projectDetailHref(ctx context.Context, orgSlug, projectSlug string) string {
	return entityDetailHref(projectRuntimeHref(ctx, organizationProjectFullPageHref(orgSlug, projectSlug)), projectListHref(ctx, orgSlug, projectSlug))
}

func projectSection(ctx context.Context) string {
	switch section := entityPageURL(ctx).Query().Get("section"); section {
	case "pipelines", "repository", "environments", "settings":
		return section
	default:
		return "overview"
	}
}

func projectSectionHref(ctx context.Context, orgSlug, projectSlug, section string) string {
	return entitySectionHref(ctx, projectRuntimeHref(ctx, organizationProjectFullPageHref(orgSlug, projectSlug)), section)
}

func projectRepositoryPath(ctx context.Context) string {
	switch path := entityPageURL(ctx).Query().Get("repository_path"); path {
	case "existing", "create":
		return path
	default:
		return ""
	}
}

func projectRepositoryHref(ctx context.Context, orgSlug, projectSlug, path string) string {
	u, _ := url.Parse(projectSectionHref(ctx, orgSlug, projectSlug, "repository"))
	values := u.Query()
	if path != "" {
		values.Set("repository_path", path)
	}
	u.RawQuery = values.Encode()
	return u.String()
}

func projectSectionAction(ctx context.Context, action, section string) string {
	u, _ := url.Parse(action)
	values := u.Query()
	values.Set("section", section)
	if path := projectRepositoryPath(ctx); path != "" {
		values.Set("repository_path", path)
	}
	if listReturn := entityPageURL(ctx).Query().Get("list_return"); listReturn != "" {
		values.Set("list_return", listReturn)
	}
	u.RawQuery = values.Encode()
	return projectRuntimeHref(ctx, u.String())
}

func projectRepositoryAction(ctx context.Context, action string) string {
	return projectSectionAction(ctx, action, "repository")
}

func projectRuntimeTargetProblem(projectContext ProjectOperationalContext) string {
	if projectContext.EnvironmentsUnavailable {
		return "Project environment enablement is unavailable."
	}
	if len(projectContext.Environments) == 0 {
		return "No runtime environments enabled."
	}
	if ProjectDefaultEnvironmentLabel(projectContext.Environments) == "none" {
		return "Default runtime environment missing."
	}
	return ""
}

func projectEnvironmentProblem(projectContext ProjectOperationalContext) string {
	if problem := projectRuntimeTargetProblem(projectContext); problem != "" {
		return problem
	}
	if projectContext.PipelineDetailsUnavailable || projectContext.BindingsUnavailable {
		return "Logical binding readiness is unavailable."
	}
	if ProjectBindingAttentionCount(ProjectBindingRows(projectContext)) > 0 {
		return "Logical bindings need attention."
	}
	return ""
}

func projectRepositoryLabel(project api.Project) string {
	if project.PipelineBackend == nil {
		return "Git backend not attached"
	}
	if !projectRepositoryValidated(project) {
		return "Repository validation unavailable"
	}
	return "Repository validated at an exact commit"
}

func projectRepositoryValidated(project api.Project) bool {
	return project.PipelineBackend != nil && project.PipelineBackend.FirstCommitSha != nil && strings.TrimSpace(*project.PipelineBackend.FirstCommitSha) != ""
}

func projectRuntimeHref(ctx context.Context, href string) string {
	u, _ := url.Parse(href)
	values := u.Query()
	if target := entityPageURL(ctx).Query().Get("environment_slug"); target != "" {
		values.Set("environment_slug", target)
	}
	u.RawQuery = values.Encode()
	return u.String()
}

// ProjectDeletedListHref removes only the deleted selection from a scoped return location.
func ProjectDeletedListHref(ctx context.Context, orgSlug string) string {
	u, _ := url.Parse(entityBackHref(ctx, organizationProjectSelectionHref(orgSlug, "")))
	values := u.Query()
	values.Del("project_slug")
	u.RawQuery = values.Encode()
	return u.String()
}
