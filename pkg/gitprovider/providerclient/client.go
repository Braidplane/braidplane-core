package providerclient

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	gitlabprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/gitlab"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// NewProposalReader keeps provider selection at the adapter boundary so proof
// services depend only on provider-neutral proposal state.
func NewProposalReader(
	ctx context.Context,
	provider repostore.Provider,
	repoURL string,
	authEnv []string,
) (gitprovider.ProposalReader, error) {
	repo, err := gitprovider.RepositoryRefFromRemoteURL(provider, repoURL)
	if err != nil {
		return nil, err
	}
	switch provider {
	case repostore.ProviderGitHub:
		return githubprovider.New(ctx, gitprovider.Config{}, gitprovider.Credentials{Token: tokenFromAuthEnv(authEnv, "GITHUB_TOKEN")}, repo)
	case repostore.ProviderGitLab:
		// GitLab accepts bearer authentication for both OAuth grants and access tokens.
		return gitlabprovider.NewOAuth(ctx, gitprovider.Config{}, gitprovider.Credentials{Token: tokenFromAuthEnv(authEnv, "GITLAB_TOKEN")}, repo)
	default:
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("proposal reader requires GitHub or GitLab")
	}
}

// NewProofCheckPublisher keeps provider selection at the adapter boundary.
func NewProofCheckPublisher(
	ctx context.Context,
	provider repostore.Provider,
	repoURL string,
	authEnv []string,
) (gitprovider.ProofCheckClient, error) {
	repo, err := gitprovider.RepositoryRefFromRemoteURL(provider, repoURL)
	if err != nil {
		return nil, err
	}
	switch provider {
	case repostore.ProviderGitHub:
		return githubprovider.New(ctx, gitprovider.Config{}, gitprovider.Credentials{Token: tokenFromAuthEnv(authEnv, "GITHUB_TOKEN")}, repo)
	case repostore.ProviderGitLab:
		return gitlabprovider.NewOAuth(ctx, gitprovider.Config{}, gitprovider.Credentials{Token: tokenFromAuthEnv(authEnv, "GITLAB_TOKEN")}, repo)
	default:
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("Proof check publication requires GitHub or GitLab")
	}
}

// authEnv is the short-lived credential vector returned by ResolveGitAuth
// after decrypting the configured data-source credential. It is not read from
// the ambient process environment.
func tokenFromAuthEnv(authEnv []string, key string) string {
	prefix := key + "="
	for _, item := range authEnv {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}
