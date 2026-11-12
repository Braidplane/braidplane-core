package run

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	formpayload "github.com/Braidplane/braidplane-core/pkg/payload/form"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
)

func TestRuntimePayloadFromExecutionResult_SQLRows(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromExecutionResult(result.NewSQLResult(
		"postgres",
		&result.SQLResult{
			Rows:        []map[string]any{{"id": 1, "email": "alice@example.com"}},
			ColumnNames: []string{"id", "email"},
			RowCount:    1,
		},
		11,
		nil,
	))

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationRows, payload.Representation)
	require.Equal(t, model.ContractRootShapeUnknown, payload.RootShape)
	require.Equal(t, pipepayload.VerificationStatusUnknown, payload.VerificationStatus)
	require.Equal(t, pipepayload.ProvenanceExecutionResult, payload.Provenance)
	require.Equal(t, []string{"id", "email"}, payload.ColumnNames)
	require.Equal(t, 1, payload.RowCount)
	require.Equal(t, []map[string]any{{"id": 1, "email": "alice@example.com"}}, payload.Rows)
}

func TestRuntimePayloadFromExecutionResult_HTTPDocument(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromExecutionResult(result.NewHTTPResult(
		&result.HTTPResult{
			Headers: map[string]string{"Content-Type": "application/json; charset=utf-8"},
			Body:    []byte(`[{"id":1}]`),
		},
		5,
		nil,
	))

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationJSON, payload.Representation)
	require.Equal(t, model.ContractRootShapeArray, payload.RootShape)
	require.Equal(t, pipepayload.ProvenanceExecutionResult, payload.Provenance)
	require.Equal(t, []byte(`[{"id":1}]`), []byte(payload.Document))
	require.Empty(t, payload.Rows)
	require.Empty(t, payload.CSV)
}

func TestRuntimePayloadFromExecutionResult_HTTPCSV(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromExecutionResult(result.NewHTTPResult(
		&result.HTTPResult{
			Headers: map[string]string{"Content-Type": "text/csv"},
			Body:    []byte("id,email\n1,alice@example.com\n"),
		},
		5,
		nil,
	))

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationCSV, payload.Representation)
	require.Equal(t, model.ContractRootShapeUnknown, payload.RootShape)
	require.Equal(t, pipepayload.ProvenanceExecutionResult, payload.Provenance)
	require.Equal(t, "id,email\n1,alice@example.com\n", payload.CSV)
	require.Nil(t, payload.Document)
	require.Empty(t, payload.Rows)
}

func TestRuntimePayloadFromExecutionResult_HTTPCSVUnquotesConnectorJSONString(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromExecutionResult(result.NewHTTPResult(
		&result.HTTPResult{
			Headers: map[string]string{"Content-Type": "text/csv"},
			Body:    []byte(`"id,email\n1,alice@example.com\n"`),
		},
		5,
		nil,
	))

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationCSV, payload.Representation)
	require.Equal(t, "id,email\n1,alice@example.com\n", payload.CSV)
}

func TestRuntimePayloadFromExecutionResult_HTTPURLEncodedForm(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromExecutionResult(result.NewHTTPResult(
		&result.HTTPResult{
			Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
			Body:    []byte(`"name=Alice&tag=urgent&tag=customer"`),
		},
		5,
		nil,
	))

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationMultipartForm, payload.Representation)
	require.Equal(t, formpayload.FormEncodingURLEncoded, payload.Form.Encoding)
	require.Equal(t, []formpayload.FormField{
		{Name: "name", ValueCount: 1, SizeBytes: 5, Values: []string{"Alice"}},
		{Name: "tag", ValueCount: 2, SizeBytes: 14, Values: []string{"urgent", "customer"}},
	}, payload.Form.Fields)
}

