package ftp_e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	ftpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/ftp"
)

// The FTP fixture server exposes a single passive data port for this worktree.
func TestFTPConnector_SQLTransformStructuredReadWritesCSVSink(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	port := depsports.FTPConnectorPort()
	require.Truef(t, isReachable(ftpHost, port), "FTP server is not reachable at %s:%d", ftpHost, port)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := ftpconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(port), testCredentials())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	testCases := []struct {
		name                string
		filePath            string
		format              string
		additionalConfig    map[string]any
		sql                 string
		expectedColumnNames []string
		expectedRows        []map[string]any
	}{
		{
			name:     "StructuredCSVReadWritesCSVSink",
			filePath: "users.csv",
			format:   "csv",
			sql:      "SELECT email AS email FROM input ORDER BY email",
			expectedColumnNames: []string{
				"email",
			},
			expectedRows: []map[string]any{
				{"email": "alice@example.com"},
				{"email": "bob@example.com"},
			},
		},
		{
			name:     "StructuredSpreadsheetReadWritesCSVSink",
			filePath: "users.xlsx",
			format:   "spreadsheet",
			additionalConfig: map[string]any{
				"sheet": "Orders",
				"range": "B2:D4",
			},
			sql: "SELECT email AS email, total AS total FROM input ORDER BY email",
			expectedColumnNames: []string{
				"email",
				"total",
			},
			expectedRows: []map[string]any{
				{"email": "alice@example.com", "total": "42"},
				{"email": "bob@example.com", "total": "19"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			readOperation := map[string]any{
				"type":       "ftp",
				"operation":  "read",
				"path":       tc.filePath,
				"format":     tc.format,
				"has_header": true,
			}
			for key, value := range tc.additionalConfig {
				readOperation[key] = value
			}

			sourceResult, err := executeFTP(ctx, conn, readOperation, map[string]any{})
			require.NoError(t, err)

			transformOutput, err := transformsql.NewRuntime().ExecuteContext(
				ctx,
				transformsql.Transform{
					SQL:        tc.sql,
					OutputKind: model.TransformOutputKindRows,
				},
				map[string]*pipepayload.Payload{"input": fileRowsPayload(sourceResult.ColumnNames, sourceResult.RowCount, sourceResult.Rows)},
			)
			require.NoError(t, err)
			require.Equal(t, model.TransformOutputKindRows, transformOutput.OutputKind)
			require.Equal(t, tc.expectedColumnNames, transformOutput.ColumnNames)
			require.Equal(t, tc.expectedRows, transformOutput.Rows)

			targetPath := fmt.Sprintf("sql-transform-%s-%d.csv", tc.format, time.Now().UnixNano())
			writeAndVerifyCSVRows(ctx, t, conn, targetPath, tc.expectedColumnNames, transformOutput.Rows)
		})
	}
}

// The FTP fixture server exposes a single passive data port for this worktree.
func TestFTPConnector_SQLTransformNestedJSONWritesJSONSink(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	port := depsports.FTPConnectorPort()
	require.Truef(t, isReachable(ftpHost, port), "FTP server is not reachable at %s:%d", ftpHost, port)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := ftpconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(port), testCredentials())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	sourceResult, err := executeFTP(ctx, conn, map[string]any{
		"type":       "ftp",
		"operation":  "read",
		"path":       "users.csv",
		"format":     "csv",
		"has_header": true,
	}, map[string]any{})
	require.NoError(t, err)

	transformOutput, err := transformsql.NewRuntime().Execute(
		transformsql.Transform{
			SQL: "SELECT JSON_OBJECT('contact', JSON_OBJECT('email', email), 'id', id) AS profile, " +
				"JSON_ARRAY(JSON_OBJECT('sku', email, 'quantity', id)) AS items FROM input",
			OutputKind: model.TransformOutputKindJSON,
		},
		map[string]*pipepayload.Payload{
			"input": fileRowsPayload(
				sourceResult.ColumnNames,
				sourceResult.RowCount,
				sourceResult.Rows,
			),
		},
	)
	require.NoError(t, err)
	require.Equal(t, model.TransformOutputKindJSON, transformOutput.OutputKind)
	require.JSONEq(
		t,
		`[
			{"profile":{"contact":{"email":"alice@example.com"},"id":"1"},"items":[{"sku":"alice@example.com","quantity":"1"}]},
			{"profile":{"contact":{"email":"bob@example.com"},"id":"2"},"items":[{"sku":"bob@example.com","quantity":"2"}]}
		]`,
		string(transformOutput.Document),
	)

	var content any
	require.NoError(t, json.Unmarshal(transformOutput.Document, &content))

	targetPath := fmt.Sprintf("sql-transform-nested-json-%d.json", time.Now().UnixNano())
	writeAndVerifyJSONRows(ctx, t, conn, targetPath, content)
}

