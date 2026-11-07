package gitprovider

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
)

func TestRepositoryRefFromRemoteURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		provider    repostore.Provider
		repoURL     string
		want        RepositoryRef
		wantErrText string
	}{
		{
			name:     "GitHubOwnerRepo",
			provider: repostore.ProviderGitHub,
			repoURL:  "https://github.com/acme/pipelines.git",
			want: RepositoryRef{
				Provider:    repostore.ProviderGitHub,
				Owner:       "acme",
				Name:        "pipelines",
				ProjectPath: "acme/pipelines",
				FullName:    "acme/pipelines",
			},
		},
		{
			name:     "GitLabNestedProjectPath",
			provider: repostore.ProviderGitLab,
			repoURL:  "https://gitlab.com/acme/platform/pipelines.git",
			want: RepositoryRef{
				Provider:    repostore.ProviderGitLab,
				Owner:       "acme/platform",
				Name:        "pipelines",
				ProjectPath: "acme/platform/pipelines",
				FullName:    "acme/platform/pipelines",
			},
		},
		{
			name:        "GitHubRejectsNestedPath",
			provider:    repostore.ProviderGitHub,
			repoURL:     "https://github.com/acme/platform/pipelines.git",
			wantErrText: "GitHub repo URL must include owner and repo",
		},
		{
			name:        "GenericRejectsProviderProposalRef",
			provider:    repostore.ProviderGeneric,
			repoURL:     "https://git.example.com/acme/pipelines.git",
			wantErrText: "provider proposals require github or gitlab",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := RepositoryRefFromRemoteURL(tt.provider, tt.repoURL)
			if tt.wantErrText != "" {
				require.ErrorContains(t, err, tt.wantErrText)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
