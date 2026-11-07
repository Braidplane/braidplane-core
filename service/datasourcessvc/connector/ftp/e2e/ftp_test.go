package ftp_e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	ftpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/ftp"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

const ftpHost = "127.0.0.1"

// The FTP fixture server exposes a single passive data port for this worktree.
func TestFTPConnector_ExecuteReturnsRowLimitViolation(t *testing.T) {
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

	_, err = executeFTPWithLimits(ctx, conn, map[string]any{
		"type":       "ftp",
		"operation":  "read",
		"path":       "users.csv",
		"format":     "csv",
		"has_header": true,
	}, map[string]any{}, types.ExecutionLimits{MaxResultRows: 1, MaxResponseBytes: 1024 * 1024})
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
}

// The FTP fixture server exposes a single passive data port for this worktree.
func TestFTPConnector_ExecuteSupportsNamespacedRuntimePathParams(t *testing.T) {
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

	pathTemplate := "{{runtime.fixture_file}}"

	readResult, err := executeFTP(ctx, conn, map[string]any{
		"type":       "ftp",
		"operation":  "read",
		"path":       pathTemplate,
		"format":     "csv",
		"has_header": true,
	}, map[string]any{
		"runtime.fixture_file": "users.csv",
	})
	require.NoError(t, err)
	require.Equal(t, "/users.csv", readResult.TargetPath)
	require.Equal(t, 2, readResult.RowCount)
	require.Equal(t, "alice@example.com", readResult.Rows[0]["email"])
	require.Equal(t, "bob@example.com", readResult.Rows[1]["email"])
}

// The FTP fixture server exposes a single passive data port for this worktree.
func TestFTPConnector_ExecuteReadsSpreadsheetFixture(t *testing.T) {
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

	readResult, err := executeFTP(ctx, conn, map[string]any{
		"type":       "ftp",
		"operation":  "read",
		"path":       "users.xlsx",
		"format":     "spreadsheet",
		"sheet":      "Orders",
		"range":      "B2:D4",
		"has_header": true,
	}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "/users.xlsx", readResult.TargetPath)
	require.Equal(t, "Orders", readResult.Sheet)
	require.Equal(t, "B2:D4", readResult.Range)
	require.Equal(t, []string{"email", "id", "total"}, readResult.ColumnNames)
	require.Equal(t, 2, readResult.RowCount)
	require.Equal(t, "alice@example.com", readResult.Rows[0]["email"])
	require.Equal(t, "42", readResult.Rows[0]["total"])
	require.Equal(t, "bob@example.com", readResult.Rows[1]["email"])
}

// The FTP fixture server exposes a single passive data port for this worktree.
func TestFTPConnector_ExecuteWritesSpreadsheetFile(t *testing.T) {
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

	targetPath := fmt.Sprintf("spreadsheet-write-%d.xlsx", time.Now().UnixNano())
	writeResult, err := executeFTP(ctx, conn, map[string]any{
		"type":       "ftp",
		"operation":  "write",
		"path":       targetPath,
		"format":     "spreadsheet",
		"sheet":      "Exports",
		"range":      "C3",
		"has_header": true,
		"overwrite":  true,
		"content": []map[string]any{
			{"email": "charlie@example.com", "id": 3, "total": 64},
		},
	}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "/"+targetPath, writeResult.TargetPath)
	require.NotNil(t, writeResult.WrittenRows)
	require.Equal(t, 1, *writeResult.WrittenRows)

	readResult, err := executeFTP(ctx, conn, map[string]any{
		"type":       "ftp",
		"operation":  "read",
		"path":       targetPath,
		"format":     "spreadsheet",
		"sheet":      "Exports",
		"range":      "C3:E4",
		"has_header": true,
	}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, []string{"email", "id", "total"}, readResult.ColumnNames)
	require.Equal(t, 1, readResult.RowCount)
	require.Equal(t, "charlie@example.com", readResult.Rows[0]["email"])
	require.Equal(t, "64", readResult.Rows[0]["total"])
}

func testConfig(port int) ftpconnector.Config {
	return ftpconnector.Config{
		Host:              ftpHost,
		Port:              port,
		BasePath:          "/",
		ConnectTimeout:    5,
		AllowPrivateHosts: true,
	}
}

func testCredentials() ftpconnector.Credentials {
	return ftpconnector.Credentials{Username: "braidplane", Password: "braidplanepass"}
}

func executeFTP(
	ctx context.Context,
	conn *ftpconnector.Connector,
	query map[string]any,
	params map[string]any,
) (*result.FileResult, error) {
	return executeFTPWithLimits(ctx, conn, query, params, types.ExecutionLimits{})
}

func executeFTPWithLimits(
	ctx context.Context,
	conn *ftpconnector.Connector,
	query map[string]any,
	params map[string]any,
	limits types.ExecutionLimits,
) (*result.FileResult, error) {
	queryData, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}

	typedParams := make(map[string]types.TypedParam, len(params))
	for name, value := range params {
		typedParams[name] = types.TypedParam{Value: value, DataType: "text"}
	}

	execResult, err := conn.Execute(ctx, queryData, typedParams, limits)
	if err != nil {
		return nil, err
	}
	if execResult.Error() != nil {
		return nil, execResult.Error()
	}

	fileResult, ok := execResult.FTP()
	if !ok {
		return nil, xerror.Unexpected().Detailf("expected ftp execution result, got: %s", execResult.Type())
	}
	return fileResult, nil
}

func isReachable(host string, port int) bool {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(context.Background(), "tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func TestFTPConnector_IntrospectionDiscoversStructuredAndNestedRawFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("dependency-backed FTP fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := ftpconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(depsports.FTPConnectorPort()), testCredentials())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	schema, err := conn.IntrospectSchema(ctx)
	require.NoError(t, err)
	foundCSV, foundRaw := false, false
	for _, file := range schema.Files {
		if file.Name == "users.csv" {
			require.NotEmpty(t, file.Fields)
			foundCSV = true
		}
		if file.Name == "nested/report.bin" {
			require.Empty(t, file.Fields)
			require.NotNil(t, file.SizeBytes)
			foundRaw = true
		}
	}
	require.True(t, foundCSV)
	require.True(t, foundRaw)
	_, err = conn.IntrospectSchema(schemaquota.WithQuota(ctx, 1, 1024))
	require.ErrorContains(t, err, "item limit")
}
