package git

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestGitQueryDataUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantPath   string
		wantOp     string
		wantFmt    QueryFormat
		wantBranch string
	}{
		{
			name:       "ReadOperation",
			input:      `{"type":"file","operation":"read","path":"data/file.csv","format":"csv","branch":"main"}`,
			wantPath:   "data/file.csv",
			wantOp:     "read",
			wantFmt:    QueryFormatCSV,
			wantBranch: "main",
		},
		{
			name:     "WriteOperation",
			input:    `{"type":"file","operation":"write","path":"out.json","format":"json"}`,
			wantPath: "out.json",
			wantOp:   "write",
			wantFmt:  QueryFormatJSON,
		},
		{
			name:     "DefaultsToEmptyOperation",
			input:    `{"path":"report.csv"}`,
			wantPath: "report.csv",
			wantOp:   "",
			wantFmt:  QueryFormatUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var qd gitQueryData
			err := json.Unmarshal([]byte(tc.input), &qd)

			require.NoError(t, err)
			require.Equal(t, tc.wantPath, qd.Path)
			require.Equal(t, tc.wantOp, qd.Operation)
			require.Equal(t, tc.wantFmt, qd.Format)
			require.Equal(t, tc.wantBranch, qd.Branch)
		})
	}
}

func TestExtractHostname(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		url         string
		wantHost    string
		wantErrText string
	}{
		{
			name:     "HTTPS",
			url:      "https://github.com/org/repo.git",
			wantHost: "github.com",
		},
		{
			name:     "SCPStyleSSH",
			url:      "git@github.com:org/repo.git",
			wantHost: "github.com",
		},
		{
			name:     "SSHScheme",
			url:      "ssh://git@github.com/org/repo.git",
			wantHost: "github.com",
		},
		{
			name:        "EmptyURL",
			url:         "",
			wantErrText: "hostname",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := extractHostname(tc.url)

			if tc.wantErrText != "" {
				require.ErrorContains(t, err, tc.wantErrText)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.wantHost, got)
		})
	}
}

func TestNew_RejectsHTTPRepoURL(t *testing.T) {
	t.Parallel()

	_, err := New(
		context.Background(),
		Config{
			RepoURL:           "http://example.com/repo.git",
			AllowPrivateHosts: true,
		},
		Credentials{AuthType: AuthTypeHTTPS},
	)

	require.ErrorContains(t, err, "repo_url must use https or ssh")
}

func TestNew_RejectsUnsupportedAuthType(t *testing.T) {
	t.Parallel()

	_, err := New(
		context.Background(),
		Config{
			RepoURL:           "https://example.com/repo.git",
			AllowPrivateHosts: true,
		},
		Credentials{AuthType: AuthType(99)},
	)

	require.ErrorContains(t, err, "oneof")
}

func TestTestConnection_LocalFailureHasPublicDetail(t *testing.T) {
	t.Parallel()

	conn, err := New(
		context.Background(),
		Config{
			RepoURL:           "ssh://git@127.0.0.1:1/acme/repo.git",
			AllowPrivateHosts: true,
		},
		Credentials{AuthType: AuthTypeHTTPS},
	)
	require.NoError(t, err)

	err = conn.TestConnection(context.Background())
	require.Error(t, err)
	require.Contains(t, xerror.PublicDetail(err), "git ls-remote failed")
}

func TestApplyWriteParamOverridesUsesReservedInputBindings(t *testing.T) {
	t.Parallel()

	qd := &gitQueryData{
		Path:      "users.csv",
		Operation: gitOperationWrite,
	}

	applyWriteParamOverrides(qd, map[string]any{
		sinkbinding.Input: "id,email\n1,alice@example.com\n",
		"format":          "spreadsheet",
		"delimiter":       ";",
		"has_header":      true,
		"sheet":           "Orders",
		"range":           "B2:C10",
	})

	content, ok := qd.Content.(string)
	require.True(t, ok)
	require.Equal(t, "id,email\n1,alice@example.com\n", content)
	require.Equal(t, QueryFormatSpreadsheet, qd.Format)
	require.Equal(t, ";", qd.Delimiter)
	require.Equal(t, "Orders", qd.Sheet)
	require.Equal(t, "B2:C10", qd.Range)
	require.NotNil(t, qd.HasHeader)
	require.True(t, *qd.HasHeader)
}

