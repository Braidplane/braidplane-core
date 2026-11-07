package pipelinegit

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	gitlabprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/gitlab"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

type managedRepositoryCredentials interface {
	GetOrganizationGitLabConnection(context.Context, slug.Slug) (credentialssvc.OrganizationGitLabConnection, error)
	ResolveRuntimeCredential(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error)
}

// ManagedRepositoryBootstrapper uses the organization grant before a repository source exists.
type ManagedRepositoryBootstrapper struct {
	credentials managedRepositoryCredentials
	config      gitprovider.Config
}

func NewManagedRepositoryBootstrapper(credentials managedRepositoryCredentials, config gitprovider.Config) (*ManagedRepositoryBootstrapper, error) {
	if credentials == nil {
		return nil, xerror.ExecutionUnavailable().Detail("managed repository credentials are required")
	}
	return &ManagedRepositoryBootstrapper{credentials: credentials, config: config}, nil
}

func (b *ManagedRepositoryBootstrapper) BootstrapGitLabRepository(ctx context.Context, org slug.Slug, request gitprovider.RepositoryBootstrapRequest) (*gitprovider.RepositoryBootstrapResult, error) {
	connection, err := b.credentials.GetOrganizationGitLabConnection(ctx, org)
	if err != nil {
		return nil, err
	}
	if connection.Credential.Status != credentialssvc.CredentialStatusActive || !slices.Contains(connection.Credential.OAuth.Scopes, "api") {
		return nil, xerror.InvalidCredentials().Detail("Reconnect the organization GitLab connection with API write permission before creating a private repository.")
	}
	runtime, err := b.credentials.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{Owner: credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindOrganizationGitLab, OrgSlug: org}})
	if err != nil {
		return nil, err
	}
	var token credentialssvc.OAuthTokenSet
	if runtime.Kind != credentialssvc.CredentialKindOAuth2Grant || json.Unmarshal(runtime.PlaintextJSON, &token) != nil || strings.TrimSpace(token.AccessToken) == "" {
		return nil, xerror.InvalidCredentials().Detail("The organization GitLab grant is unavailable; reconnect GitLab.")
	}
	fullName := request.Owner + "/" + request.Name
	client, err := gitlabprovider.NewOAuth(ctx, b.config, gitprovider.Credentials{Token: token.AccessToken}, gitprovider.RepositoryRef{Provider: repostore.ProviderGitLab, Owner: request.Owner, Name: request.Name, ProjectPath: fullName, FullName: fullName})
	if err != nil {
		return nil, err
	}
	return client.BootstrapRepository(ctx, request)
}
