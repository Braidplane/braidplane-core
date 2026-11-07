package git_e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	gitconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/git"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

const (
	softServeHost = "127.0.0.1"
	testRepoName  = "git-connector-e2e-repo"
)

var (
	bootstrapOnce       sync.Once
	errBootstrap        error
	bootstrapRepoURL    string
	bootstrapPrivateKey string
)

func TestGitConnector_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	sshPort := gitSSHPort()
	require.Truef(
		t,
		isReachable(softServeHost, sshPort),
		"soft-serve is not reachable at %s:%d",
		softServeHost,
		sshPort,
	)
	_, err := os.Stat(testPrivateKeyPath())
	require.NoError(t, err, "git test SSH key must exist at %s", testPrivateKeyPath())
	knownHosts, err := scanSoftServeKnownHosts(context.Background(), sshPort)
	require.NoError(t, err)

	t.Parallel()

	t.Run("SSHConnectionSuccessWithValidKey", func(t *testing.T) {
		t.Parallel()

		repoURL, privateKey := ensureBootstrap(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		conn, err := gitconnector.New(ctx, testConfig(repoURL), gitconnector.Credentials{
			AuthType:      authTypeSSH,
			SSHPrivateKey: privateKey,
			SSHKnownHosts: knownHosts,
		})
		require.NoError(t, err)

		err = conn.TestConnection(ctx)
		require.NoError(t, err)
	})

	t.Run("SSHConnectionFailureWithWrongKey", func(t *testing.T) {
		t.Parallel()

		repoURL, _ := ensureBootstrap(t)
		wrongPrivateKey, wrongPublicKey, err := generateEd25519KeyPair()
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(wrongPublicKey, "ssh-ed25519 "))

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		conn, err := gitconnector.New(ctx, testConfig(repoURL), gitconnector.Credentials{
			AuthType:      authTypeSSH,
			SSHPrivateKey: wrongPrivateKey,
			SSHKnownHosts: knownHosts,
		})
		require.NoError(t, err)

		err = conn.TestConnection(ctx)
		require.Error(t, err)
	})

	t.Run("ReadCSVOverSSH", func(t *testing.T) {
		t.Parallel()

		repoURL, privateKey := ensureBootstrap(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		conn, err := gitconnector.New(ctx, testConfig(repoURL), gitconnector.Credentials{
			AuthType:      authTypeSSH,
			SSHPrivateKey: privateKey,
			SSHKnownHosts: knownHosts,
		})
		require.NoError(t, err)

		res, err := executeGit(ctx, conn, map[string]any{
			"type":       "file",
			"operation":  "read",
			"path":       "users.csv",
			"format":     "csv",
			"has_header": true,
			"branch":     "main",
		}, map[string]any{})
		require.NoError(t, err)
		require.Equal(t, 2, res.RowCount)
		require.Equal(t, "alice@example.com", res.Rows[0]["email"])
		require.Equal(t, "bob@example.com", res.Rows[1]["email"])
	})

	t.Run("ReadJSONOverSSH", func(t *testing.T) {
		t.Parallel()

		repoURL, privateKey := ensureBootstrap(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		conn, err := gitconnector.New(ctx, testConfig(repoURL), gitconnector.Credentials{
			AuthType:      authTypeSSH,
			SSHPrivateKey: privateKey,
			SSHKnownHosts: knownHosts,
		})
		require.NoError(t, err)

		res, err := executeGit(ctx, conn, map[string]any{
			"type":      "file",
			"operation": "read",
			"path":      "users.json",
			"format":    "json",
			"branch":    "main",
		}, map[string]any{})
		require.NoError(t, err)
		require.Equal(t, 2, res.RowCount)
		require.Equal(t, "alice@example.com", res.Rows[0]["email"])
		require.Equal(t, "bob@example.com", res.Rows[1]["email"])
	})

	t.Run("ReadSpreadsheetOverSSH", func(t *testing.T) {
		t.Parallel()

		repoURL, privateKey := ensureBootstrap(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		conn, err := gitconnector.New(ctx, testConfig(repoURL), gitconnector.Credentials{
			AuthType:      authTypeSSH,
			SSHPrivateKey: privateKey,
			SSHKnownHosts: knownHosts,
		})
		require.NoError(t, err)

		res, err := executeGit(ctx, conn, map[string]any{
			"type":       "file",
			"operation":  "read",
			"path":       "users.xlsx",
			"format":     "spreadsheet",
			"sheet":      "Orders",
			"range":      "B2:D4",
			"has_header": true,
			"branch":     "main",
		}, map[string]any{})
		require.NoError(t, err)
		require.Equal(t, 2, res.RowCount)
		require.Equal(t, "Orders", res.Sheet)
		require.Equal(t, "B2:D4", res.Range)
		require.Equal(t, []string{"email", "id", "total"}, res.ColumnNames)
		require.Equal(t, "alice@example.com", res.Rows[0]["email"])
		require.Equal(t, "42", res.Rows[0]["total"])
		require.Equal(t, "bob@example.com", res.Rows[1]["email"])
	})

	t.Run("ReadCSVReturnsRowLimitViolation", func(t *testing.T) {
		t.Parallel()

		repoURL, privateKey := ensureBootstrap(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		conn, err := gitconnector.New(ctx, testConfig(repoURL), gitconnector.Credentials{
			AuthType:      authTypeSSH,
			SSHPrivateKey: privateKey,
			SSHKnownHosts: knownHosts,
		})
		require.NoError(t, err)

		_, err = executeGitWithLimits(ctx, conn, map[string]any{
			"type":       "file",
			"operation":  "read",
			"path":       "users.csv",
			"format":     "csv",
			"has_header": true,
			"branch":     "main",
		}, map[string]any{}, types.ExecutionLimits{MaxResultRows: 1})
		require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
	})

	t.Run("WriteCSVOverSSH", func(t *testing.T) {
		t.Parallel()

		repoURL, privateKey := ensureBootstrap(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		conn, err := gitconnector.New(ctx, testConfig(repoURL), gitconnector.Credentials{
			AuthType:      authTypeSSH,
			SSHPrivateKey: privateKey,
			SSHKnownHosts: knownHosts,
		})
		require.NoError(t, err)

		writeResult, err := executeGit(ctx, conn, map[string]any{
			"type":       "file",
			"operation":  "write",
			"path":       "roundtrip.csv",
			"format":     "csv",
			"has_header": true,
			"branch":     "main",
			"message":    "write roundtrip csv fixture",
			"content": []any{
				map[string]any{"id": 101, "email": "carol@example.com"},
				map[string]any{"id": 102, "email": "dave@example.com"},
			},
		}, map[string]any{})
		require.NoError(t, err)
		require.NotNil(t, writeResult.WrittenRows)
		require.Equal(t, 2, *writeResult.WrittenRows)

		readResult, err := executeGit(ctx, conn, map[string]any{
			"type":       "file",
			"operation":  "read",
			"path":       "roundtrip.csv",
			"format":     "csv",
			"has_header": true,
			"branch":     "main",
		}, map[string]any{})
		require.NoError(t, err)
		require.Equal(t, 2, readResult.RowCount)
		require.Equal(t, "carol@example.com", readResult.Rows[0]["email"])
		require.Equal(t, "dave@example.com", readResult.Rows[1]["email"])
	})

	// This subtest writes to the shared Soft Serve repo and should not race another writer.
	t.Run("WriteAndReadUsingNamespacedRuntimePathParams", func(t *testing.T) {
		repoURL, privateKey := ensureBootstrap(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		conn, err := gitconnector.New(ctx, testConfig(repoURL), gitconnector.Credentials{
			AuthType:      authTypeSSH,
			SSHPrivateKey: privateKey,
			SSHKnownHosts: knownHosts,
		})
		require.NoError(t, err)

		runDateTime := time.Now().UTC().Format(time.RFC3339)
		pathSuffix := time.Now().UnixNano()
		pathTemplate := fmt.Sprintf("reports/{{runtime.run.datetime}}/users-%d.json", pathSuffix)
		expectedPath := fmt.Sprintf("reports/%s/users-%d.json", runDateTime, pathSuffix)

		writeResult, err := executeGit(ctx, conn, map[string]any{
			"type":      "file",
			"operation": "write",
			"path":      pathTemplate,
			"format":    "json",
			"branch":    "main",
			"message":   "write runtime path json fixture",
			"content": []any{
				map[string]any{"id": 301, "email": "runtime-carol@example.com"},
				map[string]any{"id": 302, "email": "runtime-dave@example.com"},
			},
		}, map[string]any{
			"runtime.run.datetime": runDateTime,
		})
		require.NoError(t, err)
		require.Equal(t, expectedPath, writeResult.TargetPath)
		require.NotNil(t, writeResult.WrittenRows)
		require.Equal(t, 2, *writeResult.WrittenRows)

		readResult, err := executeGit(ctx, conn, map[string]any{
			"type":      "file",
			"operation": "read",
			"path":      pathTemplate,
			"format":    "json",
			"branch":    "main",
		}, map[string]any{
			"runtime.run.datetime": runDateTime,
		})
		require.NoError(t, err)
		require.Equal(t, expectedPath, readResult.TargetPath)
		require.Equal(t, 2, readResult.RowCount)
		require.Equal(t, "runtime-carol@example.com", readResult.Rows[0]["email"])
		require.Equal(t, "runtime-dave@example.com", readResult.Rows[1]["email"])
	})

	t.Run("SchemaIntrospectionOverSSH", func(t *testing.T) {
		t.Parallel()

		repoURL, privateKey := ensureBootstrap(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		conn, err := gitconnector.New(ctx, testConfig(repoURL), gitconnector.Credentials{
			AuthType:      authTypeSSH,
			SSHPrivateKey: privateKey,
			SSHKnownHosts: knownHosts,
		})
		require.NoError(t, err)

		schema, err := conn.IntrospectSchema(ctx)
		require.NoError(t, err)

		files := map[string]filedata.File{}
		for _, file := range schema.Files {
			files[file.Name] = file
		}

		usersCSV, ok := files["users.csv"]
		require.True(t, ok)
		require.Contains(t, fieldNames(usersCSV.Fields), "id")
		require.Contains(t, fieldNames(usersCSV.Fields), "email")

		usersJSON, ok := files["users.json"]
		require.True(t, ok)
		require.Contains(t, fieldNames(usersJSON.Fields), "profile.locale")
		require.Contains(t, fieldNames(usersJSON.Fields), "items[]")
		require.Contains(t, fieldNames(usersJSON.Fields), "items[].sku")

		usersXLSX, ok := files["users.xlsx"]
		require.True(t, ok)
		require.Contains(t, fieldNames(usersXLSX.Fields), "email")
		require.Contains(t, fieldNames(usersXLSX.Fields), "total")
	})
}

// TestRunGitInDirIgnoresAmbientGitRepoContext verifies fixture setup cannot
// mutate the caller repository when git hook environment variables are present.
func TestRunGitInDirIgnoresAmbientGitRepoContext(t *testing.T) {
	ctx := context.Background()
	victimDir := t.TempDir()
	fixtureDir := t.TempDir()
	victimEnv := isolatedGitEnv(victimDir, nil)
	fixtureEnv := isolatedGitEnv(fixtureDir, nil)

	require.NoError(t, os.WriteFile(filepath.Join(victimDir, "README.md"), []byte("# victim\n"), 0o600))
	if out, err := runGitInDir(ctx, victimDir, victimEnv, "init"); err != nil {
		t.Fatalf("init victim repo: %v (%s)", err, out)
	}
	if out, err := runGitInDir(ctx, victimDir, victimEnv, "add", "-A"); err != nil {
		t.Fatalf("stage victim repo: %v (%s)", err, out)
	}
	if out, err := runGitInDir(
		ctx,
		victimDir,
		victimEnv,
		"-c",
		"user.name=victim",
		"-c",
		"user.email=victim@example.com",
		"-c",
		"commit.gpgsign=false",
		"commit",
		"-m",
		"seed victim",
	); err != nil {
		t.Fatalf("commit victim repo: %v (%s)", err, out)
	}
	victimHead, err := runGitInDir(ctx, victimDir, victimEnv, "rev-parse", "HEAD")
	require.NoError(t, err)

	t.Setenv("GIT_DIR", filepath.Join(victimDir, ".git"))
	t.Setenv("GIT_WORK_TREE", victimDir)

	require.NoError(t, writeFixtureFiles(ctx, fixtureDir, fixtureEnv))

	afterHead, err := runGitInDir(ctx, victimDir, victimEnv, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, victimHead, afterHead)

	status, err := runGitInDir(ctx, victimDir, victimEnv, "status", "--short")
	require.NoError(t, err)
	require.Empty(t, status)

	fixtureEmail, err := runGitInDir(ctx, fixtureDir, fixtureEnv, "log", "-1", "--pretty=%ae")
	require.NoError(t, err)
	require.Equal(t, "git-e2e-fixture@example.com", fixtureEmail)
}

func ensureBootstrap(t *testing.T) (string, string) {
	t.Helper()

	bootstrapOnce.Do(func() {
		bootstrapRepoURL, bootstrapPrivateKey, errBootstrap = bootstrapSoftServe()
	})

	require.NoError(t, errBootstrap)
	return bootstrapRepoURL, bootstrapPrivateKey
}

func bootstrapSoftServe() (string, string, error) {
	sshPort := gitSSHPort()
	keyPath := testPrivateKeyPath()
	privateKeyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return "", "", fmt.Errorf("read test SSH private key: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	knownHosts, err := scanSoftServeKnownHosts(ctx, sshPort)
	if err != nil {
		return "", "", err
	}
	knownHostsPath, err := writeKnownHostsFile(knownHosts)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.Remove(knownHostsPath) }()

	sshArgs := []string{
		"-p", strconv.Itoa(sshPort),
		"-i", keyPath,
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHostsPath,
		"-o", "IdentitiesOnly=yes",
		"admin@" + softServeHost,
	}
	createArgs := slices.Concat(sshArgs, []string{"repo", "create", testRepoName})
	createOut, createErr := runCommand(ctx, nil, "ssh", createArgs...)
	if createErr != nil && !strings.Contains(strings.ToLower(createOut), "already") {
		return "", "", fmt.Errorf("create soft serve repo: %w (%s)", createErr, createOut)
	}
	privateArgs := slices.Concat(sshArgs, []string{"repo", "private", testRepoName, "true"})
	if out, err := runCommand(ctx, nil, "ssh", privateArgs...); err != nil {
		return "", "", fmt.Errorf("make soft serve repo private: %w (%s)", err, out)
	}

	tmpDir, err := os.MkdirTemp("", "braidplane-soft-serve-seed-*")
	if err != nil {
		return "", "", fmt.Errorf("create temp repo dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	env := isolatedGitEnv(tmpDir, nil)
	if err := writeFixtureFiles(ctx, tmpDir, env); err != nil {
		return "", "", err
	}

	repoURL := sshRepoURL(sshPort)

	gitSSHCommand := fmt.Sprintf(
		"ssh -i %s -o StrictHostKeyChecking=yes -o UserKnownHostsFile=%s -o IdentitiesOnly=yes",
		keyPath,
		knownHostsPath,
	)
	pushEnv := isolatedGitEnv(tmpDir, []string{"GIT_SSH_COMMAND=" + gitSSHCommand})
	if err := repostore.PushRemote(ctx, tmpDir, repoURL, pushEnv, repostore.PushOptions{
		Force:   true,
		Refspec: "HEAD:main",
	}); err != nil {
		return "", "", fmt.Errorf("git push fixtures: %w", err)
	}

	return repoURL, string(privateKeyBytes), nil
}

// writeFixtureFiles creates the Git fixture repository used by connector E2E tests.
func writeFixtureFiles(ctx context.Context, repoDir string, env []string) error {
	if out, err := runGitInDir(ctx, repoDir, env, "init"); err != nil {
		return fmt.Errorf("init git fixture repo: %w (%s)", err, out)
	}

	csvContent := "id,email\n1,alice@example.com\n2,bob@example.com\n"
	if err := writeFixtureFile(repoDir, "users.csv", []byte(csvContent)); err != nil {
		return fmt.Errorf("write users.csv fixture: %w", err)
	}

	jsonContent := `[
  {
    "id": 1,
    "email": "alice@example.com",
    "profile": {
      "locale": "en"
    },
    "items": [
      {
        "sku": "SKU-1",
        "price": 12.5
      }
    ]
  },
  {
    "id": 2,
    "email": "bob@example.com",
    "profile": {
      "locale": "pl"
    },
    "items": [
      {
        "sku": "SKU-2",
        "price": 14.5
      }
    ]
  }
]`
	if err := writeFixtureFile(repoDir, "users.json", []byte(jsonContent)); err != nil {
		return fmt.Errorf("write users.json fixture: %w", err)
	}
	spreadsheetContent, _, err := filedata.EncodeSpreadsheetContent(
		[]map[string]any{
			{"id": 1, "email": "alice@example.com", "total": 42},
			{"id": 2, "email": "bob@example.com", "total": 19},
		},
		filedata.SpreadsheetOptions{Sheet: "Orders", Range: "B2"},
	)
	if err != nil {
		return fmt.Errorf("build users.xlsx fixture: %w", err)
	}
	if err := writeFixtureFile(repoDir, "users.xlsx", spreadsheetContent); err != nil {
		return fmt.Errorf("write users.xlsx fixture: %w", err)
	}
	if out, err := runGitInDir(ctx, repoDir, env, "add", "-A"); err != nil {
		return fmt.Errorf("stage git fixture files: %w (%s)", err, out)
	}
	if out, err := runGitInDir(
		ctx,
		repoDir,
		env,
		"-c",
		"user.name=git-e2e-fixture",
		"-c",
		"user.email=git-e2e-fixture@example.com",
		"-c",
		"commit.gpgsign=false",
		"commit",
		"-m",
		"seed git connector fixtures",
	); err != nil {
		return fmt.Errorf("commit git fixture files: %w (%s)", err, out)
	}

	return nil
}

// writeFixtureFile writes one fixture file below the temporary repository root.
func writeFixtureFile(repoDir, path string, content []byte) error {
	fullPath := filepath.Join(repoDir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
		return err
	}
	return os.WriteFile(fullPath, content, 0o600)
}

func executeGit(
	ctx context.Context,
	conn *gitconnector.Connector,
	query map[string]any,
	params map[string]any,
) (*result.FileResult, error) {
	return executeGitWithLimits(ctx, conn, query, params, types.ExecutionLimits{})
}

func executeGitWithLimits(
	ctx context.Context,
	conn *gitconnector.Connector,
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

	fileResult, ok := execResult.Git()
	if !ok {
		return nil, fmt.Errorf("expected file execution result, got: %s", execResult.Type())
	}
	return fileResult, nil
}

func fieldNames(fields []filedata.Field) []string {
	names := make([]string, 0, len(fields))
	for _, field := range fields {
		names = append(names, field.Name)
	}
	return names
}

func runCommand(ctx context.Context, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = repostore.SanitizedGitEnv(os.Environ(), env)
	}
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func runGitInDir(ctx context.Context, repoDir string, env []string, args ...string) (string, error) {
	if mutatesGitRepo(args) {
		if err := verifyGitRepoRoot(ctx, repoDir, env); err != nil {
			return "", err
		}
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	cmd.Env = repostore.SanitizedGitEnv(os.Environ(), env)
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

// isolatedGitEnv returns Git process settings isolated from user/global config.
func isolatedGitEnv(repoDir string, extra []string) []string {
	home := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-git-home")
	configHome := filepath.Join(home, ".config")
	return append(
		[]string{
			"HOME=" + home,
			"XDG_CONFIG_HOME=" + configHome,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0",
		},
		extra...,
	)
}

// mutatesGitRepo reports whether a git command can change repository state.
func mutatesGitRepo(args []string) bool {
	switch gitSubcommand(args) {
	case "add", "checkout", "commit", "config", "push", "remote":
		return true
	default:
		return false
	}
}

// gitSubcommand returns the first non-option git argument after global options.
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" && i+1 < len(args) {
			i++
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		return args[i]
	}
	return ""
}

// verifyGitRepoRoot prevents fixture commands from mutating a redirected repo.
func verifyGitRepoRoot(ctx context.Context, repoDir string, env []string) error {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = repoDir
	cmd.Env = repostore.SanitizedGitEnv(os.Environ(), env)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("verify git fixture root: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	got, err := canonicalPath(strings.TrimSpace(string(output)))
	if err != nil {
		return err
	}
	want, err := canonicalPath(repoDir)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("refusing fixture git mutation in %s; git resolved %s", want, got)
	}
	return nil
}

// canonicalPath resolves symlinks so temp paths compare reliably on macOS.
func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func scanSoftServeKnownHosts(ctx context.Context, sshPort int) (string, error) {
	out, err := runCommand(
		ctx,
		nil,
		"ssh-keyscan",
		"-p", strconv.Itoa(sshPort),
		softServeHost,
	)
	if err != nil {
		return "", fmt.Errorf("scan soft serve host key: %w (%s)", err, out)
	}

	knownHosts := strings.TrimSpace(out)
	if knownHosts == "" {
		return "", fmt.Errorf("scan soft serve host key: empty known_hosts output")
	}

	return knownHosts + "\n", nil
}

func writeKnownHostsFile(knownHosts string) (string, error) {
	tmpFile, err := os.CreateTemp("", "braidplane-soft-serve-known-hosts-*")
	if err != nil {
		return "", fmt.Errorf("create known_hosts file: %w", err)
	}

	if _, err := tmpFile.WriteString(knownHosts); err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", fmt.Errorf("write known_hosts file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", fmt.Errorf("close known_hosts file: %w", err)
	}

	return tmpFile.Name(), nil
}

func generateEd25519KeyPair() (string, string, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate ed25519 key pair: %w", err)
	}

	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal private key: %w", err)
	}

	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	if len(privatePEM) == 0 {
		return "", "", fmt.Errorf("encode private key to PEM")
	}

	sshPublic, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return "", "", fmt.Errorf("convert public key to SSH format: %w", err)
	}

	publicAuthorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublic)))
	return string(privatePEM), publicAuthorized, nil
}

func testPrivateKeyPath() string {
	return depsports.GitTestPrivateKeyPath()
}

func gitSSHPort() int {
	return depsports.GitSSHPort()
}

func sshRepoURL(port int) string {
	return "ssh://admin@" + net.JoinHostPort(softServeHost, strconv.Itoa(port)) + "/" + testRepoName
}

func testConfig(repoURL string) gitconnector.Config {
	return gitconnector.Config{
		RepoURL:           repoURL,
		DefaultBranch:     "main",
		AllowPrivateHosts: true,
	}
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

const (
	authTypeSSH   = gitconnector.AuthTypeSSH
	authTypeHTTPS = gitconnector.AuthTypeHTTPS
)
