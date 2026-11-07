package views

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

const (
	DataSourceListFilterReady          = "ready"
	DataSourceListFilterActive         = "active"
	DataSourceListFilterAttention      = "attention"
	DataSourceListFilterReadOnly       = "read_only"
	DataSourceListFilterReadWrite      = "read_write"
	DataSourceListFilterFailed         = "failed"
	DataSourceListFilterUntested       = "untested"
	DataSourceListFilterWithQueries    = "with_queries"
	DataSourceListFilterWithoutQueries = "without_queries"

	dataSourceIconTypeUnknown      = "unknown"
	dataSourceIdentityVariantCard  = "card"
	dataSourceIdentityVariantList  = "list"
	dataSourceIdentityVariantTable = "table"
	dataSourceSidePanelSelector    = "#data-source-side-panel"
)

// DataSourceCreateFormData stores sticky values for the datasource create form.
type DataSourceCreateFormData struct {
	SSLMode                     string
	HTTPAuth                    HTTPAuthFormData
	WorkspacePrivacyURL         string
	WorkspaceReadOnly           bool
	BigQueryReadOnly            bool
	Name                        string
	Slug                        string
	Type                        string
	GitAuthType                 string
	GitLabProjectID             string
	GitLabRepositories          []api.ProviderRepository
	GitLabConnectionAvailable   bool
	GitLabRepositoryError       string
	GitLabReadOnly              bool
	GitHubRepositoryID          string
	GitHubRepositories          []api.GitHubAppRepository
	GitHubConnectionAvailable   bool
	GitHubReadOnly              bool
	HubSpotAuthType             string
	HubSpotReadOnly             bool
	SnowflakeAuthType           string
	SnowflakeSelectTables       string
	SnowflakeInsertTables       string
	Host                        string
	Port                        string
	Database                    string
	Account                     string
	Warehouse                   string
	Schema                      string
	Role                        string
	ProjectID                   string
	Location                    string
	DefaultDataset              string
	MaximumBytesBilled          string
	ServiceAccountJSON          string
	Username                    string
	GitUsername                 string
	GitToken                    string
	GitRepoURL                  string
	GitBasePath                 string
	GitBranch                   string
	GitPrivateKey               string
	GitKnownHosts               string
	GitPassphrase               string
	GitAllowPrivateHosts        bool
	DatabaseAllowPrivateHosts   bool
	AllowPrivateDataSourceHosts bool
	ConnectTimeout              string
	AuthDatabase                string
	S3Endpoint                  string
	S3Region                    string
	S3Bucket                    string
	S3Prefix                    string
	S3ForcePathStyle            bool
	S3AllowPrivateHosts         bool
	BaseURL                     string
	HealthPath                  string
	TimeoutSeconds              string
	OpenAPIURL                  string
	OpenAPIFetchTimeout         string
	HTTPAllowPrivateHosts       bool
	PrivateAppToken             string
	TestMessage                 string
	TestSuccess                 bool
	InvalidField                string
}

// DataSourcePageFeedback carries safe, top-level feedback for datasource workflows.
type DataSourcePageFeedback struct {
	Message string
	Kind    string
}

// DataSourceSchemaPanelData carries cache-only schema browser state for SSR detail panels.
type DataSourceSchemaPanelData struct {
	Operations []api.OperationInfo
	Status     api.DataSourceSchemaStatus
	Title      string
	Lines      []string
	Error      string
}

// DataSourceOperationsPanelData holds the first page of saved queries for a source.
type DataSourceOperationsPanelData struct {
	Queries []api.Query
	HasMore bool
	Error   string
}

// DataSourceListFilterOption describes one operator-facing data-source filter.
type DataSourceListFilterOption struct {
	Value string
	Label string
}

// DefaultDataSourceSchemaPanelData returns the cold-cache schema browser state.
func DefaultDataSourceSchemaPanelData() DataSourceSchemaPanelData {
	return DataSourceSchemaPanelData{Status: api.DataSourceSchemaStatusNotImported}
}

// NormalizeDataSourceSchemaStatus maps missing or unknown API status to not_imported.
func NormalizeDataSourceSchemaStatus(status api.DataSourceSchemaStatus) api.DataSourceSchemaStatus {
	switch status {
	case api.DataSourceSchemaStatusReady,
		api.DataSourceSchemaStatusImporting,
		api.DataSourceSchemaStatusFailed,
		api.DataSourceSchemaStatusNotImported:
		return status
	default:
		return api.DataSourceSchemaStatusNotImported
	}
}

// DataSourceSchemaStatusLabel returns compact status copy for the schema browser badge.
func DataSourceSchemaStatusLabel(status api.DataSourceSchemaStatus) string {
	switch NormalizeDataSourceSchemaStatus(status) {
	case api.DataSourceSchemaStatusReady:
		return DataSourceListFilterReady
	case api.DataSourceSchemaStatusImporting:
		return "importing"
	case api.DataSourceSchemaStatusFailed:
		return DataSourceListFilterFailed
	default:
		return "not imported"
	}
}

