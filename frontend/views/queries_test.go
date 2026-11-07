package views

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestQuerySourceNameUsesSlugForUnnamedSource(t *testing.T) {
	t.Parallel()
	sources := []api.DataSource{{Slug: "warehouse", Name: "  "}}
	require.Equal(t, "warehouse", QuerySourceName(sources, "warehouse"))
}

func TestQueriesEmptyStateProvidesPermissionAwareFirstResultAction(t *testing.T) {
	t.Parallel()
	for _, canCreate := range []bool{true, false} {
		permissions := DefaultOrganizationPermissions()
		if canCreate {
			permissions = NewOrganizationPermissions("datasources:create")
		}
		var out bytes.Buffer
		require.NoError(t, QueriesPageContent(nil, "", "", nil, nil, QueryEditorFormData{}, false, "", "", "acme", QueryEditorFormData{}, nil, "", "", false, permissions).Render(context.Background(), &out))
		require.Contains(t, out.String(), "Get your first result")
		require.Contains(t, out.String(), "You do not need a project or Git repository.")
		if canCreate {
			require.Contains(t, out.String(), `href="/orgs/acme/data-sources"`)
			require.Contains(t, out.String(), ">Connect a system</a>")
		} else {
			require.NotContains(t, out.String(), ">Connect a system</a>")
			require.Contains(t, out.String(), "Ask a workspace admin")
		}
	}
}

func TestQueryOperationBehaviorWorkspace(t *testing.T) {
	t.Parallel()
	for _, resource := range []string{"document", "presentation"} {
		for operation, expected := range map[string]string{"read": "Reads Workspace data", "export": "Exports Workspace files", "create": "Creates a Workspace file", "update": "Updates Workspace data", "unknown": "May write data; review the definition before running"} {
			t.Run(resource+"/"+operation, func(t *testing.T) {
				var query api.Query
				require.NoError(t, json.Unmarshal([]byte(`{"query_statement":{"type":"google_workspace","resource":"`+resource+`","operation":"`+operation+`","file_id":"synthetic"}}`), &query))
				require.Equal(t, expected, QueryOperationBehavior(query))
			})
		}
	}
}

func TestQueriesPageContentCanonicalizesGitProviderStatementType(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := QueriesPageContent(
		nil,
		"repo",
		"github",
		nil,
		nil,
		QueryEditorFormData{},
		false,
		"",
		"",
		"acme",
		QueryEditorFormData{},
		nil,
		"",
		"",
		false,
		NewOrganizationPermissions("queries:create"),
	).Render(context.Background(), &out)
	require.NoError(t, err)

	body := out.String()
	require.Contains(t, body, `name="statement_type" value="git"`)
	require.NotContains(t, body, `name="statement_type" value="github"`)
}

func TestGitQueryFormsExposeNativeFileFormat(t *testing.T) {
	t.Parallel()

	formData := DefaultQueryEditorFormData()
	formData.StatementType = "git"
	formData.GitFormat = "file"
	permissions := NewOrganizationPermissions("queries:create", "queries:update")

	var createOut bytes.Buffer
	require.NoError(t, QueriesPageContent(
		nil, "repo", "git", nil, nil, QueryEditorFormData{}, false, "", "", "acme",
		formData, nil, "", "", false, permissions,
	).Render(context.Background(), &createOut))

	var editOut bytes.Buffer
	require.NoError(t, QueryDetailPageContent(
		"acme", "repo", api.Query{Name: "Native file", Slug: "native-file"},
		formData, nil, "", "", permissions,
	).Render(context.Background(), &editOut))

	for _, html := range []string{createOut.String(), editOut.String()} {
		require.Contains(t, html, `<option value="file" selected>File</option>`)
	}
}

func TestQueriesPageContentRendersSelectedDataSourceIconReference(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := QueriesPageContent(
		[]api.DataSource{{
			IsActive: true,
			Name:     "Warehouse",
			Slug:     "warehouse",
			Type:     api.DataSourceTypePostgres,
		}},
		"warehouse",
		"postgres",
		nil,
		nil,
		QueryEditorFormData{},
		false,
		"",
		"",
		"acme",
		QueryEditorFormData{},
		nil,
		"",
		"",
		false,
		DefaultOrganizationPermissions(),
	).Render(context.Background(), &out)
	require.NoError(t, err)

	requireDataSourceIconMarker(t, out.String(), "postgres")
}

