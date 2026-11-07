package views

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

const (
	projectPipelineBackendSchemeSSH    = "ssh"
	projectPipelineBackendSourceGitHub = "github"
)

// ProjectPipelineBackendDataSourceOption describes a Git-family data source that can back project pipeline specs.
type ProjectPipelineBackendDataSourceOption struct {
	Slug          string
	Name          string
	Type          string
	RepoURL       string
	DefaultBranch string
}

// ProjectPipelineBackendBranchOption describes a branch choice shown in the project backend form.
type ProjectPipelineBackendBranchOption struct {
	Name      string
	Label     string
	IsDefault bool
	IsMissing bool
}

// ProjectPipelineBackendControls carries backend source and branch choices for the selected project.
type ProjectPipelineBackendControls struct {
	RepositoryProvider       string
	RepositorySetup          *api.ProjectRepositorySetup
	PreviousRepositorySetup  *api.ProjectRepositorySetup
	PreviousSetupLoadError   string
	RepositorySetupLoadError string
	PipelineDir              string
	ProviderOwner            string
	RepositoryName           string
	CreationBranch           string
	DataSourceOptions        []ProjectPipelineBackendDataSourceOption
	Branches                 []api.DataSourceBranch
	BranchesPending          bool
	SelectedDataSource       string
	SelectedBranch           string
	DataSourceLoadError      string
	BranchLoadError          string
	DataSourceLoadDetails    string
	BranchLoadDetails        string
}

// ProjectPipelineBackendExternalLink describes a browser link to the project backend repository.
type ProjectPipelineBackendExternalLink struct {
	Label string
	Href  string
}

// ProjectOperationalContext carries read-only project-scoped workflow state for project summary pages.
type ProjectOperationalContext struct {
	Environments                   []api.ProjectEnvironment
	EnvironmentsUnavailable        bool
	Catalog                        []api.Environment
	CatalogUnavailable             bool
	Pipelines                      []api.PipelineRef
	PipelineDetails                []api.Pipeline
	PipelinesUnavailable           bool
	PipelineDetailsUnavailable     bool
	DataSourceRefs                 []api.DataSourceRef
	Bindings                       map[string][]api.DataSourceBinding
	DataSources                    []api.DataSource
	BindingUnavailableEnvironments map[string]bool
	BindingsUnavailable            bool
	DataSourcesUnavailable         bool
}

// ProjectCreateFormData carries sticky form values for project creation.
type ProjectCreateFormData struct {
	Name string
	Slug string
}

// DefaultProjectCreateFormData returns an empty project create form.
func DefaultProjectCreateFormData() ProjectCreateFormData {
	return ProjectCreateFormData{}
}

// HasInput reports whether the project create form should stay open after validation feedback.
func (form ProjectCreateFormData) HasInput() bool {
	return strings.TrimSpace(form.Name) != "" || strings.TrimSpace(form.Slug) != ""
}

// ProjectPipelineBackendDataSourceOptions filters organization data sources to the Git-family backend choices.
func ProjectPipelineBackendDataSourceOptions(dataSources []api.DataSource) []ProjectPipelineBackendDataSourceOption {
	options := make([]ProjectPipelineBackendDataSourceOption, 0, len(dataSources))
	for _, dataSource := range dataSources {
		if !dataSource.IsActive || dataSource.ReadOnly || !projectPipelineBackendDataSourceTypeAllowed(dataSource.Type) {
			continue
		}

		gitConfig, err := dataSource.Config.AsGitConfig()
		if err != nil {
			// Malformed Git-family config still renders as an option without unsafe repo details.
			gitConfig = api.GitConfig{}
		}

		option := ProjectPipelineBackendDataSourceOption{
			Slug:    dataSource.Slug,
			Name:    dataSource.Name,
			Type:    string(dataSource.Type),
			RepoURL: strings.TrimSpace(gitConfig.RepoUrl),
		}
		if gitConfig.DefaultBranch != nil {
			option.DefaultBranch = strings.TrimSpace(*gitConfig.DefaultBranch)
		}
		options = append(options, option)
	}
	return options
}

// ProjectPipelineBackendDataSourceLabel formats a backend source option for compact select controls.
func ProjectPipelineBackendDataSourceLabel(option ProjectPipelineBackendDataSourceOption) string {
	label := strings.TrimSpace(option.Name)
	if label == "" {
		label = option.Slug
	}
	if option.Type != "" {
		label += " - " + option.Type
	}
	if option.RepoURL != "" {
		label += " - " + option.RepoURL
	}
	return label
}

