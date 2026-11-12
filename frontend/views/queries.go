package views

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

const (
	queryStatementTypePostgres  = "postgres"
	queryStatementTypeMySQL     = "mysql"
	queryStatementTypeBigQuery  = "bigquery"
	queryStatementTypeSnowflake = "snowflake"
	queryStatementTypeHTTP      = "http"
	queryStatementTypeMongoDB   = "mongodb"
	queryStatementTypeHubSpot   = "hubspot"
	queryStatementTypeFTP       = "ftp"
	queryStatementTypeGit       = "git"
	queryStatementTypeGitLab    = "gitlab"
	queryStatementTypeS3        = "s3"

	queryOperationRead      = "read"
	queryFormatAuto         = "auto"
	htmxMainContentSelector = "#main-content"
	htmxScrollPreserveSwap  = "outerHTML show:none focus-scroll:false"
)

func queryStatementTypeValue(dataSourceType string) string {
	normalized := strings.TrimSpace(strings.ToLower(dataSourceType))
	switch normalized {
	case projectPipelineBackendSourceGitHub, queryStatementTypeGitLab:
		return queryStatementTypeGit
	default:
		return normalized
	}
}

// QueryParameterFormData stores sticky values for query parameter rows.
type QueryParameterFormData struct {
	Name         string
	Type         string
	Required     bool
	DefaultValue string
	Description  string
	Source       string
}

// QueryKeyValueFormData stores a key-value row for HTTP params/headers editors.
type QueryKeyValueFormData struct {
	Key   string
	Value string
}

// QueryEditorFormData stores sticky values for create/edit query forms.
type QueryEditorFormData struct {
	WorkspaceResource      string
	WorkspaceSpreadsheetID string
	WorkspaceFileID        string
	WorkspaceFolderID      string
	WorkspaceDriveID       string
	WorkspaceName          string
	WorkspaceFormat        string
	WorkspaceMatchText     string
	WorkspaceContent       string
	WorkspaceMIMEType      string
	WorkspacePageToken     string
	WorkspaceDelimiter     string
	WorkspaceHasHeader     string

	WorkspaceWorksheetID string
	WorkspaceRange       string
	WorkspaceOperation   string
	WorkspaceColumns     string
	WorkspaceRows        string
	Name                 string
	Slug                 string
	Description          string
	RowLimit             string
	TimeoutSeconds       string
	Parameters           []QueryParameterFormData

	// StatementType is the data source statement discriminator used to select the editor in templates.
	StatementType string

	// Statement stores SQL text or the complete S3 operation JSON.
	Statement string
	SQLFormat string

	// HTTP-specific fields (for http type)
	HTTPMethod          string
	HTTPPath            string
	HTTPQueryParamRows  []QueryKeyValueFormData
	HTTPHeaderRows      []QueryKeyValueFormData
	HTTPBody            string // JSON or empty
	HTTPAcknowledgement HTTPAcknowledgementFormData
	HTTPPagination      string
	HTTPWriteRetry      string

	// MongoDB-specific fields (for mongodb type)
	MongoCollection string
	MongoOperation  string
	MongoFilter     string // JSON object or empty
	MongoSort       string // JSON object or empty
	MongoProjection string // JSON object or empty
	MongoLimitValue string // integer as string or empty
	MongoDocument   string // JSON object or empty

	// HubSpot-specific fields (for hubspot type)
	HubSpotObjectType   string
	HubSpotOperation    string
	HubSpotProperties   string
	HubSpotQuery        string
	HubSpotAfter        string
	HubSpotSorts        string
	HubSpotFilterGroups string
	HubSpotIDProperty   string
	HubSpotMapping      string
	HubSpotRows         string

	// FTP-specific fields (for ftp type)
	FTPPath      string
	FTPOperation string // "read" or "write"
	FTPFormat    string // "auto", "csv", "json", or "spreadsheet"
	FTPDelimiter string // single character, optional
	FTPHasHeader bool
	FTPSheet     string
	FTPRange     string
	FTPOverwrite bool
	FTPContent   string

	// S3-specific fields (for s3 type)
	S3Bucket    string
	S3Key       string
	S3Operation string // "read"
	S3Format    string // "auto", "csv", "json", "file", or "spreadsheet"
	S3Delimiter string // single character, optional
	S3HasHeader bool
	S3Sheet     string
	S3Range     string

	// Git-specific fields (for git type)
	GitPath      string
	GitOperation string // "read" or "write"
	GitFormat    string // "auto", "csv", "json", "file", or "spreadsheet"
	GitDelimiter string // single character, optional
	GitHasHeader bool
	GitSheet     string
	GitRange     string
	GitBranch    string // branch, tag, or commit SHA
	GitMessage   string // commit message for write operations
	GitContent   string
}