func TestQueriesPageContentKeepsHubSpotUpsertForWritableDataSource(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, QueriesPageContent(
		[]api.DataSource{{
			IsActive: true,
			Name:     "CRM",
			Slug:     "crm",
			Type:     api.DataSourceTypeHubspot,
			ReadOnly: false,
		}},
		"crm",
		"hubspot",
		nil,
		nil,
		QueryEditorFormData{},
		false,
		"",
		"",
		"acme",
		DefaultQueryEditorFormData(),
		nil,
		"",
		"",
		false,
		NewOrganizationPermissions("queries:create"),
	).Render(context.Background(), &out))

	require.Contains(t, out.String(), `value="upsert"`)
	require.Contains(t, out.String(), "key-based upsert sink")
}

func TestQueryPagesUseContentVersionedEditorScript(t *testing.T) {
	t.Parallel()

	permissions := NewOrganizationPermissions("queries:create", "queries:update")
	var listOut bytes.Buffer
	require.NoError(t, QueriesPageContent(
		nil,
		"warehouse",
		"postgres",
		nil,
		nil,
		QueryEditorFormData{},
		false,
		"",
		"",
		"acme",
		QueryEditorFormData{},
		nil,
		"",
		"",
		false,
		permissions,
	).Render(context.Background(), &listOut))

	var detailOut bytes.Buffer
	require.NoError(t, QueryDetailPageContent(
		"acme",
		"warehouse",
		api.Query{Name: "List users", Slug: "list-users"},
		QueryEditorFormData{},
		nil,
		"",
		"",
		permissions,
	).Render(context.Background(), &detailOut))

	for _, html := range []string{listOut.String(), detailOut.String()} {
		require.Contains(t, html, `src="/assets/js/query_param_editor.js?v=`)
		require.NotContains(t, html, `src="/assets/js/query_param_editor.js"`)
	}
}

func TestS3QueryDefinitionVisibleWithAndWithoutEditPermission(t *testing.T) {
	t.Parallel()
	const saved = `{"type":"s3","operation":"write","key":"exports/orders.csv","format":"csv","content":"{{input}}","overwrite":false,"metadata":{"source":"fixture"}}`
	var statement api.QueryStatement
	require.NoError(t, statement.UnmarshalJSON([]byte(saved)))
	query := api.Query{Name: "Write orders", Slug: "write-orders", QueryStatement: statement}
	form := QueryEditorFormDataFromQuery(query)
	require.Equal(t, "s3", form.StatementType)
	require.JSONEq(t, saved, form.Statement)
	for _, canEdit := range []bool{true, false} {
		permissions := NewOrganizationPermissions("queries:read")
		if canEdit {
			permissions = NewOrganizationPermissions("queries:read", "queries:update")
		}
		var out bytes.Buffer
		require.NoError(t, QueryDetailPageContent("acme", "store", query, form, nil, "", "", permissions).Render(context.Background(), &out))
		document := html.UnescapeString(out.String())
		require.Contains(t, document, form.Statement)
		require.Contains(t, document, "exports/orders.csv")
		if canEdit {
			require.Contains(t, document, `name="statement"`)
			require.Contains(t, document, "S3 operation JSON")
		} else {
			require.Contains(t, document, "Query definition (read-only)")
			require.NotContains(t, document, `name="statement"`)
		}
	}
}

func TestQueryDetailReadOnlyViewShowsHubSpotUpsertDefinition(t *testing.T) {
	t.Parallel()

	formData := DefaultQueryEditorFormData()
	formData.StatementType = "hubspot"
	formData.HubSpotObjectType = "contacts"
	formData.HubSpotOperation = "upsert"
	formData.HubSpotIDProperty = "external_customer_id"
	formData.HubSpotRows = "{{input}}"
	formData.HubSpotMapping = `{"customer_id":"external_customer_id"}`

	var out bytes.Buffer
	require.NoError(t, QueryDetailPageContent(
		"acme",
		"hubspot-crm",
		api.Query{Name: "Upsert contacts", Slug: "upsert-contacts"},
		formData,
		nil,
		"",
		"",
		NewOrganizationPermissions(),
	).Render(context.Background(), &out))

	html := out.String()
	require.Contains(t, html, "Query definition (read-only)")
	require.Contains(t, html, "Upsert ID property")
	require.Contains(t, html, "external_customer_id")
	require.Contains(t, html, "Rows binding")
	require.Contains(t, html, "{{input}}")
	require.Contains(t, html, "Property mapping")
	require.Contains(t, html, "customer_id")
}

