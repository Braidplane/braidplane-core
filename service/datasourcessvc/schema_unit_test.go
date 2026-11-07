package datasourcessvc

import (
	"encoding/json"
	"testing"

	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/nativetype"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	hubspotconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/hubspot"
	mongoconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/mongo"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
)

func TestSchemaTypeForDataSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dsType     model.DataSourceType
		wantSchema SchemaType
		wantType   string
		wantErr    error
	}{
		{name: "postgres", dsType: model.DataSourceTypePostgres, wantSchema: SchemaTypeSQL, wantType: "postgres"},
		{name: "mysql", dsType: model.DataSourceTypeMySQL, wantSchema: SchemaTypeSQL, wantType: "mysql"},
		{name: "bigquery", dsType: model.DataSourceTypeBigQuery, wantSchema: SchemaTypeSQL, wantType: "bigquery"},
		{name: "http", dsType: model.DataSourceTypeHTTP, wantSchema: SchemaTypeHTTP, wantType: "http"},
		{name: "mongodb", dsType: model.DataSourceTypeMongoDB, wantSchema: SchemaTypeDocument, wantType: "mongodb"},
		{name: "hubspot", dsType: model.DataSourceTypeHubSpot, wantSchema: SchemaTypeObjectAPI, wantType: "hubspot"},
		{name: "ftp", dsType: model.DataSourceTypeFTP, wantSchema: SchemaTypeFile, wantType: "ftp"},
		{name: "git", dsType: model.DataSourceTypeGit, wantSchema: SchemaTypeFile, wantType: "git"},
		{name: "github", dsType: model.DataSourceTypeGithub, wantSchema: SchemaTypeFile, wantType: "github"},
		{name: "gitlab", dsType: model.DataSourceTypeGitlab, wantSchema: SchemaTypeFile, wantType: "gitlab"},
		{name: "unknown", dsType: model.DataSourceTypeUnknown, wantSchema: SchemaTypeUnknown, wantErr: ErrInvalidDataSourceType},
		{name: "unsupported", dsType: model.DataSourceType(999), wantSchema: SchemaTypeUnknown, wantErr: ErrInvalidDataSourceType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			schemaType, vendorType, err := schemaTypeForDataSource(tt.dsType)

			if tt.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantSchema, schemaType)
			assert.Equal(t, tt.wantType, vendorType)
		})
	}
}

func TestSchemaResultItemsCountsBoundedTopLevelMaterialization(t *testing.T) {
	t.Parallel()

	result := Schema[FileSchemaPayload]{
		SchemaTypeValue: SchemaTypeFile,
		Payload: FileSchemaPayload{
			Files:       []SchemaFile{{Name: "a"}, {Name: "b"}},
			Diagnostics: []SchemaFileDiagnostic{{Name: "skipped"}},
		},
	}
	require.Equal(t, 3, schemaResultItems(result))
}