// QueryExecutionResultView represents execution result data rendered on the query page.
type QueryExecutionResultView struct {
	QuerySlug  string
	Type       string
	Success    bool
	DurationMs int
	Error      string
	Columns    []string
	Rows       []map[string]string
	Summary    []string
	Raw        string
}

func DefaultQueryEditorFormData() QueryEditorFormData {
	return QueryEditorFormData{
		RowLimit:             "100",
		SQLFormat:            "rows",
		WorkspaceWorksheetID: "0",
		WorkspaceRange:       "A1:C10",
		WorkspaceResource:    "spreadsheet", WorkspaceFormat: "json",
		WorkspaceOperation: "read",
		WorkspaceRows:      "{{input}}",
		TimeoutSeconds:     "30",
		Parameters:         []QueryParameterFormData{},
		HTTPMethod:         string(api.HTTPStatementMethodGET),
		HTTPPath:           "/",
		HTTPQueryParamRows: []QueryKeyValueFormData{{
			Key:   "",
			Value: "",
		}},
		HTTPHeaderRows: []QueryKeyValueFormData{{
			Key:   "",
			Value: "",
		}},
		MongoOperation:    string(api.MongoDBStatementOperationFind),
		HubSpotObjectType: "contacts",
		HubSpotRows:       "{{input}}",
		HubSpotOperation:  string(api.HubSpotStatementOperationSearch),
		FTPOperation:      queryOperationRead,
		FTPFormat:         queryFormatAuto,
		FTPOverwrite:      true,
		S3Operation:       queryOperationRead,
		S3Format:          queryFormatAuto,
		GitOperation:      queryOperationRead,
		GitFormat:         queryFormatAuto,
	}
}

// QueryActiveCount returns the number of visible non-deleted queries.
func QueryActiveCount(queries []api.Query) int {
	count := 0
	for _, query := range queries {
		if query.DeletedAt == nil {
			count++
		}
	}
	return count
}

// QueryDeletedCount returns the number of visible soft-deleted queries.
func QueryDeletedCount(queries []api.Query) int {
	count := 0
	for _, query := range queries {
		if query.DeletedAt != nil {
			count++
		}
	}
	return count
}

// QueryParameterTotal returns the number of parameters across the visible query page.
func QueryParameterTotal(queries []api.Query) int {
	count := 0
	for _, query := range queries {
		count += len(query.Parameters)
	}
	return count
}

// QuerySourceName returns the selected data-source display name for query context panels.
func QuerySourceName(dataSources []api.DataSource, selectedDataSource string) string {
	trimmed := strings.TrimSpace(selectedDataSource)
	for _, ds := range dataSources {
		if ds.Slug == trimmed {
			if name := strings.TrimSpace(ds.Name); name != "" {
				return name
			}
			return trimmed
		}
	}
	if trimmed == "" {
		return "No data source"
	}
	return trimmed
}

// DataSourceSupportsQueryEditor keeps first-result links aligned with supported editor types.
func DataSourceSupportsQueryEditor(dsType api.DataSourceType) bool {
	switch dsType {
	case api.DataSourceTypePostgres, api.DataSourceTypeMysql,
		api.DataSourceTypeBigquery, api.DataSourceTypeGoogleWorkspace,
		api.DataSourceTypeSnowflake, api.DataSourceTypeHttp, api.DataSourceTypeMongodb,
		api.DataSourceTypeHubspot, api.DataSourceTypeFtp, api.DataSourceTypeGit,
		api.DataSourceTypeGithub, api.DataSourceTypeGitlab, api.DataSourceTypeS3:
		return true
	default:
		return false
	}
}

// QuerySourceIsReadOnly reports whether the selected data source forbids saved
// mutating operations in the query editor.
func QuerySourceIsReadOnly(dataSources []api.DataSource, selectedDataSource string) bool {
	trimmed := strings.TrimSpace(selectedDataSource)
	for _, ds := range dataSources {
		if ds.Slug == trimmed {
			return ds.ReadOnly
		}
	}

	return false
}

