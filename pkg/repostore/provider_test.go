package repostore

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL string
		want    Provider
	}{
		{
			name:    "DetectsGitHubHTTPS",
			repoURL: "https://github.com/acme/repo.git",
			want:    ProviderGitHub,
		},
		{
			name:    "DetectsGitHubSSH",
			repoURL: "git@github.com:acme/repo.git",
			want:    ProviderGitHub,
		},
		{
			name:    "DetectsGitLabHTTPS",
			repoURL: "https://gitlab.com/acme/repo.git",
			want:    ProviderGitLab,
		},
		{
			name:    "DetectsGitLabSSH",
			repoURL: "git@gitlab.com:acme/repo.git",
			want:    ProviderGitLab,
		},
		{
			name:    "FallsBackToGenericForSelfHosted",
			repoURL: "https://git.internal.example.com/acme/repo.git",
			want:    ProviderGeneric,
		},
		{
			name:    "FallsBackToGenericForInvalidURL",
			repoURL: "not-a-url",
			want:    ProviderGeneric,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := DetectProvider(tt.repoURL)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeRemoteURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repoURL string
		want    string
	}{
		{
			name:    "NormalizesGitHubHTTPS",
			repoURL: "https://github.com/acme/repo.git",
			want:    "github.com/acme/repo",
		},
		{
			name:    "NormalizesGitHubSSH",
			repoURL: "git@github.com:acme/repo.git",
			want:    "github.com/acme/repo",
		},
		{
			name:    "NormalizesGitLabHTTPS",
			repoURL: "https://gitlab.com/acme/repo",
			want:    "gitlab.com/acme/repo",
		},
		{
			name:    "NormalizesGitLabSSH",
			repoURL: "git@gitlab.com:acme/repo.git",
			want:    "gitlab.com/acme/repo",
		},
		{
			name:    "KeepsSelfHostedUnchanged",
			repoURL: "https://git.internal.example.com/acme/repo.git",
			want:    "https://git.internal.example.com/acme/repo.git",
		},
		{
			name:    "KeepsInvalidURLTrimmed",
			repoURL: "  not-a-url  ",
			want:    "not-a-url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := NormalizeRemoteURL(tt.repoURL)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizedProviderIdentity(t *testing.T) {
	t.Parallel()
	for _, repo := range []string{"https://github.com/acme/repo.git", "git@gitlab.com:acme/repo.git"} {
		normalized := NormalizeRemoteURL(repo)
		require.Equal(t, DetectProvider(repo), DetectProvider(normalized))
		require.Equal(t, normalized, NormalizeRemoteURL(normalized))
	}
	for _, repo := range []string{"github.com.evil.example/acme/repo", "evil.example/github.com/acme/repo", "github.com@evil.example/acme/repo"} {
		require.Equal(t, ProviderGeneric, DetectProvider(repo))
	}
}