// DataSourceSchemaStatusBadgeClass maps schema import state to DaisyUI badge classes.
func DataSourceSchemaStatusBadgeClass(status api.DataSourceSchemaStatus) string {
	switch NormalizeDataSourceSchemaStatus(status) {
	case api.DataSourceSchemaStatusReady:
		return badgeSuccessSoft
	case api.DataSourceSchemaStatusImporting:
		return badgeInfoSoft
	case api.DataSourceSchemaStatusFailed:
		return badgeWarningSoft
	default:
		return badgeGhostSoft
	}
}

// DataSourceSchemaRefreshLabel returns the action label for the current schema state.
func DataSourceSchemaRefreshLabel(status api.DataSourceSchemaStatus) string {
	switch NormalizeDataSourceSchemaStatus(status) {
	case api.DataSourceSchemaStatusImporting:
		return "Importing…"
	case api.DataSourceSchemaStatusNotImported:
		return "Import schema"
	case api.DataSourceSchemaStatusFailed:
		return "Retry import"
	default:
		return "Refresh schema"
	}
}

// DataSourceSchemaEmptyMessage returns the empty-state copy for the schema browser.
func DataSourceSchemaEmptyMessage(status api.DataSourceSchemaStatus) string {
	switch NormalizeDataSourceSchemaStatus(status) {
	case api.DataSourceSchemaStatusReady:
		return "Imported schema contains no browseable metadata."
	case api.DataSourceSchemaStatusImporting:
		return "Schema import is running. This panel updates automatically."
	case api.DataSourceSchemaStatusFailed:
		return "Schema import failed."
	default:
		return "Schema has not been imported yet."
	}
}

func DefaultDataSourceCreateFormData() DataSourceCreateFormData {
	return DataSourceCreateFormData{
		GitAuthType:       "https",
		BigQueryReadOnly:  true,
		GitLabReadOnly:    true,
		GitHubReadOnly:    true,
		HubSpotAuthType:   "oauth2",
		HubSpotReadOnly:   true,
		WorkspaceReadOnly: true,
		SnowflakeAuthType: "workload_identity",
	}
}

// DataSourceOAuthFeedback maps callback outcomes to safe operator-facing copy.
func DataSourceOAuthFeedback(outcome string) DataSourcePageFeedback {
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "success":
		return DataSourcePageFeedback{
			Message: "HubSpot connected successfully.",
			Kind:    "success",
		}
	case "denied":
		return DataSourcePageFeedback{
			Message: "HubSpot connection was canceled. You can try again when you are ready.",
			Kind:    "warning",
		}
	case "expired":
		return DataSourcePageFeedback{
			Message: "HubSpot authorization expired. Start the connection again to continue.",
			Kind:    "warning",
		}
	case "error":
		return DataSourcePageFeedback{
			Message: "HubSpot could not be connected. Check the data source and try again.",
			Kind:    "error",
		}
	default:
		return DataSourcePageFeedback{}
	}
}

// DataSourceProviderOAuthIntent selects grant creation or rotation from safe credential metadata.
func DataSourceProviderOAuthIntent(dataSource api.DataSource) api.ProviderInstallIntent {
	if !dataSource.IsActive && dataSource.Credential != nil &&
		dataSource.Credential.Kind == api.Oauth2Grant &&
		dataSource.Credential.Status == api.CredentialStatusPending {
		return api.CreateGrant
	}
	return api.RotateGrant
}

// CanStartDataSourceProviderOAuth applies create permission only to trusted pending grants and update otherwise.
func CanStartDataSourceProviderOAuth(dataSource api.DataSource, permissions OrganizationPermissions) bool {
	if dataSource.Type != api.DataSourceTypeHubspot && dataSource.Type != api.DataSourceTypeBigquery && dataSource.Type != api.DataSourceTypeGoogleWorkspace {
		return false
	}
	if DataSourceProviderOAuthIntent(dataSource) == api.CreateGrant {
		return permissions.Can("datasources", "create")
	}
	return permissions.Can("datasources", "update")
}

