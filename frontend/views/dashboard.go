package views

import (
	"net/url"
	"strconv"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

const (
	dashboardStatusSuccess = "success"
	dashboardStatusWarning = "warning"
	dashboardStatusError   = "error"
	dashboardStatusInfo    = "info"
	dashboardStatusNeutral = "neutral"
)

// DashboardData is the server-rendered contract for the command-center aggregate surface.
type DashboardData struct {
	Onboarding          DashboardOnboarding
	Permissions         OrganizationPermissions
	ProjectScope        DashboardProjectScope
	IncidentQueue       DashboardRunQueue
	RecentRuns          DashboardRecentRuns
	PipelineStatus      DashboardPipelineStatus
	DataSourceReadiness DashboardDataSourceReadiness
	QueryInventory      DashboardQueryInventory
	ContractChecks      DashboardContractChecks
}

// DashboardProjectScope is the dashboard-local project and runtime target selection.
type DashboardProjectScope struct {
	ActiveProjectSlug          string
	ActiveProjectName          string
	ActiveEnvironmentSlug      string
	ActiveEnvironmentName      string
	ActiveEnvironmentColor     string
	ActiveEnvironmentProtected bool
	Projects                   []DashboardScopeProjectOption
	Environments               []DashboardScopeEnvironmentOption
	ProjectsUnavailable        bool
	EnvironmentsUnavailable    bool
	Message                    string
	EnvironmentMessage         string
	HasPipelineBackend         bool
}

// DashboardOnboarding derives the fresh-workspace checklist from existing product state.
type DashboardOnboarding struct {
	HasProject               bool
	HasSuccessfulConnection  bool
	HasSuccessfulResult      bool
	FirstResultUnavailable   bool
	HasSuccessfulPipelineRun bool
	ProjectsHref             string
	ProjectSetupHref         string
	DataSourcesHref          string
	QueriesHref              string
	PipelinesHref            string
	TutorialHref             string
}

// DashboardOnboardingStep is one permission-aware next step.
type DashboardOnboardingStep struct {
	Label           string
	Description     string
	Action          string
	CompletedAction string
	Href            string
	Done            bool
	Current         bool
}

// DashboardOnboardingSteps returns the fixed setup sequence without storing separate progress.
func DashboardOnboardingSteps(data DashboardData) []DashboardOnboardingStep {
	projectAction, projectHref := "Open projects", data.Onboarding.ProjectsHref
	projectAllowed := data.Permissions.Can("projects", "create")
	if data.Onboarding.HasProject {
		projectAction, projectHref = "Set up repository", data.Onboarding.ProjectSetupHref
		projectAllowed = data.Permissions.Can("projects", "update")
	}
	steps := []DashboardOnboardingStep{
		{Label: "Connect a system", Description: "Save and test a connection.", Action: "Open data sources", Href: data.Onboarding.DataSourcesHref, Done: data.Onboarding.HasSuccessfulConnection},
		{Label: "Get a result", Description: "Save and run a safe read. No project or Git setup is needed.", Action: "Open queries", CompletedAction: "View query", Href: data.Onboarding.QueriesHref, Done: data.Onboarding.HasSuccessfulResult},
		{Label: "Prepare a project", Description: "Choose a project and connect its pipeline repository.", Action: projectAction, Href: projectHref, Done: data.Onboarding.HasProject && data.ProjectScope.HasPipelineBackend},
		{Label: "Run the first pipeline", Description: "Sync a workflow from Git, deploy it and inspect a successful run.", Action: "Open pipelines", Href: data.Onboarding.PipelinesHref, Done: data.Onboarding.HasSuccessfulPipelineRun},
	}
	allowed := []bool{
		data.Permissions.Can("datasources", "create"),
		data.Permissions.Can("queries", "read") && data.Permissions.Can("queries", "execute") && data.Onboarding.HasSuccessfulConnection,
		projectAllowed,
		data.Permissions.Can("pipelines", "execute") && data.ProjectScope.HasPipelineBackend,
	}
	currentSet := false
	for i := range steps {
		if !steps[i].Done && !allowed[i] {
			steps[i].Href = ""
			steps[i].Action = "Ask a workspace admin"
		}
		if !currentSet && !steps[i].Done && allowed[i] {
			steps[i].Current = true
			currentSet = true
		}
	}
	return steps
}

// DashboardOnboardingComplete requires a successful workflow in the selected runtime target.
func DashboardOnboardingComplete(data DashboardData) bool {
	return data.Onboarding.HasProject && data.Onboarding.HasSuccessfulConnection && data.Onboarding.HasSuccessfulResult && data.ProjectScope.HasPipelineBackend && data.Onboarding.HasSuccessfulPipelineRun
}

// DashboardScopeProjectOption is one project choice for project-backed dashboard panels.
type DashboardScopeProjectOption struct {
	Slug     string
	Name     string
	Href     string
	IsActive bool
}

// DashboardScopeEnvironmentOption is one runtime target choice for the selected project scope.
type DashboardScopeEnvironmentOption struct {
	Slug      string
	Name      string
	Color     string
	Protected bool
	Href      string
	IsActive  bool
	IsDefault bool
}

// DashboardMetric is a compact real value displayed inside one dashboard panel.
type DashboardMetric struct {
	Label  string
	Value  string
	Detail string
	Kind   string
	Href   string
}

// DashboardRunSummary is the dashboard-sized projection of pipeline run metadata.
type DashboardRunSummary struct {
	RunSlug       string
	PipelineSlug  string
	Status        api.PipelineRunMetadataStatus
	StartedAt     api.Timestamp
	DurationMs    int64
	ErrorSummary  string
	FailureKind   string
	SourceSummary string
	Href          string
	PipelineHref  string
}

// DashboardRunQueue summarizes failed runs that need operator attention.
type DashboardRunQueue struct {
	Unavailable bool
	Message     string
	Runs        []DashboardRunSummary
	HasMore     bool
	Href        string
}

// DashboardRecentRuns summarizes the latest loaded run window for the target runtime.
type DashboardRecentRuns struct {
	Runs        []DashboardRunSummary
	Unavailable bool
	Message     string
	WindowLabel string
	Metrics     []DashboardMetric
	HasMore     bool
	Href        string
}

// DashboardPipelineStatus summarizes project pipeline deployment state for the selected runtime target.
type DashboardPipelineStatus struct {
	Unavailable            bool
	Message                string
	DeploymentsUnavailable bool
	Metrics                []DashboardMetric
	Href                   string
}

// DashboardDataSourceReadiness summarizes organization-scoped connector readiness.
type DashboardDataSourceReadiness struct {
	Unavailable      bool
	Message          string
	Metrics          []DashboardMetric
	Issues           []DashboardInventoryIssue
	HiddenIssueCount int
	Href             string
}

// DashboardQueryInventory summarizes saved query coverage over the current organization data sources.
type DashboardQueryInventory struct {
	Unavailable bool
	Message     string
	Metrics     []DashboardMetric
	Href        string
}

// DashboardContractChecks summarizes target environment binding coverage for aggregate project ref views.
type DashboardContractChecks struct {
	Unavailable      bool
	Message          string
	Metrics          []DashboardMetric
	Issues           []DashboardInventoryIssue
	HiddenIssueCount int
	Href             string
}

// DashboardInventoryIssue is a short linked issue row for inventory and contract panels.
type DashboardInventoryIssue struct {
	Label  string
	Detail string
	Kind   string
	Href   string
}

// DashboardPipelinesHref returns the active project pipeline route when context exists.
func DashboardPipelinesHref(shellData AuthenticatedShellData) string {
	base := dashboardProjectRuntimeHref(shellData, "pipelines")
	if base == "" {
		return shellDashboardPath
	}
	return base
}

// DashboardPipelinesFilterHref returns the pipeline catalog narrowed to one supported filter.
func DashboardPipelinesFilterHref(shellData AuthenticatedShellData, key, value string) string {
	base := DashboardPipelinesHref(shellData)
	if base == shellDashboardPath || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
		return base
	}
	return dashboardAppendQuery(base, strings.TrimSpace(key), strings.TrimSpace(value))
}

