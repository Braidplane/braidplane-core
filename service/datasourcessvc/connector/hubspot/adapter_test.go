package hubspot

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	hubspotclient "github.com/Braidplane/braidplane-core/pkg/hubspot/crm"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type stubClient struct {
	accountDetailsFunc          func(context.Context) (*hubspotclient.AccountDetails, error)
	listPropertiesFunc          func(context.Context, string) ([]hubspotclient.Property, error)
	listCustomObjectSchemasFunc func(context.Context) ([]hubspotclient.CustomObjectSchema, error)
	listFunc                    func(context.Context, hubspotclient.ListRequest) (*hubspotclient.Page, error)
	searchFunc                  func(context.Context, hubspotclient.SearchRequest) (*hubspotclient.Page, error)
	upsertFunc                  func(context.Context, hubspotclient.UpsertRequest) (*hubspotclient.UpsertResult, error)
	closeFunc                   func() error
}

type boundedCustomSchemaStub struct {
	*stubClient
	called bool
}

func (s *boundedCustomSchemaStub) ListCustomObjectSchemasBounded(context.Context, int64) ([]hubspotclient.CustomObjectSchema, error) {
	s.called = true
	return nil, nil
}

func (s *stubClient) Upsert(
	ctx context.Context,
	req hubspotclient.UpsertRequest,
) (*hubspotclient.UpsertResult, error) {
	if s.upsertFunc == nil {
		return nil, assert.AnError
	}
	return s.upsertFunc(ctx, req)
}

func (s *stubClient) ListCustomObjectSchemas(
	ctx context.Context,
) ([]hubspotclient.CustomObjectSchema, error) {
	if s.listCustomObjectSchemasFunc == nil {
		return nil, nil
	}
	return s.listCustomObjectSchemasFunc(ctx)
}

func (s *stubClient) AccountDetails(ctx context.Context) (*hubspotclient.AccountDetails, error) {
	if s.accountDetailsFunc == nil {
		return nil, assert.AnError
	}
	return s.accountDetailsFunc(ctx)
}

func (s *stubClient) ListProperties(
	ctx context.Context,
	objectType string,
) ([]hubspotclient.Property, error) {
	if s.listPropertiesFunc == nil {
		return nil, assert.AnError
	}
	return s.listPropertiesFunc(ctx, objectType)
}

func (s *stubClient) List(
	ctx context.Context,
	req hubspotclient.ListRequest,
) (*hubspotclient.Page, error) {
	if s.listFunc == nil {
		return nil, assert.AnError
	}
	return s.listFunc(ctx, req)
}

func (s *stubClient) Search(
	ctx context.Context,
	req hubspotclient.SearchRequest,
) (*hubspotclient.Page, error) {
	if s.searchFunc == nil {
		return nil, assert.AnError
	}
	return s.searchFunc(ctx, req)
}

func (s *stubClient) Close() error {
	if s.closeFunc == nil {
		return nil
	}
	return s.closeFunc()
}