// DataSourceProviderOAuthActionLabel describes the recovery or credential migration action.
func DataSourceProviderOAuthActionLabel(dataSource api.DataSource) string {
	label := "HubSpot"
	if dataSource.Type == api.DataSourceTypeHttp {
		if dataSource.Credential != nil && dataSource.Credential.Status == api.CredentialStatusPending {
			return "Connect HTTP API"
		}
		return "Reconnect HTTP API"
	}
	if dataSource.Type == api.DataSourceTypeBigquery || dataSource.Type == api.DataSourceTypeGoogleWorkspace {
		label = "Google"
	}
	if dataSource.Credential == nil {
		if dataSource.Type == api.DataSourceTypeBigquery || dataSource.Type == api.DataSourceTypeGoogleWorkspace {
			return "Connect with Google"
		}
		return "Connect HubSpot with OAuth"
	}
	if dataSource.Credential.Kind != api.Oauth2Grant {
		return "Switch to OAuth"
	}
	if dataSource.Credential.Status == api.CredentialStatusPending {
		return "Retry " + label + " connection"
	}
	return "Reconnect " + label
}

// HasInput reports whether the create form has user-entered or feedback state that should keep it visible.
func (f DataSourceCreateFormData) HasInput() bool {
	return strings.TrimSpace(f.Name) != "" ||
		strings.TrimSpace(f.Slug) != "" ||
		strings.TrimSpace(f.Type) != "" ||
		strings.TrimSpace(f.Host) != "" ||
		strings.TrimSpace(f.Port) != "" ||
		strings.TrimSpace(f.Database) != "" ||
		strings.TrimSpace(f.Username) != "" ||
		strings.TrimSpace(f.GitUsername) != "" ||
		strings.TrimSpace(f.GitToken) != "" ||
		strings.TrimSpace(f.GitRepoURL) != "" ||
		strings.TrimSpace(f.GitBasePath) != "" ||
		strings.TrimSpace(f.GitBranch) != "" ||
		strings.TrimSpace(f.GitPrivateKey) != "" ||
		strings.TrimSpace(f.GitKnownHosts) != "" ||
		strings.TrimSpace(f.GitPassphrase) != "" ||
		(f.AllowPrivateDataSourceHosts && f.GitAllowPrivateHosts) ||
		(f.AllowPrivateDataSourceHosts && f.DatabaseAllowPrivateHosts) ||
		strings.TrimSpace(f.ConnectTimeout) != "" ||
		strings.TrimSpace(f.AuthDatabase) != "" ||
		strings.TrimSpace(f.S3Endpoint) != "" || strings.TrimSpace(f.S3Region) != "" ||
		strings.TrimSpace(f.S3Bucket) != "" || strings.TrimSpace(f.S3Prefix) != "" ||
		f.S3ForcePathStyle || (f.AllowPrivateDataSourceHosts && f.S3AllowPrivateHosts) ||
		strings.TrimSpace(f.BaseURL) != "" ||
		strings.TrimSpace(f.HealthPath) != "" ||
		strings.TrimSpace(f.TimeoutSeconds) != "" ||
		strings.TrimSpace(f.OpenAPIURL) != "" ||
		strings.TrimSpace(f.OpenAPIFetchTimeout) != "" ||
		(f.AllowPrivateDataSourceHosts && f.HTTPAllowPrivateHosts) ||
		strings.TrimSpace(f.PrivateAppToken) != "" ||
		strings.TrimSpace(f.TestMessage) != ""
}

// DataSourceReadyCount returns the number of active tested data sources without a stored test failure.
func DataSourceReadyCount(dataSources []api.DataSource) int {
	count := 0
	for _, ds := range dataSources {
		if DataSourceStatusLabel(ds) == "ready" {
			count++
		}
	}
	return count
}

// DataSourceAttentionCount returns the number of inactive, untested, or failing data sources.
func DataSourceAttentionCount(dataSources []api.DataSource) int {
	count := 0
	for _, ds := range dataSources {
		if DataSourceStatusLabel(ds) != "ready" {
			count++
		}
	}
	return count
}

// DataSourceReadWriteCount returns the number of active data sources that can be used as sinks.
func DataSourceReadWriteCount(dataSources []api.DataSource) int {
	count := 0
	for _, ds := range dataSources {
		if ds.IsActive && !ds.ReadOnly {
			count++
		}
	}
	return count
}

// NormalizeDataSourceListFilter returns a supported data-source list filter key.
func NormalizeDataSourceListFilter(filter string) string {
	switch strings.ToLower(strings.TrimSpace(filter)) {
	case DataSourceListFilterReady:
		return DataSourceListFilterReady
	case DataSourceListFilterActive:
		return DataSourceListFilterActive
	case DataSourceListFilterAttention:
		return DataSourceListFilterAttention
	case DataSourceListFilterReadOnly:
		return DataSourceListFilterReadOnly
	case DataSourceListFilterReadWrite:
		return DataSourceListFilterReadWrite
	case DataSourceListFilterFailed:
		return DataSourceListFilterFailed
	case DataSourceListFilterUntested:
		return DataSourceListFilterUntested
	case DataSourceListFilterWithQueries:
		return DataSourceListFilterWithQueries
	case DataSourceListFilterWithoutQueries:
		return DataSourceListFilterWithoutQueries
	default:
		return ""
	}
}