func TestSchemaFromDocumentFileAndHubSpot(t *testing.T) {
	t.Parallel()

	documentSchema, err := schemaFromDocument(model.DataSourceTypeMongoDB, &mongoconnector.Schema{
		Collections: []mongoconnector.Collection{
			{
				Name: "contacts",
				Fields: []mongoconnector.Field{
					{Name: "email", Type: mongoconnector.FieldTypeStr},
					{Name: "active", Type: mongoconnector.FieldTypeBoolean},
				},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, Schema[DocumentSchemaPayload]{
		SchemaTypeValue: SchemaTypeDocument,
		Type:            "mongodb",
		Payload: DocumentSchemaPayload{
			Collections: []SchemaCollection{
				{
					Name: "contacts",
					Fields: []SchemaField{
						{Name: "email", Type: "string"},
						{Name: "active", Type: "boolean"},
					},
				},
			},
		},
	}, documentSchema)

	fileSchema, err := schemaFromFileConnector(model.DataSourceTypeFTP, &filedata.Schema{
		Files: []filedata.File{
			{
				Name: "contacts.csv",
				Fields: []filedata.Field{
					{Name: "email", Type: nativetype.TypeString},
				},
			},
		},
		Diagnostics: []filedata.SchemaDiagnostic{
			{
				Name:     "bad.json",
				Target:   "imports/bad.json",
				Severity: "warning",
				Code:     "parse_failed",
				Message:  "Skipped file because fields could not be inferred from the sample.",
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, Schema[FileSchemaPayload]{
		SchemaTypeValue: SchemaTypeFile,
		Type:            "ftp",
		Payload: FileSchemaPayload{
			Files: []SchemaFile{
				{
					Name: "contacts.csv",
					Fields: []SchemaField{
						{Name: "email", Type: nativetype.TypeString},
					},
				},
			},
			Diagnostics: []SchemaFileDiagnostic{
				{
					Name:     "bad.json",
					Target:   "imports/bad.json",
					Severity: "warning",
					Code:     "parse_failed",
					Message:  "Skipped file because fields could not be inferred from the sample.",
				},
			},
		},
	}, fileSchema)

	hubspotSchema, err := schemaFromHubSpot(model.DataSourceTypeHubSpot, &hubspotconnector.Schema{
		Objects: []hubspotconnector.ObjectSchema{
			{
				Name:  "contacts",
				Label: "Contacts",
				ProviderMetadata: map[string]any{
					"custom":         false,
					"object_type_id": "0-1",
				},
				Properties: []hubspotconnector.PropertySchema{
					{
						Name:  "email",
						Label: "Email",
						Type:  "string",
						ProviderMetadata: map[string]any{
							"has_unique_value": true,
						},
					},
				},
				Operations: []hubspotconnector.OperationSchema{
					{Name: "list", Label: "List"},
					{Name: "search", Label: "Search"},
				},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, Schema[ObjectAPISchemaPayload]{
		SchemaTypeValue: SchemaTypeObjectAPI,
		Type:            "hubspot",
		Payload: ObjectAPISchemaPayload{
			Objects: []SchemaObjectAPIObject{
				{
					Name:  "contacts",
					Label: "Contacts",
					ProviderMetadata: map[string]any{
						"custom":         false,
						"object_type_id": "0-1",
					},
					Fields: []SchemaObjectAPIField{
						{
							Name:  hubspotconnector.RecordIDFieldName,
							Label: "HubSpot record ID",
							Type:  "string",
							ProviderMetadata: map[string]any{
								"source": "record_id",
							},
						},
						{
							Name:  "email",
							Label: "Email",
							Type:  "string",
							ProviderMetadata: map[string]any{
								"has_unique_value": true,
							},
						},
					},
					Operations: []SchemaObjectAPIOperation{
						{Name: "list", Label: "List"},
						{Name: "search", Label: "Search"},
					},
				},
			},
		},
	}, hubspotSchema)
}

func TestSchemaFromHTTPAndSQLAndNilVariants(t *testing.T) {
	t.Parallel()

	httpSchema, err := schemaFromHTTP(model.DataSourceTypeHTTP, nil)
	require.NoError(t, err)
	assert.Equal(t, Schema[HTTPSchemaPayload]{
		SchemaTypeValue: SchemaTypeHTTP,
		Type:            "http",
		Payload:         HTTPSchemaPayload{Operations: []SchemaOperation{}},
	}, httpSchema)

	sqlSchema, err := schemaFromSQL(model.DataSourceTypePostgres, &sqlconnector.Schema{
		Tables: []sqlconnector.Table{
			{
				Name: "users",
				Columns: []sqlconnector.Column{
					{Name: "id", Type: "integer", Nullable: false},
				},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, Schema[SQLSchemaPayload]{
		SchemaTypeValue: SchemaTypeSQL,
		Type:            "postgres",
		Payload: SQLSchemaPayload{
			Tables: []SchemaTable{
				{
					Name: "users",
					Columns: []SchemaColumn{
						{Name: "id", Type: "integer", Nullable: false},
					},
				},
			},
		},
	}, sqlSchema)

	bigQuerySchema, err := schemaFromSQL(model.DataSourceTypeBigQuery, &sqlconnector.Schema{
		Tables: []sqlconnector.Table{
			{
				Name: "analytics.orders",
				Columns: []sqlconnector.Column{
					{Name: "order_id", Type: "STRING", Nullable: false},
					{Name: "total_amount", Type: "NUMERIC", Nullable: true},
				},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, Schema[SQLSchemaPayload]{
		SchemaTypeValue: SchemaTypeSQL,
		Type:            "bigquery",
		Payload: SQLSchemaPayload{
			Tables: []SchemaTable{
				{
					Name: "analytics.orders",
					Columns: []SchemaColumn{
						{Name: "order_id", Type: "STRING", Nullable: false},
						{Name: "total_amount", Type: "NUMERIC", Nullable: true},
					},
				},
			},
		},
	}, bigQuerySchema)

	fileSchema, err := schemaFromFileConnector(model.DataSourceTypeGit, nil)
	require.NoError(t, err)
	assert.Equal(t, Schema[FileSchemaPayload]{
		SchemaTypeValue: SchemaTypeFile,
		Type:            "git",
		Payload:         FileSchemaPayload{Files: []SchemaFile{}},
	}, fileSchema)

	hubspotSchema, err := schemaFromHubSpot(model.DataSourceTypeHubSpot, nil)
	require.NoError(t, err)
	assert.Equal(t, Schema[ObjectAPISchemaPayload]{
		SchemaTypeValue: SchemaTypeObjectAPI,
		Type:            "hubspot",
		Payload:         ObjectAPISchemaPayload{Objects: []SchemaObjectAPIObject{}},
	}, hubspotSchema)
}

func TestEmptySchemaForType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dsType  model.DataSourceType
		wantErr error
	}{
		{name: "sql", dsType: model.DataSourceTypePostgres},
		{name: "warehouse", dsType: model.DataSourceTypeBigQuery},
		{name: "http", dsType: model.DataSourceTypeHTTP},
		{name: "document", dsType: model.DataSourceTypeMongoDB},
		{name: "file", dsType: model.DataSourceTypeFTP},
		{name: "hubspot", dsType: model.DataSourceTypeHubSpot},
		{name: "unknown", dsType: model.DataSourceTypeUnknown, wantErr: ErrInvalidDataSourceType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := emptySchemaForType(tt.dsType)

			if tt.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
		})
	}
}

func TestDecodeSchemaJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   string
		assertion func(t *testing.T, result SchemaResult)
		wantErr   error
	}{
		{
			name:    "sql",
			payload: `{"schema_type":"sql","type":"postgres","payload":{"tables":[{"name":"users","columns":[{"name":"id","type":"integer"}]}]}}`,
			assertion: func(t *testing.T, result SchemaResult) {
				t.Helper()
				typed, ok := result.(Schema[SQLSchemaPayload])
				require.True(t, ok)
				assert.Equal(t, "users", typed.Payload.Tables[0].Name)
			},
		},
		{
			name:    "http",
			payload: `{"schema_type":"http","type":"http","payload":{"operations":[{"id":"listUsers","path":"/users","method":"GET","request_body":{},"responses":{}}]}}`,
			assertion: func(t *testing.T, result SchemaResult) {
				t.Helper()
				typed, ok := result.(Schema[HTTPSchemaPayload])
				require.True(t, ok)
				assert.Equal(t, "/users", typed.Payload.Operations[0].Path)
			},
		},
		{
			name:    "document",
			payload: `{"schema_type":"document","type":"mongodb","payload":{"collections":[{"name":"contacts","fields":[{"name":"email","type":"string"}]}]}}`,
			assertion: func(t *testing.T, result SchemaResult) {
				t.Helper()
				typed, ok := result.(Schema[DocumentSchemaPayload])
				require.True(t, ok)
				assert.Equal(t, "contacts", typed.Payload.Collections[0].Name)
			},
		},
		{
			name:    "file",
			payload: `{"schema_type":"file","type":"ftp","payload":{"files":[{"name":"contacts.csv","fields":[{"name":"email","type":"string"}]}]}}`,
			assertion: func(t *testing.T, result SchemaResult) {
				t.Helper()
				typed, ok := result.(Schema[FileSchemaPayload])
				require.True(t, ok)
				assert.Equal(t, "contacts.csv", typed.Payload.Files[0].Name)
			},
		},
		{
			name:    "object_api",
			payload: `{"schema_type":"object_api","type":"hubspot","payload":{"objects":[{"name":"contacts","label":"Contacts","fields":[{"name":"email","label":"Email","type":"string"}],"operations":[{"name":"search","label":"Search"}]}]}}`,
			assertion: func(t *testing.T, result SchemaResult) {
				t.Helper()
				typed, ok := result.(Schema[ObjectAPISchemaPayload])
				require.True(t, ok)
				assert.Equal(t, "contacts", typed.Payload.Objects[0].Name)
				assert.Equal(t, "email", typed.Payload.Objects[0].Fields[0].Name)
				assert.Equal(t, "search", typed.Payload.Objects[0].Operations[0].Name)
			},
		},
		{
			name:    "invalid envelope json",
			payload: `{"schema_type":`,
			wantErr: xerror.Unexpected(),
		},
		{
			name:    "invalid payload json",
			payload: `{"schema_type":"sql","type":"postgres","payload":{"tables":"bad"}}`,
			wantErr: xerror.Unexpected(),
		},
		{
			name:    "unsupported schema type",
			payload: `{"schema_type":"unknown","type":"x","payload":{}}`,
			wantErr: ErrInvalidDataSourceType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := decodeSchemaJSON(json.RawMessage(tt.payload))

			if tt.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			tt.assertion(t, result)
		})
	}
}

func TestHTTPDraftSurvivesCachedSchemaRoundTrip(t *testing.T) {
	draft := json.RawMessage(`{"statement":{"type":"http","method":"GET","path":"/users"},"parameters":[]}`)
	schema, err := schemaFromHTTP(model.DataSourceTypeHTTP, &httpconnector.HTTPSchema{Operations: []httpconnector.Operation{{OperationID: "listUsers", Method: "GET", Path: "/users", Draft: draft}, {OperationID: "upload", Method: "POST", Path: "/upload", Diagnostics: []string{"unsupported body"}}}})
	require.NoError(t, err)
	encoded, err := json.Marshal(schema)
	require.NoError(t, err)
	decoded, err := decodeSchemaJSON(encoded)
	require.NoError(t, err)
	typed := decoded.(Schema[HTTPSchemaPayload])
	require.JSONEq(t, string(draft), string(typed.Payload.Operations[0].Draft))
	require.Equal(t, []string{"unsupported body"}, typed.Payload.Operations[1].Diagnostics)
}
