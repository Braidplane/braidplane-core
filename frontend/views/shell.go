package views

import (
	"net/url"
	"strings"
)

const (
	shellDashboardPath         = "/dashboard"
	shellBuilderPath           = "/builder"
	environmentSlugProd        = "prod"
	environmentSlugProduction  = "production"
	environmentSlugStaging     = "staging"
	environmentSlugStage       = "stage"
	environmentSlugStg         = "stg"
	environmentSlugDev         = "dev"
	environmentSlugDevelopment = "development"
)

// ShellNavItem represents a sidebar navigation link in the authenticated shell.
type ShellNavItem struct {
	Group    string
	Label    string
	Icon     string
	Href     string
	IsActive bool
	Enabled  bool
}

// ShellOrganizationOption is one organization option in the shell switcher.
type ShellOrganizationOption struct {
	Slug     string
	Name     string
	IsActive bool
}

// ShellProjectOption is one project option in the active workspace context.
type ShellProjectOption struct {
	Slug     string
	Name     string
	IsActive bool
}

// ShellEnvironmentOption is one project runtime target option in the shell switcher.
type ShellEnvironmentOption struct {
	Slug      string
	Name      string
	Color     string
	Protected bool
	Href      string
	IsActive  bool
	IsDefault bool
}

// ShellUserSummary holds minimal user context shown in the shell account affordance.
type ShellUserSummary struct {
	DisplayName string
	Email       string
}

// BreadcrumbItem represents one breadcrumb segment in the authenticated shell.
type BreadcrumbItem struct {
	Label string
	Href  string
}

// AuthenticatedShellData contains shared shell chrome data for authenticated pages.
type AuthenticatedShellData struct {
	NotificationReturnHref     string
	Title                      string
	Subtitle                   string
	ActiveOrganizationName     string
	CurrentPath                string
	ActiveOrganizationSlug     string
	Organizations              []ShellOrganizationOption
	ActiveProjectName          string
	ActiveProjectSlug          string
	Projects                   []ShellProjectOption
	ActiveEnvironmentName      string
	ActiveEnvironmentSlug      string
	ActiveEnvironmentColor     string
	ActiveEnvironmentProtected bool
	Environments               []ShellEnvironmentOption
	User                       ShellUserSummary
	NavItems                   []ShellNavItem
	Breadcrumbs                []BreadcrumbItem
	PageError                  string
	PageErrorDetails           string
	DocumentationURL           string
}