// QuerySourceStatusLabel returns a compact selected data-source readiness label.
func QuerySourceStatusLabel(dataSources []api.DataSource, selectedDataSource string) string {
	trimmed := strings.TrimSpace(selectedDataSource)
	for _, ds := range dataSources {
		if ds.Slug == trimmed {
			return DataSourceStatusLabel(ds)
		}
	}
	return bindingCellStateUnavailable
}

// QuerySourceStatusBadgeClass returns the badge class for the selected data source.
func QuerySourceStatusBadgeClass(dataSources []api.DataSource, selectedDataSource string) string {
	trimmed := strings.TrimSpace(selectedDataSource)
	for _, ds := range dataSources {
		if ds.Slug == trimmed {
			return DataSourceStatusBadgeClass(ds)
		}
	}
	return badgeGhostOutline
}

// QueryStatusBadgeClass maps query lifecycle state to a colored badge.
func QueryStatusBadgeClass(query api.Query) string {
	if query.DeletedAt != nil {
		return "badge border-warning/50 bg-warning/15 text-warning"
	}
	return "badge border-success/50 bg-success/15 text-success"
}

// QueryContractBadgeClass maps contract state to a colored badge.
func QueryContractBadgeClass(query api.Query) string {
	if query.ExpectedInputContract != nil {
		return "badge border-info/50 bg-info/15 text-info"
	}
	return "badge border-base-content/25 bg-base-content/10 text-base-content/70"
}

// QueryPanelFeedbackClass maps embedded query action feedback to visible dark-theme alerts.
func QueryPanelFeedbackClass(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), "success") {
		return "mb-3 rounded-lg border border-success/40 bg-success/15 px-3 py-2 text-sm text-base-content"
	}
	return "mb-3 rounded-lg border border-warning/45 bg-warning/15 px-3 py-2 text-sm text-base-content"
}

// QueriesListHref builds a query library URL with optional source, query, and deleted state.
func QueriesListHref(orgSlug, dataSourceSlug, querySlug string, showDeleted bool) string {
	href := "/orgs/" + strings.TrimSpace(orgSlug) + "/queries"
	params := url.Values{}
	if dataSourceSlug = strings.TrimSpace(dataSourceSlug); dataSourceSlug != "" {
		params.Set("ds", dataSourceSlug)
	}
	if querySlug = strings.TrimSpace(querySlug); querySlug != "" {
		params.Set("query", querySlug)
	}
	if showDeleted {
		params.Set("deleted", "true")
	}
	if encoded := params.Encode(); encoded != "" {
		href += "?" + encoded
	}
	return href
}

// QueriesListCursorHref builds a paginated query library URL with encoded source and lifecycle filters.
func QueriesListCursorHref(orgSlug, dataSourceSlug string, showDeleted bool, cursor string) string {
	href := "/orgs/" + strings.TrimSpace(orgSlug) + "/queries"
	params := url.Values{}
	if dataSourceSlug = strings.TrimSpace(dataSourceSlug); dataSourceSlug != "" {
		params.Set("ds", dataSourceSlug)
	}
	if showDeleted {
		params.Set("deleted", "true")
	}
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		params.Set("cursor", cursor)
	}
	if encoded := params.Encode(); encoded != "" {
		href += "?" + encoded
	}
	return href
}

// QueryCardClass returns clickable query card styling for selected and lifecycle states.
func QueryCardClass(query api.Query, selectedQuerySlug string) string {
	base := "block min-w-0 rounded-xl border p-4 transition hover:-translate-y-0.5 hover:border-primary/55 hover:bg-primary/10 focus:outline-none focus:ring-2 focus:ring-primary/45"
	if query.Slug == strings.TrimSpace(selectedQuerySlug) {
		return base + " border-primary/60 bg-primary/15 shadow-sm shadow-primary/10"
	}
	if query.DeletedAt != nil {
		return base + " border-warning/35 bg-warning/10"
	}
	if query.ExpectedInputContract != nil {
		return base + " border-info/30 bg-info/10"
	}
	return base + " border-success/25 bg-base-100"
}

