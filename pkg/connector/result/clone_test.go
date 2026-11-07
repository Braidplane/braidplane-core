package result

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecutionResult_Clone_Nil(t *testing.T) {
	t.Parallel()

	var input *ExecutionResult
	require.Nil(t, input.Clone())
}

func TestExecutionResult_Clone_SQLDeepCopy(t *testing.T) {
	t.Parallel()

	lastInsertID := int64(42)
	input := NewSQLResult(
		"postgres",
		&SQLResult{
			Rows:         []map[string]any{{"id": 1}},
			ColumnNames:  []string{"id"},
			RowCount:     1,
			LastInsertID: &lastInsertID,
		},
		7,
		fmt.Errorf("boom"),
	)

	cloned := input.Clone()
	require.NotNil(t, cloned)
	require.Equal(t, input.Type(), cloned.Type())
	require.Equal(t, input.VendorType(), cloned.VendorType())
	require.Equal(t, input.DurationMs(), cloned.DurationMs())
	require.EqualError(t, cloned.Error(), "boom")

	srcSQL, ok := input.SQL()
	require.True(t, ok)
	clonedSQL, ok := cloned.SQL()
	require.True(t, ok)

	require.Equal(t, srcSQL.RowCount, clonedSQL.RowCount)
	require.Equal(t, srcSQL.ColumnNames, clonedSQL.ColumnNames)
	require.Equal(t, srcSQL.Rows, clonedSQL.Rows)
	require.Equal(t, *srcSQL.LastInsertID, *clonedSQL.LastInsertID)

	clonedSQL.Rows[0]["id"] = 999
	clonedSQL.ColumnNames[0] = "changed"
	*clonedSQL.LastInsertID = 100

	require.Equal(t, 1, srcSQL.Rows[0]["id"])
	require.Equal(t, "id", srcSQL.ColumnNames[0])
	require.Equal(t, int64(42), *srcSQL.LastInsertID)
}

func TestExecutionResult_Clone_MongoDBDeepCopy(t *testing.T) {
	t.Parallel()

	documentCount := 1
	insertedCount := int64(2)
	matchedCount := int64(3)
	modifiedCount := int64(4)
	deletedCount := int64(5)
	upsertedCount := int64(6)
	input := NewMongoDBResult(&MongoDBResult{
		OperationType: "aggregate",
		Documents: []map[string]any{
			{"_id": "one", "total": float64(12)},
		},
		DocumentCount: &documentCount,
		InsertedID:    "inserted-one",
		InsertedIDs:   []any{"inserted-one", "inserted-two"},
		InsertedCount: &insertedCount,
		MatchedCount:  &matchedCount,
		ModifiedCount: &modifiedCount,
		DeletedCount:  &deletedCount,
		UpsertedID:    "upserted-one",
		UpsertedCount: &upsertedCount,
	}, 13, nil)

	cloned := input.Clone()
	require.NotNil(t, cloned)
	require.Equal(t, ExecutionResultTypeMongoDB, cloned.Type())
	require.Equal(t, "mongodb", cloned.VendorType())
	require.Equal(t, int64(13), cloned.DurationMs())

	srcMongo, ok := input.MongoDB()
	require.True(t, ok)
	clonedMongo, ok := cloned.MongoDB()
	require.True(t, ok)
	require.Equal(t, srcMongo, clonedMongo)

	clonedMongo.Documents[0]["total"] = float64(99)
	clonedMongo.InsertedIDs[0] = "changed"
	*clonedMongo.DocumentCount = 42
	*clonedMongo.InsertedCount = 43
	*clonedMongo.MatchedCount = 44
	*clonedMongo.ModifiedCount = 45
	*clonedMongo.DeletedCount = 46
	*clonedMongo.UpsertedCount = 47

	require.InEpsilon(t, float64(12), srcMongo.Documents[0]["total"], 0.001)
	require.Equal(t, "inserted-one", srcMongo.InsertedIDs[0])
	require.Equal(t, 1, *srcMongo.DocumentCount)
	require.Equal(t, int64(2), *srcMongo.InsertedCount)
	require.Equal(t, int64(3), *srcMongo.MatchedCount)
	require.Equal(t, int64(4), *srcMongo.ModifiedCount)
	require.Equal(t, int64(5), *srcMongo.DeletedCount)
	require.Equal(t, int64(6), *srcMongo.UpsertedCount)
}