func TestApplyWriteParamOverridesReservedBindingsOverrideInlineContentAndMetadata(t *testing.T) {
	t.Parallel()

	qd := &gitQueryData{
		Path:      "stale/users.csv",
		Operation: gitOperationWrite,
		Content:   "stale-inline-content",
		Branch:    "stale-branch",
		Message:   "stale-message",
	}

	applyWriteParamOverrides(qd, map[string]any{
		sinkbinding.Input: []any{map[string]any{"id": 1}},
		"path":            "runtime/users.csv",
		"branch":          "main",
		"message":         "sync users",
	})

	require.Equal(t, "runtime/users.csv", qd.Path)
	require.True(t, qd.ContentSet)
	contentRows, ok := qd.Content.([]any)
	require.True(t, ok)
	require.Len(t, contentRows, 1)
	require.Equal(t, "main", qd.Branch)
	require.Equal(t, "sync users", qd.Message)
}

func TestApplyWriteParamOverridesIgnoresBlankBranchAndMessageOverrides(t *testing.T) {
	t.Parallel()

	qd := &gitQueryData{
		Path:      "users.csv",
		Operation: gitOperationWrite,
		Branch:    "saved-branch",
		Message:   "saved-message",
	}

	applyWriteParamOverrides(qd, map[string]any{
		"branch":  "   ",
		"message": "",
	})

	require.Equal(t, "saved-branch", qd.Branch)
	require.Equal(t, "saved-message", qd.Message)
}

func TestApplyWriteParamOverridesPreservesExplicitNullInput(t *testing.T) {
	t.Parallel()

	qd := &gitQueryData{
		Path:      "users.json",
		Operation: gitOperationWrite,
		Format:    QueryFormatJSON,
	}

	applyWriteParamOverrides(qd, map[string]any{
		sinkbinding.Input: nil,
	})

	require.True(t, qd.ContentSet)
	require.Nil(t, qd.Content)

	payload, format, writtenRows, err := serializeWriteContent(*qd, "exports/users.json")
	_ = payload
	_ = format
	_ = writtenRows
	require.ErrorContains(t, err, "scalar and null content")
}

func TestExecuteWrite_RequiresContentWhenNoBindingProvided(t *testing.T) {
	t.Parallel()

	fileResult, err := executeWrite(
		context.Background(),
		nil,
		"exports/users.json",
		gitQueryData{Operation: gitOperationWrite, Format: QueryFormatJSON},
		"",
		"update exports/users.json",
		"",
		nil,
	)
	require.Nil(t, fileResult)
	require.ErrorContains(t, err, "requires content")
}