// ProjectPipelineBackendSelectedDataSourceSlug returns the source selected in the local form or saved backend.
func ProjectPipelineBackendSelectedDataSourceSlug(project api.Project, requestedSlug string) string {
	if requested := strings.TrimSpace(requestedSlug); requested != "" {
		return requested
	}
	return projectPipelineBackendDataSourceSlug(project)
}

// ProjectPipelineBackendBranchOptions returns loaded branch choices for the selected Git data source.
func ProjectPipelineBackendBranchOptions(
	project api.Project,
	controls ProjectPipelineBackendControls,
	selectedDataSourceSlug string,
) []ProjectPipelineBackendBranchOption {
	options := make([]ProjectPipelineBackendBranchOption, 0, len(controls.Branches)+1)
	for _, branch := range controls.Branches {
		name := strings.TrimSpace(branch.Name)
		if name == "" {
			continue
		}
		label := name
		if branch.IsDefault {
			label += " (default)"
		}
		options = appendUniqueBranchOption(options, ProjectPipelineBackendBranchOption{
			Name:      name,
			Label:     label,
			IsDefault: branch.IsDefault,
		})
	}

	if project.PipelineBackend != nil && project.PipelineBackend.DataSourceSlug == strings.TrimSpace(selectedDataSourceSlug) {
		branchName := strings.TrimSpace(project.PipelineBackend.TrackedBranch)
		if controls.BranchesPending && controls.SelectedBranch != "" {
			branchName = controls.SelectedBranch
		}
		if branchName != "" && !projectPipelineBackendBranchOptionExists(options, branchName) {
			options = append(options, ProjectPipelineBackendBranchOption{
				Name: branchName,
				Label: func() string {
					if controls.BranchesPending {
						return branchName
					}
					return branchName + " (not found on remote)"
				}(),
				IsMissing: true,
			})
		}
	}
	return options
}

// ProjectPipelineBackendSelectedTrackedBranch returns the selected branch for the current backend source.
func ProjectPipelineBackendSelectedTrackedBranch(
	project api.Project,
	branchOptions []ProjectPipelineBackendBranchOption,
	selectedDataSourceSlug string,
	requestedBranch string,
) string {
	selectedSource := strings.TrimSpace(selectedDataSourceSlug)
	savedSource := projectPipelineBackendDataSourceSlug(project)
	if selectedSource == savedSource {
		if branch := strings.TrimSpace(requestedBranch); branch != "" {
			return branch
		}
		return projectPipelineBackendTrackedBranch(project)
	}
	for _, option := range branchOptions {
		if option.IsDefault {
			return option.Name
		}
	}
	if len(branchOptions) > 0 {
		return branchOptions[0].Name
	}
	return ""
}

// ProjectPipelineBackendBranchHelpText explains the current branch selector source.
func ProjectPipelineBackendBranchHelpText(
	controls ProjectPipelineBackendControls,
	selectedDataSourceSlug string,
	branchOptions []ProjectPipelineBackendBranchOption,
) string {
	if strings.TrimSpace(selectedDataSourceSlug) == "" {
		return "Select a Git data source before choosing a tracked branch."
	}
	if controls.BranchLoadError != "" {
		return controls.BranchLoadError
	}
	for _, option := range branchOptions {
		if option.IsMissing {
			return "The saved tracked branch is not present in the remote branch list."
		}
	}
	if len(branchOptions) == 0 {
		return "No branches were returned for the selected data source."
	}
	return "Branches are loaded from the selected data source."
}

// ProjectPipelineBackendDataSourceChangeHref builds the local backend source-selection URL for the current project surface.
func ProjectPipelineBackendDataSourceChangeHref(orgSlug, projectSlug string, fullPage bool) string {
	if fullPage {
		return "/organizations/" + strings.TrimSpace(orgSlug) + "/projects/" + strings.TrimSpace(projectSlug)
	}
	return organizationProjectSelectionHref(orgSlug, projectSlug)
}

// ProjectPipelineBackendFormAction builds the backend submit URL for the current project surface.
func ProjectPipelineBackendFormAction(orgSlug, projectSlug string, fullPage bool) string {
	action := "/organizations/" + strings.TrimSpace(orgSlug) + "/projects/" + strings.TrimSpace(projectSlug) + "/settings/pipeline-backend"
	if fullPage {
		return action + "?return_to=full"
	}
	return action
}

// ProjectDeleteFormAction builds the project delete URL for the current project surface.
func ProjectDeleteFormAction(orgSlug, projectSlug string, fullPage bool) string {
	action := "/organizations/" + strings.TrimSpace(orgSlug) + "/projects/" + strings.TrimSpace(projectSlug) + "/delete"
	if fullPage {
		return action + "?return_to=full"
	}
	return action
}