func TestExecutionResult_Clone_HTTPDeepCopy(t *testing.T) {
	t.Parallel()

	count := 2
	input := NewHTTPResult(&HTTPResult{
		StatusCode:            201,
		PaginationRecordCount: &count,
		Headers: map[string]string{
			"content-type": "application/json",
		},
		Body:    json.RawMessage(`{"ok":true}`),
		RawBody: []byte(`{"ok":true}`),
	}, 17, nil)

	cloned := input.Clone()
	clonedHTTP, ok := cloned.HTTP()
	require.True(t, ok)
	require.Equal(t, 201, clonedHTTP.StatusCode)
	require.Equal(t, "application/json", clonedHTTP.Headers["content-type"])
	require.JSONEq(t, `{"ok":true}`, string(clonedHTTP.Body))
	require.JSONEq(t, `{"ok":true}`, string(clonedHTTP.RawBody))

	srcHTTP, ok := input.HTTP()
	require.True(t, ok)
	require.Equal(t, 2, clonedHTTP.RowCount())
	*clonedHTTP.PaginationRecordCount = 3
	require.Equal(t, 2, srcHTTP.RowCount())
	clonedHTTP.Headers["content-type"] = "text/csv"
	clonedHTTP.Body[0] = '['
	clonedHTTP.RawBody[0] = '['

	require.Equal(t, "application/json", srcHTTP.Headers["content-type"])
	require.JSONEq(t, `{"ok":true}`, string(srcHTTP.Body))
	require.JSONEq(t, `{"ok":true}`, string(srcHTTP.RawBody))
}

func TestExecutionResult_JSONRoundTripRestoresDiscriminatedPayload(t *testing.T) {
	t.Parallel()

	input := NewSQLResult("postgres", &SQLResult{
		Rows:        []map[string]any{{"id": float64(1)}},
		ColumnNames: []string{"id"},
		RowCount:    1,
	}, 23, nil)

	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	var decoded ExecutionResult
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, ExecutionResultTypeSQL, decoded.Type())
	require.Equal(t, "postgres", decoded.VendorType())
	require.Equal(t, int64(23), decoded.DurationMs())
	decodedSQL, ok := decoded.SQL()
	require.True(t, ok)
	require.Equal(t, 1, decodedSQL.RowCount)
	require.Equal(t, []string{"id"}, decodedSQL.ColumnNames)
	require.InDelta(t, float64(1), decodedSQL.Rows[0]["id"], 0)
}

func TestExecutionResult_JSONRoundTripPreservesFileNumbers(t *testing.T) {
	t.Parallel()
	rows := []map[string]any{{"id": 9007199254740993, "amount": 1.25}}
	for _, input := range []*ExecutionResult{
		NewFTPResult(&FileResult{Rows: rows}, 1, nil),
		NewGitResult(&FileResult{Rows: rows}, 1, nil),
		NewS3Result(&FileResult{Rows: rows}, 1, nil),
		NewWorkspaceFileResult(&FileResult{Rows: rows, ResourceID: "id", Name: "crm.json", MIMEType: "application/json"}, 1, nil),
	} {
		t.Run(input.Type().String(), func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(input)
			require.NoError(t, err)
			var decoded ExecutionResult
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			file, ok := decoded.File()
			require.True(t, ok)
			require.Equal(t, []map[string]any{{"id": 9007199254740993, "amount": json.Number("1.25")}}, file.Rows)
		})
	}
}