func TestQueryFormsIncludeSpreadsheetFieldsInParameterSync(t *testing.T) {
	t.Parallel()

	permissions := NewOrganizationPermissions("queries:create", "queries:update")
	testCases := []struct {
		name               string
		dataSourceType     string
		createFormData     QueryEditorFormData
		editFormData       QueryEditorFormData
		wantCreateSnippets []string
		wantEditSnippets   []string
	}{
		{
			name:           "FTP",
			dataSourceType: "ftp",
			createFormData: QueryEditorFormData{
				Name:           "FTP spreadsheet",
				StatementType:  "ftp",
				FTPPath:        "/exports/{{run_date}}.xlsx",
				FTPFormat:      "spreadsheet",
				FTPSheet:       "{{sheet_name}}",
				FTPRange:       "A1:D{{end_row}}",
				RowLimit:       "100",
				TimeoutSeconds: "30",
			},
			editFormData: QueryEditorFormData{
				StatementType:  "ftp",
				FTPPath:        "/exports/{{run_date}}.xlsx",
				FTPFormat:      "spreadsheet",
				FTPSheet:       "{{sheet_name}}",
				FTPRange:       "A1:D{{end_row}}",
				RowLimit:       "100",
				TimeoutSeconds: "30",
			},
			wantCreateSnippets: []string{
				`name="ftp_sheet" value="{{sheet_name}}" placeholder="Orders" data-param-source-input`,
				`name="ftp_range" value="A1:D{{end_row}}" placeholder="A1:D200" data-param-source-input`,
			},
			wantEditSnippets: []string{
				`name="ftp_sheet" value="{{sheet_name}}" placeholder="Orders" data-param-source-input`,
				`name="ftp_range" value="A1:D{{end_row}}" placeholder="A1:D200" data-param-source-input`,
			},
		},
		{
			name:           "Git",
			dataSourceType: "git",
			createFormData: QueryEditorFormData{
				Name:           "Git spreadsheet",
				StatementType:  "git",
				GitPath:        "exports/{{run_date}}.xlsx",
				GitFormat:      "spreadsheet",
				GitSheet:       "{{sheet_name}}",
				GitRange:       "A1:D{{end_row}}",
				RowLimit:       "100",
				TimeoutSeconds: "30",
			},
			editFormData: QueryEditorFormData{
				StatementType:  "git",
				GitPath:        "exports/{{run_date}}.xlsx",
				GitFormat:      "spreadsheet",
				GitSheet:       "{{sheet_name}}",
				GitRange:       "A1:D{{end_row}}",
				RowLimit:       "100",
				TimeoutSeconds: "30",
			},
			wantCreateSnippets: []string{
				`name="git_sheet" value="{{sheet_name}}" placeholder="Orders" data-param-source-input`,
				`name="git_range" value="A1:D{{end_row}}" placeholder="A1:D200" data-param-source-input`,
			},
			wantEditSnippets: []string{
				`name="git_sheet" value="{{sheet_name}}" placeholder="Orders" data-param-source-input`,
				`name="git_range" value="A1:D{{end_row}}" placeholder="A1:D200" data-param-source-input`,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var createOut bytes.Buffer
			require.NoError(t, QueriesPageContent(
				[]api.DataSource{{Name: "Spreadsheet source", Slug: "spreadsheet-source"}},
				"spreadsheet-source",
				tc.dataSourceType,
				nil,
				nil,
				QueryEditorFormData{},
				false,
				"",
				"",
				"acme",
				tc.createFormData,
				nil,
				"",
				"",
				false,
				permissions,
			).Render(context.Background(), &createOut))
			createHTML := createOut.String()
			for _, snippet := range tc.wantCreateSnippets {
				require.Contains(t, createHTML, snippet)
			}

			var editOut bytes.Buffer
			require.NoError(t, QueryDetailPageContent(
				"acme",
				"spreadsheet-source",
				api.Query{Name: "Spreadsheet query", Slug: "spreadsheet-query"},
				tc.editFormData,
				nil,
				"",
				"",
				permissions,
			).Render(context.Background(), &editOut))
			editHTML := editOut.String()
			for _, snippet := range tc.wantEditSnippets {
				require.Contains(t, editHTML, snippet)
			}
		})
	}
}