// ProjectEnvironmentEnableFormAction builds the project-local environment enable URL.
func ProjectEnvironmentEnableFormAction(orgSlug, projectSlug string, fullPage bool) string {
	action := "/organizations/" + strings.TrimSpace(orgSlug) + "/projects/" + strings.TrimSpace(projectSlug) + "/environments/enable"
	if fullPage {
		return action + "?return_to=full"
	}
	return action
}

// ProjectEnvironmentDefaultFormAction builds the project-local default-environment URL.
func ProjectEnvironmentDefaultFormAction(orgSlug, projectSlug, environmentSlug string, fullPage bool) string {
	action := "/organizations/" + strings.TrimSpace(orgSlug) + "/projects/" + strings.TrimSpace(projectSlug) + "/environments/" + strings.TrimSpace(environmentSlug) + "/default"
	if fullPage {
		return action + "?return_to=full"
	}
	return action
}

// ProjectEnvironmentDisableFormAction builds the project-local environment disable URL.
func ProjectEnvironmentDisableFormAction(orgSlug, projectSlug, environmentSlug string, fullPage bool) string {
	action := "/organizations/" + strings.TrimSpace(orgSlug) + "/projects/" + strings.TrimSpace(projectSlug) + "/environments/" + strings.TrimSpace(environmentSlug) + "/disable"
	if fullPage {
		return action + "?return_to=full"
	}
	return action
}

// ProjectProtectedEnvironmentActionModalID builds a stable dialog id for protected project environment actions.
func ProjectProtectedEnvironmentActionModalID(projectSlug, environmentSlug, actionKey string) string {
	return "project-protected-env-" + strings.TrimSpace(projectSlug) + "-" + strings.TrimSpace(environmentSlug) + "-" + strings.TrimSpace(actionKey)
}

// ProjectDetailHTMXTarget returns the partial target for project detail local interactions.
func ProjectDetailHTMXTarget(fullPage bool) string {
	if fullPage {
		return "#organization-project-full-page"
	}
	return "#organization-projects-workspace"
}

// ProjectPipelineBackendDirtyBaseline returns the persisted backend state used for dirty-form comparison.
func ProjectPipelineBackendDirtyBaseline(project api.Project) string {
	fields := [][]string{
		{"data_source_slug", projectPipelineBackendDataSourceSlug(project)},
		{"pipeline_dir", projectPipelineBackendPipelineDir(project)},
	}
	if projectPipelineBackendDataSourceSlug(project) != "" {
		fields = append(fields, []string{"tracked_branch", projectPipelineBackendTrackedBranch(project)})
	}

	data, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	return string(data)
}

// ProjectDefaultEnvironmentLabel returns the default runtime target or a stable empty-state label.
func ProjectDefaultEnvironmentLabel(environments []api.ProjectEnvironment) string {
	for _, environment := range environments {
		if environment.IsDefault {
			return environment.EnvironmentSlug
		}
	}
	return "none"
}

// ProjectBoundBindingCount counts bound logical ref cells across enabled environments.
func ProjectBoundBindingCount(
	refs []api.DataSourceRef,
	environments []api.ProjectEnvironment,
	bindings map[string][]api.DataSourceBinding,
) int {
	count := 0
	for _, row := range EnvironmentBindingMatrixRows(refs, environments, bindings) {
		for _, cell := range row.Cells {
			if cell.IsBound {
				count++
			}
		}
	}
	return count
}

func projectPipelineBackendDataSourceTypeAllowed(dataSourceType api.DataSourceType) bool {
	switch dataSourceType {
	case api.DataSourceTypeGit, api.DataSourceTypeGithub, api.DataSourceTypeGitlab:
		return true
	default:
		return false
	}
}

func projectPipelineBackendOptionBySlug(
	options []ProjectPipelineBackendDataSourceOption,
	slug string,
) *ProjectPipelineBackendDataSourceOption {
	trimmed := strings.TrimSpace(slug)
	for idx := range options {
		if options[idx].Slug == trimmed {
			return &options[idx]
		}
	}
	return nil
}

// ProjectPipelineBackendSelectedDataSourceMissing reports whether the selected source is absent from the loaded options.
func ProjectPipelineBackendSelectedDataSourceMissing(controls ProjectPipelineBackendControls) bool {
	if strings.TrimSpace(controls.SelectedDataSource) == "" {
		return false
	}
	return projectPipelineBackendOptionBySlug(
		controls.DataSourceOptions,
		controls.SelectedDataSource,
	) == nil
}

