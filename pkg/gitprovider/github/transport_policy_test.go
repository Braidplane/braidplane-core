package github_test

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/stretchr/testify/require"
)

func TestProviderTransportRequiresExplicitDevelopmentPlaintextPermission(t *testing.T) {
	t.Parallel()
	config := gitprovider.Config{BaseURL: "http://127.0.0.1:1", AllowPrivateHosts: true}
	repo := gitprovider.RepositoryRef{Provider: repostore.ProviderGitHub, Owner: "synthetic", Name: "test"}
	_, err := githubprovider.New(t.Context(), config, gitprovider.Credentials{Token: "synthetic"}, repo)
	require.ErrorContains(t, err, "requires HTTPS")
	config.AllowPlaintext = true
	_, err = githubprovider.New(t.Context(), config, gitprovider.Credentials{Token: "synthetic"}, repo)
	require.NoError(t, err)
}
