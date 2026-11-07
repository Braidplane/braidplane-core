package frontend

import (
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
)

func openAPIOperationForm(source *api.DataSource, operationID string) (views.QueryEditorFormData, string) {
	empty := views.DefaultQueryEditorFormData()
	if source == nil || source.Type != api.DataSourceTypeHttp {
		return empty, "Select an HTTP data source with an imported OpenAPI schema."
	}
	schema, err := source.Schema.AsHTTPSchemaResponse()
	if err != nil {
		return empty, "Refresh the OpenAPI schema before creating a query draft."
	}
	var selected *api.OperationInfo
	for i := range schema.Operations {
		if schema.Operations[i].Id == operationID {
			if selected != nil {
				return empty, "Operation IDs must be unique. Correct the OpenAPI document and refresh the schema."
			}
			selected = &schema.Operations[i]
		}
	}
	if selected == nil {
		return empty, "The imported operation is unavailable. Refresh the schema and select it again."
	}
	if selected.Diagnostics != nil && len(*selected.Diagnostics) > 0 {
		return empty, strings.Join(*selected.Diagnostics, " ")
	}
	if selected.Draft == nil {
		return empty, "Refresh the OpenAPI schema to generate query drafts for this import."
	}
	statement := api.QueryStatement{}
	if err := statement.FromHTTPStatement(selected.Draft.Statement); err != nil {
		return empty, "The imported operation draft could not be read. Refresh the schema."
	}
	form := views.QueryEditorFormDataFromQuery(api.Query{
		Name:           selected.Id,
		QueryStatement: statement,
		Parameters:     selected.Draft.Parameters,
		RowLimit:       1000,
		TimeoutSeconds: 30,
	})
	form.Description = "Imported OpenAPI operation. Review parameters and request body before saving. Pipelines can reference the saved query with operation_ref."
	return form, ""
}