func appendUniqueBranchOption(
	options []ProjectPipelineBackendBranchOption,
	option ProjectPipelineBackendBranchOption,
) []ProjectPipelineBackendBranchOption {
	if strings.TrimSpace(option.Name) == "" {
		return options
	}
	if projectPipelineBackendBranchOptionExists(options, option.Name) {
		return options
	}
	return append(options, option)
}

func projectPipelineBackendBranchOptionExists(
	options []ProjectPipelineBackendBranchOption,
	branch string,
) bool {
	trimmed := strings.TrimSpace(branch)
	for _, option := range options {
		if option.Name == trimmed {
			return true
		}
	}
	return false
}

// ProjectPipelineBackendOptionCountLabel formats the backend source count for empty and loaded states.
func ProjectPipelineBackendOptionCountLabel(options []ProjectPipelineBackendDataSourceOption) string {
	switch len(options) {
	case 0:
		return "No Git sources"
	case 1:
		return "1 Git source"
	default:
		return fmt.Sprintf("%d Git sources", len(options))
	}
}

// ProjectPipelineBackendExternalURL returns a browser-friendly repo link for GitHub/GitLab backends.
func ProjectPipelineBackendExternalURL(
	project api.Project,
	controls ProjectPipelineBackendControls,
) *ProjectPipelineBackendExternalLink {
	if project.PipelineBackend == nil {
		return nil
	}

	sourceSlug := strings.TrimSpace(project.PipelineBackend.DataSourceSlug)
	option := projectPipelineBackendOptionBySlug(controls.DataSourceOptions, sourceSlug)
	sourceType := ""
	repoURL := strings.TrimSpace(project.PipelineBackend.Repo)
	if option != nil {
		sourceType = strings.ToLower(strings.TrimSpace(option.Type))
	}
	if sourceType != string(api.DataSourceTypeGithub) && sourceType != string(api.DataSourceTypeGitlab) {
		return nil
	}

	href := projectPipelineBackendWebURL(repoURL)
	if href == "" {
		return nil
	}

	label := "Open GitLab"
	if sourceType == string(api.DataSourceTypeGithub) {
		label = "Open GitHub"
	}
	return &ProjectPipelineBackendExternalLink{Label: label, Href: href}
}

// projectPipelineBackendWebURL converts supported Git remote URL shapes into browser links.
func projectPipelineBackendWebURL(rawRepoURL string) string {
	rawRepoURL = strings.TrimSpace(rawRepoURL)
	if rawRepoURL == "" {
		return ""
	}

	if sshHost, sshPath, ok := parseSCPStyleGitURL(rawRepoURL); ok {
		if cut := strings.IndexAny(sshPath, "?#"); cut >= 0 {
			sshPath = sshPath[:cut]
		}
		sshPath = strings.Trim(strings.TrimSuffix(sshPath, ".git"), "/")
		if sshPath == "" {
			return ""
		}
		return "https://" + sshHost + "/" + sshPath
	}

	parsed, err := url.Parse(rawRepoURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	host := parsed.Hostname()
	if host == "" {
		return ""
	}
	if parsed.Scheme == queryStatementTypeHTTP || parsed.Scheme == "https" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.Path = strings.TrimSuffix(parsed.Path, ".git")
		return parsed.String()
	}
	if parsed.Scheme == projectPipelineBackendSchemeSSH || parsed.Scheme == queryStatementTypeGit {
		path := strings.TrimPrefix(parsed.Path, "/")
		if path == "" {
			return ""
		}
		return "https://" + host + "/" + strings.TrimSuffix(path, ".git")
	}
	return ""
}

// parseSCPStyleGitURL extracts host and path from scp-like Git remotes such as git@example.com:org/repo.git.
func parseSCPStyleGitURL(rawRepoURL string) (string, string, bool) {
	if strings.Contains(rawRepoURL, "://") {
		return "", "", false
	}
	at := strings.LastIndex(rawRepoURL, "@")
	hostStart := 0
	if at >= 0 {
		hostStart = at + 1
	}
	hostPath := rawRepoURL[hostStart:]
	colonOffset := strings.Index(hostPath, ":")
	if colonOffset <= 0 {
		return "", "", false
	}
	colon := hostStart + colonOffset
	host := strings.TrimSpace(rawRepoURL[hostStart:colon])
	path := strings.TrimSpace(rawRepoURL[colon+1:])
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	if host == "" || path == "" {
		return "", "", false
	}
	return host, path, true
}