func TestRuntimePayloadFromExecutionResult_HTTPMultipartFormUsesRawBody(t *testing.T) {
	t.Parallel()

	buffer := &bytes.Buffer{}
	writer := multipart.NewWriter(buffer)
	require.NoError(t, writer.WriteField("name", "Alice"))
	file, err := writer.CreateFormFile("attachment", "import.csv")
	require.NoError(t, err)
	_, err = file.Write([]byte("id\n1\n"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	payload := runtimePayloadFromExecutionResult(result.NewHTTPResult(
		&result.HTTPResult{
			Headers: map[string]string{"Content-Type": writer.FormDataContentType()},
			Body:    []byte(`"<stored string body>"`),
			RawBody: buffer.Bytes(),
		},
		5,
		nil,
	))

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationMultipartForm, payload.Representation)
	require.Equal(t, formpayload.FormEncodingMultipart, payload.Form.Encoding)
	require.Equal(t, []formpayload.FormField{{
		Name:       "name",
		ValueCount: 1,
		SizeBytes:  5,
		Values:     []string{"Alice"},
	}}, payload.Form.Fields)
	require.Len(t, payload.Form.Files, 1)
	require.Equal(t, "attachment", payload.Form.Files[0].Name)
	require.Equal(t, "import.csv", payload.Form.Files[0].Filename)
	require.Equal(t, []byte("id\n1\n"), payload.Form.Files[0].Content)
}

func TestRuntimePayloadFromExecutionResult_HTTPDocumentWithoutContentType(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromExecutionResult(result.NewHTTPResult(
		&result.HTTPResult{
			Body: []byte(`[{"id":1}]`),
		},
		5,
		nil,
	))

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationJSON, payload.Representation)
	require.Equal(t, model.ContractRootShapeArray, payload.RootShape)
	require.Equal(t, pipepayload.ProvenanceExecutionResult, payload.Provenance)
	require.Equal(t, []byte(`[{"id":1}]`), []byte(payload.Document))
}

func TestRuntimePayloadFromExecutionResult_HTTPUnsupportedContentTypeReturnsNil(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromExecutionResult(result.NewHTTPResult(
		&result.HTTPResult{
			Headers: map[string]string{"Content-Type": "application/xml"},
			Body:    []byte(`<xml/>`),
		},
		5,
		nil,
	))

	require.Nil(t, payload)
}

func TestRuntimePayloadFromExecutionResult_HTTPFile(t *testing.T) {
	t.Parallel()

	content := []byte("%PDF")
	metadata := filepayload.NewFilePayloadMetadata("invoice.pdf", "application/pdf", content)

	payload := runtimePayloadFromExecutionResult(result.NewHTTPResult(
		&result.HTTPResult{
			Headers: map[string]string{"Content-Type": "application/pdf"},
			File:    &metadata,
			RawBody: content,
		},
		5,
		nil,
	))

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationFile, payload.Representation)
	require.Equal(t, pipepayload.ProvenanceExecutionResult, payload.Provenance)
	require.Equal(t, &metadata, payload.File)
	require.Equal(t, content, payload.FileBytes)
	require.Empty(t, payload.Rows)
	require.Empty(t, payload.CSV)
	require.Empty(t, payload.Document)
}

func TestRuntimePayloadFromExecutionResult_FileConnectorFile(t *testing.T) {
	t.Parallel()

	content := []byte{0, 1, 2}
	metadata := filepayload.NewFilePayloadMetadata("users.bin", "application/octet-stream", content)

	payload := runtimePayloadFromExecutionResult(result.NewFTPResult(
		&result.FileResult{
			Format:  "file",
			File:    &metadata,
			Content: content,
		},
		5,
		nil,
	))

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationFile, payload.Representation)
	require.Equal(t, &metadata, payload.File)
	require.Equal(t, content, payload.FileBytes)
}

func TestRuntimePayloadFromExecutionResult_HTTPEmptyBodyReturnsNil(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromExecutionResult(result.NewHTTPResult(
		&result.HTTPResult{},
		5,
		nil,
	))

	require.Nil(t, payload)
}

func TestRuntimePayloadFromExecutionResult_MongoDocuments(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromExecutionResult(result.NewMongoDBResult(
		&result.MongoDBResult{
			OperationType: "find",
			Documents:     []map[string]any{{"id": 1, "email": "alice@example.com"}},
		},
		6,
		nil,
	))

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationJSON, payload.Representation)
	require.Equal(t, model.ContractRootShapeArray, payload.RootShape)
	require.Equal(t, 1, payload.RowCount)
	require.Equal(t, pipepayload.ProvenanceExecutionResult, payload.Provenance)
	require.JSONEq(t, `[{"email":"alice@example.com","id":1}]`, string(payload.Document))
}