func TestConnector_Execute_SearchDelegatesResolvedStatementToClientAndNormalizesRows(t *testing.T) {
	t.Parallel()

	client := &stubClient{
		searchFunc: func(
			_ context.Context,
			req hubspotclient.SearchRequest,
		) (*hubspotclient.Page, error) {
			assert.Equal(t, "contacts", req.ObjectType)
			assert.Equal(t, []string{"email", "firstname"}, req.Properties)
			assert.Equal(t, "alice@example.com", req.Query)
			assert.Equal(t, "cursor-200", req.After)
			assert.Equal(t, []string{"-createdate"}, req.Sorts)
			assert.Equal(t, 2, req.Limit)
			require.Len(t, req.FilterGroups, 1)
			require.Len(t, req.FilterGroups[0].Filters, 1)
			assert.Equal(t, "email", req.FilterGroups[0].Filters[0].PropertyName)
			assert.Equal(t, "EQ", req.FilterGroups[0].Filters[0].Operator)
			assert.Equal(t, "alice@example.com", req.FilterGroups[0].Filters[0].Value)

			nextAfter := "cursor-201"
			return &hubspotclient.Page{
				ObjectType: "contacts",
				Records: []hubspotclient.Record{
					{
						ID: "101",
						Properties: map[string]any{
							"email":     "alice@example.com",
							"firstname": "Alice",
						},
					},
				},
				NextAfter: &nextAfter,
				HasMore:   true,
			}, nil
		},
	}

	conn := newWithClient(client)
	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{
			"type": "hubspot",
			"object_type": "contacts",
			"operation": "search",
			"properties": ["email", "firstname"],
			"query": "{{search_term}}",
			"after": "{{cursor}}",
			"sorts": ["{{sort_field}}"],
			"limit": 2,
			"filter_groups": [
				{
					"filters": [
						{
							"property_name": "email",
							"operator": "EQ",
							"value": "{{search_term}}"
						}
					]
				}
			]
		}`),
		map[string]types.TypedParam{
			"search_term": {Value: "alice@example.com", DataType: "text"},
			"cursor":      {Value: "cursor-200", DataType: "text"},
			"sort_field":  {Value: "-createdate", DataType: "text"},
		},
	)
	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.Equal(t, result.ExecutionResultTypeObjectRows, execResult.Type())
	require.Equal(t, "hubspot", execResult.VendorType())

	hubspotResult, ok := execResult.ObjectRows()
	require.True(t, ok)
	require.NotNil(t, hubspotResult)
	assert.Equal(t, "contacts", hubspotResult.ObjectType)
	assert.Equal(t, "search", hubspotResult.Operation)
	assert.Equal(t, []string{"hs_object_id", "email", "firstname"}, hubspotResult.ColumnNames)
	assert.Equal(t, 1, hubspotResult.RowCount)
	require.Len(t, hubspotResult.Rows, 1)
	assert.Equal(t, "101", hubspotResult.Rows[0]["hs_object_id"])
	assert.Equal(t, "alice@example.com", hubspotResult.Rows[0]["email"])
	require.NotNil(t, hubspotResult.NextCursor)
	assert.Equal(t, "cursor-201", *hubspotResult.NextCursor)
	assert.True(t, hubspotResult.HasMore)
}

func TestConnector_Execute_ListBindsIncrementalCursorAsAfter(t *testing.T) {
	t.Parallel()

	client := &stubClient{
		listFunc: func(
			_ context.Context,
			req hubspotclient.ListRequest,
		) (*hubspotclient.Page, error) {
			assert.Equal(t, "contacts", req.ObjectType)
			assert.Equal(t, "cursor-200", req.After)

			nextAfter := "cursor-201"
			return &hubspotclient.Page{
				ObjectType: "contacts",
				Records: []hubspotclient.Record{{
					ID: "101",
					Properties: map[string]any{
						"email": "alice@example.com",
					},
				}},
				NextAfter: &nextAfter,
				HasMore:   true,
			}, nil
		},
	}

	conn := newWithClient(client)
	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{
			"type": "hubspot",
			"object_type": "contacts",
			"operation": "list",
			"properties": ["email"],
			"after": "{{runtime.incremental.lower_bound.cursor}}",
			"limit": 100
		}`),
		map[string]types.TypedParam{
			"runtime.incremental.lower_bound.cursor": {
				Value:    "cursor-200",
				DataType: "text",
			},
		},
	)

	require.NoError(t, err)
	hubspotResult, ok := execResult.ObjectRows()
	require.True(t, ok)
	require.NotNil(t, hubspotResult.NextCursor)
	assert.Equal(t, "cursor-201", *hubspotResult.NextCursor)
}