func TestConnectorExecute_InvalidQueryDataUsesGitVendorType(t *testing.T) {
	t.Parallel()

	connector := &Connector{creds: Credentials{AuthType: AuthTypeHTTPS}}

	execResult, execErr := connector.Execute(
		context.Background(),
		[]byte(`{"method"`),
		nil,
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.Equal(t, "git", execResult.VendorType())
	require.ErrorContains(t, execResult.Error(), "unexpected end of JSON input")
}

func TestConnectorExecute_MissingPathUsesGitVendorType(t *testing.T) {
	t.Parallel()

	connector := &Connector{creds: Credentials{AuthType: AuthTypeHTTPS}}

	execResult, execErr := connector.Execute(
		context.Background(),
		[]byte(`{"type":"file","operation":"read"}`),
		nil,
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.Equal(t, "git", execResult.VendorType())
	require.ErrorContains(t, execResult.Error(), "must specify a path")
}

func TestConnectorExecute_PathSubstitutionFailureUsesGitVendorType(t *testing.T) {
	t.Parallel()

	connector := &Connector{creds: Credentials{AuthType: AuthTypeHTTPS}}

	execResult, execErr := connector.Execute(
		context.Background(),
		[]byte(`{"type":"file","operation":"read","path":"{{missing}}"}`),
		nil,
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.Equal(t, "git", execResult.VendorType())
	require.ErrorContains(t, execResult.Error(), "failed to substitute path parameters")
}

func TestConnectorExecute_InvalidGitPathUsesGitVendorType(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()

	initBareRemote(t, remoteDir)
	seedRemoteRepo(t, ctx, sourceDir, remoteDir, map[string]string{
		"exports/users.json": `[{"id":0}]`,
	})

	connector := &Connector{
		config: Config{RepoURL: remoteDir, DefaultBranch: "main"},
		creds:  Credentials{AuthType: AuthTypeHTTPS},
	}

	execResult, execErr := connector.Execute(
		ctx,
		[]byte(`{"type":"file","operation":"read","path":"../escape"}`),
		nil,
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.Equal(t, "git", execResult.VendorType())
	require.ErrorContains(t, execResult.Error(), "invalid Git path")
}

func TestConnectorExecute_WriteOverridesInlineContentPathBranchAndMessage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()

	initBareRemote(t, remoteDir)
	seedRemoteRepo(t, ctx, sourceDir, remoteDir, map[string]string{
		"exports/users.json": `[{"id":0}]`,
	})

	connector := &Connector{
		config: Config{RepoURL: remoteDir, DefaultBranch: "main"},
		creds:  Credentials{AuthType: AuthTypeHTTPS},
	}

	execResult, execErr := connector.Execute(
		ctx,
		[]byte(
			`{"type":"file","operation":"write","path":"{{unused}}","format":"json","branch":"missing-branch","message":"stale-message","content":[{"id":999}]}`,
		),
		map[string]types.TypedParam{
			"path":            {Value: "exports/users.json", DataType: "text"},
			sinkbinding.Input: {Value: []any{map[string]any{"id": 1}}, DataType: "text"},
			"branch":          {Value: "main", DataType: "text"},
			"message":         {Value: "runtime message", DataType: "text"},
		},
	)
	require.NoError(t, execErr)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())
	require.Equal(t, "git", execResult.VendorType())

	fileResult, ok := execResult.Git()
	require.True(t, ok)
	require.NotNil(t, fileResult.WrittenRows)
	require.Equal(t, 1, *fileResult.WrittenRows)

	cloneStore, cloneDir, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	data, err := cloneStore.ReadFile(ctx, "exports/users.json", 0)
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":1}]`, string(data))
	require.Equal(t, "runtime message", latestGitCommitMessage(t, cloneDir))
}

func TestConnectorExecute_ConfinesPathsToBasePath(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		basePath string
		path     string
		override string
		param    string
		write    bool
		wantErr  bool
	}{
		{name: "read slash escape", basePath: "exports/nested", path: "../secret.json", wantErr: true},
		{name: "read backslash escape", basePath: "exports/nested", path: `..\secret.json`, wantErr: true},
		{name: "read substituted escape", basePath: "exports/nested", path: "{{file}}", param: "../secret.json", wantErr: true},
		{name: "write slash override", basePath: "exports/nested", path: "users.json", override: "../secret.json", write: true, wantErr: true},
		{name: "write backslash override", basePath: "exports/nested", path: "users.json", override: `..\secret.json`, write: true, wantErr: true},
		{name: "write mixed override", basePath: "exports/nested", path: "users.json", override: `sub/..\..\secret.json`, write: true, wantErr: true},
		{name: "absolute operation path", basePath: "exports/nested", path: "/secret.json", wantErr: true},
		{name: "traversing base", basePath: "exports/../exports/nested", path: "users.json", wantErr: true},
		{name: "absolute base", basePath: "/exports/nested", path: "users.json", wantErr: true},
		{name: "valid nested read", basePath: "exports/nested", path: "users.json"},
		{name: "valid backslash base", basePath: `exports\nested`, path: "users.json"},
		{name: "valid dot base", basePath: "./exports/nested/", path: "./users.json"},
		{name: "valid nested write override", basePath: "exports/nested", path: "ignored.json", override: "users.json", write: true},
		{name: "valid root read", path: "exports/nested/users.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			remoteDir, sourceDir := t.TempDir(), t.TempDir()
			initBareRemote(t, remoteDir)
			seedRemoteRepo(t, ctx, sourceDir, remoteDir, map[string]string{
				"exports/nested/users.json": `[{"id":1}]`,
				"exports/secret.json":       `[{"id":99}]`,
			})
			connector := &Connector{
				config: Config{RepoURL: remoteDir, DefaultBranch: "main", BasePath: tc.basePath},
				creds:  Credentials{AuthType: AuthTypeHTTPS},
			}
			qd := gitQueryData{Path: tc.path, Format: QueryFormatJSON}
			if tc.write {
				qd.Operation = gitOperationWrite
				qd.Content = []any{map[string]any{"id": 2}}
			}
			queryData, err := json.Marshal(qd)
			require.NoError(t, err)
			params := map[string]types.TypedParam{}
			if tc.override != "" {
				params["path"] = types.TypedParam{Value: tc.override, DataType: "text"}
			}
			if tc.param != "" {
				params["file"] = types.TypedParam{Value: tc.param, DataType: "text"}
			}
			execResult, err := connector.Execute(ctx, queryData, params)
			require.NoError(t, err)
			require.NotNil(t, execResult)
			if tc.wantErr {
				require.ErrorContains(t, execResult.Error(), "invalid Git path")
			} else {
				require.NoError(t, execResult.Error())
				fileResult, ok := execResult.Git()
				require.True(t, ok)
				require.Equal(t, "exports/nested/users.json", fileResult.TargetPath)
				if !tc.write {
					require.Equal(t, json.Number("1"), fileResult.Rows[0]["id"])
				}
			}
			store, cloneDir, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
			require.NoError(t, err)
			t.Cleanup(cleanup)
			secret, err := store.ReadFile(ctx, "exports/secret.json", 0)
			require.NoError(t, err)
			require.JSONEq(t, `[{"id":99}]`, string(secret))
			if tc.wantErr {
				require.Equal(t, "seed fixtures", latestGitCommitMessage(t, cloneDir))
			} else if tc.write {
				data, err := store.ReadFile(ctx, "exports/nested/users.json", 0)
				require.NoError(t, err)
				require.JSONEq(t, `[{"id":2}]`, string(data))
			}
		})
	}
}