func TestQueryFormsRenderBooleanAttributes(t *testing.T) {
	t.Parallel()

	permissions := NewOrganizationPermissions("queries:create", "queries:update")
	var listOut bytes.Buffer
	require.NoError(t, QueriesPageContent(
		[]api.DataSource{
			{Name: "CRM", Slug: "crm", Type: api.DataSourceTypeHubspot, IsActive: true, ReadOnly: true},
			{Name: "Warehouse", Slug: "warehouse", Type: api.DataSourceTypeGit, IsActive: true},
		},
		"warehouse",
		"git",
		nil,
		nil,
		QueryEditorFormData{},
		false,
		"",
		"",
		"acme",
		DefaultQueryEditorFormData(),
		nil,
		"",
		"",
		false,
		permissions,
	).Render(context.Background(), &listOut))
	listHTML := listOut.String()
	require.NotContains(t, listHTML, ` selected="false"`)
	require.NotContains(t, listHTML, `checked="false"`)
	require.Contains(t, listHTML, `<option selected value="warehouse">Warehouse</option>`)
	require.Contains(t, listHTML, `<option value="crm">CRM</option>`)
	require.Contains(t, listHTML, `name="git_has_header" value="on">`)

	var detailOut bytes.Buffer
	formData := DefaultQueryEditorFormData()
	formData.StatementType = "git"
	formData.GitHasHeader = true
	formData.Parameters = []QueryParameterFormData{
		{Name: "limit", Type: string(api.QueryParameterDataTypeInteger), Required: true, Source: "manual"},
		{Name: "is_active", Type: string(api.QueryParameterDataTypeBoolean), Required: false, Source: "manual"},
	}
	require.NoError(t, QueryDetailPageContent(
		"acme",
		"warehouse",
		api.Query{Name: "List users", Slug: "list-users"},
		formData,
		nil,
		"",
		"",
		permissions,
	).Render(context.Background(), &detailOut))
	detailHTML := detailOut.String()
	require.NotContains(t, detailHTML, ` selected="false"`)
	require.NotContains(t, detailHTML, `checked="false"`)
	require.Contains(t, detailHTML, `<option selected value="integer">integer</option>`)
	require.Contains(t, detailHTML, `<option selected value="boolean">boolean</option>`)
	require.Contains(t, detailHTML, `<option selected value="true">required</option>`)
	require.Contains(t, detailHTML, `<option selected value="false">optional</option>`)
	require.Contains(t, detailHTML, `name="git_has_header" value="on" checked>`)
}

func TestQueryEditorFormDataFromQuery_HTTPStatement(t *testing.T) {
	t.Parallel()

	queryStatement := api.QueryStatement{}
	err := queryStatement.FromHTTPStatement(api.HTTPStatement{
		Pagination:      &api.HTTPPagination{CursorParam: new("after"), RecordsPath: "data.items", NextCursorPath: new("paging.next"), MaxPages: 3, TimeoutSeconds: 2},
		Acknowledgement: &api.HTTPAcknowledgement{StatusCodes: []int{200}, Success: []api.HTTPAcknowledgementCheck{{Path: "status.code", Equals: json.RawMessage(`"OK"`)}}},
		Method:          api.HTTPStatementMethodGET,
		Path:            "/items",
		QueryParams: &map[string]any{
			"status": "{{status}}",
		},
	})
	require.NoError(t, err)

	query := api.Query{
		Name:           "HTTP query",
		RowLimit:       100,
		TimeoutSeconds: 30,
		QueryStatement: queryStatement,
	}

	formData := QueryEditorFormDataFromQuery(query)
	require.Equal(t, HTTPAcknowledgementFormData{Enabled: true, StatusCodes: "200", Success: []QueryKeyValueFormData{{Key: "status.code", Value: `"OK"`}}}, formData.HTTPAcknowledgement)
	require.JSONEq(t, `{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.next","max_pages":3,"timeout_seconds":2}`, formData.HTTPPagination)
	require.Equal(t, "http", formData.StatementType)
	require.Equal(t, "GET", formData.HTTPMethod)
	require.Equal(t, "/items?status={{status}}", formData.HTTPPath)
	require.Len(t, formData.HTTPQueryParamRows, 1)
	require.Equal(t, "status", formData.HTTPQueryParamRows[0].Key)
	require.Equal(t, "{{status}}", formData.HTTPQueryParamRows[0].Value)
}

