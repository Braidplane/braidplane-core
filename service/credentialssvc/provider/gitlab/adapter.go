package gitlab

import (
	"context"
	"strconv"

	"github.com/Braidplane/braidplane-core/pkg/gitlab/oauth"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

type Config = oauth.Config

type Adapter struct{ client *oauth.Client }

func New(config Config) (*Adapter, error) {
	client, err := oauth.New(config)
	if err != nil {
		return nil, err
	}
	return &Adapter{client: client}, nil
}

func (*Adapter) OAuthScopePlan() credentialssvc.OAuthScopePlan {
	return credentialssvc.OAuthScopePlan{Required: []string{"read_user", "read_api", "read_repository"}}
}

func (*Adapter) OAuthScopePlanForInstall(params credentialssvc.OAuthScopePlanParams) credentialssvc.OAuthScopePlan {
	if params.IncludeWriteScopes {
		return credentialssvc.OAuthScopePlan{Required: []string{"read_user", "api", "write_repository"}}
	}
	return credentialssvc.OAuthScopePlan{Required: []string{"read_user", "read_api", "read_repository"}}
}

func (a *Adapter) AuthorizationURL(_ context.Context, params credentialssvc.AuthorizationURLParams) (string, error) {
	return a.client.AuthorizationURL(params.State, append(append([]string{}, params.Scopes...), params.OptionalScopes...))
}

func (a *Adapter) ExchangeCode(ctx context.Context, params credentialssvc.ExchangeCodeParams) (credentialssvc.OAuthTokenSet, error) {
	return credentialTokenSet(a.client.ExchangeCode(ctx, params.Code))
}

func (a *Adapter) Refresh(ctx context.Context, params credentialssvc.RefreshParams) (credentialssvc.OAuthTokenSet, error) {
	return credentialTokenSet(a.client.Refresh(ctx, params.RefreshToken))
}

func (a *Adapter) Revoke(ctx context.Context, params credentialssvc.RevokeProviderGrantParams) error {
	err := a.client.Revoke(ctx, params.AccessToken)
	if xerror.Is(err, oauth.ErrInvalidGrant) {
		return credentialssvc.ErrProviderInvalidGrant
	}
	return err
}

func (a *Adapter) ListRepositories(ctx context.Context, accessToken string) ([]credentialssvc.ProviderRepository, error) {
	projects, err := a.client.ListProjects(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	repositories := make([]credentialssvc.ProviderRepository, 0, len(projects))
	for _, project := range projects {
		repositories = append(repositories, providerRepository(project))
	}
	return repositories, nil
}

func (a *Adapter) ResolveRepository(ctx context.Context, accessToken, repositoryID string) (credentialssvc.ProviderRepository, error) {
	project, err := a.client.GetProject(ctx, accessToken, repositoryID)
	if err != nil {
		return credentialssvc.ProviderRepository{}, err
	}
	return providerRepository(project), nil
}

func providerRepository(project oauth.Project) credentialssvc.ProviderRepository {
	return credentialssvc.ProviderRepository{
		ID:            strconv.FormatInt(project.ID, 10),
		Name:          project.Name,
		FullName:      project.PathWithNS,
		WebURL:        project.WebURL,
		CloneURL:      project.HTTPURLToRepo,
		DefaultBranch: project.DefaultBranch,
	}
}

func credentialTokenSet(token oauth.TokenSet, err error) (credentialssvc.OAuthTokenSet, error) {
	if xerror.Is(err, oauth.ErrInvalidGrant) {
		return credentialssvc.OAuthTokenSet{}, credentialssvc.ErrProviderInvalidGrant
	}
	if err != nil {
		return credentialssvc.OAuthTokenSet{}, err
	}
	return credentialssvc.OAuthTokenSet{
		AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresAt: token.ExpiresAt,
		Scopes:  append([]string(nil), token.Scopes...),
		Account: credentialssvc.ProviderAccount{ID: token.AccountID, Name: token.AccountName, Email: token.AccountEmail},
	}, nil
}

var (
	_ credentialssvc.ScopePlanningProvider           = (*Adapter)(nil)
	_ credentialssvc.ContextualScopePlanningProvider = (*Adapter)(nil)
	_ credentialssvc.RepositoryProvider              = (*Adapter)(nil)
)