func initBareRemote(t *testing.T, remoteDir string) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "git", "init", "--bare", remoteDir)
	cmd.Env = testGitEnv()
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git init --bare failed: %s", string(output))
}

func seedRemoteRepo(
	t *testing.T,
	ctx context.Context,
	sourceDir, remoteDir string,
	files map[string]string,
) {
	t.Helper()

	runTestGit(t, ctx, sourceDir, "init")
	runTestGit(t, ctx, sourceDir, "config", "user.name", "connector-fixture")
	runTestGit(t, ctx, sourceDir, "config", "user.email", "connector-fixture@example.com")
	runTestGit(t, ctx, sourceDir, "config", "commit.gpgsign", "false")
	for path, content := range files {
		fullPath := filepath.Join(sourceDir, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o750))
		require.NoError(t, os.WriteFile(fullPath, []byte(content), 0o600))
	}
	runTestGit(t, ctx, sourceDir, "add", "-A")
	runTestGit(t, ctx, sourceDir, "commit", "-m", "seed fixtures")

	require.NoError(t, repostore.PushRemote(ctx, sourceDir, remoteDir, nil, repostore.PushOptions{
		Force:   true,
		Refspec: "HEAD:main",
	}))
}

func runTestGit(t *testing.T, ctx context.Context, repoDir string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	cmd.Env = testGitEnv()
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s failed: %s", strings.Join(args, " "), string(output))
}