// QueryStatementKindLabel extracts the query statement discriminator for display.
func QueryStatementKindLabel(query api.Query) string {
	discriminator, err := query.QueryStatement.Discriminator()
	if err != nil || strings.TrimSpace(discriminator) == "" {
		return "unknown"
	}
	return discriminator
}

// QueryDescriptionLabel renders a stable fallback for blank query descriptions.
func QueryDescriptionLabel(query api.Query) string {
	if query.Description != nil && strings.TrimSpace(*query.Description) != "" {
		return strings.TrimSpace(*query.Description)
	}
	return "No description"
}

// QueryEditorFormDataFromQuery maps a saved API query into sticky form values.
func QueryEditorFormDataFromQuery(query api.Query) QueryEditorFormData {
	result := DefaultQueryEditorFormData()
	result.Name = query.Name
	result.Description = valueOrEmpty(query.Description)
	result.RowLimit = fmt.Sprintf("%d", query.RowLimit)
	result.TimeoutSeconds = fmt.Sprintf("%d", query.TimeoutSeconds)
	result.Parameters = make([]QueryParameterFormData, 0, len(query.Parameters))

	discriminator, err := query.QueryStatement.Discriminator()
	if err == nil {
		result.StatementType = discriminator
		switch discriminator {
		case "google_workspace":
			raw, marshalErr := json.Marshal(query.QueryStatement)
			var stmt workspace.Statement

			if marshalErr == nil && json.Unmarshal(raw, &stmt) == nil {
				if stmt.WorksheetID != nil {
					result.WorkspaceWorksheetID = fmt.Sprint(*stmt.WorksheetID)
				}
				result.WorkspaceResource = stmt.Resource
				result.WorkspaceSpreadsheetID = stmt.SpreadsheetID
				result.WorkspaceFileID = stmt.FileID
				result.WorkspaceFolderID = stmt.FolderID
				result.WorkspaceDriveID = stmt.DriveID
				result.WorkspaceName = stmt.Name
				result.WorkspaceFormat = stmt.Format
				result.WorkspaceContent = string(stmt.Content)
				result.WorkspaceMatchText = stmt.MatchText
				result.WorkspaceMIMEType = stmt.MIMEType
				result.WorkspacePageToken = stmt.PageToken
				result.WorkspaceDelimiter = stmt.Delimiter
				if stmt.HasHeader != nil {
					result.WorkspaceHasHeader = fmt.Sprint(*stmt.HasHeader)
				}
				result.WorkspaceRange = stmt.Range
				result.WorkspaceOperation = stmt.Operation
				if len(stmt.Columns) > 0 {
					if columns, err := json.Marshal(stmt.Columns); err == nil {
						result.WorkspaceColumns = string(columns)
					}
				}
				result.WorkspaceRows = string(stmt.Rows)
				if result.WorkspaceRows == `"{{input}}"` {
					result.WorkspaceRows = "{{input}}"
				}
			}

		case queryStatementTypePostgres:
			if stmt, parseErr := query.QueryStatement.AsPostgresStatement(); parseErr == nil {
				result.Statement = stmt.Sql
				if stmt.Format != nil {
					result.SQLFormat = string(*stmt.Format)
				}
			}
		case queryStatementTypeMySQL:
			if stmt, parseErr := query.QueryStatement.AsMySQLStatement(); parseErr == nil {
				result.Statement = stmt.Sql
				if stmt.Format != nil {
					result.SQLFormat = string(*stmt.Format)
				}
			}
		case queryStatementTypeBigQuery:
			if stmt, parseErr := query.QueryStatement.AsBigQueryStatement(); parseErr == nil && stmt.Sql != nil {
				result.Statement = *stmt.Sql
				if stmt.Format != nil {
					result.SQLFormat = string(*stmt.Format)
				}
			}
		case queryStatementTypeSnowflake:
			if stmt, parseErr := query.QueryStatement.AsSnowflakeStatement(); parseErr == nil {
				result.Statement = stmt.Sql
				if stmt.Format != nil {
					result.SQLFormat = string(*stmt.Format)
				}
			}
		case queryStatementTypeFTP:
			if stmt, parseErr := query.QueryStatement.AsFTPStatement(); parseErr == nil {
				result.FTPPath = stmt.Path
				if stmt.Content != nil {
					if raw, err := json.MarshalIndent(stmt.Content, "", "  "); err == nil {
						result.FTPContent = string(raw)
					}
				}
				if stmt.Operation != nil {
					result.FTPOperation = string(*stmt.Operation)
				} else {
					result.FTPOperation = queryOperationRead
				}
				if stmt.Format != nil {
					result.FTPFormat = string(*stmt.Format)
				} else {
					result.FTPFormat = queryFormatAuto
				}
				if stmt.Delimiter != nil {
					result.FTPDelimiter = *stmt.Delimiter
				}
				if stmt.HasHeader != nil {
					result.FTPHasHeader = *stmt.HasHeader
				}
				if stmt.Sheet != nil {
					result.FTPSheet = *stmt.Sheet
				}
				if stmt.Range != nil {
					result.FTPRange = *stmt.Range
				}
				result.FTPOverwrite = stmt.Overwrite == nil || *stmt.Overwrite
			}
		case queryStatementTypeGit:
			if stmt, parseErr := query.QueryStatement.AsGitStatement(); parseErr == nil {
				result.GitPath = stmt.Path
				if stmt.Content != nil {
					if raw, err := json.MarshalIndent(stmt.Content, "", "  "); err == nil {
						result.GitContent = string(raw)
					}
				}
				if stmt.Operation != nil {
					result.GitOperation = string(*stmt.Operation)
				} else {
					result.GitOperation = queryOperationRead
				}
				if stmt.Format != nil {
					result.GitFormat = string(*stmt.Format)
				} else {
					result.GitFormat = queryFormatAuto
				}
				if stmt.Delimiter != nil {
					result.GitDelimiter = *stmt.Delimiter
				}
				if stmt.HasHeader != nil {
					result.GitHasHeader = *stmt.HasHeader
				}
				if stmt.Sheet != nil {
					result.GitSheet = *stmt.Sheet
				}
				if stmt.Range != nil {
					result.GitRange = *stmt.Range
				}
				if stmt.Branch != nil {
					result.GitBranch = *stmt.Branch
				}
				if stmt.Message != nil {
					result.GitMessage = *stmt.Message
				}
			}
		case queryStatementTypeS3:
			if raw, marshalErr := json.MarshalIndent(query.QueryStatement, "", "  "); marshalErr == nil {
				result.Statement = string(raw)
			}
			if stmt, parseErr := query.QueryStatement.AsS3Statement(); parseErr == nil {
				result.S3Key = valueOrEmpty(stmt.Key)
				result.S3Bucket = valueOrEmpty(stmt.Bucket)
				if stmt.Operation != nil {
					result.S3Operation = string(*stmt.Operation)
				} else {
					result.S3Operation = queryOperationRead
				}
				if stmt.Format != nil {
					result.S3Format = string(*stmt.Format)
				} else {
					result.S3Format = queryFormatAuto
				}
				if stmt.Delimiter != nil {
					result.S3Delimiter = *stmt.Delimiter
				}
				if stmt.HasHeader != nil {
					result.S3HasHeader = *stmt.HasHeader
				}
				if stmt.Sheet != nil {
					result.S3Sheet = *stmt.Sheet
				}
				if stmt.Range != nil {
					result.S3Range = *stmt.Range
				}
			}
		case queryStatementTypeHTTP:
			if stmt, parseErr := query.QueryStatement.AsHTTPStatement(); parseErr == nil {
				result.HTTPMethod = string(stmt.Method)
				result.HTTPPath = stmt.Path
				if stmt.WriteRetry != nil {
					if raw, err := json.Marshal(stmt.WriteRetry); err == nil {
						result.HTTPWriteRetry = string(raw)
					}
				}
				if stmt.Acknowledgement != nil {
					result.HTTPAcknowledgement = httpAcknowledgementFormData(stmt.Acknowledgement)
				}
				if stmt.Pagination != nil {
					if raw, err := json.Marshal(stmt.Pagination); err == nil {
						result.HTTPPagination = string(raw)
					}
				}
				if stmt.QueryParams != nil {
					result.HTTPQueryParamRows = keyValueRowsFromAnyMap(*stmt.QueryParams)
					result.HTTPPath = httpPathWithDisplayQueryParams(stmt.Path, *stmt.QueryParams)
				}
				if stmt.Headers != nil {
					result.HTTPHeaderRows = keyValueRowsFromStringMap(*stmt.Headers)
				}
				if stmt.Body != nil {
					if raw, marshalErr := json.MarshalIndent(
						stmt.Body,
						"",
						"  ",
					); marshalErr == nil {
						result.HTTPBody = string(raw)
					}
				}
			}
		case queryStatementTypeMongoDB:
			if stmt, parseErr := query.QueryStatement.AsMongoDBStatement(); parseErr == nil {
				result.MongoCollection = stmt.Collection
				result.MongoOperation = string(stmt.Operation)
				if stmt.Filter != nil {
					if raw, marshalErr := json.MarshalIndent(
						stmt.Filter,
						"",
						"  ",
					); marshalErr == nil {
						result.MongoFilter = string(raw)
					}
				}
				if stmt.Sort != nil {
					if raw, marshalErr := json.MarshalIndent(
						stmt.Sort,
						"",
						"  ",
					); marshalErr == nil {
						result.MongoSort = string(raw)
					}
				}
				if stmt.Projection != nil {
					if raw, marshalErr := json.MarshalIndent(
						stmt.Projection,
						"",
						"  ",
					); marshalErr == nil {
						result.MongoProjection = string(raw)
					}
				}
				if stmt.LimitValue != nil {
					result.MongoLimitValue = fmt.Sprintf("%d", *stmt.LimitValue)
				}
				if stmt.Document != nil {
					if raw, marshalErr := json.MarshalIndent(
						stmt.Document,
						"",
						"  ",
					); marshalErr == nil {
						result.MongoDocument = string(raw)
					}
				}
			}
		case queryStatementTypeHubSpot:
			if stmt, parseErr := query.QueryStatement.AsHubSpotStatement(); parseErr == nil {
				result.HubSpotObjectType = stmt.ObjectType
				if stmt.Operation != nil {
					result.HubSpotOperation = string(*stmt.Operation)
				} else {
					result.HubSpotOperation = string(api.HubSpotStatementOperationSearch)
				}
				if stmt.Properties != nil {
					result.HubSpotProperties = strings.Join(*stmt.Properties, ", ")
				}
				if stmt.Query != nil {
					result.HubSpotQuery = *stmt.Query
				}
				if stmt.After != nil {
					result.HubSpotAfter = *stmt.After
				}
				if stmt.Sorts != nil {
					result.HubSpotSorts = strings.Join(*stmt.Sorts, ", ")
				}
				if stmt.FilterGroups != nil {
					if raw, marshalErr := json.MarshalIndent(
						stmt.FilterGroups,
						"",
						"  ",
					); marshalErr == nil {
						result.HubSpotFilterGroups = string(raw)
					}
				}
				if stmt.IdProperty != nil {
					result.HubSpotIDProperty = *stmt.IdProperty
				}
				if stmt.PropertyMapping != nil {
					if raw, marshalErr := json.MarshalIndent(*stmt.PropertyMapping, "", "  "); marshalErr == nil {
						result.HubSpotMapping = string(raw)
					}
				}
				if stmt.Rows != nil {
					if value, rowsErr := stmt.Rows.AsHubSpotStatementRows2(); rowsErr == nil {
						result.HubSpotRows = value
					} else if raw, marshalErr := json.Marshal(stmt.Rows); marshalErr == nil {
						result.HubSpotRows = string(raw)
					}
				}
			}
		}
	}

	for _, parameter := range query.Parameters {
		result.Parameters = append(result.Parameters, QueryParameterFormData{
			Name:         parameter.Name,
			Type:         string(parameter.DataType),
			Required:     QueryParameterRequired(parameter),
			DefaultValue: valueOrEmpty(parameter.DefaultValue),
			Description:  valueOrEmpty(parameter.Description),
			Source:       "manual",
		})
	}

	return result
}

