package repostore

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// RemoteGit encapsulates remote git operations.
type RemoteGit interface {
	CheckRemote(ctx context.Context, repoURL string, env []string) error
	ListRemoteBranches(ctx context.Context, repoURL string, env []string) ([]RemoteBranch, error)
	CloneRemote(
		ctx context.Context,
		repoURL, branch string,
		env []string,
	) (RepoStore, string, func(), error)
	PushRemote(ctx context.Context, repoDir, repoURL string, env []string, opts PushOptions) error
}

// NewRemoteGit returns the default remote git operation client.
func NewRemoteGit() RemoteGit {
	return remoteGit{}
}

type remoteGit struct{}

type gitRemoteAdapter struct{}

type githubRemoteAdapter struct {
	delegate RemoteGit
}

type gitlabRemoteAdapter struct {
	delegate RemoteGit
}

type providerPATConfig struct {
	tokenEnvKey  string
	username     string
	expectedHost string
}

var genericGitRemoteAdapter RemoteGit = gitRemoteAdapter{}

func remoteAdapterForRepoURL(repoURL string) RemoteGit {
	switch DetectProvider(repoURL) {
	case ProviderGitHub:
		return githubRemoteAdapter{delegate: genericGitRemoteAdapter}
	case ProviderGitLab:
		return gitlabRemoteAdapter{delegate: genericGitRemoteAdapter}
	default:
		return genericGitRemoteAdapter
	}
}

func (remoteGit) CheckRemote(ctx context.Context, repoURL string, env []string) error {
	return remoteAdapterForRepoURL(repoURL).CheckRemote(ctx, repoURL, env)
}

func (remoteGit) ListRemoteBranches(
	ctx context.Context,
	repoURL string,
	env []string,
) ([]RemoteBranch, error) {
	return remoteAdapterForRepoURL(repoURL).ListRemoteBranches(ctx, repoURL, env)
}

func (remoteGit) CloneRemote(
	ctx context.Context,
	repoURL, branch string,
	env []string,
) (RepoStore, string, func(), error) {
	return remoteAdapterForRepoURL(repoURL).CloneRemote(ctx, repoURL, branch, env)
}

func (remoteGit) PushRemote(
	ctx context.Context,
	repoDir, repoURL string,
	env []string,
	opts PushOptions,
) error {
	return remoteAdapterForRepoURL(repoURL).PushRemote(ctx, repoDir, repoURL, env, opts)
}

func (gitRemoteAdapter) CheckRemote(ctx context.Context, repoURL string, env []string) error {
	if err := validateRemoteRepoURLScheme(repoURL); err != nil {
		return err
	}

	safeRepoURL := SanitizeRemoteURL(repoURL)

	stderrMsg, err := runGitCommand(ctx, "", env, "ls-remote", "--exit-code", repoURL)
	if err != nil {
		if xerror.Is(err, errGitCommandOutputExceeded) {
			return ErrRepositorySetup.Detail("git ls-remote output exceeded its byte limit").WithPublicDetail(
				"The remote repository returned too much data",
			)
		}
		if stderrMsg == "" {
			stderrMsg = err.Error()
		}
		publicDetail := gitRemoteCommandPublicDetail("ls-remote", stderrMsg)
		return ErrRepositorySetup.Detailf(
			"git ls-remote %q: %s",
			safeRepoURL,
			sanitizeGitOutput(stderrMsg),
		).WithPublicDetail(publicDetail)
	}

	return nil
}