func TestExecutionResult_Clone_ObjectRowsDeepCopy(t *testing.T) {
	t.Parallel()

	nextCursor := "next-page"
	input := NewObjectRowsResult("hubspot", &ObjectRowsResult{
		ObjectType: "contacts",
		Operation:  "search",
		Rows: []map[string]any{
			{"id": "1", "email": "one@example.test"},
		},
		ColumnNames:      []string{"id", "email"},
		RowCount:         1,
		NextCursor:       &nextCursor,
		HasMore:          true,
		ProviderMetadata: map[string]any{"api_version": "v3"},
	}, 19, nil)

	cloned := input.Clone()
	clonedObjectRows, ok := cloned.ObjectRows()
	require.True(t, ok)
	require.Equal(t, "contacts", clonedObjectRows.ObjectType)
	require.Equal(t, "search", clonedObjectRows.Operation)
	require.True(t, clonedObjectRows.HasMore)

	srcObjectRows, ok := input.ObjectRows()
	require.True(t, ok)
	clonedObjectRows.Rows[0]["email"] = "changed@example.test"
	clonedObjectRows.ColumnNames[0] = "changed"
	*clonedObjectRows.NextCursor = "changed-page"
	clonedObjectRows.ProviderMetadata["api_version"] = "changed"

	require.Equal(t, "one@example.test", srcObjectRows.Rows[0]["email"])
	require.Equal(t, "id", srcObjectRows.ColumnNames[0])
	require.Equal(t, "next-page", *srcObjectRows.NextCursor)
	require.Equal(t, "v3", srcObjectRows.ProviderMetadata["api_version"])
}

func TestExecutionResult_Clone_FilePayloadDeepCopyForFTPAndGit(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		input *ExecutionResult
		read  func(*ExecutionResult) (*FileResult, bool)
	}{
		{
			name:  "FTP",
			input: NewFTPResult(testFileResult(), 23, nil),
			read:  (*ExecutionResult).FTP,
		},
		{
			name:  "Git",
			input: NewGitResult(testFileResult(), 29, nil),
			read:  (*ExecutionResult).Git,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cloned := tt.input.Clone()
			clonedFile, ok := tt.read(cloned)
			require.True(t, ok)
			srcFile, ok := tt.read(tt.input)
			require.True(t, ok)
			require.Equal(t, srcFile, clonedFile)

			clonedFile.Rows[0]["id"] = 99
			clonedFile.ColumnNames[0] = "changed"
			*clonedFile.WrittenBytes = 1000
			*clonedFile.WrittenRows = 1001

			require.Equal(t, 1, srcFile.Rows[0]["id"])
			require.Equal(t, "id", srcFile.ColumnNames[0])
			require.Equal(t, 128, *srcFile.WrittenBytes)
			require.Equal(t, 2, *srcFile.WrittenRows)
		})
	}
}

func TestExecutionResult_MarshalJSON_UsesGitStorageEnvelope(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(NewGitResult(&FileResult{
		Rows:        []map[string]any{{"id": 1}},
		ColumnNames: []string{"id"},
		RowCount:    1,
		Format:      "json",
		Operation:   "write",
		TargetPath:  "exports/users.json",
	}, 11, nil))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Equal(t, "Git", doc["result_type"])
	require.Equal(t, "git", doc["vendor_type"])
	_, hasGit := doc["git"]
	require.True(t, hasGit)
	_, hasFTP := doc["ftp"]
	require.False(t, hasFTP)
}

func TestExecutionResult_MarshalJSON_UsesFTPStorageEnvelope(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(NewFTPResult(&FileResult{
		Rows:        []map[string]any{{"id": 1}},
		ColumnNames: []string{"id"},
		RowCount:    1,
		Format:      "csv",
		Operation:   "read",
		TargetPath:  "exports/users.csv",
	}, 11, nil))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Equal(t, "FTP", doc["result_type"])
	require.Equal(t, "ftp", doc["vendor_type"])
	_, hasFTP := doc["ftp"]
	require.True(t, hasFTP)
	_, hasGit := doc["git"]
	require.False(t, hasGit)
}

func testFileResult() *FileResult {
	writtenBytes := 128
	writtenRows := 2
	return &FileResult{
		Rows:          []map[string]any{{"id": 1}, {"id": 2}},
		ColumnNames:   []string{"id"},
		RowCount:      2,
		Format:        "csv",
		IsHeadlessCSV: false,
		Operation:     "write",
		TargetPath:    "exports/users.csv",
		WrittenBytes:  &writtenBytes,
		WrittenRows:   &writtenRows,
	}
}