// DashboardRunsHref returns the active project run route when context exists.
func DashboardRunsHref(shellData AuthenticatedShellData) string {
	base := dashboardProjectRuntimeHref(shellData, "runs")
	if base == "" {
		return shellDashboardPath
	}
	return base
}

// DashboardRunsStatusHref returns the run list narrowed to one supported status.
func DashboardRunsStatusHref(shellData AuthenticatedShellData, status string) string {
	base := DashboardRunsHref(shellData)
	if base == shellDashboardPath || strings.TrimSpace(status) == "" {
		return base
	}
	return dashboardAppendQuery(base, "status", strings.TrimSpace(status))
}

// DashboardFailedRunsHref returns the active project run route filtered to failed runs.
func DashboardFailedRunsHref(shellData AuthenticatedShellData) string {
	return DashboardRunsStatusHref(shellData, string(api.PipelineRunMetadataStatusFailed))
}

// DashboardEnvironmentsHref returns the active project environment route when context exists.
func DashboardEnvironmentsHref(shellData AuthenticatedShellData) string {
	base := dashboardProjectRuntimeHref(shellData, "environments")
	if base == "" {
		return shellDashboardPath
	}
	return base
}

// DashboardEnvironmentBindingMatrixHref returns the environment route focused on bindings.
func DashboardEnvironmentBindingMatrixHref(shellData AuthenticatedShellData) string {
	return dashboardAppendFragment(dashboardAppendQuery(DashboardEnvironmentsHref(shellData), "section", "configuration"), "project-binding-matrix")
}