func TestConnector_IntrospectSchema_DelegatesToClient(t *testing.T) {
	t.Parallel()

	conn := newWithClient(&stubClient{
		listPropertiesFunc: func(_ context.Context, objectType string) ([]hubspotclient.Property, error) {
			switch objectType {
			case "contacts":
				return []hubspotclient.Property{{Name: "email", Label: "Email", Type: "string"}}, nil
			case "companies", "deals":
				return []hubspotclient.Property{}, nil
			default:
				return nil, assert.AnError
			}
		},
		listCustomObjectSchemasFunc: func(context.Context) ([]hubspotclient.CustomObjectSchema, error) {
			return []hubspotclient.CustomObjectSchema{{
				ObjectTypeID: "2-7282133",
				Name:         "cars",
				Label:        "Cars",
				Properties: []hubspotclient.Property{{
					Name:           "vin",
					Label:          "VIN",
					Type:           "string",
					HasUniqueValue: true,
				}},
			}}, nil
		},
	})

	schema, err := conn.IntrospectSchema(context.Background())
	require.NoError(t, err)
	require.NotNil(t, schema)
	require.Len(t, schema.Objects, 4)
	assert.Equal(t, "contacts", schema.Objects[0].Name)
	assert.Equal(t, "Email", schema.Objects[0].Properties[0].Label)
	require.Len(t, schema.Objects[0].Operations, 3)
	assert.Equal(t, "list", schema.Objects[0].Operations[0].Name)
	assert.Equal(t, "search", schema.Objects[0].Operations[1].Name)
	assert.Equal(t, "upsert", schema.Objects[0].Operations[2].Name)
	assert.Equal(t, "2-7282133", schema.Objects[3].Name)
	assert.Equal(t, "Cars", schema.Objects[3].Label)
	assert.Equal(t, true, schema.Objects[3].ProviderMetadata["custom"])
	assert.Equal(t, "cars", schema.Objects[3].ProviderMetadata["provider_name"])
	assert.Equal(t, true, schema.Objects[3].Properties[0].ProviderMetadata["has_unique_value"])
}

func TestConnector_Execute_CustomObjectValidatesDiscoveryBeforeSearch(t *testing.T) {
	t.Parallel()

	client := &stubClient{
		listCustomObjectSchemasFunc: func(context.Context) ([]hubspotclient.CustomObjectSchema, error) {
			return []hubspotclient.CustomObjectSchema{{
				ObjectTypeID: "2-7282133",
				Name:         "cars",
				Label:        "Cars",
			}}, nil
		},
		searchFunc: func(
			_ context.Context,
			req hubspotclient.SearchRequest,
		) (*hubspotclient.Page, error) {
			assert.Equal(t, "2-7282133", req.ObjectType)
			return &hubspotclient.Page{
				ObjectType: "2-7282133",
				Records: []hubspotclient.Record{{
					ID:         "501",
					Properties: map[string]any{"vin": "VIN-001"},
				}},
			}, nil
		},
	}

	conn := newWithClientAndScopes(client, []string{"crm.objects.custom.read"})
	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{
			"type": "hubspot",
			"object_type": "2-7282133",
			"operation": "search",
			"properties": ["vin"]
		}`),
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, execResult.Error())
	hubspotResult, ok := execResult.ObjectRows()
	require.True(t, ok)
	assert.Equal(t, "2-7282133", hubspotResult.ObjectType)
	assert.Equal(t, "VIN-001", hubspotResult.Rows[0]["vin"])
}

func TestConnector_Execute_RejectsUndiscoveredCustomObject(t *testing.T) {
	t.Parallel()

	conn := newWithClient(&stubClient{
		listCustomObjectSchemasFunc: func(context.Context) ([]hubspotclient.CustomObjectSchema, error) {
			return []hubspotclient.CustomObjectSchema{}, nil
		},
		searchFunc: func(context.Context, hubspotclient.SearchRequest) (*hubspotclient.Page, error) {
			t.Fatal("search must not run for an undiscovered custom object")
			return &hubspotclient.Page{}, nil
		},
	})

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{
			"type": "hubspot",
			"object_type": "2-999",
			"operation": "search"
		}`),
		nil,
	)
	require.NoError(t, err)
	require.Error(t, execResult.Error())
	require.ErrorIs(t, execResult.Error(), xerror.InvalidInput(xerror.Query))
	assert.ErrorContains(t, execResult.Error(), "refresh the data source schema")
}