func TestQueryEditorFormDataFromQuery_MongoStatement(t *testing.T) {
	t.Parallel()

	queryStatement := api.QueryStatement{}
	err := queryStatement.FromMongoDBStatement(api.MongoDBStatement{
		Collection: "orders",
		Operation:  api.MongoDBStatementOperationFind,
		Filter: &map[string]any{
			"status": "{{status}}",
		},
	})
	require.NoError(t, err)

	query := api.Query{
		Name:           "Mongo query",
		RowLimit:       100,
		TimeoutSeconds: 30,
		QueryStatement: queryStatement,
	}

	formData := QueryEditorFormDataFromQuery(query)
	require.Equal(t, "mongodb", formData.StatementType)
	require.Equal(t, "orders", formData.MongoCollection)
	require.Equal(t, "find", formData.MongoOperation)
	require.Contains(t, formData.MongoFilter, `"status": "{{status}}"`)
}

func TestQueryEditorFormDataFromQuery_HubSpotStatement(t *testing.T) {
	t.Parallel()

	operation := api.HubSpotStatementOperationSearch
	properties := []string{"email", "firstname"}
	queryText := "{{search_term}}"
	after := "200"
	sorts := []string{"-createdate"}
	filterGroups := []api.HubSpotFilterGroup{{
		Filters: []api.HubSpotFilter{{
			PropertyName: "email",
			Operator:     api.HubSpotFilterOperator("EQ"),
			Value:        "{{search_term}}",
		}},
	}}

	queryStatement := api.QueryStatement{}
	err := queryStatement.FromHubSpotStatement(api.HubSpotStatement{
		Type:         api.HubSpotStatementTypeHubspot,
		ObjectType:   "contacts",
		Operation:    &operation,
		Properties:   &properties,
		Query:        &queryText,
		After:        &after,
		Sorts:        &sorts,
		FilterGroups: &filterGroups,
	})
	require.NoError(t, err)

	query := api.Query{
		Name:           "HubSpot contacts",
		RowLimit:       100,
		TimeoutSeconds: 30,
		QueryStatement: queryStatement,
	}

	formData := QueryEditorFormDataFromQuery(query)
	require.Equal(t, "hubspot", formData.StatementType)
	require.Equal(t, "contacts", formData.HubSpotObjectType)
	require.Equal(t, "search", formData.HubSpotOperation)
	require.Equal(t, "email, firstname", formData.HubSpotProperties)
	require.Equal(t, "{{search_term}}", formData.HubSpotQuery)
	require.Equal(t, "200", formData.HubSpotAfter)
	require.Equal(t, "-createdate", formData.HubSpotSorts)
	require.Contains(t, formData.HubSpotFilterGroups, `"property_name": "email"`)
}