func TestRuntimePayloadFromExecutionResult_MongoEmptyDocumentsReturnsNil(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromExecutionResult(result.NewMongoDBResult(
		&result.MongoDBResult{
			OperationType: "find",
			Documents:     []map[string]any{},
		},
		6,
		nil,
	))

	require.Nil(t, payload)
}

func TestRuntimePayloadFromTransformOutput_Rows(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromTransformOutput(&pipepayload.Output{
		OutputKind:  model.TransformOutputKindRows,
		RowCount:    2,
		ColumnNames: []string{"id", "email"},
		Rows: []map[string]any{
			{"id": 1, "email": "alice@example.com"},
			{"id": 2, "email": "bob@example.com"},
		},
	})

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationRows, payload.Representation)
	require.Equal(t, 2, payload.RowCount)
	require.Equal(t, pipepayload.ProvenanceTransformOutput, payload.Provenance)
	require.Equal(t, []map[string]any{
		{"id": 1, "email": "alice@example.com"},
		{"id": 2, "email": "bob@example.com"},
	}, payload.Rows)
}

func TestRuntimePayloadFromTransformOutput_Document(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromTransformOutput(&pipepayload.Output{
		OutputKind: model.TransformOutputKindJSON,
		RowCount:   1,
		Document:   []byte(`{"users":[{"id":1}]}`),
	})

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationJSON, payload.Representation)
	require.Equal(t, model.ContractRootShapeObject, payload.RootShape)
	require.Equal(t, pipepayload.ProvenanceTransformOutput, payload.Provenance)
	require.JSONEq(t, `{"users":[{"id":1}]}`, string(payload.Document))
}

func TestRuntimePayloadFromTransformOutput_CSV(t *testing.T) {
	t.Parallel()

	payload := runtimePayloadFromTransformOutput(&pipepayload.Output{
		OutputKind: model.TransformOutputKindCSV,
		RowCount:   1,
		CSV:        "id\n1\n",
	})

	require.NotNil(t, payload)
	require.Equal(t, model.ContractRepresentationCSV, payload.Representation)
	require.Equal(t, model.ContractRootShapeUnknown, payload.RootShape)
	require.Equal(t, pipepayload.ProvenanceTransformOutput, payload.Provenance)
	require.Equal(t, "id\n1\n", payload.CSV)
}

func TestRetainedFileReferenceRemainsRuntimeOnly(t *testing.T) {
	t.Parallel()
	metadata := filepayload.NewFilePayloadMetadata("fixture.bin", "application/octet-stream", []byte("fixture"))
	ref := filepayload.Reference{ID: "ea946443-7b39-4f64-8faf-eb3fac36c17f", SizeBytes: metadata.SizeBytes, SHA256: metadata.Digest}
	file := &result.FileResult{Format: "file", File: &metadata, Reference: &ref}
	for _, execution := range []*result.ExecutionResult{
		result.NewFTPResult(file, 0, nil), result.NewGitResult(file, 0, nil),
		result.NewS3Result(file, 0, nil), result.NewWorkspaceFileResult(file, 0, nil),
		result.NewHTTPResult(&result.HTTPResult{Headers: map[string]string{"Content-Type": metadata.MIMEType}, File: &metadata, Reference: &ref}, 0, nil),
	} {
		t.Run(execution.VendorType(), func(t *testing.T) {
			source := NewStepResult(execution)
			payload, ok := source.Payload()
			require.True(t, ok)
			require.Equal(t, &ref, payload.FileReference)
			require.Empty(t, payload.FileBytes)
			clone := payload.Clone()
			clone.FileReference.SizeBytes++
			require.Equal(t, metadata.SizeBytes, payload.FileReference.SizeBytes)
			stored, err := json.Marshal(source)
			require.NoError(t, err)
			require.NotContains(t, string(stored), ref.ID)
			var restored StepResult
			require.NoError(t, json.Unmarshal(stored, &restored))
			restoredPayload, ok := restored.Payload()
			require.True(t, ok)
			require.Nil(t, restoredPayload.FileReference)
		})
	}
}