// QueryParameterRequired returns true only when the optional API flag is explicitly set.
func QueryParameterRequired(parameter api.QueryParameter) bool {
	return parameter.IsRequired != nil && *parameter.IsRequired
}

// QueryExecutionFieldName creates a stable HTML form field name for execution inputs.
// It normalizes parameter names to alphanumeric/underscore characters and prefixes them.
func QueryExecutionFieldName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "exec_param"
	}

	var builder strings.Builder
	builder.Grow(len(trimmed) + 5)
	builder.WriteString("exec_")

	lastUnderscore := false
	for _, ch := range trimmed {
		switch {
		case unicode.IsLetter(ch) || unicode.IsDigit(ch):
			builder.WriteRune(ch)
			lastUnderscore = false
		case !lastUnderscore:
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}

	value := strings.Trim(builder.String(), "_")
	if value == "" || value == "exec" {
		return "exec_param"
	}
	return value
}

func valueOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func valueOrFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func boolText(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func keyValueRowsFromAnyMap(source map[string]any) []QueryKeyValueFormData {
	if len(source) == 0 {
		return []QueryKeyValueFormData{{Key: "", Value: ""}}
	}

	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	rows := make([]QueryKeyValueFormData, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, QueryKeyValueFormData{
			Key:   key,
			Value: stringifyHTTPMapValue(source[key]),
		})
	}

	return rows
}

