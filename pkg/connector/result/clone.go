package result

import (
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// Clone returns a deep copy of the execution result.
func (r *ExecutionResult) Clone() *ExecutionResult {
	if r == nil {
		return nil
	}

	cloned := &ExecutionResult{
		resultType: r.resultType,
		vendorType: r.vendorType,
		durationMs: r.durationMs,
	}

	if r.err != nil {
		cloned.err = xerror.Unexpected().Detail(r.err.Error())
	}

	switch r.resultType {
	case ExecutionResultTypeUnknown:
		// Keep payload fields nil for unknown result type.
	case ExecutionResultTypeSQL:
		cloned.sql = cloneSQLResult(r.sql)
	case ExecutionResultTypeMongoDB:
		cloned.mongo = cloneMongoDBResult(r.mongo)
	case ExecutionResultTypeHTTP:
		cloned.http = cloneHTTPResult(r.http)
	case ExecutionResultTypeObjectRows:
		cloned.objectRows = cloneObjectRowsResult(r.objectRows)
	case ExecutionResultTypeFTP:
		cloned.file = cloneFileResult(r.file)
	case ExecutionResultTypeGit:
		cloned.file = cloneFileResult(r.file)
	case ExecutionResultTypeS3, ExecutionResultTypeWorkspaceFile:
		cloned.file = cloneFileResult(r.file)
	}

	return cloned
}

func cloneSQLResult(input *SQLResult) *SQLResult {
	if input == nil {
		return nil
	}

	rows := make([]map[string]any, len(input.Rows))
	for i, row := range input.Rows {
		rows[i] = cloneMapAny(row)
	}

	var lastInsertID *int64
	if input.LastInsertID != nil {
		value := *input.LastInsertID
		lastInsertID = &value
	}

	return &SQLResult{
		Rows:         rows,
		ColumnNames:  append([]string(nil), input.ColumnNames...),
		RowCount:     input.RowCount,
		LastInsertID: lastInsertID,
		File:         cloneFilePayloadMetadata(input.File),
		Reference:    filepayload.CloneReference(input.Reference),
	}
}

func cloneMongoDBResult(input *MongoDBResult) *MongoDBResult {
	if input == nil {
		return nil
	}

	documents := make([]map[string]any, len(input.Documents))
	for i, doc := range input.Documents {
		documents[i] = cloneMapAny(doc)
	}

	copyInt := func(v *int) *int {
		if v == nil {
			return nil
		}
		n := *v
		return &n
	}
	copyInt64 := func(v *int64) *int64 {
		if v == nil {
			return nil
		}
		n := *v
		return &n
	}

	return &MongoDBResult{
		OperationType: input.OperationType,
		Documents:     documents,
		DocumentCount: copyInt(input.DocumentCount),
		InsertedID:    input.InsertedID,
		InsertedIDs:   append([]any(nil), input.InsertedIDs...),
		InsertedCount: copyInt64(input.InsertedCount),
		MatchedCount:  copyInt64(input.MatchedCount),
		ModifiedCount: copyInt64(input.ModifiedCount),
		DeletedCount:  copyInt64(input.DeletedCount),
		UpsertedID:    input.UpsertedID,
		UpsertedCount: copyInt64(input.UpsertedCount),
	}
}

func cloneHTTPResult(input *HTTPResult) *HTTPResult {
	if input == nil {
		return nil
	}

	headers := make(map[string]string, len(input.Headers))
	for key, value := range input.Headers {
		headers[key] = value
	}

	var count *int
	if input.PaginationRecordCount != nil {
		value := *input.PaginationRecordCount
		count = &value
	}
	return &HTTPResult{
		PaginationRecordCount: count,
		StatusCode:            input.StatusCode,
		Headers:               headers,
		Body:                  append([]byte(nil), input.Body...),
		File:                  cloneFilePayloadMetadata(input.File),
		Reference:             filepayload.CloneReference(input.Reference),
		RawBody:               append([]byte(nil), input.RawBody...),
	}
}

func cloneObjectRowsResult(input *ObjectRowsResult) *ObjectRowsResult {
	if input == nil {
		return nil
	}

	rows := make([]map[string]any, len(input.Rows))
	for i, row := range input.Rows {
		rows[i] = cloneMapAny(row)
	}

	var nextCursor *string
	if input.NextCursor != nil {
		value := *input.NextCursor
		nextCursor = &value
	}

	return &ObjectRowsResult{
		ObjectType:       input.ObjectType,
		Operation:        input.Operation,
		Rows:             rows,
		ColumnNames:      append([]string(nil), input.ColumnNames...),
		RowCount:         input.RowCount,
		NextCursor:       nextCursor,
		HasMore:          input.HasMore,
		ProviderMetadata: cloneMapAny(input.ProviderMetadata),
	}
}

func cloneFileResult(input *FileResult) *FileResult {
	if input == nil {
		return nil
	}

	rows := make([]map[string]any, len(input.Rows))
	for i, row := range input.Rows {
		rows[i] = cloneMapAny(row)
	}

	return &FileResult{
		Rows:        rows,
		ColumnNames: append([]string(nil), input.ColumnNames...),
		RowCount:    input.RowCount,
		ResourceID:  input.ResourceID, Name: input.Name, MIMEType: input.MIMEType,
		Format:                input.Format,
		IsHeadlessCSV:         input.IsHeadlessCSV,
		IsHeadlessSpreadsheet: input.IsHeadlessSpreadsheet,
		Sheet:                 input.Sheet,
		Range:                 input.Range,
		Operation:             input.Operation,
		TargetPath:            input.TargetPath,
		File:                  cloneFilePayloadMetadata(input.File),
		Reference:             filepayload.CloneReference(input.Reference),
		Content:               append([]byte(nil), input.Content...),
		WrittenBytes:          cloneIntPointer(input.WrittenBytes),
		WrittenRows:           cloneIntPointer(input.WrittenRows),
	}
}

// cloneFilePayloadMetadata delegates metadata copying to the shared model helper.
func cloneFilePayloadMetadata(input *filepayload.FilePayloadMetadata) *filepayload.FilePayloadMetadata {
	return filepayload.CloneFilePayloadMetadata(input)
}

func cloneIntPointer(input *int) *int {
	if input == nil {
		return nil
	}
	v := *input
	return &v
}

func cloneMapAny(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}

	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}

	return cloned
}