// DataSourceListFilterOptions returns the filter tabs supported by the inventory.
func DataSourceListFilterOptions() []DataSourceListFilterOption {
	return []DataSourceListFilterOption{
		{Value: "", Label: "All"},
		{Value: DataSourceListFilterReady, Label: "Ready"},
		{Value: DataSourceListFilterActive, Label: "Active"},
		{Value: DataSourceListFilterAttention, Label: "Attention"},
		{Value: DataSourceListFilterFailed, Label: "Failed tests"},
		{Value: DataSourceListFilterUntested, Label: "Untested"},
		{Value: DataSourceListFilterReadOnly, Label: "Read-only"},
		{Value: DataSourceListFilterReadWrite, Label: "Read/write"},
		{Value: DataSourceListFilterWithQueries, Label: "Has queries"},
		{Value: DataSourceListFilterWithoutQueries, Label: "No queries"},
	}
}

// DataSourceListFilterLabel returns concise copy for the active filter.
func DataSourceListFilterLabel(filter string) string {
	switch NormalizeDataSourceListFilter(filter) {
	case DataSourceListFilterReady:
		return "Ready sources"
	case DataSourceListFilterActive:
		return "Active sources"
	case DataSourceListFilterAttention:
		return "Sources needing attention"
	case DataSourceListFilterReadOnly:
		return "Read-only sources"
	case DataSourceListFilterReadWrite:
		return "Read/write sources"
	case DataSourceListFilterFailed:
		return "Sources with failed tests"
	case DataSourceListFilterUntested:
		return "Untested sources"
	case DataSourceListFilterWithQueries:
		return "Sources with queries"
	case DataSourceListFilterWithoutQueries:
		return "Sources without queries"
	default:
		return "All sources"
	}
}

// DataSourceMatchesListFilter reports whether a source belongs in an operational filter.
func DataSourceMatchesListFilter(ds api.DataSource, filter string) bool {
	switch NormalizeDataSourceListFilter(filter) {
	case DataSourceListFilterReady:
		return DataSourceStatusLabel(ds) == "ready"
	case DataSourceListFilterActive:
		return ds.IsActive
	case DataSourceListFilterAttention:
		return DataSourceStatusLabel(ds) != "ready"
	case DataSourceListFilterReadOnly:
		return ds.ReadOnly
	case DataSourceListFilterReadWrite:
		return ds.IsActive && !ds.ReadOnly
	case DataSourceListFilterFailed:
		return DataSourceHasTestError(ds)
	case DataSourceListFilterUntested:
		return ds.LastTestedAt == nil
	default:
		return true
	}
}

// DataSourceListFilterNeedsQueryInventory reports whether filtering requires query lookups.
func DataSourceListFilterNeedsQueryInventory(filter string) bool {
	normalized := NormalizeDataSourceListFilter(filter)
	return normalized == DataSourceListFilterWithQueries || normalized == DataSourceListFilterWithoutQueries
}

// DataSourceHasTestError reports stored failure evidence, independently of authorization.
func DataSourceHasTestError(ds api.DataSource) bool {
	return ds.TestError != nil && strings.TrimSpace(*ds.TestError) != ""
}

// DataSourceStatusLabel describes the current connection state for operator cards.
func DataSourceStatusLabel(ds api.DataSource) string {
	if ds.Credential != nil {
		switch ds.Credential.Status {
		case api.CredentialStatusActive:
			// Active authorization still requires connection-test evidence.
		case api.CredentialStatusPending:
			return "authorization pending"
		case api.CredentialStatusFailed, api.CredentialStatusRevoked, api.CredentialStatusRotated:
			return "authorization unavailable"
		}
	}
	if DataSourceHasTestError(ds) {
		return "test failed"
	}
	if !ds.IsActive {
		return "inactive"
	}
	if ds.LastTestedAt == nil {
		return "untested"
	}
	return "ready"
}

// DataSourceStatusBadgeClass maps data-source state to DaisyUI badge classes.
func DataSourceStatusBadgeClass(ds api.DataSource) string {
	if DataSourceStatusLabel(ds) != "ready" {
		return "badge border-warning/50 bg-warning/15 text-warning"
	}
	return "badge border-success/50 bg-success/15 text-success"
}

// DataSourceCapabilityLabel summarizes whether a data source can participate in sink writes.
func DataSourceCapabilityLabel(ds api.DataSource) string {
	if ds.ReadOnly {
		return "read only"
	}
	return "read/write"
}

// DataSourceCapabilityBadgeClass maps read/write capability to DaisyUI badge classes.
func DataSourceCapabilityBadgeClass(ds api.DataSource) string {
	if ds.ReadOnly {
		return "badge border-info/50 bg-info/15 text-info"
	}
	return "badge border-primary/50 bg-primary/15 text-primary"
}