func TestQueryEditorFormDataFromQuery_FTPStatement(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		stmt       api.FTPStatement
		assertData func(t *testing.T, data QueryEditorFormData)
	}{
		{
			name: "MinimalPathOnly",
			stmt: api.FTPStatement{
				Type: api.FTPStatementTypeFtp,
				Path: "exports/{{runtime.run.date}}/customers.csv",
			},
			assertData: func(t *testing.T, data QueryEditorFormData) {
				t.Helper()
				require.Equal(t, "ftp", data.StatementType)
				require.Equal(t, "exports/{{runtime.run.date}}/customers.csv", data.FTPPath)
				require.Equal(t, "read", data.FTPOperation)
				require.Equal(t, "auto", data.FTPFormat)
				require.True(t, data.FTPOverwrite)
			},
		},
		{
			name: "AllFieldsPopulated",
			stmt: func() api.FTPStatement {
				op := api.FTPStatementOperationWrite
				format := api.FTPStatementFormatSpreadsheet
				delim := ";"
				hasHeader := true
				overwrite := false
				sheet := "Orders"
				cellRange := "A1:D20"
				return api.FTPStatement{
					Type:      api.FTPStatementTypeFtp,
					Path:      "/out/report.xlsx",
					Operation: &op,
					Format:    &format,
					Delimiter: &delim,
					HasHeader: &hasHeader,
					Sheet:     &sheet,
					Range:     &cellRange,
					Overwrite: &overwrite,
				}
			}(),
			assertData: func(t *testing.T, data QueryEditorFormData) {
				t.Helper()
				require.Equal(t, "/out/report.xlsx", data.FTPPath)
				require.Equal(t, "write", data.FTPOperation)
				require.Equal(t, "spreadsheet", data.FTPFormat)
				require.Equal(t, ";", data.FTPDelimiter)
				require.True(t, data.FTPHasHeader)
				require.Equal(t, "Orders", data.FTPSheet)
				require.Equal(t, "A1:D20", data.FTPRange)
				require.False(t, data.FTPOverwrite)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryStatement := api.QueryStatement{}
			require.NoError(t, queryStatement.FromFTPStatement(tc.stmt))

			query := api.Query{
				Name:           "FTP query",
				RowLimit:       100,
				TimeoutSeconds: 30,
				QueryStatement: queryStatement,
			}

			tc.assertData(t, QueryEditorFormDataFromQuery(query))
		})
	}
}

func TestQueryEditorFormDataFromQuery_S3ReadStatement(t *testing.T) {
	t.Parallel()

	bucket, key := "archive", "exports/customers.xlsx"
	operation := api.S3StatementOperationRead
	format := api.S3StatementFormatSpreadsheet
	delimiter, sheet, cellRange := ";", "Customers", "A1:D20"
	hasHeader := true
	statement := api.QueryStatement{}
	require.NoError(t, statement.FromS3Statement(api.S3Statement{
		Type:      api.S3StatementTypeS3,
		Bucket:    &bucket,
		Key:       &key,
		Operation: &operation,
		Format:    &format,
		Delimiter: &delimiter,
		HasHeader: &hasHeader,
		Sheet:     &sheet,
		Range:     &cellRange,
	}))

	formData := QueryEditorFormDataFromQuery(api.Query{QueryStatement: statement})
	require.Equal(t, "s3", formData.StatementType)
	require.Equal(t, bucket, formData.S3Bucket)
	require.Equal(t, key, formData.S3Key)
	require.Equal(t, "read", formData.S3Operation)
	require.Equal(t, "spreadsheet", formData.S3Format)
	require.Equal(t, delimiter, formData.S3Delimiter)
	require.True(t, formData.S3HasHeader)
	require.Equal(t, sheet, formData.S3Sheet)
	require.Equal(t, cellRange, formData.S3Range)
}

func TestS3WriteQueryRetainsCompleteEditorAndRunControls(t *testing.T) {
	t.Parallel()

	key := "exports/customers.json"
	operation := api.S3StatementOperationWrite
	format := api.S3StatementFormatJson
	metadata := map[string]string{"source": "pipeline"}
	var content api.S3Statement_Content
	require.NoError(t, json.Unmarshal([]byte(`[{"id":1}]`), &content))
	statement := api.QueryStatement{}
	require.NoError(t, statement.FromS3Statement(api.S3Statement{
		Type:      api.S3StatementTypeS3,
		Key:       &key,
		Operation: &operation,
		Format:    &format,
		Content:   &content,
		Metadata:  &metadata,
	}))
	query := api.Query{Name: "Export customers", Slug: "export-customers", QueryStatement: statement}

	var out bytes.Buffer
	require.NoError(t, QueryDetailPageContent(
		"acme", "archive", query, QueryEditorFormDataFromQuery(query), nil, "", "",
		NewOrganizationPermissions("queries:update", "queries:execute"),
	).Render(context.Background(), &out))

	html := out.String()
	require.Contains(t, html, `name="statement"`)
	require.Contains(t, html, "S3 operation JSON")
	require.Contains(t, html, `id="query-update-form"`)
	require.NotContains(t, html, `name="s3_operation"`)
	require.Contains(t, html, `id="query-execute-form"`)
	require.Contains(t, html, `id="query-run-btn"`)
}