// DashboardDataSourcesHref returns the active organization data source route when context exists.
func DashboardDataSourcesHref(shellData AuthenticatedShellData) string {
	if strings.TrimSpace(shellData.ActiveOrganizationSlug) == "" {
		return shellDashboardPath
	}
	return "/orgs/" + shellData.ActiveOrganizationSlug + "/data-sources"
}

// DashboardDataSourcesFilterHref returns the data-source inventory narrowed to one filter.
func DashboardDataSourcesFilterHref(shellData AuthenticatedShellData, filter string) string {
	base := DashboardDataSourcesHref(shellData)
	filter = NormalizeDataSourceListFilter(filter)
	if base == shellDashboardPath || filter == "" {
		return base
	}
	return dashboardAppendQuery(base, "filter", filter)
}

// DashboardQueriesHref returns the active organization query route when context exists.
func DashboardQueriesHref(shellData AuthenticatedShellData) string {
	if strings.TrimSpace(shellData.ActiveOrganizationSlug) == "" {
		return shellDashboardPath
	}
	return "/orgs/" + shellData.ActiveOrganizationSlug + "/queries"
}

// DashboardProjectsHref returns the active organization project route when context exists.
func DashboardProjectsHref(shellData AuthenticatedShellData) string {
	if strings.TrimSpace(shellData.ActiveOrganizationSlug) == "" {
		return shellDashboardPath
	}
	return "/organizations/" + shellData.ActiveOrganizationSlug + "/projects"
}

// DashboardProjectSetupHref keeps the active project selected on the project settings page.
func DashboardProjectSetupHref(shellData AuthenticatedShellData) string {
	return dashboardAppendQuery(DashboardProjectsHref(shellData), "project_slug", strings.TrimSpace(shellData.ActiveProjectSlug))
}

// DashboardFirstResultTutorialHref returns the configured outcome-led introduction.
func DashboardFirstResultTutorialHref(shellData AuthenticatedShellData) string {
	documentationURL := strings.TrimSpace(shellData.DocumentationURL)
	if documentationURL == "" {
		return ""
	}
	return strings.TrimRight(documentationURL, "/") + "/getting-started/first-result/"
}

// DashboardScopeHref returns the command-center route for one local project/runtime scope.
func DashboardScopeHref(projectSlug, environmentSlug string) string {
	projectSlug = strings.TrimSpace(projectSlug)
	if projectSlug == "" {
		return shellDashboardPath
	}

	values := url.Values{}
	values.Set("project_slug", projectSlug)
	if strings.TrimSpace(environmentSlug) != "" {
		values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	}
	return shellDashboardPath + "?" + values.Encode()
}