// DataSourceCardClass returns the clickable inventory card styling for selected and operational states.
func DataSourceCardClass(ds api.DataSource, selectedDataSource *api.DataSource) string {
	base := "block rounded-lg border p-3 transition hover:-translate-y-0.5 hover:border-primary/55 hover:bg-primary/10 focus:outline-none focus:ring-2 focus:ring-primary/45"
	if selectedDataSource != nil && selectedDataSource.Slug == ds.Slug {
		return base + " border-primary/60 bg-primary/15 shadow-sm shadow-primary/10"
	}
	if !ds.IsActive {
		return base + " border-base-300 bg-base-200/35"
	}
	if DataSourceHasTestError(ds) {
		return base + " border-warning/35 bg-warning/10"
	}
	if ds.ReadOnly {
		return base + " border-info/30 bg-info/10"
	}
	return base + " border-success/30 bg-success/10"
}

// DataSourceMetricCardClass maps summary metrics to stronger but theme-compatible panels.
func DataSourceMetricCardClass(kind string) string {
	base := "block rounded-lg border p-4 transition-colors hover:border-primary/40 hover:bg-base-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary"
	switch kind {
	case "ready":
		return base + " border-success/35 bg-success/10"
	case "attention":
		return base + " border-warning/40 bg-warning/10"
	case failureSideSink:
		return base + " border-primary/35 bg-primary/10"
	default:
		return base + " border-base-300 bg-base-200/40"
	}
}

// DataSourcePanelFeedbackClass maps side-panel action feedback to visible dark-theme alerts.
func DataSourcePanelFeedbackClass(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), "success") {
		return "mb-3 rounded-lg border border-success/40 bg-success/15 px-3 py-2 text-sm text-base-content"
	}
	return "mb-3 rounded-lg border border-warning/45 bg-warning/15 px-3 py-2 text-sm text-base-content"
}

// DataSourcePanelHTMXTarget returns the swap target for embedded side-panel forms.
func DataSourcePanelHTMXTarget(embedded bool) string {
	if embedded {
		return dataSourceSidePanelSelector
	}
	return htmxMainContentSelector
}

// DataSourcePanelHTMXSelect returns the response fragment selector for side-panel forms.
func DataSourcePanelHTMXSelect(embedded bool) string {
	if embedded {
		return dataSourceSidePanelSelector
	}
	return htmxMainContentSelector
}

// DataSourcePanelHTMXSwap returns a scroll-preserving replacement mode for panel actions.
func DataSourcePanelHTMXSwap() string {
	return htmxScrollPreserveSwap
}

// DataSourcesListHref builds a data-source list URL while preserving optional environment and selection state.
func DataSourcesListHref(orgSlug, activeEnvironmentSlug, selectedDataSourceSlug string) string {
	return DataSourcesListFilteredHref(orgSlug, activeEnvironmentSlug, selectedDataSourceSlug, "")
}

// DataSourcesListFilteredHref builds a data-source list URL with an inventory filter.
func DataSourcesListFilteredHref(orgSlug, activeEnvironmentSlug, selectedDataSourceSlug, filter string) string {
	href := "/orgs/" + strings.TrimSpace(orgSlug) + "/data-sources"
	params := url.Values{}
	if environmentSlug := strings.TrimSpace(activeEnvironmentSlug); environmentSlug != "" {
		params.Set("environment_slug", environmentSlug)
	}
	if dataSourceSlug := strings.TrimSpace(selectedDataSourceSlug); dataSourceSlug != "" {
		params.Set("selected", dataSourceSlug)
	}
	if filter = NormalizeDataSourceListFilter(filter); filter != "" {
		params.Set("filter", filter)
	}
	if encoded := params.Encode(); encoded != "" {
		href += "?" + encoded
	}
	return href
}

// DataSourcesListCursorHref builds a paginated data-source list URL while preserving runtime context.
func DataSourcesListCursorHref(orgSlug, activeEnvironmentSlug, cursor string) string {
	return DataSourcesListCursorFilteredHref(orgSlug, activeEnvironmentSlug, cursor, "")
}

// DataSourcesListCursorFilteredHref builds a paginated data-source list URL with an inventory filter.
func DataSourcesListCursorFilteredHref(orgSlug, activeEnvironmentSlug, cursor, filter string) string {
	href := "/orgs/" + strings.TrimSpace(orgSlug) + "/data-sources"
	params := url.Values{}
	if environmentSlug := strings.TrimSpace(activeEnvironmentSlug); environmentSlug != "" {
		params.Set("environment_slug", environmentSlug)
	}
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		params.Set("cursor", cursor)
	}
	if filter = NormalizeDataSourceListFilter(filter); filter != "" {
		params.Set("filter", filter)
	}
	if encoded := params.Encode(); encoded != "" {
		href += "?" + encoded
	}
	return href
}

