package run

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestNewStepResult_PanicsOnNilExecution(t *testing.T) {
	t.Parallel()

	require.PanicsWithValue(t, "step result execution is required", func() {
		_ = newStepResult(nil)
	})
}

func TestStepResult_MarshalJSON_ReturnsErrorForNilExecution(t *testing.T) {
	t.Parallel()

	result := &StepResult{}
	payload, err := result.MarshalJSON()
	require.ErrorContains(t, err, "step result execution is required")
	require.Nil(t, payload)
}

func TestStepResult_MarshalJSON_EmitsRunInputMetadataAndError(t *testing.T) {
	t.Parallel()

	stepResult := newRunInputStepResult(&pipepayload.Payload{
		Representation: model.ContractRepresentationJSON,
		Document:       json.RawMessage(`{"secret":"raw-value"}`),
	})
	stepResult.err = xerror.Unexpected().Detail("input failed")

	encoded, err := stepResult.MarshalJSON()
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(encoded, &doc))
	require.Equal(t, runInputResultType, doc["result_type"])
	require.Equal(t, runInputVendorType, doc["vendor_type"])
	require.Contains(t, doc["error"].(string), "input failed")
	require.NotContains(t, string(encoded), "raw-value")
}

func TestStepResult_MarshalJSON_EmitsExecutionPayloadDiagnostics(t *testing.T) {
	t.Parallel()

	stepResult := newStepResult(result.NewSQLResult(
		"postgres",
		&result.SQLResult{
			Rows:        []map[string]any{{"id": 1}},
			ColumnNames: []string{"id"},
			RowCount:    1,
		},
		7,
		nil,
	))

	payloadModel, ok := stepResult.Payload()
	require.True(t, ok)
	payloadModel.Contract = &pipepayload.Contract{
		Name:           "users-output",
		Representation: model.ContractRepresentationRows,
		Fields: []pipepayload.ContractField{{
			Name:     "id",
			Type:     model.ContractFieldTypeValueInteger,
			Required: true,
		}},
	}
	payloadModel.VerificationStatus = pipepayload.VerificationStatusVerified

	payload, err := stepResult.MarshalJSON()
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(payload, &doc))
	require.Equal(t, "SQL", doc["result_type"])
	require.Equal(t, "postgres", doc["vendor_type"])

	rawSQL, ok := doc["sql"].(map[string]any)
	require.True(t, ok)
	rows, ok := rawSQL["rows"].([]any)
	require.True(t, ok)
	require.Len(t, rows, 1)

	rawPayload, ok := doc["payload"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "rows", rawPayload["representation"])
	require.Equal(t, "verified", rawPayload["verification_status"])
	require.Equal(t, "execution_result", rawPayload["provenance"])
	require.EqualValues(t, 1, rawPayload["row_count"])
	_, hasRows := rawPayload["rows"]
	require.False(t, hasRows)

	rawContract, ok := rawPayload["contract"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "users-output", rawContract["name"])
	fields, ok := rawContract["fields"].([]any)
	require.True(t, ok)
	require.Len(t, fields, 1)
}

func TestStepResult_MarshalJSON_EmitsGitExecutionPayloadDiagnostics(t *testing.T) {
	t.Parallel()

	stepResult := newStepResult(result.NewGitResult(&result.FileResult{
		Rows:        []map[string]any{{"id": 1}},
		ColumnNames: []string{"id"},
		RowCount:    1,
		Format:      "json",
		Operation:   "write",
		TargetPath:  "exports/users.json",
	}, 9, nil))

	payload, err := stepResult.MarshalJSON()
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(payload, &doc))
	require.Equal(t, "Git", doc["result_type"])
	require.Equal(t, "git", doc["vendor_type"])

	rawGit, ok := doc["git"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "write", rawGit["operation"])
	require.Equal(t, "exports/users.json", rawGit["target_path"])

	_, hasFTP := doc["ftp"]
	require.False(t, hasFTP)
}

