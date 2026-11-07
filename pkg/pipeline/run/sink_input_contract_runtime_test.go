package run

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	formpayload "github.com/Braidplane/braidplane-core/pkg/payload/form"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestValidateSinkInputContract_JSONPathFields(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{Fields: []payload.ContractField{{
		Name:     "profile.email",
		Required: true,
	}}}

	tests := []struct {
		name      string
		source    *StepResult
		assertErr func(t *testing.T, err error)
	}{
		{
			name: "FTPJSONNestedFieldPresent",
			source: newStepResult(result.NewFTPResult(&result.FileResult{
				Rows: []map[string]any{{
					"profile": map[string]any{"email": "user@example.com"},
				}},
				ColumnNames: []string{"profile"},
				RowCount:    1,
				Format:      "json",
			}, 0, nil)),
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name: "HTTPJSONNestedFieldPresent",
			source: newStepResult(result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
				Body: []byte(`[{"profile":{"email":"user@example.com"}}]`),
			}, 0, nil)),
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name: "MongoDBNestedFieldPresent",
			source: newStepResult(result.NewMongoDBResult(&result.MongoDBResult{
				Documents: []map[string]any{{
					"profile": map[string]any{"email": "user@example.com"},
				}},
			}, 0, nil)),
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name: "FTPJSONNestedFieldMissing",
			source: newStepResult(result.NewFTPResult(&result.FileResult{
				Rows: []map[string]any{{
					"profile": map[string]any{"name": "user"},
				}},
				ColumnNames: []string{"profile"},
				RowCount:    1,
				Format:      "json",
			}, 0, nil)),
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrSinkInputContractViolation)
			},
		},
		{
			name: "HTTPJSONNestedFieldMissing",
			source: newStepResult(result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
				Body: []byte(`[{"profile":{"name":"user"}}]`),
			}, 0, nil)),
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrSinkInputContractViolation)
			},
		},
		{
			name: "HTTPCSVFieldPresent",
			source: newStepResult(result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers: map[string]string{
					"Content-Type": "text/csv",
				},
				Body: []byte("email\nuser@example.com\n"),
			}, 0, nil)),
			// CSV column is "email", not "profile.email"
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrSinkInputContractViolation)
			},
		},
		{
			name: "HTTPJSONMalformedBodyFailsContractValidation",
			source: newStepResult(result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
				Body: []byte(`{"profile":`),
			}, 0, nil)),
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrSinkInputContractViolation)
				require.ErrorIs(t, err, xerror.UnsupportedHTTPResponseFormat())
			},
		},
		{
			name: "HTTPUnsupportedContentTypeFailsContractValidation",
			source: newStepResult(result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers: map[string]string{
					"Content-Type": "application/xml",
				},
				Body: []byte(`<user/>`),
			}, 0, nil)),
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrSinkInputContractViolation)
				require.ErrorIs(t, err, xerror.UnsupportedHTTPContentType())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateSinkInputContract(tt.source, contract)
			tt.assertErr(t, err)
		})
	}
}

func TestValidateSinkInputContract_FlatField(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{Fields: []payload.ContractField{{
		Name:     "email",
		Required: true,
	}}}

	tests := []struct {
		name      string
		source    *StepResult
		assertErr func(t *testing.T, err error)
	}{
		{
			name: "SQLFieldPresent",
			source: newStepResult(result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"email": "user@example.com"}},
				ColumnNames: []string{"email"},
			}, 0, nil)),
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
		{
			name: "SQLFieldMissing",
			source: newStepResult(result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"name": "user"}},
				ColumnNames: []string{"name"},
			}, 0, nil)),
			assertErr: func(t *testing.T, err error) {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrSinkInputContractViolation)
			},
		},
		{
			name: "HTTPJSONFlatFieldPresent",
			source: newStepResult(result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
				Body: []byte(`[{"email":"user@example.com"}]`),
			}, 0, nil)),
			assertErr: func(t *testing.T, err error) {
				require.NoError(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateSinkInputContract(tt.source, contract)
			tt.assertErr(t, err)
		})
	}
}

