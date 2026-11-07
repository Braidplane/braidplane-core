package gitlab_test

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	gitlabprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/gitlab"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/stretchr/testify/require"
)

func TestProviderTransportRequiresExplicitDevelopmentPlaintextPermission(t *testing.T) {
	t.Parallel()
	for _, newClient := range []func() error{
		func() error {
			_, err := gitlabprovider.New(t.Context(), gitprovider.Config{BaseURL: "http://127.0.0.1:1", AllowPrivateHosts: true}, gitprovider.Credentials{Token: "synthetic"}, gitprovider.RepositoryRef{Provider: repostore.ProviderGitLab})
			return err
		},
		func() error {
			_, err := gitlabprovider.NewOAuth(t.Context(), gitprovider.Config{BaseURL: "http://127.0.0.1:1", AllowPrivateHosts: true}, gitprovider.Credentials{Token: "synthetic"}, gitprovider.RepositoryRef{Provider: repostore.ProviderGitLab})
			return err
		},
	} {
		require.ErrorContains(t, newClient(), "requires HTTPS")
	}
	_, err := gitlabprovider.NewOAuth(t.Context(), gitprovider.Config{BaseURL: "http://127.0.0.1:1", AllowPrivateHosts: true, AllowPlaintext: true}, gitprovider.Credentials{Token: "synthetic"}, gitprovider.RepositoryRef{Provider: repostore.ProviderGitLab})
	require.NoError(t, err)
}