func latestGitCommitMessage(t *testing.T, repoDir string) string {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "git", "log", "-1", "--pretty=%B")
	cmd.Dir = repoDir
	cmd.Env = testGitEnv()
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git log failed: %s", string(output))
	return strings.TrimSpace(string(output))
}

func testGitEnv() []string {
	base := os.Environ()
	filtered := make([]string, 0, len(base))
	for _, entry := range base {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func TestSerializeWriteContent_JSONStringContentReturnsErr(t *testing.T) {
	t.Parallel()

	payload, format, writtenRows, err := serializeWriteContent(
		gitQueryData{
			Format:  QueryFormatJSON,
			Content: "id,email\n1,alice@example.com\n2,bob@example.com\n",
		},
		"exports/users.json",
	)
	_ = payload
	_ = format
	_ = writtenRows
	require.ErrorContains(t, err, "JSON Git write requires object or array content")
}

func TestSerializeWriteContent_JSONStructuredContent(t *testing.T) {
	t.Parallel()

	content := []any{
		map[string]any{"id": float64(1), "email": "alice@example.com"},
		map[string]any{"id": float64(2), "email": "bob@example.com"},
	}

	payload, format, writtenRows, err := serializeWriteContent(
		gitQueryData{
			Format:  QueryFormatJSON,
			Content: content,
		},
		"exports/users.json",
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

func TestSerializeWriteContent_JSONScalarContentReturnsErr(t *testing.T) {
	t.Parallel()

	payload, format, writtenRows, err := serializeWriteContent(
		gitQueryData{
			Format:  QueryFormatJSON,
			Content: 42,
		},
		"exports/users.json",
	)
	_ = payload
	_ = format
	_ = writtenRows
	require.ErrorContains(t, err, "JSON Git write requires object or array content")
}

func TestSerializeWriteContent_CSVRawStringContent(t *testing.T) {
	t.Parallel()

	payload, format, writtenRows, err := serializeWriteContent(
		gitQueryData{
			Format:  QueryFormatCSV,
			Content: "id,email\n1,alice@example.com\n2,bob@example.com\n",
		},
		"exports/users.csv",
	)
	require.NoError(t, err)
	require.Equal(t, QueryFormatCSV, format)
	require.Zero(t, writtenRows)
	require.Equal(t, "id,email\n1,alice@example.com\n2,bob@example.com\n", string(payload))
}

func TestSerializeWriteContent_CSVObjectRows(t *testing.T) {
	t.Parallel()

	content := []any{
		map[string]any{"id": float64(1), "email": "alice@example.com"},
		map[string]any{"id": float64(2), "email": "bob@example.com"},
	}

	payload, format, writtenRows, err := serializeWriteContent(
		gitQueryData{
			Format:    QueryFormatCSV,
			Delimiter: ";",
			Content:   content,
		},
		"exports/users.csv",
	)
	require.NoError(t, err)
	require.Equal(t, QueryFormatCSV, format)
	require.Equal(t, 2, writtenRows)
	require.Equal(t, "email;id\nalice@example.com;1\nbob@example.com;2\n", string(payload))
}

func TestSerializeWriteContent_FileBytes(t *testing.T) {
	t.Parallel()

	content := []byte{0, 1, 2}
	payload, format, writtenRows, err := serializeWriteContent(
		gitQueryData{
			Format:  QueryFormatFile,
			Content: content,
		},
		"exports/users.bin",
	)
	require.NoError(t, err)
	require.Equal(t, QueryFormatFile, format)
	require.Zero(t, writtenRows)
	require.Equal(t, content, payload)
}

func TestSerializeWriteContent_SpreadsheetObjectRows(t *testing.T) {
	t.Parallel()

	content := []any{
		map[string]any{"id": float64(1), "email": "alice@example.com"},
		map[string]any{"id": float64(2), "email": "bob@example.com"},
	}

	payload, format, writtenRows, err := serializeWriteContent(
		gitQueryData{
			Format:  QueryFormatSpreadsheet,
			Sheet:   "Orders",
			Range:   "B2",
			Content: content,
		},
		"exports/users.xlsx",
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
