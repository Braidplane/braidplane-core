package frontend

import (
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
)

type environmentSwitcherState struct {
	ActiveSlug       string
	ActiveName       string
	ActiveColor      string
	ActiveProtected  bool
	DashboardOptions []views.DashboardScopeEnvironmentOption
	ShellOptions     []views.ShellEnvironmentOption
}

// buildEnvironmentSwitcherState selects the active runtime target and builds matching dashboard and shell options.
func buildEnvironmentSwitcherState(
	projectEnvironments []api.ProjectEnvironment,
	requestedEnvironmentSlug string,
	hrefForEnvironment func(string) string,
) environmentSwitcherState {
	requestedEnvironmentSlug = strings.TrimSpace(requestedEnvironmentSlug)
	activeEnvironmentSlug := ""
	if projectEnvironmentSlugIsEnabled(projectEnvironments, requestedEnvironmentSlug) {
		activeEnvironmentSlug = requestedEnvironmentSlug
	}
	if activeEnvironmentSlug == "" {
		activeEnvironmentSlug = selectDefaultProjectEnvironmentSlug(projectEnvironments)
	}

	state := environmentSwitcherState{
		ActiveSlug:       activeEnvironmentSlug,
		ActiveName:       activeEnvironmentSlug,
		DashboardOptions: make([]views.DashboardScopeEnvironmentOption, 0, len(projectEnvironments)),
		ShellOptions:     make([]views.ShellEnvironmentOption, 0, len(projectEnvironments)),
	}
	for _, environment := range projectEnvironments {
		environmentSlug := environment.EnvironmentSlug
		href := ""
		if hrefForEnvironment != nil {
			href = hrefForEnvironment(environmentSlug)
		}
		if environmentSlug == activeEnvironmentSlug {
			state.ActiveName = environment.EnvironmentName
			state.ActiveColor = string(environment.EnvironmentColor)
			state.ActiveProtected = environment.EnvironmentProtected
		}
		option := views.DashboardScopeEnvironmentOption{
			Slug:      environmentSlug,
			Name:      environment.EnvironmentName,
			Color:     string(environment.EnvironmentColor),
			Protected: environment.EnvironmentProtected,
			Href:      href,
			IsActive:  environmentSlug == activeEnvironmentSlug,
			IsDefault: environment.IsDefault,
		}
		state.DashboardOptions = append(state.DashboardOptions, option)
		state.ShellOptions = append(state.ShellOptions, views.ShellEnvironmentOption(option))
	}
	return state
}