func TestConnector_Execute_CustomObjectRequiresOAuthScope(t *testing.T) {
	t.Parallel()

	conn := newWithClientAndScopes(&stubClient{}, []string{"crm.objects.contacts.read"})
	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{
			"type": "hubspot",
			"object_type": "2-7282133",
			"operation": "list"
		}`),
		nil,
	)
	require.NoError(t, err)
	require.Error(t, execResult.Error())
	require.ErrorIs(t, execResult.Error(), xerror.InvalidInput(xerror.Query))
	assert.ErrorContains(t, execResult.Error(), customObjectReadScope)
}

func TestConnector_Execute_OAuthWithoutScopeMetadataFailsClosed(t *testing.T) {
	t.Parallel()

	conn := newWithClientAndScopes(&stubClient{}, []string{})
	execResult, err := conn.Execute(
		t.Context(),
		json.RawMessage(`{
			"type":"hubspot",
			"object_type":"contacts",
			"operation":"upsert",
			"id_property":"email",
			"property_mapping":{"email":"email"},
			"rows":[{"email":"alice@example.com"}]
		}`),
		nil,
	)
	require.NoError(t, err)
	require.Error(t, execResult.Error())
	require.ErrorIs(t, execResult.Error(), xerror.InvalidInput(xerror.Query))
	assert.ErrorContains(t, execResult.Error(), "crm.objects.contacts.read")
}

func TestConnector_Execute_UpsertValidatesSchemaAndMapsRows(t *testing.T) {
	t.Parallel()

	client := &stubClient{
		listPropertiesFunc: func(_ context.Context, objectType string) ([]hubspotclient.Property, error) {
			assert.Equal(t, "contacts", objectType)
			return []hubspotclient.Property{
				{Name: "external_customer_id", HasUniqueValue: true},
				{Name: "email"},
			}, nil
		},
		upsertFunc: func(_ context.Context, req hubspotclient.UpsertRequest) (*hubspotclient.UpsertResult, error) {
			require.Len(t, req.Inputs, 2)
			assert.Equal(t, "customer-1", req.Inputs[0].ID)
			assert.Equal(t, "external_customer_id", req.Inputs[0].IDProperty)
			assert.Equal(t, map[string]any{"email": "alice@example.com"}, req.Inputs[0].Properties)
			return &hubspotclient.UpsertResult{Records: []hubspotclient.UpsertRecord{
				{ID: "101", Created: true},
				{ID: "102", Created: false},
			}}, nil
		},
	}
	conn := newWithClientAndScopes(client, []string{
		"crm.objects.contacts.read",
		"crm.objects.contacts.write",
	})
	execResult, err := conn.Execute(
		t.Context(),
		json.RawMessage(`{
			"type":"hubspot",
			"object_type":"contacts",
			"operation":"upsert",
			"id_property":"external_customer_id",
			"property_mapping":{"customer_id":"external_customer_id","email":"email"},
			"rows":"{{input}}"
		}`),
		map[string]types.TypedParam{
			"input": {Value: []any{
				map[string]any{"customer_id": "customer-1", "email": "alice@example.com"},
				map[string]any{"customer_id": "customer-2", "email": "bob@example.com"},
			}},
		},
	)
	require.NoError(t, err)
	require.NoError(t, execResult.Error())
	rows, ok := execResult.ObjectRows()
	require.True(t, ok)
	require.Len(t, rows.Rows, 2)
	assert.Equal(t, "101", rows.Rows[0][RecordIDFieldName])
	assert.Equal(t, true, rows.Rows[0]["created"])
}

func TestConnector_Execute_UpsertBatchesInputsAndReportsLaterFailure(t *testing.T) {
	t.Parallel()

	call := 0
	client := &stubClient{
		listPropertiesFunc: func(context.Context, string) ([]hubspotclient.Property, error) {
			return []hubspotclient.Property{{Name: "external_customer_id", HasUniqueValue: true}}, nil
		},
		upsertFunc: func(_ context.Context, req hubspotclient.UpsertRequest) (*hubspotclient.UpsertResult, error) {
			call++
			if call == 2 {
				return nil, xerror.Unavailable(xerror.DataSource)
			}
			records := make([]hubspotclient.UpsertRecord, len(req.Inputs))
			for index := range records {
				records[index] = hubspotclient.UpsertRecord{ID: fmt.Sprintf("record-%d", index)}
			}
			return &hubspotclient.UpsertResult{Records: records}, nil
		},
	}
	rows := make([]any, hubspotclient.MaxBatchUpsertInputs+1)
	for index := range rows {
		rows[index] = map[string]any{"customer_id": fmt.Sprintf("customer-%d", index)}
	}
	conn := newWithClientAndScopes(client, []string{"crm.objects.contacts.read", "crm.objects.contacts.write"})
	execResult, err := conn.Execute(t.Context(), json.RawMessage(`{
		"type":"hubspot","object_type":"contacts","operation":"upsert",
		"id_property":"external_customer_id",
		"property_mapping":{"customer_id":"external_customer_id"},
		"rows":"{{input}}"
	}`), map[string]types.TypedParam{"input": {Value: rows}})
	require.NoError(t, err)
	require.Error(t, execResult.Error())
	require.ErrorContains(t, execResult.Error(), "prior batch completed")
	assert.Equal(t, 2, call)
}

func TestConnector_Execute_CustomObjectUpsertReusesDiscoveredSchema(t *testing.T) {
	t.Parallel()

	discoveryCalls := 0
	client := &stubClient{
		listCustomObjectSchemasFunc: func(context.Context) ([]hubspotclient.CustomObjectSchema, error) {
			discoveryCalls++
			return []hubspotclient.CustomObjectSchema{{
				ObjectTypeID: "2-7282133",
				Properties:   []hubspotclient.Property{{Name: "vin", HasUniqueValue: true}},
			}}, nil
		},
		upsertFunc: func(_ context.Context, req hubspotclient.UpsertRequest) (*hubspotclient.UpsertResult, error) {
			return &hubspotclient.UpsertResult{Records: []hubspotclient.UpsertRecord{{ID: "record-1"}}}, nil
		},
	}
	conn := newWithClientAndScopes(client, []string{"crm.objects.custom.read", "crm.objects.custom.write"})
	execResult, err := conn.Execute(t.Context(), json.RawMessage(`{
		"type":"hubspot","object_type":"2-7282133","operation":"upsert",
		"id_property":"vin","property_mapping":{"vin":"vin"},"rows":[{"vin":"VIN-1"}]
	}`), nil)
	require.NoError(t, err)
	require.NoError(t, execResult.Error())
	assert.Equal(t, 1, discoveryCalls)
}

func TestConnector_CustomObjectDiscoveryFailsWhenSchemaByteQuotaIsExhausted(t *testing.T) {
	t.Parallel()

	client := &boundedCustomSchemaStub{stubClient: &stubClient{}}
	conn := newWithClientAndScopes(client, []string{"crm.objects.custom.read"})
	ctx := schemaquota.WithQuota(t.Context(), 1, 0)
	execResult, err := conn.Execute(ctx, json.RawMessage(`{
		"type":"hubspot","object_type":"2-7282133","operation":"list"
	}`), nil)
	require.NoError(t, err)
	require.Error(t, execResult.Error())
	require.ErrorContains(t, execResult.Error(), "configured byte limit")
	assert.False(t, client.called)
}

func TestConnector_Execute_UpsertFailsClosedBeforeDispatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		scopes     []string
		mapping    string
		wantDetail string
	}{
		{
			name:       "missing write scope",
			scopes:     []string{"crm.objects.contacts.read"},
			mapping:    `{"customer_id":"external_customer_id"}`,
			wantDetail: "crm.objects.contacts.write",
		},
		{
			name:       "read only target",
			scopes:     []string{"crm.objects.contacts.read", "crm.objects.contacts.write"},
			mapping:    `{"customer_id":"external_customer_id","calculated":"hs_calculated"}`,
			wantDetail: "read-only",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &stubClient{
				listPropertiesFunc: func(context.Context, string) ([]hubspotclient.Property, error) {
					return []hubspotclient.Property{
						{Name: "external_customer_id", HasUniqueValue: true},
						{Name: "hs_calculated", ReadOnlyValue: true},
					}, nil
				},
				upsertFunc: func(context.Context, hubspotclient.UpsertRequest) (*hubspotclient.UpsertResult, error) {
					t.Fatal("upsert must not dispatch after validation failure")
					return nil, assert.AnError
				},
			}
			conn := newWithClientAndScopes(client, tt.scopes)
			statementJSON := fmt.Sprintf(`{
				"type":"hubspot",
				"object_type":"contacts",
				"operation":"upsert",
				"id_property":"external_customer_id",
				"property_mapping":%s,
				"rows":[{"customer_id":"customer-1","calculated":"value"}]
			}`, tt.mapping)
			execResult, err := conn.Execute(t.Context(), json.RawMessage(statementJSON), nil)
			require.NoError(t, err)
			require.Error(t, execResult.Error())
			assert.ErrorContains(t, execResult.Error(), tt.wantDetail)
		})
	}
}

func TestBuildUpsertInputs_RejectsUnsafeKeysAndUnmappedFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statement  statement
		rows       []map[string]any
		properties []hubspotclient.Property
		wantDetail string
		wantID     string
	}{
		{
			name: "large native float id",
			statement: statement{
				IDProperty:      "external_customer_id",
				PropertyMapping: map[string]string{"customer_id": "external_customer_id"},
			},
			rows: []map[string]any{{"customer_id": float64(1234567890123456)}},
			properties: []hubspotclient.Property{{
				Name:           "external_customer_id",
				HasUniqueValue: true,
			}},
			wantID: "1234567890123456",
		},
		{
			name: "non unique id property",
			statement: statement{
				IDProperty:      "external_customer_id",
				PropertyMapping: map[string]string{"customer_id": "external_customer_id"},
			},
			rows:       []map[string]any{{"customer_id": "customer-1"}},
			properties: []hubspotclient.Property{{Name: "external_customer_id"}},
			wantDetail: "unique-value constraint",
		},
		{
			name: "unmapped row field",
			statement: statement{
				IDProperty:      "external_customer_id",
				PropertyMapping: map[string]string{"customer_id": "external_customer_id"},
			},
			rows: []map[string]any{{"customer_id": "customer-1", "email": "alice@example.com"}},
			properties: []hubspotclient.Property{{
				Name:           "external_customer_id",
				HasUniqueValue: true,
			}},
			wantDetail: "unmapped input field",
		},
		{
			name: "duplicate id value",
			statement: statement{
				IDProperty:      "external_customer_id",
				PropertyMapping: map[string]string{"customer_id": "external_customer_id"},
			},
			rows: []map[string]any{
				{"customer_id": "customer-1"},
				{"customer_id": "customer-1"},
			},
			properties: []hubspotclient.Property{{
				Name:           "external_customer_id",
				HasUniqueValue: true,
			}},
			wantDetail: "duplicate id_property value",
		},
		{
			name: "object id value",
			statement: statement{
				IDProperty:      "external_customer_id",
				PropertyMapping: map[string]string{"customer_id": "external_customer_id"},
			},
			rows: []map[string]any{{"customer_id": map[string]any{"nested": "value"}}},
			properties: []hubspotclient.Property{{
				Name:           "external_customer_id",
				HasUniqueValue: true,
			}},
			wantDetail: "missing id field",
		},
		{
			name: "fractional float id value",
			statement: statement{
				IDProperty:      "external_customer_id",
				PropertyMapping: map[string]string{"customer_id": "external_customer_id"},
			},
			rows: []map[string]any{{"customer_id": 12.5}},
			properties: []hubspotclient.Property{{
				Name:           "external_customer_id",
				HasUniqueValue: true,
			}},
			wantDetail: "missing id field",
		},
		{
			name: "inexact float id value",
			statement: statement{
				IDProperty:      "external_customer_id",
				PropertyMapping: map[string]string{"customer_id": "external_customer_id"},
			},
			rows: []map[string]any{{"customer_id": float64(9007199254740992)}},
			properties: []hubspotclient.Property{{
				Name:           "external_customer_id",
				HasUniqueValue: true,
			}},
			wantDetail: "missing id field",
		},
		{
			name: "array id value",
			statement: statement{
				IDProperty:      "external_customer_id",
				PropertyMapping: map[string]string{"customer_id": "external_customer_id"},
			},
			rows: []map[string]any{{"customer_id": []any{"nested"}}},
			properties: []hubspotclient.Property{{
				Name:           "external_customer_id",
				HasUniqueValue: true,
			}},
			wantDetail: "missing id field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inputs, err := buildUpsertInputs("contacts", tt.statement, tt.rows, tt.properties)
			if tt.wantID != "" {
				require.NoError(t, err)
				require.Len(t, inputs, 1)
				assert.Equal(t, tt.wantID, inputs[0].ID)
				return
			}
			require.Nil(t, inputs)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantDetail)
		})
	}
}
