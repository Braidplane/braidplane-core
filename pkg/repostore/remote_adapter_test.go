package repostore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type recordingRemoteAdapter struct {
	checkRemoteRepoURL string
	listRemoteRepoURL  string
	cloneRemoteRepoURL string
	pushRemoteRepoURL  string
	checkRemoteEnv     []string
	listRemoteEnv      []string
	cloneRemoteEnv     []string
	pushRemoteEnv      []string
}

func (r *recordingRemoteAdapter) CheckRemote(
	_ context.Context,
	repoURL string,
	env []string,
) error {
	r.checkRemoteRepoURL = repoURL
	r.checkRemoteEnv = append([]string(nil), env...)
	return nil
}

func (r *recordingRemoteAdapter) ListRemoteBranches(
	_ context.Context,
	repoURL string,
	env []string,
) ([]RemoteBranch, error) {
	r.listRemoteRepoURL = repoURL
	r.listRemoteEnv = append([]string(nil), env...)
	return []RemoteBranch{}, nil
}

func (r *recordingRemoteAdapter) CloneRemote(
	_ context.Context,
	repoURL, _ string,
	env []string,
) (RepoStore, string, func(), error) {
	r.cloneRemoteRepoURL = repoURL
	r.cloneRemoteEnv = append([]string(nil), env...)
	return nil, "/tmp/repo", func() {}, nil
}

func (r *recordingRemoteAdapter) PushRemote(
	_ context.Context,
	_, repoURL string,
	env []string,
	_ PushOptions,
) error {
	r.pushRemoteRepoURL = repoURL
	r.pushRemoteEnv = append([]string(nil), env...)
	return nil
}

func TestRemoteAdapterForRepoURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL string
		assert  func(t *testing.T, adapter RemoteGit)
	}{
		{
			name:    "GitHubUsesGitHubAdapter",
			repoURL: "https://github.com/acme/repo.git",
			assert: func(t *testing.T, adapter RemoteGit) {
				t.Helper()
				_, ok := adapter.(githubRemoteAdapter)
				require.True(t, ok)
			},
		},
		{
			name:    "GitLabUsesGitLabAdapter",
			repoURL: "https://gitlab.com/acme/repo.git",
			assert: func(t *testing.T, adapter RemoteGit) {
				t.Helper()
				_, ok := adapter.(gitlabRemoteAdapter)
				require.True(t, ok)
			},
		},
		{
			name:    "GenericUsesGenericAdapter",
			repoURL: "https://git.internal.example.com/acme/repo.git",
			assert: func(t *testing.T, adapter RemoteGit) {
				t.Helper()
				_, ok := adapter.(gitRemoteAdapter)
				require.True(t, ok)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			adapter := remoteAdapterForRepoURL(tt.repoURL)
			require.NotNil(t, adapter)
			tt.assert(t, adapter)
		})
	}
}

