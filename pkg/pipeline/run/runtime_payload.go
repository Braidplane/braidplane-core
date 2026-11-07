package run

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	formpayload "github.com/Braidplane/braidplane-core/pkg/payload/form"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
)

func runtimePayloadFromExecutionResult(execution *result.ExecutionResult) *pipepayload.Payload {
	if execution == nil {
		return nil
	}

	if sqlResult, ok := execution.SQL(); ok && sqlResult != nil {
		return runtimeRowsPayload(
			sqlResult.ColumnNames,
			sqlResult.RowCount,
			sqlResult.Rows,
			pipepayload.ProvenanceExecutionResult,
		)
	}

	if objectRowsResult, ok := execution.ObjectRows(); ok && objectRowsResult != nil {
		return runtimeRowsPayload(
			objectRowsResult.ColumnNames,
			objectRowsResult.RowCount,
			objectRowsResult.Rows,
			pipepayload.ProvenanceExecutionResult,
		)
	}

	if fileResult := executionFileResult(execution); fileResult != nil {
		if isNativeFileResult(fileResult) {
			return runtimeFilePayload(
				fileResult.File,
				fileResult.Content,
				pipepayload.ProvenanceExecutionResult,
			)
		}

		return runtimeRowsPayload(
			fileResult.ColumnNames,
			fileResult.RowCount,
			fileResult.Rows,
			pipepayload.ProvenanceExecutionResult,
		)
	}

	if httpResult, ok := execution.HTTP(); ok && httpResult != nil {
		return runtimePayloadFromHTTPResult(httpResult)
	}

	if mongoResult, ok := execution.MongoDB(); ok && mongoResult != nil {
		return runtimePayloadFromMongoDBResult(mongoResult)
	}

	return nil
}

func executionFileResult(execution *result.ExecutionResult) *result.FileResult {
	if execution == nil {
		return nil
	}

	if fileResult, ok := execution.File(); ok {
		return fileResult
	}

	return nil
}

func runtimePayloadFromTransformOutput(transform *pipepayload.Output) *pipepayload.Payload {
	if transform == nil {
		return nil
	}

	switch transform.OutputKind {
	case model.TransformOutputKindRows:
		return runtimeRowsPayload(
			transform.ColumnNames,
			transform.RowCount,
			transform.Rows,
			pipepayload.ProvenanceTransformOutput,
		)
	case model.TransformOutputKindJSON:
		return &pipepayload.Payload{
			Representation:     model.ContractRepresentationJSON,
			RootShape:          detectJSONRootShape(transform.Document),
			VerificationStatus: pipepayload.VerificationStatusUnknown,
			Provenance:         pipepayload.ProvenanceTransformOutput,
			RowCount:           transform.RowCount,
			Document:           append([]byte(nil), transform.Document...),
		}
	case model.TransformOutputKindCSV:
		return &pipepayload.Payload{
			Representation:     model.ContractRepresentationCSV,
			RootShape:          model.ContractRootShapeUnknown,
			VerificationStatus: pipepayload.VerificationStatusUnknown,
			Provenance:         pipepayload.ProvenanceTransformOutput,
			RowCount:           transform.RowCount,
			CSV:                transform.CSV,
		}
	default:
		return nil
	}
}

func runtimePayloadFromHTTPResult(httpResult *result.HTTPResult) *pipepayload.Payload {
	if httpResult == nil {
		return nil
	}
	if httpResult.File != nil {
		return runtimeFilePayload(
			httpResult.File,
			httpResult.RawBody,
			pipepayload.ProvenanceExecutionResult,
		)
	}
	if len(httpResult.Body) == 0 {
		return nil
	}

	mediaType := detectHTTPMediaType(httpResult.Headers)
	switch mediaType {
	case httpMediaTypeCSV, httpMediaTypeCSV2:
		return &pipepayload.Payload{
			Representation:     model.ContractRepresentationCSV,
			RootShape:          model.ContractRootShapeUnknown,
			VerificationStatus: pipepayload.VerificationStatusUnknown,
			Provenance:         pipepayload.ProvenanceExecutionResult,
			CSV:                httpCSVBody(httpResult.Body),
		}
	case httpMediaTypeFormEncoded:
		form, err := formpayload.ParseURLEncodedFormBody(httpRawBody(httpResult))
		if err != nil {
			return nil
		}
		return &pipepayload.Payload{
			Representation:     model.ContractRepresentationMultipartForm,
			RootShape:          model.ContractRootShapeUnknown,
			VerificationStatus: pipepayload.VerificationStatusUnknown,
			Provenance:         pipepayload.ProvenanceExecutionResult,
			Form:               form,
		}
	case httpMediaTypeMultipart:
		rawBody := httpRawBody(httpResult)
		form, err := formpayload.ParseMultipartFormBody(
			httpContentType(httpResult.Headers),
			rawBody,
			int64(len(rawBody)),
		)
		if err != nil {
			return nil
		}
		return &pipepayload.Payload{
			Representation:     model.ContractRepresentationMultipartForm,
			RootShape:          model.ContractRootShapeUnknown,
			VerificationStatus: pipepayload.VerificationStatusUnknown,
			Provenance:         pipepayload.ProvenanceExecutionResult,
			Form:               form,
		}
	case httpMediaTypeJSON, "":
		return &pipepayload.Payload{
			Representation:     model.ContractRepresentationJSON,
			RootShape:          detectJSONRootShape(httpResult.Body),
			VerificationStatus: pipepayload.VerificationStatusUnknown,
			Provenance:         pipepayload.ProvenanceExecutionResult,
			Document:           append([]byte(nil), httpResult.Body...),
		}
	default:
		if len(httpResult.RawBody) == 0 {
			return nil
		}
		return runtimeFilePayload(
			nil,
			httpResult.RawBody,
			pipepayload.ProvenanceExecutionResult,
		)
	}
}