// DashboardHiddenIssueLabel describes overflow issue rows suppressed by a dashboard panel limit.
func DashboardHiddenIssueLabel(count int) string {
	if count <= 0 {
		return ""
	}
	if count == 1 {
		return "1 more issue is available on the owning page."
	}
	return strconv.Itoa(count) + " more issues are available on the owning page."
}

// DashboardHasActiveOrganization reports whether dashboard organization actions have usable context.
func DashboardHasActiveOrganization(shellData AuthenticatedShellData) bool {
	return strings.TrimSpace(shellData.ActiveOrganizationSlug) != ""
}

// DashboardScopeHasActiveProject reports whether project-backed panels have a local project scope.
func DashboardScopeHasActiveProject(scope DashboardProjectScope) bool {
	return strings.TrimSpace(scope.ActiveProjectSlug) != ""
}

// DashboardScopeHasActiveEnvironment reports whether the local project scope has a runtime target.
func DashboardScopeHasActiveEnvironment(scope DashboardProjectScope) bool {
	return strings.TrimSpace(scope.ActiveEnvironmentSlug) != ""
}

// DashboardRunDetailHref returns a run detail route preserving active runtime context.
func DashboardRunDetailHref(shellData AuthenticatedShellData, runSlug string) string {
	if strings.TrimSpace(shellData.ActiveOrganizationSlug) == "" ||
		strings.TrimSpace(shellData.ActiveProjectSlug) == "" ||
		strings.TrimSpace(runSlug) == "" {
		return shellDashboardPath
	}

	href := "/orgs/" + shellData.ActiveOrganizationSlug +
		"/projects/" + shellData.ActiveProjectSlug +
		"/runs/" + strings.TrimSpace(runSlug)
	if strings.TrimSpace(shellData.ActiveEnvironmentSlug) != "" {
		href = dashboardAppendQuery(href, "environment_slug", shellData.ActiveEnvironmentSlug)
	}
	return href
}

// DashboardPipelineDetailHref returns a pipeline detail route preserving active runtime context.
func DashboardPipelineDetailHref(shellData AuthenticatedShellData, pipelineSlug string) string {
	if strings.TrimSpace(shellData.ActiveOrganizationSlug) == "" ||
		strings.TrimSpace(shellData.ActiveProjectSlug) == "" ||
		strings.TrimSpace(pipelineSlug) == "" {
		return shellDashboardPath
	}

	href := "/orgs/" + shellData.ActiveOrganizationSlug +
		"/projects/" + shellData.ActiveProjectSlug +
		"/pipelines/" + strings.TrimSpace(pipelineSlug)
	if strings.TrimSpace(shellData.ActiveEnvironmentSlug) != "" {
		href = dashboardAppendQuery(href, "environment_slug", shellData.ActiveEnvironmentSlug)
	}
	return href
}

// DashboardDataSourceDetailHref returns a data-source detail route when organization context exists.
func DashboardDataSourceDetailHref(shellData AuthenticatedShellData, dataSourceSlug string) string {
	if strings.TrimSpace(shellData.ActiveOrganizationSlug) == "" || strings.TrimSpace(dataSourceSlug) == "" {
		return shellDashboardPath
	}
	return "/orgs/" + shellData.ActiveOrganizationSlug + "/data-sources/" + strings.TrimSpace(dataSourceSlug)
}

// DashboardQueriesForDataSourceHref returns the query library filtered to one data source.
func DashboardQueriesForDataSourceHref(shellData AuthenticatedShellData, dataSourceSlug string) string {
	base := DashboardQueriesHref(shellData)
	if base == shellDashboardPath || strings.TrimSpace(dataSourceSlug) == "" {
		return base
	}
	return dashboardAppendQuery(base, "ds", strings.TrimSpace(dataSourceSlug))
}

// DashboardStatusBadgeClass maps dashboard status kinds to compact badge classes.
func DashboardStatusBadgeClass(kind string) string {
	switch strings.TrimSpace(kind) {
	case dashboardStatusSuccess:
		return badgeSuccessSoft
	case dashboardStatusWarning:
		return badgeWarningSoft
	case dashboardStatusError:
		return badgeErrorSoft
	case dashboardStatusInfo:
		return badgeInfoSoft
	default:
		return badgeGhostSoft
	}
}