func TestStepResult_MarshalJSON_StoresFileMetadataWithoutRawBytes(t *testing.T) {
	t.Parallel()

	content := []byte{0, 1, 2}
	metadata := filepayload.NewFilePayloadMetadata("users.bin", "application/octet-stream", content)
	stepResult := newStepResult(result.NewFTPResult(&result.FileResult{
		Format:     "file",
		Operation:  "read",
		TargetPath: "/exports/users.bin",
		File:       &metadata,
		Content:    content,
	}, 9, nil))

	payload, err := stepResult.MarshalJSON()
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(payload, &doc))

	rawFTP, ok := doc["ftp"].(map[string]any)
	require.True(t, ok)
	_, hasContent := rawFTP["content"]
	require.False(t, hasContent)
	rawFile, ok := rawFTP["file"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "users.bin", rawFile["filename"])
	require.EqualValues(t, len(content), rawFile["size_bytes"])

	rawPayload, ok := doc["payload"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "file", rawPayload["representation"])
	_, hasFileBytes := rawPayload["file_bytes"]
	require.False(t, hasFileBytes)
	payloadFile, ok := rawPayload["file"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "users.bin", payloadFile["filename"])
}

func TestStepResult_DurationMs_DelegatesToExecution(t *testing.T) {
	t.Parallel()

	execution := result.NewSQLResult(
		"postgres",
		&result.SQLResult{
			Rows:        []map[string]any{{"id": 1}},
			ColumnNames: []string{"id"},
			RowCount:    1,
		},
		7,
		nil,
	)

	stepResult := newStepResult(execution)
	require.Equal(t, int64(7), stepResult.DurationMs())
	payload, ok := stepResult.Payload()
	require.True(t, ok)
	require.Equal(t, model.ContractRepresentationRows, payload.Representation)
	require.Equal(t, []string{"id"}, payload.ColumnNames)
	require.Equal(t, 1, payload.RowCount)
	require.Equal(t, []map[string]any{{"id": 1}}, payload.Rows)
}

func TestStepResult_RedactedClone_RedactsObjectRows(t *testing.T) {
	t.Parallel()

	nextCursor := "after-secret"
	objectRows := &result.ObjectRowsResult{
		ObjectType:  "contacts",
		Operation:   "search",
		Rows:        []map[string]any{{"email": "alice@example.test"}},
		ColumnNames: []string{"email"},
		RowCount:    1,
		NextCursor:  &nextCursor,
		HasMore:     true,
		ProviderMetadata: map[string]any{
			"request_id":     "secret-request",
			"typed_slice":    []string{"secret-scope"},
			"typed_map":      map[string]string{"secret-key": "secret-value"},
			"typed_map_list": []map[string]string{{"secret-nested-key": "secret-nested-value"}},
		},
	}
	stepResult := newStepResult(result.NewObjectRowsResult(
		"hubspot",
		objectRows,
		11,
		xerror.Unexpected().Detail("secret execution failure"),
	))

	cloned := stepResult.RedactedClone(func(value string) string {
		return "redacted-" + value
	})
	require.NotNil(t, cloned)
	require.ErrorContains(t, cloned.Error(), "redacted")

	clonedRows, ok := cloned.ObjectRows()
	require.True(t, ok)
	require.Equal(t, "redacted-contacts", clonedRows.ObjectType)
	require.Equal(t, "redacted-search", clonedRows.Operation)
	require.Equal(t, []string{"redacted-email"}, clonedRows.ColumnNames)
	require.Equal(t, []map[string]any{{"redacted-email": "redacted-alice@example.test"}}, clonedRows.Rows)
	require.NotNil(t, clonedRows.NextCursor)
	require.Equal(t, "redacted-after-secret", *clonedRows.NextCursor)
	require.Equal(t, map[string]any{
		"redacted-request_id":     "redacted-secret-request",
		"redacted-typed_slice":    []string{"redacted-secret-scope"},
		"redacted-typed_map":      map[string]string{"redacted-secret-key": "redacted-secret-value"},
		"redacted-typed_map_list": []map[string]string{{"redacted-secret-nested-key": "redacted-secret-nested-value"}},
	}, clonedRows.ProviderMetadata)

	require.Equal(t, "contacts", objectRows.ObjectType)
	require.Equal(t, "search", objectRows.Operation)
	require.Equal(t, []string{"email"}, objectRows.ColumnNames)
	require.Equal(t, []map[string]any{{"email": "alice@example.test"}}, objectRows.Rows)
	require.Equal(t, "after-secret", *objectRows.NextCursor)
	require.Equal(t, map[string]any{
		"request_id":     "secret-request",
		"typed_slice":    []string{"secret-scope"},
		"typed_map":      map[string]string{"secret-key": "secret-value"},
		"typed_map_list": []map[string]string{{"secret-nested-key": "secret-nested-value"}},
	}, objectRows.ProviderMetadata)
}

