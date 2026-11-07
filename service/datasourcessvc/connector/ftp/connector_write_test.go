package ftp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestDetectWriteFormat(t *testing.T) {
	t.Parallel()

	t.Run("UsesExplicitCSV", func(t *testing.T) {
		t.Parallel()
		format, err := filedata.DetectWriteFormat(QueryFormatCSV, "/tmp/users.json")
		require.NoError(t, err)
		require.Equal(t, QueryFormatCSV, format)
	})

	t.Run("InfersFromExtension", func(t *testing.T) {
		t.Parallel()
		format, err := filedata.DetectWriteFormat(QueryFormatAuto, "/tmp/users.csv")
		require.NoError(t, err)
		require.Equal(t, QueryFormatCSV, format)
	})

	t.Run("InfersSpreadsheetFromExtension", func(t *testing.T) {
		t.Parallel()
		format, err := filedata.DetectWriteFormat(QueryFormatAuto, "/tmp/users.xlsx")
		require.NoError(t, err)
		require.Equal(t, QueryFormatSpreadsheet, format)
	})

	t.Run("DefaultsToJSON", func(t *testing.T) {
		t.Parallel()
		format, err := filedata.DetectWriteFormat(QueryFormatAuto, "/tmp/users")
		require.NoError(t, err)
		require.Equal(t, QueryFormatJSON, format)
	})

	t.Run("UsesExplicitFile", func(t *testing.T) {
		t.Parallel()
		format, err := filedata.DetectWriteFormat(QueryFormatFile, "/tmp/users.csv")
		require.NoError(t, err)
		require.Equal(t, QueryFormatFile, format)
	})
}

func TestSerializeWriteContent_JSON(t *testing.T) {
	t.Parallel()

	content := []any{
		map[string]any{"id": float64(1), "email": "alice@example.com"},
		map[string]any{"id": float64(2), "email": "bob@example.com"},
	}

	payload, format, writtenRows, err := serializeWriteContent(
		ftpQueryData{
			Format:  QueryFormatJSON,
			Content: content,
		},
		"/exports/users.json",
	)
	require.NoError(t, err)
	require.Equal(t, QueryFormatJSON, format)
	require.Equal(t, 2, writtenRows)

	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	require.Len(t, decoded, 2)
	require.Equal(t, "alice@example.com", decoded[0]["email"])
	require.Equal(t, "bob@example.com", decoded[1]["email"])
}

func TestSerializeWriteContent_JSONStringContentReturnsErr(t *testing.T) {
	t.Parallel()

	payload, format, writtenRows, err := serializeWriteContent(
		ftpQueryData{
			Format:  QueryFormatJSON,
			Content: "id,email\n1,alice@example.com\n2,bob@example.com\n",
		},
		"/exports/users.json",
	)
	_ = payload
	_ = format
	_ = writtenRows
	require.ErrorContains(t, err, "JSON FTP write requires object or array content")
}

func TestSerializeWriteContent_JSONScalarContentReturnsErr(t *testing.T) {
	t.Parallel()

	payload, format, writtenRows, err := serializeWriteContent(
		ftpQueryData{
			Format:  QueryFormatJSON,
			Content: true,
		},
		"/exports/users.json",
	)
	_ = payload
	_ = format
	_ = writtenRows
	require.ErrorContains(t, err, "JSON FTP write requires object or array content")
}

func TestSerializeWriteContent_CSVObjects(t *testing.T) {
	t.Parallel()

	content := []any{
		map[string]any{"id": float64(1), "email": "alice@example.com"},
		map[string]any{"id": float64(2), "email": "bob@example.com"},
	}

	payload, format, writtenRows, err := serializeWriteContent(
		ftpQueryData{
			Format:    QueryFormatCSV,
			Delimiter: ";",
			Content:   content,
		},
		"/exports/users.csv",
	)
	require.NoError(t, err)
	require.Equal(t, QueryFormatCSV, format)
	require.Equal(t, 2, writtenRows)
	require.Equal(t, "email;id\nalice@example.com;1\nbob@example.com;2\n", string(payload))
}

func TestSerializeWriteContent_SpreadsheetObjects(t *testing.T) {
	t.Parallel()

	content := []any{
		map[string]any{"id": float64(1), "email": "alice@example.com"},
		map[string]any{"id": float64(2), "email": "bob@example.com"},
	}

	payload, format, writtenRows, err := serializeWriteContent(
		ftpQueryData{
			Format:  QueryFormatSpreadsheet,
			Sheet:   "Orders",
			Range:   "B2",
			Content: content,
		},
		"/exports/users.xlsx",
	)
	require.NoError(t, err)
	require.Equal(t, QueryFormatSpreadsheet, format)
	require.Equal(t, 2, writtenRows)

	parsed, err := filedata.ParseSpreadsheet(payload, filedata.SpreadsheetOptions{
		Sheet: "Orders",
		Range: "B2:C4",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"email", "id"}, parsed.ColumnNames)
	require.Equal(t, "alice@example.com", parsed.Rows[0]["email"])
}

func TestSerializeWriteContent_CSVRequiresObjectRows(t *testing.T) {
	t.Parallel()

	payload, format, writtenRows, err := serializeWriteContent(
		ftpQueryData{
			Format:  QueryFormatCSV,
			Content: []any{"invalid-row"},
		},
		"/exports/users.csv",
	)
	_ = payload
	_ = format
	_ = writtenRows
	require.ErrorContains(t, err, "must be an object")
}