func (gitRemoteAdapter) ListRemoteBranches(
	ctx context.Context,
	repoURL string,
	env []string,
) ([]RemoteBranch, error) {
	if err := validateRemoteRepoURLScheme(repoURL); err != nil {
		return nil, err
	}

	safeRepoURL := SanitizeRemoteURL(repoURL)

	stdoutMsg, stderrMsg, err := runGitCommandOutput(
		ctx,
		"",
		env,
		"ls-remote",
		"--symref",
		repoURL,
		"HEAD",
		"refs/heads/*",
	)
	if err != nil {
		if xerror.Is(err, errGitCommandOutputExceeded) {
			return nil, ErrRepositorySetup.Detail("git ls-remote output exceeded its byte limit").WithPublicDetail(
				"The remote repository returned too many branch references",
			)
		}
		if stderrMsg == "" {
			stderrMsg = err.Error()
		}
		publicDetail := gitRemoteCommandPublicDetail("ls-remote", stderrMsg)
		return nil, ErrRepositorySetup.Detailf(
			"git ls-remote %q: %s",
			safeRepoURL,
			sanitizeGitOutput(stderrMsg),
		).WithPublicDetail(publicDetail)
	}

	return parseRemoteBranches(stdoutMsg, gitRemoteBranchMax)
}

// parseRemoteBranches extracts branch names and the default HEAD target from git ls-remote output.
func parseRemoteBranches(output string, maxBranches int) ([]RemoteBranch, error) {
	defaultBranch := ""
	seen := make(map[string]bool)

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) != 2 {
			continue
		}

		if strings.HasPrefix(fields[0], "ref: refs/heads/") && fields[1] == "HEAD" {
			defaultBranch = strings.TrimPrefix(fields[0], "ref: refs/heads/")
			continue
		}

		if strings.HasPrefix(fields[1], "refs/heads/") {
			branchName := strings.TrimPrefix(fields[1], "refs/heads/")
			if branchName != "" {
				if !seen[branchName] && len(seen) >= maxBranches {
					return nil, ErrRepositorySetup.Detail("git ls-remote returned too many branch references").WithPublicDetail(
						"The remote repository returned too many branch references",
					)
				}
				seen[branchName] = true
			}
		}
	}

	branches := make([]RemoteBranch, 0, len(seen))
	for branchName := range seen {
		branches = append(branches, RemoteBranch{
			Name:      branchName,
			IsDefault: branchName == defaultBranch,
		})
	}

	sort.Slice(branches, func(i, j int) bool {
		if branches[i].IsDefault != branches[j].IsDefault {
			return branches[i].IsDefault
		}
		return branches[i].Name < branches[j].Name
	})

	return branches, nil
}

// gitRemoteCommandPublicDetail formats sanitized git stderr for client-facing diagnostics.
func gitRemoteCommandPublicDetail(command, stderrMsg string) string {
	detail := strings.TrimSpace(sanitizeGitOutput(stderrMsg))
	if detail == "" {
		return fmt.Sprintf("git %s failed", command)
	}
	return fmt.Sprintf("git %s failed: %s", command, detail)
}

func (gitRemoteAdapter) CloneRemote(
	ctx context.Context,
	repoURL, branch string,
	env []string,
) (RepoStore, string, func(), error) {
	return cloneRemote(ctx, repoURL, branch, env, nil)
}

func cloneRemote(ctx context.Context, repoURL, branch string, env []string, nativeFile *nativeFileCheckout) (RepoStore, string, func(), error) {
	if err := validateRemoteRepoURLScheme(repoURL); err != nil {
		return nil, "", nil, err
	}

	safeRepoURL := SanitizeRemoteURL(repoURL)

	targetDir, err := os.MkdirTemp("", "braidplane-repostore-clone-*")
	if err != nil {
		return nil, "", nil, ErrRepositorySetup.With(err)
	}

	cleanup := func() {
		_ = os.RemoveAll(targetDir)
	}

	args := []string{"clone", "--depth=1"}
	if nativeFile != nil {
		args = append(args, "--no-checkout", "--no-local", "--no-tags", "--no-recurse-submodules")
		for _, config := range nativeFileCloneConfig {
			args = append(args, "--config", config)
		}
	}
	if strings.TrimSpace(branch) != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, repoURL, targetDir)

	stderrMsg, err := runGitCommand(ctx, "", env, args...)
	if err != nil {
		cleanup()
		if stderrMsg == "" {
			stderrMsg = err.Error()
		}
		publicDetail := gitRemoteCommandPublicDetail("clone", stderrMsg)
		return nil, "", nil, ErrRepositorySetup.Detailf(
			"git clone %q: %s",
			safeRepoURL,
			sanitizeGitOutput(stderrMsg),
		).WithPublicDetail(publicDetail)
	}

	if nativeFile != nil {
		if err := prepareNativeCheckout(ctx, targetDir, *nativeFile); err != nil {
			cleanup()
			return nil, "", nil, err
		}
	}
	store, err := newCheckoutRepoStore(ctx, targetDir)
	if err != nil {
		cleanup()
		return nil, "", nil, ErrRepositorySetup.With(err)
	}

	return store, targetDir, cleanup, nil
}