// DataSourceDetailHref builds a data-source detail URL while preserving the active environment context.
func DataSourceDetailHref(orgSlug, activeEnvironmentSlug, dataSourceSlug string) string {
	href := "/orgs/" + strings.TrimSpace(orgSlug) + "/data-sources/" + strings.TrimSpace(dataSourceSlug)
	if environmentSlug := strings.TrimSpace(activeEnvironmentSlug); environmentSlug != "" {
		href += "?environment_slug=" + url.QueryEscape(environmentSlug)
	}
	return href
}

// DataSourceCreateHref builds the create URL while preserving the active runtime environment.
func DataSourceCreateHref(orgSlug, activeEnvironmentSlug string) string {
	return dataSourceMutationHref(orgSlug, activeEnvironmentSlug, "", "")
}

// DataSourceCreateTestHref builds the create-form test URL while preserving runtime context.
func DataSourceCreateTestHref(orgSlug, activeEnvironmentSlug string) string {
	return dataSourceMutationHref(orgSlug, activeEnvironmentSlug, "", "test-connection")
}

// DataSourceHubSpotOAuthHref builds the OAuth-first HubSpot create URL while preserving runtime context.
func DataSourceHubSpotOAuthHref(orgSlug, activeEnvironmentSlug string) string {
	return dataSourceMutationHref(orgSlug, activeEnvironmentSlug, "", "hubspot/oauth")
}

func DataSourceProviderRepositoryHref(orgSlug, activeEnvironmentSlug, dataSourceSlug string) string {
	return dataSourceMutationHref(orgSlug, activeEnvironmentSlug, dataSourceSlug, "provider-repository")
}

// DataSourceTestHref builds the stored-source test URL while preserving runtime context.
func DataSourceTestHref(orgSlug, activeEnvironmentSlug, dataSourceSlug string) string {
	return dataSourceMutationHref(orgSlug, activeEnvironmentSlug, dataSourceSlug, "test-connection")
}

// DataSourceRefreshHref builds the schema refresh URL while preserving runtime context.
func DataSourceRefreshHref(orgSlug, activeEnvironmentSlug, dataSourceSlug string) string {
	return dataSourceMutationHref(orgSlug, activeEnvironmentSlug, dataSourceSlug, "schema/refresh")
}

// DataSourceUpdateHref builds the config update URL while preserving runtime context.
func DataSourceUpdateHref(orgSlug, activeEnvironmentSlug, dataSourceSlug string) string {
	return dataSourceMutationHref(orgSlug, activeEnvironmentSlug, dataSourceSlug, "update")
}

// DataSourceUpdateCredentialHref builds the credential rotation URL while preserving runtime context.
func DataSourceUpdateCredentialHref(orgSlug, activeEnvironmentSlug, dataSourceSlug string) string {
	return dataSourceMutationHref(orgSlug, activeEnvironmentSlug, dataSourceSlug, "credentials/update")
}

// DataSourceDeleteHref builds the data-source delete URL while preserving runtime context.
func DataSourceDeleteHref(orgSlug, activeEnvironmentSlug, dataSourceSlug string) string {
	return dataSourceMutationHref(orgSlug, activeEnvironmentSlug, dataSourceSlug, "delete")
}

// dataSourceMutationHref centralizes mutation route construction so forms retain runtime context.
func dataSourceMutationHref(orgSlug, activeEnvironmentSlug, dataSourceSlug, actionPath string) string {
	href := "/orgs/" + strings.TrimSpace(orgSlug) + "/data-sources"
	if dataSourceSlug = strings.TrimSpace(dataSourceSlug); dataSourceSlug != "" {
		href += "/" + dataSourceSlug
	}
	if actionPath = strings.Trim(strings.TrimSpace(actionPath), "/"); actionPath != "" {
		href += "/" + actionPath
	}
	if environmentSlug := strings.TrimSpace(activeEnvironmentSlug); environmentSlug != "" {
		href += "?" + url.Values{"environment_slug": {environmentSlug}}.Encode()
	}
	return href
}

// DataSourceQueriesHref builds the query library URL for a source and preserves runtime context.
func DataSourceQueriesHref(orgSlug, activeEnvironmentSlug, dataSourceSlug string) string {
	href := "/orgs/" + strings.TrimSpace(orgSlug) + "/queries"
	params := url.Values{}
	if dataSourceSlug = strings.TrimSpace(dataSourceSlug); dataSourceSlug != "" {
		params.Set("ds", dataSourceSlug)
	}
	if environmentSlug := strings.TrimSpace(activeEnvironmentSlug); environmentSlug != "" {
		params.Set("environment_slug", environmentSlug)
	}
	if encoded := params.Encode(); encoded != "" {
		href += "?" + encoded
	}
	return href
}