func keyValueRowsFromStringMap(source map[string]string) []QueryKeyValueFormData {
	if len(source) == 0 {
		return []QueryKeyValueFormData{{Key: "", Value: ""}}
	}

	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	rows := make([]QueryKeyValueFormData, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, QueryKeyValueFormData{
			Key:   key,
			Value: source[key],
		})
	}

	return rows
}

func stringifyHTTPMapValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}

	return string(raw)
}

func httpPathWithDisplayQueryParams(path string, params map[string]any) string {
	basePath := strings.TrimSpace(path)
	if basePath == "" {
		basePath = "/"
	}

	if len(params) == 0 || strings.Contains(basePath, "?") {
		return basePath
	}

	keys := make([]string, 0, len(params))
	for key := range params {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	if len(keys) == 0 {
		return basePath
	}

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf(
			"%s=%s",
			encodeHTTPDisplayQueryComponent(key),
			encodeHTTPDisplayQueryComponent(stringifyHTTPMapValue(params[key])),
		))
	}

	return basePath + "?" + strings.Join(parts, "&")
}

func encodeHTTPDisplayQueryComponent(value string) string {
	encoded := url.QueryEscape(value)
	encoded = strings.ReplaceAll(encoded, "%7B", "{")
	encoded = strings.ReplaceAll(encoded, "%7D", "}")
	encoded = strings.ReplaceAll(encoded, "%7b", "{")
	encoded = strings.ReplaceAll(encoded, "%7d", "}")
	return encoded
}