// isNativeFileResult reports whether a file connector result carries opaque bytes.
func isNativeFileResult(fileResult *result.FileResult) bool {
	return fileResult != nil &&
		(fileResult.File != nil ||
			len(fileResult.Content) > 0 ||
			fileResult.Format == model.ContractRepresentationFile.String())
}

// runtimeFilePayload wraps file metadata and bytes in a runtime-only payload.
func runtimeFilePayload(
	metadata *filepayload.FilePayloadMetadata,
	content []byte,
	provenance pipepayload.Provenance,
) *pipepayload.Payload {
	fileMetadata := filepayload.CloneFilePayloadMetadata(metadata)
	if fileMetadata == nil {
		computed := filepayload.NewFilePayloadMetadata("", "", content)
		fileMetadata = &computed
	}

	return &pipepayload.Payload{
		Representation:     model.ContractRepresentationFile,
		RootShape:          model.ContractRootShapeUnknown,
		VerificationStatus: pipepayload.VerificationStatusUnknown,
		Provenance:         provenance,
		File:               fileMetadata,
		FileBytes:          append([]byte(nil), content...),
	}
}

func httpCSVBody(body []byte) string {
	var decoded string
	if err := json.Unmarshal(body, &decoded); err == nil {
		return decoded
	}

	return string(body)
}

// httpRawBody returns the byte-preserving HTTP response body when available.
func httpRawBody(httpResult *result.HTTPResult) []byte {
	if httpResult == nil {
		return nil
	}
	if len(httpResult.RawBody) > 0 {
		return append([]byte(nil), httpResult.RawBody...)
	}

	var decoded string
	if err := json.Unmarshal(httpResult.Body, &decoded); err == nil {
		return []byte(decoded)
	}

	return append([]byte(nil), httpResult.Body...)
}

// httpContentType retrieves Content-Type regardless of response header casing.
func httpContentType(headers map[string]string) string {
	for name, value := range headers {
		if strings.EqualFold(name, "Content-Type") {
			return value
		}
	}
	return ""
}

func runtimePayloadFromMongoDBResult(mongoResult *result.MongoDBResult) *pipepayload.Payload {
	if mongoResult == nil || len(mongoResult.Documents) == 0 {
		return nil
	}

	documents, err := json.Marshal(mongoResult.Documents)
	if err != nil {
		return nil
	}

	return &pipepayload.Payload{
		Representation:     model.ContractRepresentationJSON,
		RootShape:          model.ContractRootShapeArray,
		VerificationStatus: pipepayload.VerificationStatusUnknown,
		Provenance:         pipepayload.ProvenanceExecutionResult,
		RowCount:           len(mongoResult.Documents),
		Document:           documents,
	}
}

func runtimeRowsPayload(
	columnNames []string,
	rowCount int,
	rows []map[string]any,
	provenance pipepayload.Provenance,
) *pipepayload.Payload {
	clonedRows := make([]map[string]any, len(rows))
	for i, row := range rows {
		clonedRows[i] = cloneMapAny(row)
	}

	return &pipepayload.Payload{
		Representation:     model.ContractRepresentationRows,
		RootShape:          model.ContractRootShapeUnknown,
		VerificationStatus: pipepayload.VerificationStatusUnknown,
		Provenance:         provenance,
		ColumnNames:        append([]string(nil), columnNames...),
		RowCount:           rowCount,
		Rows:               clonedRows,
	}
}

func detectJSONRootShape(body []byte) model.ContractRootShape {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return model.ContractRootShapeUnknown
	}

	switch trimmed[0] {
	case '{':
		return model.ContractRootShapeObject
	case '[':
		return model.ContractRootShapeArray
	default:
		return model.ContractRootShapeUnknown
	}
}