func TestQueryEditorFormDataFromQuery_GitStatement(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		stmt       api.GitStatement
		assertData func(t *testing.T, data QueryEditorFormData)
	}{
		{
			name: "MinimalPathOnly",
			stmt: api.GitStatement{
				Type: api.GitStatementTypeGit,
				Path: "data/{{release_date}}/manifest.json",
			},
			assertData: func(t *testing.T, data QueryEditorFormData) {
				t.Helper()
				require.Equal(t, "git", data.StatementType)
				require.Equal(t, "data/{{release_date}}/manifest.json", data.GitPath)
				require.Equal(t, "read", data.GitOperation)
				require.Equal(t, "auto", data.GitFormat)
			},
		},
		{
			name: "AllFieldsPopulated",
			stmt: func() api.GitStatement {
				op := api.GitStatementOperationWrite
				format := api.GitStatementFormatSpreadsheet
				delim := ","
				hasHeader := true
				branch := "release/v2"
				msg := "Update export"
				sheet := "Orders"
				cellRange := "A1:D20"
				return api.GitStatement{
					Type:      api.GitStatementTypeGit,
					Path:      "output/report.xlsx",
					Operation: &op,
					Format:    &format,
					Delimiter: &delim,
					HasHeader: &hasHeader,
					Sheet:     &sheet,
					Range:     &cellRange,
					Branch:    &branch,
					Message:   &msg,
				}
			}(),
			assertData: func(t *testing.T, data QueryEditorFormData) {
				t.Helper()
				require.Equal(t, "output/report.xlsx", data.GitPath)
				require.Equal(t, "write", data.GitOperation)
				require.Equal(t, "spreadsheet", data.GitFormat)
				require.Equal(t, ",", data.GitDelimiter)
				require.True(t, data.GitHasHeader)
				require.Equal(t, "Orders", data.GitSheet)
				require.Equal(t, "A1:D20", data.GitRange)
				require.Equal(t, "release/v2", data.GitBranch)
				require.Equal(t, "Update export", data.GitMessage)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			queryStatement := api.QueryStatement{}
			require.NoError(t, queryStatement.FromGitStatement(tc.stmt))

			query := api.Query{
				Name:           "Git query",
				RowLimit:       100,
				TimeoutSeconds: 30,
				QueryStatement: queryStatement,
			}

			tc.assertData(t, QueryEditorFormDataFromQuery(query))
		})
	}
}

func TestQueryQuickLookKeepsDefinitionsAndExecutionOnFullPage(t *testing.T) {
	for _, kind := range []string{"postgres", "http", "hubspot", "git"} {
		t.Run(kind, func(t *testing.T) {
			query := api.Query{Name: "Saved operation", Slug: "saved-operation", RowLimit: 100, TimeoutSeconds: 30}
			switch kind {
			case "postgres":
				require.NoError(t, query.QueryStatement.FromPostgresStatement(api.PostgresStatement{Type: api.PostgresStatementTypePostgres, Sql: "SELECT secret_definition"}))
			case "http":
				require.NoError(t, query.QueryStatement.FromHTTPStatement(api.HTTPStatement{Type: api.HTTPStatementTypeHttp, Method: api.HTTPStatementMethodPOST, Path: "/secret_definition"}))
			case "hubspot":
				op := api.HubSpotStatementOperationUpsert
				require.NoError(t, query.QueryStatement.FromHubSpotStatement(api.HubSpotStatement{Type: api.HubSpotStatementTypeHubspot, ObjectType: "contacts", Operation: &op}))
			case "git":
				op := api.GitStatementOperationWrite
				require.NoError(t, query.QueryStatement.FromGitStatement(api.GitStatement{Type: api.GitStatementTypeGit, Path: "secret_definition", Operation: &op}))
			}
			var out bytes.Buffer
			ctx := WithEntityPageURL(context.Background(), "/orgs/acme/queries?ds=source&query=saved-operation&cursor=next")
			require.NoError(t, QueryQuickLook("acme", "source", query).Render(ctx, &out))
			html := out.String()
			require.Contains(t, html, "100 rows · 30s timeout")
			require.Contains(t, strings.ToLower(html), "write")
			require.Contains(t, html, "Open full page")
			require.Contains(t, html, "cursor%3Dnext")
			for _, unwanted := range []string{"<form", "<input", "<textarea", "<table", "secret_definition", "query-execution-feedback"} {
				require.NotContains(t, html, unwanted)
			}
		})
	}
}

