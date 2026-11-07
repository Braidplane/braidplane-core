package frontend

import (
	"encoding/json"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestOpenAPIOperationForm(t *testing.T) {
	var statement api.HTTPStatement
	require.NoError(t, json.Unmarshal([]byte(`{"type":"http","method":"GET","path":"/users/{{path_id}}","query_params":{"limit":"{{query_limit}}"}}`), &statement))
	required := true
	operation := api.OperationInfo{Id: "getUser", Method: api.OperationInfoMethodGET, Path: "/users/{id}", Draft: &api.HTTPOperationDraft{Statement: statement, Parameters: []api.QueryParameter{{Name: "path_id", DataType: api.QueryParameterDataTypeText, IsRequired: &required}}}}
	source := &api.DataSource{Type: api.DataSourceTypeHttp}
	setOperations := func(ops ...api.OperationInfo) {
		require.NoError(t, source.Schema.FromHTTPSchemaResponse(api.HTTPSchemaResponse{Operations: ops}))
	}
	setOperations(operation)
	form, diagnostic := openAPIOperationForm(source, "getUser")
	require.Empty(t, diagnostic)
	require.Equal(t, "getUser", form.Name)
	require.Equal(t, "/users/{{path_id}}?limit={{query_limit}}", form.HTTPPath)
	require.Equal(t, "path_id", form.Parameters[0].Name)
	require.True(t, form.Parameters[0].Required)
	require.Empty(t, form.Parameters[0].DefaultValue)
	_, diagnostic = openAPIOperationForm(source, "missing")
	require.Contains(t, diagnostic, "unavailable")
	setOperations(operation, operation)
	_, diagnostic = openAPIOperationForm(source, "getUser")
	require.Contains(t, diagnostic, "unique")
	operation.Draft = nil
	setOperations(operation)
	_, diagnostic = openAPIOperationForm(source, "getUser")
	require.Contains(t, diagnostic, "Refresh")
	diagnostics := []string{"Cookie parameters are unsupported."}
	operation.Diagnostics = &diagnostics
	setOperations(operation)
	_, diagnostic = openAPIOperationForm(source, "getUser")
	require.Equal(t, diagnostics[0], diagnostic)
	source.Type = api.DataSourceTypePostgres
	_, diagnostic = openAPIOperationForm(source, "getUser")
	require.Contains(t, diagnostic, "HTTP data source")
}