func TestValidateSinkInputContract_RejectsRuntimeFileRepresentationMismatch(t *testing.T) {
	t.Parallel()

	fileContent := []byte("binary")
	fileMetadata := filepayload.NewFilePayloadMetadata(
		"users.bin",
		"application/octet-stream",
		fileContent,
	)

	tests := []struct {
		name     string
		source   *StepResult
		contract *payload.Contract
	}{
		{
			name: "FilePayloadToRowsContract",
			source: newStepResult(result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "application/octet-stream"},
				File:       &fileMetadata,
				RawBody:    fileContent,
			}, 0, nil)),
			contract: &payload.Contract{Representation: model.ContractRepresentationRows},
		},
		{
			name: "RowsPayloadToFileContract",
			source: newStepResult(result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1}},
				ColumnNames: []string{"id"},
				RowCount:    1,
			}, 0, nil)),
			contract: &payload.Contract{Representation: model.ContractRepresentationFile},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateSinkInputContract(tt.source, tt.contract)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrSinkInputContractViolation)
			require.ErrorContains(t, err, "runtime payload representation")
		})
	}
}

func TestValidateSinkInputContract_FieldValidations(t *testing.T) {
	t.Parallel()

	contract := loadRuntimeQueryOutputContractFromYAML(t, `metadata:
  slug: sink-validation-runtime
  name: Sink Validation Runtime
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-users
      output_contract:
        name: sink_users_rows
        representation: rows
        fields:
          - name: id
            type: integer
            required: true
            validations:
              unique: true
          - name: email
            type: string
            required: true
            validations:
              min_length: 3
              max_length: 80
`)
	source := newStepResult(result.NewSQLResult("postgres", &result.SQLResult{
		Rows: []map[string]any{
			{"id": 1, "email": "a@example.com"},
			{"id": 1, "email": "b@example.com"},
		},
		ColumnNames: []string{"id", "email"},
		RowCount:    2,
	}, 0, nil))

	err := validateSinkInputContract(source, contract)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSinkInputContractViolation)
	require.ErrorContains(t, err, "sink_users_rows")
	require.ErrorContains(t, err, "id")
	require.ErrorContains(t, err, "unique")
	require.NotContains(t, err.Error(), "a@example.com")
	require.NotContains(t, err.Error(), "b@example.com")
}

func TestValidateSinkInputContract_MalformedOptionalValidatedPayloadFails(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Name:           "http_sink_amounts",
		Representation: model.ContractRepresentationJSON,
		Fields: []payload.ContractField{{
			Name: "amount",
			Type: model.ContractFieldTypeValueNumber,
			Validations: &payload.ContractFieldValidations{
				Min: float64(1),
			},
		}},
	}
	source := newStepResult(result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       []byte(`{"amount":`),
	}, 0, nil))

	err := validateSinkInputContract(source, contract)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSinkInputContractViolation)
	require.ErrorIs(t, err, xerror.UnsupportedHTTPResponseFormat())
}

func TestValidateSinkInputContract_HTTPJSONPreservesIntegerPrecision(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Name:           "http_sink_users",
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
	source := newStepResult(result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       []byte(`[{"id":9007199254740993}]`),
	}, 0, nil))

	err := validateSinkInputContract(source, contract)
	require.NoError(t, err)
}

func TestValidateSinkInputContract_JSONIntegerValidationRejectsStringValues(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Name:           "http_sink_users",
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
	source := newStepResult(result.NewHTTPResult(&result.HTTPResult{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       []byte(`[{"id":"2"}]`),
	}, 0, nil))

	err := validateSinkInputContract(source, contract)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSinkInputContractViolation)
	require.ErrorContains(t, err, "http_sink_users")
	require.ErrorContains(t, err, "id")
	require.ErrorContains(t, err, "min")
	require.NotContains(t, err.Error(), `"2"`)
}

func TestValidateSinkInputContract_MultipartFormSkipsMetadataValueValidations(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{
		Name:           "form_sink",
		Representation: model.ContractRepresentationMultipartForm,
		Fields: []payload.ContractField{{
			Name:     "email",
			Type:     model.ContractFieldTypeValueString,
			Required: true,
			Validations: &payload.ContractFieldValidations{
				Format: "email",
			},
		}},
	}
	source := &StepResult{payload: &payload.Payload{
		Representation: model.ContractRepresentationMultipartForm,
		Form: &formpayload.FormData{
			Encoding: formpayload.FormEncodingMultipart,
			Fields: []formpayload.FormField{{
				Name:       "email",
				ValueCount: 1,
				Values:     []string{"user@example.com"},
				SizeBytes:  int64(len("user@example.com")),
			}},
		},
	}}

	err := validateSinkInputContract(source, contract)
	require.NoError(t, err)
}
