package run

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestValidateQueryOutputContractAtRuntime_JSONPathFields(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{Fields: []payload.ContractField{{
		Name:     "profile.email",
		Required: true,
	}}}

	tests := []struct {
		name       string
		execResult *result.ExecutionResult
		assertErr  func(t *testing.T, err error)
	}{
		{
			name: "FTPJSONNestedFieldPresent",
			execResult: result.NewFTPResult(&result.FileResult{
				Rows: []map[string]any{{
					"profile": map[string]any{"email": "user@example.com"},
				}},
				ColumnNames: []string{"profile"},
				RowCount:    1,
				Format:      "json",
			}, 0, nil),
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name: "HTTPJSONNestedFieldPresent",
			execResult: result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
				Body: []byte(`[{"profile":{"email":"user@example.com"}}]`),
			}, 0, nil),
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name: "MongoJSONNestedFieldPresent",
			execResult: result.NewMongoDBResult(&result.MongoDBResult{
				Documents: []map[string]any{{
					"profile": map[string]any{"email": "user@example.com"},
				}},
			}, 0, nil),
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name: "FTPJSONNestedFieldMissing",
			execResult: result.NewFTPResult(&result.FileResult{
				Rows: []map[string]any{{
					"profile": map[string]any{"name": "user"},
				}},
				ColumnNames: []string{"profile"},
				RowCount:    1,
				Format:      "json",
			}, 0, nil),
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
			},
		},
		{
			name: "HTTPJSONMalformedBodyFailsContractValidation",
			execResult: result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
				Body: []byte(`{"profile":`),
			}, 0, nil),
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
				require.ErrorIs(t, err, xerror.UnsupportedHTTPResponseFormat())
			},
		},
		{
			name: "HTTPUnsupportedContentTypeFailsContractValidation",
			execResult: result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers: map[string]string{
					"Content-Type": "application/xml",
				},
				Body: []byte(`<user/>`),
			}, 0, nil),
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
				require.ErrorIs(t, err, xerror.UnsupportedHTTPContentType())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateQueryOutputContractAtRuntime(tt.execResult, contract)
			tt.assertErr(t, err)
		})
	}
}

func TestValidateQueryOutputContractAtRuntime_ArrayPathRequiresAllItems(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{Fields: []payload.ContractField{{
		Name:     "items[].sku",
		Required: true,
	}}}

	execResult := result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Body: []byte(`[{"items":[{"sku":"A"},{"name":"missing"}]}]`),
	}, 0, nil)

	err := validateQueryOutputContractAtRuntime(execResult, contract)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
}

func TestValidateQueryOutputContractAtRuntime_RejectsRuntimeFileRepresentationMismatch(t *testing.T) {
	t.Parallel()

	execResult := result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       []byte(`[{"id":1}]`),
	}, 0, nil)

	err := validateQueryOutputContractAtRuntime(execResult, &payload.Contract{
		Representation: model.ContractRepresentationFile,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
	require.ErrorContains(t, err, `runtime payload representation "json"`)
}

func TestValidateQueryOutputContractAtRuntime_FieldValidations(t *testing.T) {
	t.Parallel()

	contract := loadRuntimeQueryOutputContractFromYAML(t, `metadata:
  slug: query-validation-runtime
  name: Query Validation Runtime
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-users
      output_contract:
        name: users_rows
        representation: rows
        fields:
          - name: email
            type: string
            required: true
            validations:
              format: email
          - name: status
            type: string
            required: true
            validations:
              enum:
                - active
          - name: score
            type: integer
            required: true
            validations:
              min: 10
              max: 20
`)

	execResult := result.NewSQLResult("postgres", &result.SQLResult{
		Rows: []map[string]any{{
			"email":  "not-an-email",
			"status": "disabled",
			"score":  5,
		}},
		ColumnNames: []string{"email", "status", "score"},
		RowCount:    1,
	}, 0, nil)

	err := validateQueryOutputContractAtRuntime(execResult, contract)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
	require.ErrorContains(t, err, "users_rows")
	require.ErrorContains(t, err, "email")
	require.ErrorContains(t, err, "format")
	require.NotContains(t, err.Error(), "not-an-email")
	require.NotContains(t, err.Error(), "disabled")
}

func TestValidateQueryOutputContractAtRuntime_RequiredValidatedFieldMissingInRowFailsValidation(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Name:           "http_users",
		Representation: model.ContractRepresentationJSON,
		Fields: []payload.ContractField{{
			Name:     "status",
			Type:     model.ContractFieldTypeValueString,
			Required: true,
			Validations: &payload.ContractFieldValidations{
				Enum: []any{"active"},
			},
		}},
	}
	execResult := result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       []byte(`[{"status":"active"},{}]`),
	}, 0, nil)

	err := validateQueryOutputContractAtRuntime(execResult, contract)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
	require.ErrorContains(t, err, "http_users")
	require.ErrorContains(t, err, "status")
	require.ErrorContains(t, err, "enum")
	require.NotContains(t, err.Error(), "active")
}

