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

	hostname, err := validateRemoteAuth(config.RepoURL, creds.AuthType)
	if err != nil {
		return nil, err
	}
	if _, err := hostguard.NewDestinationPolicy(config.AllowPrivateHosts).Resolve(ctx, hostname); err != nil {
		return nil, xerror.Wrapf("connect to Git server: %w", err)
	}

	return &Connector{config: config, creds: creds}, nil
}

// ValidateRemoteAuth checks the Git remote and credential transport without contacting the host.
func ValidateRemoteAuth(config *Config, credentials Credentials) error {
	_, err := validateRemoteAuth(config.RepoURL, credentials.AuthType)
	if err != nil {
		return err
	}
	if credentials.AuthType == AuthTypeSSH {
		if strings.TrimSpace(credentials.SSHPrivateKey) == "" {
			return gitRemoteInputError("SSH authentication requires ssh_private_key")
		}
		if strings.TrimSpace(credentials.SSHKnownHosts) == "" {
			return gitRemoteInputError("SSH authentication requires ssh_known_hosts")
		}
	}
	return nil
}

func validateRemoteAuth(repoURL string, authType AuthType) (string, error) {
	host, transport, err := parseRepoURL(repoURL)
	if err != nil {
		return "", err
	}
	if transport != authType {
		return "", gitRemoteInputError("Git authentication must match the repository URL transport")
	}
	return host, nil
}

func parseRepoURL(repoURL string) (string, AuthType, error) {
	repoURL = strings.TrimSpace(repoURL)

	if repoURL == "" {
		return "", AuthTypeUnknown, gitRemoteInputError("repo_url is required")
	}
	if strings.ContainsAny(repoURL, "?#\\\r\n\x00") {
		return "", AuthTypeUnknown, gitRemoteInputError("repo_url must not include a query, fragment, or invalid characters")
	}

	if !strings.Contains(repoURL, "://") {
		user, remote, at := strings.Cut(repoURL, "@")
		host, path, colon := strings.Cut(remote, ":")
		if at && colon {
			if !validSSHLogin(user) {
				return "", AuthTypeUnknown, gitRemoteInputError("repo_url must not include embedded credentials")
			}
			if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, "/@[] \t") || path == "" {
				return "", AuthTypeUnknown, gitRemoteInputError("repo_url must have a valid hostname and repository path")
			}
			return host, AuthTypeSSH, nil
		}
	}

	u, err := url.Parse(repoURL)
	if err != nil {
		return "", AuthTypeUnknown, gitRemoteInputError("repo_url is invalid")
	}
	var transport AuthType
	switch u.Scheme {
	case "https":
		transport = AuthTypeHTTPS
	case "ssh":
		transport = AuthTypeSSH
	default:
		return "", AuthTypeUnknown, gitRemoteInputError("repo_url must use https or ssh")
	}
	if u.Hostname() == "" {
		return "", AuthTypeUnknown, gitRemoteInputError("repo_url must have a valid hostname")
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); transport != AuthTypeSSH || hasPassword || !validSSHLogin(u.User.Username()) {
			return "", AuthTypeUnknown, gitRemoteInputError("repo_url must not include embedded credentials")
		}
	}
	return u.Hostname(), transport, nil
}

func validSSHLogin(user string) bool {
	return user != "" && !strings.HasPrefix(user, "-") && !strings.ContainsAny(user, ":/ \t\r\n\x00")
}

func gitRemoteInputError(message string) error {
	return xerror.ValidationFailed().WithPublicDetail(message).Detail(message)
}

// PublicRepoURL returns the safe display form of a stored Git URL.
func PublicRepoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		if parsed.Scheme == "ssh" && parsed.User != nil && parsed.User.Username() != "" {
			parsed.User = url.User(parsed.User.Username())
		} else {
			parsed.User = nil
		}
		parsed.RawQuery, parsed.Fragment, parsed.RawFragment = "", "", ""
		parsed.ForceQuery = false
		raw = parsed.String()
	} else if end := strings.IndexAny(raw, "?#"); end >= 0 {
		raw = raw[:end]
	}
	if _, _, err := parseRepoURL(raw); err != nil {
		return ""
	}
	return raw
}

// PublicRepoIdentity handles normalized host/path identities as well as Git URLs.
func PublicRepoIdentity(raw string) string {
	host, repo, slash := strings.Cut(raw, "/")
	if slash && repo != "" && strings.Contains(host, ".") && !strings.ContainsAny(host, ":%@?#\\ \t\r\n") {
		return strings.TrimPrefix(PublicRepoURL("https://"+raw), "https://")
	}
	return PublicRepoURL(raw)
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
	switch {
	case qd.Format == QueryFormatFile:
		store, repoRoot, cleanupClone, err = repostore.CloneNativeFileRemote(ctx, repoURL, branch, repoPath, execLimits.MaxFileBytes, env)
	case operation == gitOperationRead:
		store, cleanupClone, err = repostore.CloneReadFileRemote(ctx, repoURL, branch, repoPath, execLimits.MaxResponseBytes, env)
	default:
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