// QueryOperationBehavior is guidance, not the server's execution authorization.
// SQL and unrecognized definitions must never imply a read-only guarantee.
func QueryOperationBehavior(query api.Query) string {
	form := QueryEditorFormDataFromQuery(query)
	switch form.StatementType {
	case "google_workspace":
		switch form.WorkspaceOperation {
		case "read":
			return "Reads Workspace data"
		case "list":
			return "Lists Workspace files"
		case "export":
			return "Exports Workspace files"
		case "create":
			return "Creates a Workspace file"
		case "update":
			return "Updates Workspace data"
		default:
			return "May write data; review the definition before running"
		}
	case queryStatementTypeHTTP:
		if form.HTTPMethod == "GET" || form.HTTPMethod == "HEAD" {
			return "Read request (" + form.HTTPMethod + ")"
		}
		return "May write data (" + form.HTTPMethod + ")"
	case queryStatementTypeMongoDB:
		switch form.MongoOperation {
		case "find", "count_documents":
			return "Read operation (" + form.MongoOperation + ")"
		default:
			return "May write data (" + form.MongoOperation + ")"
		}
	case queryStatementTypeHubSpot:
		if form.HubSpotOperation == "list" || form.HubSpotOperation == "search" {
			return "Read operation (" + form.HubSpotOperation + ")"
		}
		return "Writes CRM data (" + form.HubSpotOperation + ")"
	case queryStatementTypeFTP:
		if form.FTPOperation == "read" {
			return "Reads files"
		}
		return "Writes files; existing content may be replaced"
	case queryStatementTypeGit:
		if form.GitOperation == "read" {
			return "Reads repository files"
		}
		return "Writes repository files and creates a commit"
	case queryStatementTypeS3:
		if form.S3Operation == queryOperationRead {
			return "Reads object storage files"
		}
		return "May write object storage files"
	default:
		return "May write data; review the definition before running"
	}
}