func TestRuntimeContracts_RequiredAndNonNullableFieldsWithoutRules(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Representation: model.ContractRepresentationJSON,
		Fields: []payload.ContractField{
			{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
			{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
		},
	}
	for _, tc := range []struct {
		name  string
		body  string
		valid bool
	}{
		{"good row", `[{"id":1,"email":"private@example.com"}]`, true},
		{"later row missing required", `[{"id":1,"email":"private@example.com"},{"id":2}]`, false},
		{"later row null non-nullable", `[{"id":1,"email":"private@example.com"},{"id":2,"email":null}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execResult := result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "application/json"},
				Body:       []byte(tc.body),
			}, 0, nil)

			queryErr := validateQueryOutputContractAtRuntime(execResult, contract)
			sinkErr := validateSinkInputContract(newStepResult(execResult), contract)
			if tc.valid {
				require.NoError(t, queryErr)
				require.NoError(t, sinkErr)
				return
			}
			require.ErrorIs(t, queryErr, ErrQueryOutputContractSchemaMismatch)
			require.ErrorIs(t, sinkErr, ErrSinkInputContractViolation)
			require.NotContains(t, queryErr.Error(), "private@example.com")
			require.NotContains(t, sinkErr.Error(), "private@example.com")
		})
	}
}

func TestValidateQueryOutputContractAtRuntime_HTTPCSVNumericValidationCoercesInferredRows(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Name: "http_invoices",
		Fields: []payload.ContractField{{
			Name:     "amount",
			Type:     model.ContractFieldTypeValueNumber,
			Required: true,
			Validations: &payload.ContractFieldValidations{
				Min: float64(1),
			},
		}},
	}
	execResult := result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "text/csv"},
		Body:       []byte("amount\n2.50\n"),
	}, 0, nil)

	err := validateQueryOutputContractAtRuntime(execResult, contract)
	require.NoError(t, err)
}

func TestValidateQueryOutputContractAtRuntime_HTTPJSONPreservesIntegerPrecision(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Name:           "http_users",
		Representation: model.ContractRepresentationJSON,
		Fields: []payload.ContractField{{
			Name:     "id",
			Type:     model.ContractFieldTypeValueInteger,
			Required: true,
			Validations: &payload.ContractFieldValidations{
				Min: json.Number("9007199254740993"),
			},
		}},
	}
	execResult := result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       []byte(`[{"id":9007199254740993}]`),
	}, 0, nil)

	err := validateQueryOutputContractAtRuntime(execResult, contract)
	require.NoError(t, err)
}

func TestValidateQueryOutputContractAtRuntime_JSONIntegerValidationRejectsStringValues(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Name:           "http_users",
		Representation: model.ContractRepresentationJSON,
		Fields: []payload.ContractField{{
			Name:     "id",
			Type:     model.ContractFieldTypeValueInteger,
			Required: true,
			Validations: &payload.ContractFieldValidations{
				Min: int64(1),
			},
		}},
	}
	execResult := result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       []byte(`[{"id":"2"}]`),
	}, 0, nil)

	err := validateQueryOutputContractAtRuntime(execResult, contract)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
	require.ErrorContains(t, err, "http_users")
	require.ErrorContains(t, err, "id")
	require.ErrorContains(t, err, "min")
	require.NotContains(t, err.Error(), `"2"`)
}

func TestValidateQueryOutputContractAtRuntime_RowsNumericValidationCoercesSerializedStrings(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Name:           "sql_invoices",
		Representation: model.ContractRepresentationRows,
		Fields: []payload.ContractField{{
			Name:     "amount",
			Type:     model.ContractFieldTypeValueNumber,
			Required: true,
			Validations: &payload.ContractFieldValidations{
				Min: float64(1),
			},
		}},
	}
	execResult := result.NewSQLResult("mysql", &result.SQLResult{
		Rows:        []map[string]any{{"amount": "2.50"}},
		ColumnNames: []string{"amount"},
		RowCount:    1,
	}, 0, nil)

	err := validateQueryOutputContractAtRuntime(execResult, contract)
	require.NoError(t, err)
}

func loadRuntimeQueryOutputContractFromYAML(t *testing.T, data string) *payload.Contract {
	t.Helper()

	pipeline, err := pipelinespec.Load("pipeline.yaml", []byte(data))
	require.NoError(t, err)
	require.Len(t, pipeline.Steps, 1)
	require.NotNil(t, pipeline.Steps[0].Query)
	require.NotNil(t, pipeline.Steps[0].Query.OutputContract)
	return pipeline.Steps[0].Query.OutputContract
}