// fileRowsPayload wraps a structured FTP result as the row payload consumed by SQL transforms.
func fileRowsPayload(
	columnNames []string,
	rowCount int,
	rows []map[string]any,
) *pipepayload.Payload {
	return &pipepayload.Payload{
		Representation: model.ContractRepresentationRows,
		ColumnNames:    append([]string(nil), columnNames...),
		RowCount:       rowCount,
		Rows:           rows,
	}
}

// writeAndVerifyCSVRows writes transform rows through FTP and reads the CSV sink side effect back.
func writeAndVerifyCSVRows(
	ctx context.Context,
	t *testing.T,
	conn *ftpconnector.Connector,
	targetPath string,
	wantColumns []string,
	wantRows []map[string]any,
) {
	t.Helper()

	writeResult, err := executeFTP(ctx, conn, map[string]any{
		"type":       "ftp",
		"operation":  "write",
		"path":       targetPath,
		"format":     "csv",
		"has_header": true,
		"overwrite":  true,
		"content":    wantRows,
	}, map[string]any{})
	require.NoError(t, err)
	require.NotNil(t, writeResult.WrittenRows)
	require.Equal(t, len(wantRows), *writeResult.WrittenRows)

	readBackResult, err := executeFTP(ctx, conn, map[string]any{
		"type":       "ftp",
		"operation":  "read",
		"path":       targetPath,
		"format":     "csv",
		"has_header": true,
	}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, wantColumns, readBackResult.ColumnNames)
	require.Equal(t, len(wantRows), readBackResult.RowCount)
	require.Equal(t, wantRows, readBackResult.Rows)
}

// writeAndVerifyJSONRows writes transform JSON through FTP and reads nested sink data back.
func writeAndVerifyJSONRows(
	ctx context.Context,
	t *testing.T,
	conn *ftpconnector.Connector,
	targetPath string,
	content any,
) {
	t.Helper()

	writeResult, err := executeFTP(ctx, conn, map[string]any{
		"type":      "ftp",
		"operation": "write",
		"path":      targetPath,
		"format":    "json",
		"overwrite": true,
		"content":   content,
	}, map[string]any{})
	require.NoError(t, err)
	require.NotNil(t, writeResult.WrittenRows)
	require.Equal(t, 2, *writeResult.WrittenRows)

	readBackResult, err := executeFTP(ctx, conn, map[string]any{
		"type":      "ftp",
		"operation": "read",
		"path":      targetPath,
		"format":    "json",
	}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 2, readBackResult.RowCount)
	require.Equal(t, []string{"items", "profile"}, readBackResult.ColumnNames)
	require.Equal(t, map[string]any{
		"contact": map[string]any{"email": "alice@example.com"},
		"id":      "1",
	}, readBackResult.Rows[0]["profile"])
	require.Equal(t, []any{map[string]any{
		"quantity": "1",
		"sku":      "alice@example.com",
	}}, readBackResult.Rows[0]["items"])
}