// ShellActiveRouteLabel returns the current route label shown in the shell topbar.
func ShellActiveRouteLabel(items []ShellNavItem, fallback string) string {
	for _, item := range items {
		if item.IsActive && strings.TrimSpace(item.Label) != "" {
			return item.Label
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return "Dashboard"
}

// ShellTopbarTitle returns the primary page title shown in the authenticated shell.
func ShellTopbarTitle(currentPath string, items []ShellNavItem, fallback string) string {
	if ShellIsCommandCenter(currentPath) {
		return "Overview"
	}
	if strings.TrimSpace(currentPath) == shellBuilderPath {
		return "Builder"
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return ShellActiveRouteLabel(items, fallback)
}

// ShellIsCommandCenter reports whether the shell is rendering the operational dashboard.
func ShellIsCommandCenter(currentPath string) bool {
	return strings.TrimSpace(currentPath) == shellDashboardPath
}

// ShellContentClass keeps the workbench aligned with other product pages.
func ShellContentClass(_ string) string {
	return "shell-content rounded-xl border border-base-300 bg-base-100 shadow-sm p-4 sm:p-5 lg:p-8"
}

// ShellTopbarControlsClass sizes context controls without letting long labels escape.
func ShellTopbarControlsClass(data AuthenticatedShellData) string {
	if ShellIsCommandCenter(data.CurrentPath) {
		return "min-w-0 w-full max-w-full sm:w-fit xl:justify-self-end"
	}
	return "flex min-w-0 max-w-full flex-wrap items-start gap-3 xl:justify-end"
}

// ShellSelectOptionLabel keeps long organization labels inside compact controls.
func ShellSelectOptionLabel(prefix, value string) string {
	label := strings.TrimSpace(value)
	if label == "" {
		label = strings.TrimSpace(prefix)
	} else if strings.TrimSpace(prefix) != "" {
		label = strings.TrimSpace(prefix) + " " + label
	}

	const maxRunes = 26
	runes := []rune(label)
	if len(runes) <= maxRunes {
		return label
	}
	return string(runes[:maxRunes-3]) + "..."
}

// ShellOrganizationDisplayName returns the active organization label shown in the topbar control.
func ShellOrganizationDisplayName(data AuthenticatedShellData) string {
	return ShellContextDisplay(data.ActiveOrganizationName, data.ActiveOrganizationSlug, "No organization")
}

// ShellOrganizationSettingsHref returns the organization detail route for the active tenant.
func ShellOrganizationSettingsHref(orgSlug string) string {
	trimmedOrgSlug := strings.TrimSpace(orgSlug)
	if trimmedOrgSlug == "" {
		return "/organizations"
	}
	return "/organizations/" + trimmedOrgSlug
}

// ShellOrganizationSwitchHref builds the CSRF-protected organization switch action URL.
func ShellOrganizationSwitchHref(orgSlug, returnTo string) string {
	trimmedOrgSlug := strings.TrimSpace(orgSlug)
	if trimmedOrgSlug == "" {
		return "/organizations"
	}

	trimmedReturnTo := strings.TrimSpace(returnTo)
	if trimmedReturnTo == "" {
		trimmedReturnTo = "/dashboard"
	}

	return "/organizations/" + trimmedOrgSlug + "/switch?return_to=" + url.QueryEscape(trimmedReturnTo)
}

// ShellContextDisplay returns the human label for an active org, project, or environment.
func ShellContextDisplay(name, slug, fallback string) string {
	if strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	if strings.TrimSpace(slug) != "" {
		return strings.TrimSpace(slug)
	}
	return fallback
}

// ShellRuntimeTargetDisplay returns the selector label for a project runtime target.
func ShellRuntimeTargetDisplay(name, slug string) string {
	return "Target: " + ShellContextDisplay(name, slug, "No runtime target")
}

// ShellContextInitial returns a compact initial for collapsed context markers.
func ShellContextInitial(name, slug, fallback string) string {
	value := ShellContextDisplay(name, slug, fallback)
	for _, letter := range value {
		return strings.ToUpper(string(letter))
	}
	return "X"
}

// ShellUserDisplay returns the primary label for the signed-in user affordance.
func ShellUserDisplay(user ShellUserSummary) string {
	if strings.TrimSpace(user.DisplayName) != "" {
		return strings.TrimSpace(user.DisplayName)
	}
	if strings.TrimSpace(user.Email) != "" {
		return strings.TrimSpace(user.Email)
	}
	return "Account"
}

// ShellUserInitial returns a compact avatar label for the signed-in user.
func ShellUserInitial(user ShellUserSummary) string {
	return ShellContextInitial(ShellUserDisplay(user), "", "Account")
}

// ShellEnvironmentBadgeClass returns the visual treatment for environment context badges.
func ShellEnvironmentBadgeClass(environmentColor string) string {
	return environmentColorOutlineClass(environmentColor) + " badge-sm font-mono uppercase"
}

// ShellEnvironmentRiskLabel returns concise operational copy for the selected environment.
func ShellEnvironmentRiskLabel(environmentSlug string, protected bool) string {
	if protected {
		return "Protected runtime"
	}
	switch strings.ToLower(strings.TrimSpace(environmentSlug)) {
	case environmentSlugProd, environmentSlugProduction:
		return "Protected runtime"
	case environmentSlugStg, environmentSlugStage, environmentSlugStaging:
		return "Pre-production target"
	case "":
		return "No runtime target selected"
	default:
		return "Runtime target"
	}
}

// ShellNavScopeRequirement names the context needed to reach a disabled destination.
func ShellNavScopeRequirement(data AuthenticatedShellData) string {
	if strings.TrimSpace(data.ActiveOrganizationSlug) == "" {
		return " requires an active organization"
	}
	return " requires an active project"
}