func TestQueryDetailReadOnlyViewShowsWorkspaceDefinition(t *testing.T) {
	t.Parallel()
	form := DefaultQueryEditorFormData()
	form.StatementType = "google_workspace"
	form.WorkspaceWorksheetID = "12"
	form.WorkspaceRange = "B2:C3"
	form.WorkspaceOperation = "read"
	var out bytes.Buffer
	require.NoError(t, QueryDetailPageContent("acme", "sheet", api.Query{Name: "Orders", Slug: "orders"}, form, nil, "", "", NewOrganizationPermissions()).Render(context.Background(), &out))
	for _, value := range []string{"Query definition (read-only)", "Worksheet ID", "12", "Range", "B2:C3", "Operation", "read"} {
		require.Contains(t, out.String(), value)
	}
	require.NotContains(t, out.String(), `name="workspace_worksheet_id"`)
}

func TestHTTPQueryFormsPreservePagination(t *testing.T) {
	form := DefaultQueryEditorFormData()
	form.StatementType = "http"
	form.HTTPMethod = "GET"
	form.HTTPPath = "/items"
	form.HTTPAcknowledgement = HTTPAcknowledgementFormData{Enabled: true, StatusCodes: "200", Success: []QueryKeyValueFormData{{Key: "status.code", Value: `"OK"`}}}
	form.HTTPPagination = `{"cursor_param":"after","records_path":"items","next_cursor_path":"next","max_pages":3,"timeout_seconds":2}`
	form.HTTPWriteRetry = `{"verified":true,"provider_contract_url":"https://provider.invalid/idempotency","key_header":"Idempotency-Key","retention_seconds":3600,"payload_conflict":"reject"}`
	permissions := NewOrganizationPermissions("queries:create", "queries:update")
	var create, edit bytes.Buffer
	require.NoError(t, QueriesPageContent(nil, "api", "http", nil, nil, QueryEditorFormData{}, false, "", "", "acme", form, nil, "", "", false, permissions).Render(context.Background(), &create))
	require.NoError(t, QueryDetailPageContent("acme", "api", api.Query{Name: "Items", Slug: "items"}, form, nil, "", "", permissions).Render(context.Background(), &edit))
	for _, page := range []string{create.String(), edit.String()} {
		require.Contains(t, page, `name="http_acknowledgement_enabled" checked`)
		require.Contains(t, page, `name="http_acknowledgement_path" value="status.code"`)
		require.Contains(t, page, `name="http_acknowledgement_equals" value="`+html.EscapeString(`"OK"`)+`"`)
		require.Contains(t, page, `name="http_pagination"`)
		require.Contains(t, page, html.EscapeString(form.HTTPPagination)+`</textarea>`)
		require.Contains(t, page, `name="http_write_retry"`)
		require.Contains(t, page, html.EscapeString(form.HTTPWriteRetry)+`</textarea>`)
	}
}

func TestHTTPAcknowledgementNumberSurvivesQueryForm(t *testing.T) {
	var statement api.QueryStatement
	require.NoError(t, json.Unmarshal([]byte(`{"type":"http","method":"POST","path":"/records","acknowledgement":{"status_codes":[200],"success":[{"path":"code","equals":1.0}]}}`), &statement))
	form := QueryEditorFormDataFromQuery(api.Query{QueryStatement: statement})
	require.Equal(t, "1.0", form.HTTPAcknowledgement.Success[0].Value)
}