func TestNewTransformStepResult_PanicsOnNilTransform(t *testing.T) {
	t.Parallel()

	require.PanicsWithValue(t, "step result transform is required", func() {
		_ = newTransformStepResult(nil, 0, nil)
	})
}

func TestStepResult_TransformDeepCopy(t *testing.T) {
	t.Parallel()

	input := newTransformStepResult(&pipepayload.Output{
		OutputKind:    model.TransformOutputKindJSON,
		RowCount:      1,
		IsHeadlessCSV: true,
		ColumnNames:   []string{"id"},
		Rows:          []map[string]any{{"id": 1}},
		Document:      []byte(`[ {"id":1} ]`),
		CSV:           "id\n1\n",
	}, 13, nil)

	cloned := input.Clone()
	require.NotNil(t, cloned)
	require.Equal(t, input.ResultTypeString(), cloned.ResultTypeString())
	require.Equal(t, input.VendorType(), cloned.VendorType())
	require.Equal(t, input.DurationMs(), cloned.DurationMs())
	require.NoError(t, cloned.Error())

	srcTransform, ok := input.Transform()
	require.True(t, ok)
	clonedTransform, ok := cloned.Transform()
	require.True(t, ok)

	require.Equal(t, srcTransform.OutputKind, clonedTransform.OutputKind)
	require.Equal(t, srcTransform.RowCount, clonedTransform.RowCount)
	require.Equal(t, srcTransform.IsHeadlessCSV, clonedTransform.IsHeadlessCSV)
	require.Equal(t, srcTransform.ColumnNames, clonedTransform.ColumnNames)
	require.Equal(t, srcTransform.Rows, clonedTransform.Rows)
	require.Equal(t, srcTransform.Document, clonedTransform.Document)
	require.Equal(t, srcTransform.CSV, clonedTransform.CSV)

	clonedTransform.Rows[0]["id"] = 777
	clonedTransform.ColumnNames[0] = "changed"
	clonedTransform.Document[0] = '{'
	clonedTransform.CSV = "changed"

	require.Equal(t, 1, srcTransform.Rows[0]["id"])
	require.Equal(t, "id", srcTransform.ColumnNames[0])
	require.NotEqual(t, "changed", srcTransform.CSV)

	srcPayload, ok := input.Payload()
	require.True(t, ok)
	clonedPayload, ok := cloned.Payload()
	require.True(t, ok)
	require.Equal(t, srcPayload.Representation, clonedPayload.Representation)
	require.Equal(t, srcPayload.Document, clonedPayload.Document)

	clonedPayload.Document[0] = '{'
	require.Equal(t, byte('['), srcPayload.Document[0])
}