// DataSourceTypeIsGitLike reports whether a type uses git repository settings and credentials.
func DataSourceTypeIsGitLike(dataSourceType string) bool {
	switch strings.ToLower(strings.TrimSpace(dataSourceType)) {
	case queryStatementTypeGit, string(api.DataSourceTypeGithub), string(api.DataSourceTypeGitlab):
		return true
	default:
		return false
	}
}

// DataSourceSelectionSlug returns the slug to select when navigating back from a detail panel.
func DataSourceSelectionSlug(dataSource *api.DataSource) string {
	if dataSource == nil {
		return ""
	}
	return dataSource.Slug
}

// DataSourceLastTestLabel formats the last connection test timestamp for compact cards.
func DataSourceLastTestLabel(ds api.DataSource) string {
	if ds.LastTestedAt == nil || ds.LastTestedAt.IsZero() {
		return "not tested"
	}
	return "last test " + ds.LastTestedAt.UTC().Format(time.RFC3339)
}

// DataSourceIconType normalizes connector types for icon rendering and test markers.
func DataSourceIconType(dataSourceType string) string {
	normalized := strings.ToLower(strings.TrimSpace(dataSourceType))
	if normalized == "" {
		return dataSourceIconTypeUnknown
	}
	return normalized
}

// DataSourceIconAssetPath returns the local transparent mark for a connector type.
func DataSourceIconAssetPath(dataSourceType string) string {
	switch DataSourceIconType(dataSourceType) {
	case string(api.DataSourceTypePostgres):
		return "/assets/icons/data-sources/vendors/brand/postgres.svg"
	case string(api.DataSourceTypeMysql):
		return "/assets/icons/data-sources/vendors/brand/mysql.svg"
	case string(api.DataSourceTypeGoogleWorkspace):
		return "/assets/icons/data-sources/vendors/brand/google-drive.png"
	case string(api.DataSourceTypeBigquery):
		return "/assets/icons/data-sources/vendors/brand/google-bigquery.svg"
	case string(api.DataSourceTypeSnowflake):
		return "/assets/icons/data-sources/vendors/brand/snowflake.svg"
	case string(api.DataSourceTypeMongodb):
		return "/assets/icons/data-sources/vendors/brand/mongodb.png"
	case string(api.DataSourceTypeHubspot):
		return "/assets/icons/data-sources/vendors/brand/hubspot.svg"
	case string(api.DataSourceTypeGit):
		return "/assets/icons/data-sources/vendors/brand/git.svg"
	case string(api.DataSourceTypeGithub):
		return "/assets/icons/data-sources/vendors/brand/github.svg"
	default:
		return ""
	}
}

// DataSourceIconClass returns stable icon container styling for data-source references.
func DataSourceIconClass(dataSourceType, size string) string {
	dimensions := "h-9 w-9"
	switch strings.TrimSpace(size) {
	case "sm":
		dimensions = "h-7 w-7"
	case "lg":
		dimensions = "h-10 w-10"
	}

	base := "grid shrink-0 place-items-center bg-transparent " + dimensions
	if DataSourceIconAssetPath(dataSourceType) != "" {
		return base
	}

	switch DataSourceIconType(dataSourceType) {
	case string(api.DataSourceTypeHttp):
		return base + " text-primary"
	case string(api.DataSourceTypeFtp):
		return base + " text-warning"
	default:
		return base + " text-base-content/70"
	}
}

// DataSourceIconImageClass sizes connector marks inside the fixed icon container.
func DataSourceIconImageClass(size string) string {
	switch strings.TrimSpace(size) {
	case "sm":
		return "h-4 max-w-5 object-contain"
	case "lg":
		return "h-6 max-w-7 object-contain"
	default:
		return "h-5 max-w-6 object-contain"
	}
}

// DataSourceFallbackIconClass sizes local protocol and unknown connector glyphs.
func DataSourceFallbackIconClass(size string) string {
	switch strings.TrimSpace(size) {
	case "sm":
		return "h-4 w-4"
	case "lg":
		return "h-5 w-5"
	default:
		return "h-5 w-5"
	}
}

// DataSourceIdentityClass returns the outer layout class for compact data-source identities.
func DataSourceIdentityClass(variant string) string {
	switch strings.TrimSpace(variant) {
	case dataSourceIdentityVariantList:
		return "flex min-w-0 gap-3"
	default:
		return "flex min-w-0 items-start gap-2"
	}
}

// DataSourceIdentityPrimaryClass returns label typography for compact data-source identities.
func DataSourceIdentityPrimaryClass(variant string) string {
	switch strings.TrimSpace(variant) {
	case dataSourceIdentityVariantCard:
		return "break-words text-base font-semibold text-base-content"
	case dataSourceIdentityVariantList:
		return "truncate text-sm font-semibold text-base-content"
	case dataSourceIdentityVariantTable:
		return "font-medium text-base-content"
	default:
		return "text-sm font-semibold text-base-content"
	}
}