// DashboardRunSourceSummary returns safe source copy for a run when source metadata exists.
func DashboardRunSourceSummary(source *api.PipelineRunSourceSummary) string {
	return runSourceLabel(apiRunSourceView(source))
}

// DashboardRunStatusBadgeClass returns the existing run badge treatment for dashboard rows.
func DashboardRunStatusBadgeClass(status api.PipelineRunMetadataStatus) string {
	return runStatusBadgeClass(status)
}

// DashboardMetricCardClass returns the interactive or static card treatment for a metric.
func DashboardMetricCardClass(metric DashboardMetric) string {
	base := "min-w-0 rounded-lg border border-base-300 bg-base-200/35 p-3"
	if strings.TrimSpace(metric.Href) == "" {
		return base
	}
	return base + " transition-colors hover:border-primary/40 hover:bg-base-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
}

// DashboardMetricAriaLabel provides a compact accessible label for linked metric cards.
func DashboardMetricAriaLabel(metric DashboardMetric) string {
	label := strings.TrimSpace(metric.Label)
	value := strings.TrimSpace(metric.Value)
	if value == "" {
		return label
	}
	if label == "" {
		return value
	}
	return label + ": " + value
}

// dashboardProjectRuntimeHref assembles project-scoped dashboard links with runtime context.
func dashboardProjectRuntimeHref(shellData AuthenticatedShellData, leaf string) string {
	if strings.TrimSpace(shellData.ActiveOrganizationSlug) == "" ||
		strings.TrimSpace(shellData.ActiveProjectSlug) == "" ||
		strings.TrimSpace(leaf) == "" {
		return ""
	}

	href := "/orgs/" + shellData.ActiveOrganizationSlug + "/projects/" + shellData.ActiveProjectSlug + "/" + leaf
	if strings.TrimSpace(shellData.ActiveEnvironmentSlug) != "" {
		href += "?environment_slug=" + shellData.ActiveEnvironmentSlug
	}
	return href
}

// dashboardAppendQuery adds one URL-encoded query parameter to an internal dashboard link.
func dashboardAppendQuery(href, key, value string) string {
	href = strings.TrimSpace(href)
	key = strings.TrimSpace(key)
	if href == "" || key == "" {
		return href
	}

	parsed, err := url.Parse(href)
	if err != nil || parsed.IsAbs() {
		return href
	}

	queryValues, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return href
	}

	queryValues.Set(key, strings.TrimSpace(value))
	parsed.RawQuery = queryValues.Encode()
	return parsed.String()
}

// dashboardAppendFragment adds a document fragment to an internal dashboard link.
func dashboardAppendFragment(href, fragment string) string {
	parsed, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return strings.TrimSpace(href)
	}
	parsed.Fragment = strings.TrimPrefix(strings.TrimSpace(fragment), "#")
	return parsed.String()
}

// DashboardHasRecordedAttention prevents unknown or incomplete checks becoming a success claim.
func DashboardHasRecordedAttention(data DashboardData) bool {
	if data.ProjectScope.ProjectsUnavailable || data.DataSourceReadiness.Unavailable || data.DataSourceReadiness.Message != "" || len(data.DataSourceReadiness.Metrics) == 0 || len(data.DataSourceReadiness.Issues) > 0 || data.DataSourceReadiness.HiddenIssueCount > 0 {
		return true
	}
	if !DashboardScopeHasActiveProject(data.ProjectScope) {
		return true
	}
	if !data.ProjectScope.HasPipelineBackend || data.PipelineStatus.Message != "" || !DashboardScopeHasActiveEnvironment(data.ProjectScope) || len(data.ContractChecks.Metrics) == 0 || len(data.PipelineStatus.Metrics) == 0 || data.ContractChecks.Unavailable || data.PipelineStatus.Unavailable || data.PipelineStatus.DeploymentsUnavailable || len(data.ContractChecks.Issues) > 0 || data.ContractChecks.HiddenIssueCount > 0 {
		return true
	}
	for _, metric := range data.PipelineStatus.Metrics {
		if metric.Kind == "warning" && (metric.Label == "Not deployed" || metric.Label == "Sync attention") {
			return true
		}
	}
	return false
}