func TestStepResult_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]*StepResult{
		"query": newStepResult(result.NewSQLResult("postgres", &result.SQLResult{
			ColumnNames: []string{"id"}, RowCount: 1,
			Rows: []map[string]any{{"id": int(9007199254740993)}},
		}, 7, nil)),
		"csv": newTransformStepResult(&pipepayload.Output{
			OutputKind: model.TransformOutputKindCSV, RowCount: 1, CSV: "id\n0001\n",
		}, 9, nil),
		"rows": newTransformStepResult(&pipepayload.Output{
			OutputKind: model.TransformOutputKindRows, RowCount: 1, ColumnNames: []string{"id"},
			Rows: []map[string]any{{"id": int(9007199254740993)}},
		}, 11, nil),
		"json": newTransformStepResult(&pipepayload.Output{
			OutputKind: model.TransformOutputKindJSON, RowCount: 1, Document: json.RawMessage(`{"id":"0001"}`),
		}, 13, nil),
		"failed-transform": newTransformStepResult(&pipepayload.Output{
			OutputKind: model.TransformOutputKindCSV,
		}, 17, xerror.Unexpected().Detail("transform failed")),
		"input-metadata": newRunInputStepResult(&pipepayload.Payload{
			Representation: model.ContractRepresentationJSON,
		}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input.payload.Contract = &pipepayload.Contract{Name: "pinned-output", Representation: input.payload.Representation}
			input.payload.VerificationStatus = pipepayload.VerificationStatusVerified
			encoded, err := json.Marshal(input)
			require.NoError(t, err)
			var restored StepResult
			require.NoError(t, json.Unmarshal(encoded, &restored))
			cloned := restored.Clone()
			require.Equal(t, input.ResultTypeString(), cloned.ResultTypeString())
			require.Equal(t, input.VendorType(), cloned.VendorType())
			require.Equal(t, input.DurationMs(), cloned.DurationMs())
			require.Equal(t, input.IsSuccess(), cloned.IsSuccess())
			if input.Error() != nil {
				require.ErrorContains(t, cloned.Error(), input.Error().Error())
			} else {
				require.NoError(t, cloned.Error())
			}
			require.Equal(t, input.Clone().payload, cloned.payload)
		})
	}
}

func TestStepResult_UnmarshalJSON_RejectsMissingResult(t *testing.T) {
	t.Parallel()
	for _, encoded := range []string{`null`, `{}`, `{"result_type":"Transform"}`, `{"result_type":"Input"}`, `{"result_type":"invalid"}`} {
		var restored StepResult
		require.Error(t, json.Unmarshal([]byte(encoded), &restored))
	}
}

func TestStepResult_MarshalJSON_EmitsTransformMetadata(t *testing.T) {
	t.Parallel()

	stepResult := newTransformStepResult(&pipepayload.Output{
		OutputKind:    model.TransformOutputKindRows,
		RowCount:      1,
		IsHeadlessCSV: true,
		Rows:          []map[string]any{{"col_0": "alice"}},
	}, 5, nil)
	payloadModel, ok := stepResult.Payload()
	require.True(t, ok)
	payloadModel.VerificationStatus = pipepayload.VerificationStatusTransitivelyVerified
	payloadModel.Contract = &pipepayload.Contract{
		Name:           "transform-output",
		Representation: model.ContractRepresentationRows,
		Fields: []pipepayload.ContractField{{
			Name: "col_0",
			Type: model.ContractFieldTypeValueString,
		}},
	}

	payload, err := stepResult.MarshalJSON()
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(payload, &doc))
	require.Equal(t, "Transform", doc["result_type"])
	require.Equal(t, "transform", doc["vendor_type"])

	rawTransform, ok := doc["transform"].(map[string]any)
	require.True(t, ok)
	value, ok := rawTransform["is_headless_csv"].(bool)
	require.True(t, ok)
	require.True(t, value)

	rawPayload, ok := doc["payload"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "rows", rawPayload["representation"])
	require.Equal(t, "transitively_verified", rawPayload["verification_status"])
	require.Equal(t, "transform_output", rawPayload["provenance"])
	_, hasRows := rawPayload["rows"]
	require.False(t, hasRows)
}