// DataSourceIdentitySlugClass returns monospace slug typography for compact data-source identities.
func DataSourceIdentitySlugClass(variant string) string {
	switch strings.TrimSpace(variant) {
	case dataSourceIdentityVariantList:
		return "mt-0.5 truncate font-mono text-xs text-base-content/55"
	case dataSourceIdentityVariantCard, dataSourceIdentityVariantTable:
		return "mt-1 break-all font-mono text-xs text-base-content/70"
	default:
		return "mt-1 break-all font-mono text-xs text-base-content/65"
	}
}

// ConnectionInfoField stores one ordered connection detail for display.
type ConnectionInfoField struct {
	Key   string
	Value string
}

// ConnectionInfo holds connection details extracted from data source config for display.
type ConnectionInfo struct {
	Type   string
	Fields []ConnectionInfoField
}

// DataSourceUpdateFormData stores sticky values for data source update forms.
type DataSourceUpdateFormData struct {
	SSLMode                     string
	WorkspacePrivacyURL         string
	Name                        string
	Type                        string
	ManagedProvider             bool
	ProviderSelectionPending    bool
	ProviderRepositories        []api.ProviderRepository
	ProviderRepositoryLoadError string
	ProviderRepositoryID        string
	ProviderRepositoryName      string
	ProviderRepositoryURL       string
	HubSpotReadOnly             bool
	GitAuthType                 string
	SnowflakeAuthType           string
	SnowflakeSelectTables       string
	SnowflakeInsertTables       string
	Host                        string
	Port                        string
	Database                    string
	Account                     string
	Warehouse                   string
	Schema                      string
	Role                        string
	ProjectID                   string
	Location                    string
	DefaultDataset              string
	MaximumBytesBilled          string
	GitUsername                 string
	GitRepoURL                  string
	GitBasePath                 string
	GitBranch                   string
	GitPrivateKey               string
	GitKnownHosts               string
	GitPassphrase               string
	GitAllowPrivateHosts        bool
	DatabaseAllowPrivateHosts   bool
	AllowPrivateDataSourceHosts bool
	ConnectTimeout              string
	AuthDatabase                string
	BaseURL                     string
	HealthPath                  string
	TimeoutSeconds              string
	OpenAPIURL                  string
	OpenAPIFetchTimeout         string
	HTTPAllowPrivateHosts       bool
	PrivateAppToken             string
	ServiceAccountJSON          string
	S3Endpoint                  string
	S3Region                    string
	S3Bucket                    string
	S3Prefix                    string
	S3ForcePathStyle            bool
	S3AllowPrivateHosts         bool
}

func DataSourceBigQueryOAuthHref(orgSlug, activeEnvironmentSlug string) string {
	return dataSourceMutationHref(orgSlug, activeEnvironmentSlug, "", "bigquery/oauth")
}

func DataSourceExistingProviderOAuthHref(orgSlug, activeEnvironmentSlug, dataSourceSlug, provider string) string {
	return dataSourceMutationHref(orgSlug, activeEnvironmentSlug, dataSourceSlug, provider+"/oauth")
}

func dataSourceDetailSection(ctx context.Context) string {
	switch section := entityPageURL(ctx).Query().Get("section"); section {
	case "operations", "schema", "settings":
		return section
	default:
		return "overview"
	}
}

// DataSourcePurposeLabel describes declared access; the API has no purpose/description field.
func DataSourcePurposeLabel(ds api.DataSource) string {
	if ds.ReadOnly {
		return "Read data for queries and pipeline sources."
	}
	return "Read data and write to pipeline destinations."
}

// UsesHTTPOAuth reads only safe credential kind metadata, never stored app settings.
func UsesHTTPOAuth(source api.DataSource) bool {
	return source.Type == api.DataSourceTypeHttp && source.Credential != nil && source.Credential.Kind == api.Oauth2Grant
}

// OpenAPIOperationDraftHref selects cached metadata without putting provider values in the URL.
func OpenAPIOperationDraftHref(orgSlug, sourceSlug, operationID string) string {
	return "/orgs/" + orgSlug + "/queries?" + url.Values{"ds": {sourceSlug}, "openapi_operation": {operationID}}.Encode()
}

func OpenAPIOperationMetadata(operation api.OperationInfo) string {
	raw, err := json.MarshalIndent(struct {
		RequestBody *api.OperationRequestBodyInfo         `json:"request_body,omitempty"`
		Responses   *map[string]api.OperationResponseInfo `json:"responses,omitempty"`
	}{operation.RequestBody, operation.Responses}, "", "  ")
	if err != nil {
		return "Request and response metadata could not be displayed."
	}
	return string(raw)
}
