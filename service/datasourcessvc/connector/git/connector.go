// Package git provides a Git connector for Braidplane data sources.
// It executes file read/write operations against a Git repository via shallow clones.
//
//go:generate -command enumer go tool -modfile=../../../../tools/go.mod enumer
//go:generate enumer -type=AuthType -output=auth_type_string.go -text -json -linecomment
package git

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

// Config holds configuration for Git repository connections.
type Config struct {
	RepoURL           string `json:"repo_url"                      validate:"required,min=1"`
	DefaultBranch     string `json:"default_branch,omitempty"`
	BasePath          string `json:"base_path,omitempty"`
	AllowPrivateHosts bool   `json:"allow_private_hosts,omitempty"`
}

type ProviderRepository struct {
	ID            string `json:"id"             validate:"required"`
	Name          string `json:"name"           validate:"required"`
	FullName      string `json:"full_name"      validate:"required"`
	WebURL        string `json:"web_url"        validate:"required,url"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

type ProviderConfig struct {
	Repository ProviderRepository `json:"repository" validate:"required"`
	BasePath   string             `json:"base_path,omitempty"`
}

type ProviderOnboardingConfig struct {
	Provider string `json:"provider" validate:"required,eq=gitlab"`
	BasePath string `json:"base_path,omitempty"`
}

// Credentials holds Git authentication values.
type Credentials struct {
	AuthType      AuthType `json:"auth_type"                 validate:"required,oneof=1 2"`
	Username      string   `json:"username,omitempty"`
	Token         string   `json:"token,omitempty"`
	SSHPrivateKey string   `json:"ssh_private_key,omitempty"`
	SSHPassphrase string   `json:"ssh_passphrase,omitempty"`
	SSHKnownHosts string   `json:"ssh_known_hosts,omitempty"`
}

type AuthType int

const (
	AuthTypeUnknown AuthType = iota // unknown
	AuthTypeHTTPS                   // https
	AuthTypeSSH                     // ssh
)

// Connector is a stateless Git connector. Each operation performs a shallow clone.
type Connector struct {
	config            Config
	creds             Credentials
	prepareRemoteFunc func(context.Context, string, []string) (string, []string, error)
}

// QueryFormat is an alias for filedata.QueryFormat.
type QueryFormat = filedata.QueryFormat

const (
	QueryFormatUnknown     = filedata.QueryFormatUnknown
	QueryFormatAuto        = filedata.QueryFormatAuto
	QueryFormatCSV         = filedata.QueryFormatCSV
	QueryFormatJSON        = filedata.QueryFormatJSON
	QueryFormatFile        = filedata.QueryFormatFile
	QueryFormatSpreadsheet = filedata.QueryFormatSpreadsheet
)

type gitQueryData = remotefile.Statement

const (
	gitOperationRead  = remotefile.OperationRead
	gitOperationWrite = remotefile.OperationWrite
)

var validate = validator.New()

var gitFileAdapter = remotefile.Adapter{
	ConnectorName: "Git",
}

// New creates a new Git connector after validating config and credentials.
// It does NOT establish a persistent connection.
func New(ctx context.Context, config Config, creds Credentials) (*Connector, error) {
	if err := validate.Struct(config); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	if err := validate.Struct(creds); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	if err := validateRepoURLScheme(config.RepoURL); err != nil {
		return nil, err
	}

	hostname, err := extractHostname(config.RepoURL)
	if err != nil {
		return nil, xerror.Wrapf("parse Git repo URL: %w", err)
	}
	if _, err := hostguard.NewDestinationPolicy(config.AllowPrivateHosts).Resolve(ctx, hostname); err != nil {
		return nil, xerror.Wrapf("connect to Git server: %w", err)
	}

	return &Connector{config: config, creds: creds}, nil
}

func validateRepoURLScheme(repoURL string) error {
	repoURL = strings.TrimSpace(repoURL)

	if repoURL == "" {
		return xerror.ValidationFailed().WithPublicDetail("repo_url is required").Detail("repo_url is required")
	}

	if !strings.Contains(repoURL, "://") && strings.Contains(repoURL, "@") &&
		strings.Contains(repoURL, ":") {
		return nil
	}

	u, err := url.Parse(repoURL)
	if err != nil {
		return xerror.ValidationFailed().With(err)
	}

	if u.Scheme == "https" || u.Scheme == "ssh" {
		// Reject URLs with empty hostname to fail fast with clear validation error.
		// Prevents URLs like "https:///repo.git" from passing validation.
		if u.Host == "" {
			return xerror.ValidationFailed().
				WithPublicDetail("repo_url must have a valid hostname").
				Detail("repo_url must have a valid hostname")
		}
		return nil
	}

	return xerror.ValidationFailed().
		WithPublicDetail("repo_url must use https or ssh").
		Detail("repo_url must use https or ssh")
}

// TestConnection verifies the repository is reachable by running git ls-remote.
func (c *Connector) TestConnection(ctx context.Context) error {
	env, modifier, cleanup, err := authEnv(c.creds)
	if err != nil {
		return xerror.Wrapf("prepare Git auth: %w", err)
	}
	defer cleanup()

	repoURL, err := modifier(c.config.RepoURL)
	if err != nil {
		return xerror.Wrapf("build Git repo URL: %w", err)
	}
	repoURL, env, err = c.prepareRemote(ctx, repoURL, env)
	if err != nil {
		return xerror.Wrapf("prepare Git destination: %w", err)
	}

	if err := repostore.CheckRemote(ctx, repoURL, env); err != nil {
		return xerror.Wrapf("git ls-remote failed: %w", err)
	}

	return nil
}

// Close is a no-op for the Git connector since it is stateless.
func (c *Connector) Close() error { return nil }

// Execute runs a Git file operation (read or write) and returns an execution result.
func (c *Connector) Execute(
	ctx context.Context,
	queryData json.RawMessage,
	params map[string]types.TypedParam,
	limits ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	execLimits := types.ResolveExecutionLimits(limits...)
	start := time.Now()

	normalized, err := types.NormalizeParams(params)
	if err != nil {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(),
			xerror.Wrapf("parameter normalization failed: %w", err)), nil
	}

	qd, err := remotefile.DecodeStatement(queryData)
	if err != nil {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(),
			xerror.Unexpected().With(err)), nil
	}

	operation := remotefile.OperationOrDefault(qd.Operation)
	if operation == remotefile.OperationWrite {
		applyWriteParamOverrides(&qd, normalized)
	}

	if strings.TrimSpace(qd.Path) == "" {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(),
			xerror.Unexpected().Detail("Git query data must specify a path")), nil
	}

	if err := substituteSpreadsheetParams(&qd, normalized); err != nil {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(), err), nil
	}

	branch := strings.TrimSpace(qd.Branch)
	if branch == "" {
		branch = strings.TrimSpace(c.config.DefaultBranch)
	}
	if qd.Format == QueryFormatFile && execLimits.MaxFileBytes > 0 {
		ctx, err = repostore.WithNativeFileLimits(ctx, execLimits.MaxFileBytes)
		if err != nil {
			return result.NewGitResult(nil, time.Since(start).Milliseconds(), err), nil
		}
	}

	env, modifier, cleanup, err := authEnv(c.creds)
	if err != nil {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(),
			xerror.Wrapf("prepare Git auth: %w", err)), nil
	}
	defer cleanup()

	repoURL, err := modifier(c.config.RepoURL)
	if err != nil {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(),
			xerror.Wrapf("build Git repo URL: %w", err)), nil
	}
	repoURL, env, err = c.prepareRemote(ctx, repoURL, env)
	if err != nil {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(),
			xerror.Wrapf("prepare Git destination: %w", err)), nil
	}

	basePath, err := joinGitPath(".", c.config.BasePath)
	if err != nil {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(),
			xerror.Wrapf("invalid Git path: %w", err)), nil
	}

	joinedPath, err := joinGitPath(basePath, qd.Path)
	if err != nil {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(),
			xerror.Wrapf("invalid Git path: %w", err)), nil
	}

	repoPath, err := repoRelativePath(".", joinedPath)
	if err != nil {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(),
			xerror.Wrapf("resolve repository-relative path: %w", err)), nil
	}
	var store repostore.RepoStore
	var repoRoot string
	var cleanupClone func()
	if qd.Format == QueryFormatFile {
		store, repoRoot, cleanupClone, err = repostore.CloneNativeFileRemote(ctx, repoURL, branch, repoPath, execLimits.MaxFileBytes, env)
	} else {
		store, repoRoot, cleanupClone, err = repostore.CloneRemote(ctx, repoURL, branch, env)
	}
	if err != nil {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(), err), nil
	}
	defer cleanupClone()

	var fileResult *result.FileResult
	switch operation {
	case gitOperationRead:
		fileResult, err = executeRead(ctx, store, repoPath, qd, execLimits)
	case gitOperationWrite:
		commitMsg := qd.Message
		if commitMsg == "" {
			commitMsg = "update " + qd.Path
		}
		fileResult, err = executeWrite(ctx, store, repoPath, qd, repoRoot, commitMsg, repoURL, env, execLimits)
	default:
		err = xerror.Unexpected().Detailf("unsupported Git operation %q", qd.Operation)
	}
	if err != nil {
		return result.NewGitResult(nil, time.Since(start).Milliseconds(), err), nil
	}

	return result.NewGitResult(fileResult, time.Since(start).Milliseconds(), nil), nil
}

// prepareRemote pins the native Git transport to the data-source destination while retaining the
// original URL hostname for HTTPS certificate validation or SSH known-hosts verification.
func (c *Connector) prepareRemote(ctx context.Context, repoURL string, env []string) (string, []string, error) {
	if c.prepareRemoteFunc != nil {
		return c.prepareRemoteFunc(ctx, repoURL, env)
	}
	return repostore.PrepareRemote(
		ctx,
		repoURL,
		env,
		hostguard.NewDestinationPolicy(c.config.AllowPrivateHosts),
	)
}

func executeRead(
	ctx context.Context,
	store repostore.RepoStore,
	repoPath string,
	qd gitQueryData,
	limits types.ExecutionLimits,
) (*result.FileResult, error) {
	return gitFileAdapter.ExecuteRead(ctx, gitRepoProvider{store: store}, repoPath, qd, limits)
}

func executeWrite(
	ctx context.Context,
	store repostore.RepoStore,
	repoPath string,
	qd gitQueryData,
	repoDir, commitMsg, repoURL string,
	env []string,
	limits ...types.ExecutionLimits,
) (*result.FileResult, error) {
	return gitFileAdapter.ExecuteWrite(ctx, gitRepoProvider{
		store:     store,
		repoDir:   repoDir,
		repoURL:   repoURL,
		env:       env,
		commitMsg: commitMsg,
	}, repoPath, qd, limits...)
}

func repoRelativePath(repoDir, absPath string) (string, error) {
	relPath, err := filepath.Rel(repoDir, absPath)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}

	relPath = filepath.ToSlash(filepath.Clean(relPath))
	if relPath == "." || relPath == "" || strings.HasPrefix(relPath, "../") || relPath == ".." {
		return "", xerror.Unexpected().Detail("path escapes repository root")
	}

	return relPath, nil
}

func applyWriteParamOverrides(qd *gitQueryData, params map[string]any) {
	if qd == nil || len(params) == 0 {
		return
	}

	remotefile.ApplyWriteParamOverrides(
		qd,
		params,
		remotefile.WriteOverrideOptions{TargetParamName: "path"},
	)

	if branch, ok := params["branch"].(string); ok && strings.TrimSpace(branch) != "" {
		qd.Branch = branch
	}

	if message, ok := params["message"].(string); ok && strings.TrimSpace(message) != "" {
		qd.Message = message
	}
}

// substituteSpreadsheetParams resolves sheet and range placeholders for spreadsheet operations.
func substituteSpreadsheetParams(qd *gitQueryData, params map[string]any) error {
	return remotefile.SubstituteCommonParams(qd, params, "path")
}

func serializeWriteContent(qd gitQueryData, filePath string) ([]byte, QueryFormat, int, error) {
	return gitFileAdapter.SerializeWriteContent(qd, filePath)
}

type gitRepoProvider struct {
	store     repostore.RepoStore
	repoDir   string
	repoURL   string
	env       []string
	commitMsg string
}

// ReadFile reads repository bytes while preserving Git file error context.
func (provider gitRepoProvider) ReadFile(
	ctx context.Context,
	target string,
	maxBytes int64,
) (*remotefile.ReadResult, error) {
	content, err := provider.store.ReadFile(ctx, target, maxBytes)
	if err != nil {
		return nil, xerror.Wrapf("read Git file %q: %w", target, err)
	}

	return &remotefile.ReadResult{Content: content}, nil
}

// WriteFile commits and pushes repository bytes through the Git provider boundary.
func (provider gitRepoProvider) WriteFile(
	ctx context.Context,
	req remotefile.WriteRequest,
) (*remotefile.WriteResult, error) {
	if err := provider.store.WriteFile(ctx, req.Target, req.Content, provider.commitMsg); err != nil {
		return nil, xerror.Wrapf("write Git file %q: %w", req.Target, err)
	}

	if err := repostore.PushRemote(
		ctx,
		provider.repoDir,
		provider.repoURL,
		provider.env,
		repostore.PushOptions{},
	); err != nil {
		return nil, xerror.Wrapf("git push: %w", err)
	}

	return &remotefile.WriteResult{WrittenBytes: len(req.Content)}, nil
}

// extractHostname parses the hostname from a Git repository URL.
// Supports https://, git@host:path (SCP-style SSH), and ssh:// schemes.
func extractHostname(repoURL string) (string, error) {
	repoURL = strings.TrimSpace(repoURL)

	// SCP-style SSH: git@github.com:org/repo.git
	if !strings.Contains(repoURL, "://") && strings.Contains(repoURL, "@") &&
		strings.Contains(repoURL, ":") {
		at := strings.Index(repoURL, "@")
		colon := strings.Index(repoURL[at:], ":")
		if colon < 0 {
			return "", xerror.Unexpected().Detailf("cannot parse SCP-style SSH URL %q", repoURL)
		}
		return repoURL[at+1 : at+colon], nil
	}

	u, err := url.Parse(repoURL)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	if u.Hostname() == "" {
		return "", xerror.Unexpected().Detailf("URL %q has no hostname", repoURL)
	}
	return u.Hostname(), nil
}