func (gitRemoteAdapter) PushRemote(
	ctx context.Context,
	repoDir, repoURL string,
	env []string,
	opts PushOptions,
) error {
	if err := validateRemoteRepoURLScheme(repoURL); err != nil {
		return err
	}

	safeRepoURL := SanitizeRemoteURL(repoURL)

	args := []string{"push"}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, repoURL)
	if strings.TrimSpace(opts.Refspec) != "" {
		args = append(args, opts.Refspec)
	}

	stderrMsg, err := runGitCommand(ctx, repoDir, env, args...)
	if err != nil {
		if stderrMsg == "" {
			stderrMsg = err.Error()
		}
		publicDetail := gitRemoteCommandPublicDetail("push", stderrMsg)
		return ErrRepositorySetup.Detailf(
			"git push %q: %s",
			safeRepoURL,
			sanitizeGitOutput(stderrMsg),
		).WithPublicDetail(publicDetail)
	}

	return nil
}

func (a githubRemoteAdapter) CheckRemote(ctx context.Context, repoURL string, env []string) error {
	repoURL, env, cleanup, err := providerPATGitEnv(repoURL, env, ProviderGitHub)
	if err != nil {
		return err
	}
	defer cleanup()

	return a.delegate.CheckRemote(ctx, repoURL, env)
}

func (a githubRemoteAdapter) ListRemoteBranches(
	ctx context.Context,
	repoURL string,
	env []string,
) ([]RemoteBranch, error) {
	repoURL, env, cleanup, err := providerPATGitEnv(repoURL, env, ProviderGitHub)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return a.delegate.ListRemoteBranches(ctx, repoURL, env)
}

func (a githubRemoteAdapter) CloneRemote(
	ctx context.Context,
	repoURL, branch string,
	env []string,
) (RepoStore, string, func(), error) {
	repoURL, env, cleanup, err := providerPATGitEnv(repoURL, env, ProviderGitHub)
	if err != nil {
		return nil, "", nil, err
	}
	defer cleanup()

	return a.delegate.CloneRemote(ctx, repoURL, branch, env)
}

func (a githubRemoteAdapter) PushRemote(
	ctx context.Context,
	repoDir, repoURL string,
	env []string,
	opts PushOptions,
) error {
	repoURL, env, cleanup, err := providerPATGitEnv(repoURL, env, ProviderGitHub)
	if err != nil {
		return err
	}
	defer cleanup()

	return a.delegate.PushRemote(ctx, repoDir, repoURL, env, opts)
}

func (a gitlabRemoteAdapter) CheckRemote(ctx context.Context, repoURL string, env []string) error {
	repoURL, env, cleanup, err := providerPATGitEnv(repoURL, env, ProviderGitLab)
	if err != nil {
		return err
	}
	defer cleanup()

	return a.delegate.CheckRemote(ctx, repoURL, env)
}

func (a gitlabRemoteAdapter) ListRemoteBranches(
	ctx context.Context,
	repoURL string,
	env []string,
) ([]RemoteBranch, error) {
	repoURL, env, cleanup, err := providerPATGitEnv(repoURL, env, ProviderGitLab)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	return a.delegate.ListRemoteBranches(ctx, repoURL, env)
}