func TestSerializeWriteContent_FileBytes(t *testing.T) {
	t.Parallel()

	content := []byte{0, 1, 2}
	payload, format, writtenRows, err := serializeWriteContent(
		ftpQueryData{
			Format:  QueryFormatFile,
			Content: content,
		},
		"/exports/users.bin",
	)
	require.NoError(t, err)
	require.Equal(t, QueryFormatFile, format)
	require.Zero(t, writtenRows)
	require.Equal(t, content, payload)
}

func TestApplyWriteParamOverrides(t *testing.T) {
	t.Parallel()

	qd := &ftpQueryData{
		Path:      "users.csv",
		Operation: ftpOperationWrite,
		Format:    QueryFormatAuto,
	}

	applyWriteParamOverrides(qd, map[string]any{
		sinkbinding.Input: []any{map[string]any{"id": 1}},
		"format":          "json",
		"delimiter":       ";",
		"has_header":      true,
		"overwrite":       false,
		"sheet":           "Orders",
		"range":           "B2:C10",
	})

	require.Equal(t, QueryFormatJSON, qd.Format)
	require.Equal(t, ";", qd.Delimiter)
	require.Equal(t, "Orders", qd.Sheet)
	require.Equal(t, "B2:C10", qd.Range)
	require.NotNil(t, qd.HasHeader)
	require.True(t, *qd.HasHeader)
	require.NotNil(t, qd.Overwrite)
	require.False(t, *qd.Overwrite)

	contentRows, ok := qd.Content.([]any)
	require.True(t, ok)
	require.Len(t, contentRows, 1)
}

func TestApplyWriteParamOverridesReservedBindingsOverrideInlineContent(t *testing.T) {
	t.Parallel()

	qd := &ftpQueryData{
		Path:      "stale/users.csv",
		Operation: ftpOperationWrite,
		Content:   "stale-inline-content",
	}

	applyWriteParamOverrides(qd, map[string]any{
		sinkbinding.Input: []any{map[string]any{"id": 1}},
		"path":            "runtime/users.csv",
	})

	require.True(t, qd.ContentSet)
	require.Equal(t, "runtime/users.csv", qd.Path)
	contentRows, ok := qd.Content.([]any)
	require.True(t, ok)
	require.Len(t, contentRows, 1)
}

func TestApplyWriteParamOverridesUsesSingleReservedInput(t *testing.T) {
	t.Parallel()

	qd := &ftpQueryData{
		Path:      "users.csv",
		Operation: ftpOperationWrite,
	}

	applyWriteParamOverrides(qd, map[string]any{
		sinkbinding.Input: "id,email\n1,alice@example.com\n",
	})

	content, ok := qd.Content.(string)
	require.True(t, ok)
	require.Equal(t, "id,email\n1,alice@example.com\n", content)
}

func TestApplyWriteParamOverridesPreservesExplicitNullInput(t *testing.T) {
	t.Parallel()

	qd := &ftpQueryData{
		Path:      "users.json",
		Operation: ftpOperationWrite,
		Format:    QueryFormatJSON,
	}

	applyWriteParamOverrides(qd, map[string]any{
		sinkbinding.Input: nil,
	})

	require.True(t, qd.ContentSet)
	require.Nil(t, qd.Content)

	payload, format, writtenRows, err := serializeWriteContent(*qd, "users.json")
	_ = payload
	_ = format
	_ = writtenRows
	require.ErrorContains(t, err, "scalar and null content")
}

func TestExecuteWrite_RequiresContentWhenNoBindingProvided(t *testing.T) {
	t.Parallel()

	connector := &Connector{}

	fileResult, err := connector.executeWrite(
		"/exports/users.json",
		ftpQueryData{Operation: ftpOperationWrite, Format: QueryFormatJSON},
	)
	require.Nil(t, fileResult)
	require.ErrorContains(t, err, "requires content")
}

func TestConnectorExecute_WriteJSONNullContentReturnsStructuredError(t *testing.T) {
	t.Parallel()

	connector := &Connector{}

	execResult, execErr := connector.Execute(
		context.Background(),
		[]byte(
			`{"type":"file","operation":"write","path":"users.json","format":"json","content":null}`,
		),
		map[string]types.TypedParam{},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.ErrorContains(t, execResult.Error(), "scalar and null content")
}

func TestConnectorExecute_InvalidStatementJSONReturnsInvalidInput(t *testing.T) {
	t.Parallel()

	connector := &Connector{}

	execResult, execErr := connector.Execute(
		context.Background(),
		[]byte(`{"type":"file","operation":"read","format":`),
		map[string]types.TypedParam{},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.ErrorIs(t, execResult.Error(), xerror.InvalidInput(xerror.Query))
}

func TestConnectorExecute_WriteRuntimePathOverridesInlinePath(t *testing.T) {
	t.Parallel()

	connector := &Connector{}

	execResult, execErr := connector.Execute(
		context.Background(),
		[]byte(
			`{"type":"file","operation":"write","path":"../escape","format":"json","content":null}`,
		),
		map[string]types.TypedParam{
			"path": {Value: "users.json", DataType: "text"},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.ErrorContains(t, execResult.Error(), "scalar and null content")
}