func TestProviderPATListRemoteBranchesKeepsTokenOutOfGitArgs(t *testing.T) {
	tests := []struct {
		name           string
		repoURL        string
		wantArgRepoURL string
		tokenKey       string
		token          string
		username       string
		forbiddenArgs  []string
	}{
		{
			name:           "GitHub",
			repoURL:        "https://github.com/acme/repo.git",
			wantArgRepoURL: "https://github.com/acme/repo.git",
			tokenKey:       "GITHUB_TOKEN",
			token:          "ghp_123",
			username:       "x-access-token",
		},
		{
			name:           "GitLab",
			repoURL:        "https://gitlab.com/acme/repo.git",
			wantArgRepoURL: "https://gitlab.com/acme/repo.git",
			tokenKey:       "GITLAB_TOKEN",
			token:          "glpat-123",
			username:       "oauth2",
		},
		{
			name:           "GitHubEmbeddedCredentials",
			repoURL:        "https://alice:old-token@github.com/acme/repo.git",
			wantArgRepoURL: "https://github.com/acme/repo.git",
			tokenKey:       "GITHUB_TOKEN",
			token:          "ghp_123",
			username:       "x-access-token",
			forbiddenArgs:  []string{"alice", "old-token"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := t.TempDir()
			writeRecordingGit(t, gitDir)
			t.Setenv("PATH", gitDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			recordDir := t.TempDir()
			argvRecord := filepath.Join(recordDir, "argv")
			envRecord := filepath.Join(recordDir, "env")
			env := []string{
				tt.tokenKey + "=" + tt.token,
				"BRAIDPLANE_EXPECTED_PROVIDER_TOKEN=" + tt.token,
				"BRAIDPLANE_GIT_ARGV_RECORD=" + argvRecord,
				"BRAIDPLANE_GIT_ENV_RECORD=" + envRecord,
			}

			branches, err := ListRemoteBranches(context.Background(), tt.repoURL, env)
			require.NoError(t, err)
			require.Equal(t, []RemoteBranch{{Name: "main", IsDefault: true}}, branches)

			argvBytes, err := os.ReadFile(argvRecord)
			require.NoError(t, err)
			argv := string(argvBytes)
			require.Contains(t, argv, "ls-remote\n")
			require.Contains(t, argv, tt.wantArgRepoURL+"\n")
			require.NotContains(t, argv, tt.token)
			require.NotContains(t, argv, tt.username)
			for _, forbiddenArg := range tt.forbiddenArgs {
				require.NotContains(t, argv, forbiddenArg)
			}

			envBytes, err := os.ReadFile(envRecord)
			require.NoError(t, err)
			gitEnv := string(envBytes)
			require.Contains(t, gitEnv, tt.tokenKey+"_PRESENT=1")
			require.Contains(t, gitEnv, tt.tokenKey+"_MATCHES_EXPECTED=1")
			require.NotContains(t, gitEnv, tt.token)
			require.Contains(t, gitEnv, "BRAIDPLANE_GIT_USERNAME="+tt.username)
			require.Contains(t, gitEnv, "BRAIDPLANE_GIT_PASSWORD_MATCHES_EXPECTED=1")
			require.Contains(t, gitEnv, "GIT_CREDENTIAL_HELPER_PRESENT=1")
			require.NotContains(t, gitEnv, "GIT_ASKPASS=")
		})
	}
}

func TestGitRemoteCommandPublicDetailRedactsCredentials(t *testing.T) {
	t.Parallel()

	detail := gitRemoteCommandPublicDetail(
		"ls-remote",
		"fatal: unable to access 'https://alice:tok123@git.internal.example.com/acme/repo.git': The requested URL returned error: 403",
	)

	require.Contains(t, detail, "git ls-remote failed")
	require.Contains(t, detail, "https://[REDACTED]@git.internal.example.com/acme/repo.git")
	require.NotContains(t, detail, "alice")
	require.NotContains(t, detail, "tok123")
}

func TestGitRemoteCheckRemoteFailureHasPublicDetail(t *testing.T) {
	t.Parallel()

	err := gitRemoteAdapter{}.CheckRemote(
		context.Background(),
		t.TempDir()+"/missing.git",
		nil,
	)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrRepositorySetup)
	require.NotEmpty(t, xerror.PublicDetail(err))
	require.Contains(t, xerror.PublicDetail(err), "git ls-remote failed")
}

func TestGitRemoteCloneFailureHasPublicDetail(t *testing.T) {
	t.Parallel()

	_, _, cleanup, err := gitRemoteAdapter{}.CloneRemote(
		context.Background(),
		t.TempDir()+"/missing.git",
		"main",
		nil,
	)

	require.Error(t, err)
	require.Nil(t, cleanup)
	require.ErrorIs(t, err, ErrRepositorySetup)
	require.NotEmpty(t, xerror.PublicDetail(err))
	require.Contains(t, xerror.PublicDetail(err), "git clone failed")
}

func TestGitRemotePushFailureHasPublicDetail(t *testing.T) {
	t.Parallel()

	err := gitRemoteAdapter{}.PushRemote(
		context.Background(),
		t.TempDir(),
		t.TempDir()+"/missing.git",
		nil,
		PushOptions{},
	)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrRepositorySetup)
	require.NotEmpty(t, xerror.PublicDetail(err))
	require.Contains(t, xerror.PublicDetail(err), "git push failed")
}

func TestSanitizeRemoteURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL string
		want    string
	}{
		{
			name:    "RemovesHTTPSUserInfo",
			repoURL: "https://alice:tok123@git.internal.example.com/acme/repo.git",
			want:    "https://git.internal.example.com/acme/repo.git",
		},
		{
			name:    "KeepsSCPStyleSSHUnchanged",
			repoURL: "git@github.com:acme/repo.git",
			want:    "git@github.com:acme/repo.git",
		},
		{
			name:    "KeepsPlainFilesystemPathUnchanged",
			repoURL: "/tmp/acme-repo",
			want:    "/tmp/acme-repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := SanitizeRemoteURL(tt.repoURL)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeRemoteURL_RemovesCredentialsFromGenericHTTPSRemote(t *testing.T) {
	t.Parallel()

	got := NormalizeRemoteURL("https://alice:tok123@git.internal.example.com/acme/repo.git")
	require.Equal(t, "https://git.internal.example.com/acme/repo.git", got)
}

func writeRecordingGit(t *testing.T, dir string) {
	t.Helper()

	script := `#!/bin/sh
printf '%s\n' "$@" > "$BRAIDPLANE_GIT_ARGV_RECORD"
github_token_present=0
github_token_matches_expected=0
gitlab_token_present=0
gitlab_token_matches_expected=0
braidplane_password_matches_expected=0
git_credential_helper_present=0
if [ -n "${GITHUB_TOKEN:-}" ]; then
	github_token_present=1
	[ "$GITHUB_TOKEN" = "${BRAIDPLANE_EXPECTED_PROVIDER_TOKEN:-}" ] && github_token_matches_expected=1
fi
if [ -n "${GITLAB_TOKEN:-}" ]; then
	gitlab_token_present=1
	[ "$GITLAB_TOKEN" = "${BRAIDPLANE_EXPECTED_PROVIDER_TOKEN:-}" ] && gitlab_token_matches_expected=1
fi
[ "${BRAIDPLANE_GIT_PASSWORD:-}" = "${BRAIDPLANE_EXPECTED_PROVIDER_TOKEN:-}" ] && braidplane_password_matches_expected=1
env | grep -q '^GIT_CONFIG_VALUE_[0-9]*=.*BRAIDPLANE_GIT_PASSWORD' && git_credential_helper_present=1
{
	printf 'GITHUB_TOKEN_PRESENT=%s\n' "$github_token_present"
	printf 'GITHUB_TOKEN_MATCHES_EXPECTED=%s\n' "$github_token_matches_expected"
	printf 'GITLAB_TOKEN_PRESENT=%s\n' "$gitlab_token_present"
	printf 'GITLAB_TOKEN_MATCHES_EXPECTED=%s\n' "$gitlab_token_matches_expected"
	printf 'BRAIDPLANE_GIT_USERNAME=%s\n' "${BRAIDPLANE_GIT_USERNAME:-}"
	printf 'BRAIDPLANE_GIT_PASSWORD_MATCHES_EXPECTED=%s\n' "$braidplane_password_matches_expected"
	printf 'GIT_CREDENTIAL_HELPER_PRESENT=%s\n' "$git_credential_helper_present"
} > "$BRAIDPLANE_GIT_ENV_RECORD"
printf '%s\n' \
	'ref: refs/heads/main	HEAD' \
	'1111111111111111111111111111111111111111	refs/heads/main'
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700))
}

func requireProviderPATDelegateEnv(t *testing.T, env []string, username string) {
	t.Helper()

	require.Contains(t, env, "GIT_TERMINAL_PROMPT=0")
	require.Contains(t, env, "BRAIDPLANE_GIT_USERNAME="+username)
	require.NotEmpty(t, envValue(env, "BRAIDPLANE_GIT_PASSWORD"))
	require.Contains(t, strings.Join(env, "\n"), "BRAIDPLANE_GIT_PASSWORD")
	require.NotContains(t, strings.Join(env, "\n"), "GIT_ASKPASS=")
}

func TestGitHubRemoteAdapter_PATSmoke(t *testing.T) {
	t.Parallel()

	delegate := &recordingRemoteAdapter{}
	adapter := githubRemoteAdapter{delegate: delegate}
	repoURL := "https://github.com/acme/repo.git"
	env := []string{"GITHUB_TOKEN=ghp_123"}

	err := adapter.CheckRemote(context.Background(), repoURL, env)
	require.NoError(t, err)
	require.Equal(t, repoURL, delegate.checkRemoteRepoURL)
	require.NotContains(t, delegate.checkRemoteRepoURL, "ghp_123")
	require.Contains(t, delegate.checkRemoteEnv, "GITHUB_TOKEN=ghp_123")
	requireProviderPATDelegateEnv(t, delegate.checkRemoteEnv, "x-access-token")

	_, _, cleanup, err := adapter.CloneRemote(context.Background(), repoURL, "main", env)
	require.NoError(t, err)
	cleanup()
	require.Equal(t, repoURL, delegate.cloneRemoteRepoURL)
	require.NotContains(t, delegate.cloneRemoteRepoURL, "ghp_123")
	require.Contains(t, delegate.cloneRemoteEnv, "GITHUB_TOKEN=ghp_123")
	requireProviderPATDelegateEnv(t, delegate.cloneRemoteEnv, "x-access-token")

	err = adapter.PushRemote(
		context.Background(),
		"/tmp/local-repo",
		repoURL,
		env,
		PushOptions{Refspec: "HEAD:main"},
	)
	require.NoError(t, err)
	require.Equal(t, repoURL, delegate.pushRemoteRepoURL)
	require.NotContains(t, delegate.pushRemoteRepoURL, "ghp_123")
	require.Contains(t, delegate.pushRemoteEnv, "GITHUB_TOKEN=ghp_123")
	requireProviderPATDelegateEnv(t, delegate.pushRemoteEnv, "x-access-token")
}

func TestGitLabRemoteAdapter_PATSmoke(t *testing.T) {
	t.Parallel()

	delegate := &recordingRemoteAdapter{}
	adapter := gitlabRemoteAdapter{delegate: delegate}
	repoURL := "https://gitlab.com/acme/repo.git"
	env := []string{"GITLAB_TOKEN=glpat-123"}

	err := adapter.CheckRemote(context.Background(), repoURL, env)
	require.NoError(t, err)
	require.Equal(t, repoURL, delegate.checkRemoteRepoURL)
	require.NotContains(t, delegate.checkRemoteRepoURL, "glpat-123")
	require.Contains(t, delegate.checkRemoteEnv, "GITLAB_TOKEN=glpat-123")
	requireProviderPATDelegateEnv(t, delegate.checkRemoteEnv, "oauth2")

	_, _, cleanup, err := adapter.CloneRemote(context.Background(), repoURL, "main", env)
	require.NoError(t, err)
	cleanup()
	require.Equal(t, repoURL, delegate.cloneRemoteRepoURL)
	require.NotContains(t, delegate.cloneRemoteRepoURL, "glpat-123")
	require.Contains(t, delegate.cloneRemoteEnv, "GITLAB_TOKEN=glpat-123")
	requireProviderPATDelegateEnv(t, delegate.cloneRemoteEnv, "oauth2")

	err = adapter.PushRemote(
		context.Background(),
		"/tmp/local-repo",
		repoURL,
		env,
		PushOptions{Refspec: "HEAD:main"},
	)
	require.NoError(t, err)
	require.Equal(t, repoURL, delegate.pushRemoteRepoURL)
	require.NotContains(t, delegate.pushRemoteRepoURL, "glpat-123")
	require.Contains(t, delegate.pushRemoteEnv, "GITLAB_TOKEN=glpat-123")
	requireProviderPATDelegateEnv(t, delegate.pushRemoteEnv, "oauth2")
}

func TestValidateRemoteRepoURLScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		repoURL     string
		wantErrText string
	}{
		{
			name:    "AcceptsHTTPS",
			repoURL: "https://github.com/acme/repo.git",
		},
		{
			name:    "AcceptsSSHScheme",
			repoURL: "ssh://git@github.com/acme/repo.git",
		},
		{
			name:    "AcceptsSCPStyleSSH",
			repoURL: "git@github.com:acme/repo.git",
		},
		{
			name:        "RejectsHTTP",
			repoURL:     "http://github.com/acme/repo.git",
			wantErrText: "remote repository URL must use https or ssh",
		},
		{
			name:        "RejectsGitProtocol",
			repoURL:     "git://github.com/acme/repo.git",
			wantErrText: "remote repository URL must use https or ssh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateRemoteRepoURLScheme(tt.repoURL)
			if tt.wantErrText != "" {
				require.ErrorContains(t, err, tt.wantErrText)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestParseRemoteBranchesRejectsBranchCardinalityOverflow(t *testing.T) {
	t.Parallel()

	output := strings.Join([]string{
		"0123456789abcdef\trefs/heads/main",
		"fedcba9876543210\trefs/heads/release",
	}, "\n")
	branches, err := parseRemoteBranches(output, 1)
	require.Error(t, err)
	require.Nil(t, branches)
	require.ErrorContains(t, err, "too many branch references")
}

func TestLimitedBufferRejectsOutputPastBound(t *testing.T) {
	t.Parallel()

	buffer := limitedBuffer{remaining: 4}
	written, err := buffer.Write([]byte("attacker-controlled-output"))
	require.ErrorIs(t, err, errGitCommandOutputExceeded)
	require.Equal(t, 4, written)
	require.Equal(t, "atta", buffer.String())
}

func TestCancelOnOutputOverflowCancelsCommand(t *testing.T) {
	t.Parallel()

	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			buffer := limitedBuffer{remaining: 1}
			writer := cancelOnOutputOverflow{writer: &buffer, cancel: cancel}

			_, err := writer.Write([]byte("overflow"))
			require.ErrorIs(t, err, errGitCommandOutputExceeded)
			require.ErrorIs(t, writer.OverflowError(), errGitCommandOutputExceeded)
			require.ErrorIs(t, ctx.Err(), context.Canceled)
		})
	}
}