func (a gitlabRemoteAdapter) CloneRemote(
	ctx context.Context,
	repoURL, branch string,
	env []string,
) (RepoStore, string, func(), error) {
	repoURL, env, cleanup, err := providerPATGitEnv(repoURL, env, ProviderGitLab)
	if err != nil {
		return nil, "", nil, err
	}
	defer cleanup()

	return a.delegate.CloneRemote(ctx, repoURL, branch, env)
}

func (a gitlabRemoteAdapter) PushRemote(
	ctx context.Context,
	repoDir, repoURL string,
	env []string,
	opts PushOptions,
) error {
	repoURL, env, cleanup, err := providerPATGitEnv(repoURL, env, ProviderGitLab)
	if err != nil {
		return err
	}
	defer cleanup()

	return a.delegate.PushRemote(ctx, repoDir, repoURL, env, opts)
}

// providerPATConfigFor returns the HTTPS Basic Auth values expected by a hosted git provider.
func providerPATConfigFor(provider Provider) (providerPATConfig, bool) {
	switch provider {
	case ProviderGitHub:
		return providerPATConfig{
			tokenEnvKey:  "GITHUB_TOKEN",
			username:     "x-access-token",
			expectedHost: "github.com",
		}, true
	case ProviderGitLab:
		return providerPATConfig{
			tokenEnvKey:  "GITLAB_TOKEN",
			username:     "oauth2",
			expectedHost: "gitlab.com",
		}, true
	default:
		return providerPATConfig{}, false
	}
}

// providerPATGitEnv wires hosted-provider PATs through askpass without embedding them in git argv.
func providerPATGitEnv(
	repoURL string,
	env []string,
	provider Provider,
) (string, []string, func(), error) {
	cleanup := func() {}

	cfg, ok := providerPATConfigFor(provider)
	if !ok {
		return repoURL, env, cleanup, nil
	}

	if strings.TrimSpace(envValue(env, cfg.tokenEnvKey)) == "" {
		return repoURL, env, cleanup, nil
	}

	parsed, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return "", nil, cleanup, ErrRepositorySetup.Detailf("parse repository URL: %w", err)
	}
	if parsed.Scheme != "https" ||
		!strings.EqualFold(parsed.Hostname(), cfg.expectedHost) {
		return repoURL, env, cleanup, nil
	}
	parsed.User = nil
	repoURL = parsed.String()

	env = HTTPSAuthEnv(env, cfg.username, envValue(env, cfg.tokenEnvKey))

	return repoURL, env, cleanup, nil
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}

	return ""
}

// withGitEnvOverrides appends git subprocess overrides after removing older entries for those keys.
func withGitEnvOverrides(env []string, overrides ...string) []string {
	overrideKeys := make(map[string]struct{}, len(overrides))
	for _, override := range overrides {
		overrideKeys[envKey(override)] = struct{}{}
	}

	result := make([]string, 0, len(env)+len(overrides))
	for _, item := range env {
		if _, ok := overrideKeys[envKey(item)]; ok {
			continue
		}
		result = append(result, item)
	}

	return append(result, overrides...)
}

func validateRemoteRepoURLScheme(repoURL string) error {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return ErrRepositorySetup.Detail("remote repository URL is required")
	}

	if !strings.Contains(repoURL, "://") &&
		(!strings.Contains(repoURL, "@") || !strings.Contains(repoURL, ":")) {
		return nil
	}

	if !strings.Contains(repoURL, "://") && strings.Contains(repoURL, "@") &&
		strings.Contains(repoURL, ":") {
		return nil
	}

	parsed, err := url.Parse(repoURL)
	if err != nil {
		return ErrRepositorySetup.With(err)
	}

	if parsed.Scheme == "https" || parsed.Scheme == "ssh" {
		return nil
	}

	return ErrRepositorySetup.Detail("remote repository URL must use https or ssh")
}
