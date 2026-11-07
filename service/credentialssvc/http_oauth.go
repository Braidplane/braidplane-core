package credentialssvc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/httpoauth"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
)

// HTTPOAuthApp is encrypted both as desired source configuration and as a grant/install snapshot.
// AllowPrivateHosts is supplied only by the deployment's validated data-source policy.
type HTTPOAuthApp struct {
	Config            httpoauth.Config `json:"config"`
	ClientSecret      string           `json:"client_secret"`
	RedirectURI       string           `json:"redirect_uri"`
	Destination       string           `json:"destination"`
	AllowPrivateHosts bool             `json:"allow_private_hosts"`
}

type ConfigureHTTPOAuthAppParams struct {
	Owner CredentialOwner
	App   HTTPOAuthApp
}

// ConfigureHTTPOAuthApp replaces desired app settings without changing any active grant's provider binding.
func (s *service) ConfigureHTTPOAuthApp(ctx context.Context, params ConfigureHTTPOAuthAppParams) error {
	if err := validateHTTPOAuthOwner(params.Owner); err != nil {
		return err
	}
	if _, err := s.newHTTPOAuthProvider(params.App); err != nil {
		return err
	}
	// #nosec G117 -- This payload is encrypted immediately and never returned to callers.
	payload, err := json.Marshal(params.App)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	encrypted, err := s.encryptPayload(credentialScope(params.Owner.OrgSlug, params.Owner.DataSourceSlug, "http-oauth-app"), payload)
	if err != nil {
		return err
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.repo.SaveHTTPOAuthApp(ctx, tx, toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: params.Owner}), encrypted)
	})
}

func validateHTTPOAuthOwner(owner CredentialOwner) error {
	if err := validateCredentialOwner(owner); err != nil {
		return err
	}
	if owner.Kind != CredentialOwnerKindDataSource {
		return ErrInvalidInput.Detail("HTTP OAuth requires a data source owner")
	}
	return nil
}

func (s *service) httpOAuthProvider(ctx context.Context, owner CredentialOwner) (*httpOAuthProvider, error) {
	if err := validateHTTPOAuthOwner(owner); err != nil {
		return nil, err
	}
	encrypted, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]byte, error) {
		return s.repo.FindHTTPOAuthApp(ctx, db, toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: owner}))
	})
	if err != nil {
		return nil, err
	}
	payload, err := s.decryptPayload(credentialScope(owner.OrgSlug, owner.DataSourceSlug, "http-oauth-app"), encrypted)
	if err != nil {
		return nil, err
	}
	var app HTTPOAuthApp
	if json.Unmarshal(payload, &app) != nil {
		return nil, ErrInvalidInput.Detail("invalid stored HTTP OAuth app")
	}
	return s.newHTTPOAuthProvider(app)
}

// The envelope pins the verifier to the app that created its challenge; neither can be supplied by a callback.
type httpOAuthInstallContext struct {
	Verifier string       `json:"verifier"`
	App      HTTPOAuthApp `json:"app"`
}

func (s *service) httpOAuthInstallProvider(install CredentialInstall) (*httpOAuthProvider, string, error) {
	payload, err := s.decryptPayload(credentialScope(install.Owner.OrgSlug, install.Slug, "oauth-install-snapshot"), install.EncryptedPKCEVerifier)
	if err != nil {
		return nil, "", err
	}
	var envelope httpOAuthInstallContext
	if json.Unmarshal(payload, &envelope) != nil || envelope.Verifier == "" {
		return nil, "", ErrOAuthStateInvalid
	}
	provider, err := s.newHTTPOAuthProvider(envelope.App)
	return provider, envelope.Verifier, err
}

func (s *service) providerForGrant(grant CredentialGrant, tokens OAuthTokenSet) (ProviderAdapter, error) {
	if grant.Provider != "http" {
		return s.provider(grant.Provider)
	}
	if tokens.HTTPApp == nil {
		return nil, ErrProviderNotConfigured
	}
	return s.newHTTPOAuthProvider(*tokens.HTTPApp)
}

// ConnectHTTPOAuthClientCredentials acquires a machine grant outside the transaction, then checks the exact source revision.
func (s *service) ConnectHTTPOAuthClientCredentials(ctx context.Context, params ResolveRuntimeCredentialParams) (CredentialMetadata, error) {
	if err := validateHTTPOAuthOwner(params.Owner); err != nil {
		return CredentialMetadata{}, err
	}
	var prepared CredentialInstall
	var provider *httpOAuthProvider
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		state, err := s.repo.LockOAuthGrantIntent(ctx, tx, toRepoResolveParams(params))
		if err != nil {
			return err
		}
		provider, err = s.httpOAuthProvider(ctx, params.Owner)
		if err != nil {
			return err
		}
		intent := OAuthInstallIntentRotate
		if state.Kind == CredentialKindOAuth2Grant.String() && state.Status == CredentialStatusPending.String() && !state.HasGrant {
			intent = OAuthInstallIntentCreate
		}
		revision := state.SourceRevision
		prepared = CredentialInstall{Owner: params.Owner, Provider: "http", Intent: intent, ExpectedCredentialSlug: state.CredentialSlug, ExpectedCredentialStatus: mustCredentialStatus(state.Status), ExpectedSourceRevision: &revision}
		return validateOAuthGrantIntentState(prepared, state)
	})
	if err != nil {
		return CredentialMetadata{}, err
	}
	if provider.app.Config.GrantType != "client_credentials" {
		return CredentialMetadata{}, ErrInvalidInput.Detail("HTTP OAuth app does not use client_credentials")
	}
	tokens, err := provider.client.ClientCredentials(postgres.WithoutTransaction(ctx))
	if err != nil {
		return CredentialMetadata{}, httpOAuthError(err)
	}
	grant, err := s.grantFromTokenSet(params.Owner, "http", provider.tokenSet(tokens), s.now(), initialGrantSlug(prepared))
	if err != nil {
		return CredentialMetadata{}, err
	}
	finalizeCtx, cancel := credentialFinalizeContext(ctx)
	defer cancel()
	var result CredentialMetadata
	err = s.txm.RunInTx(finalizeCtx, func(ctx context.Context, tx postgres.DBTX) error {
		state, err := s.repo.LockOAuthGrantIntent(ctx, tx, toRepoResolveParams(params))
		if err != nil {
			return err
		}
		if err := validateOAuthGrantIntentState(prepared, state); err != nil {
			return err
		}
		stored, err := s.repo.CreateOAuthGrant(ctx, tx, toRepoCredentialGrant(grant), prepared.Intent.String())
		if err != nil {
			return err
		}
		result = metadataFromGrant(fromRepoCredentialGrant(stored))
		return nil
	})
	return result, err
}

type httpOAuthProvider struct {
	app    HTTPOAuthApp
	client *httpoauth.Client
}

func (s *service) newHTTPOAuthProvider(app HTTPOAuthApp) (*httpOAuthProvider, error) {
	if app.AllowPrivateHosts && !s.allowPrivateDataSourceHosts {
		return nil, ErrInvalidInput.Detail("private HTTP OAuth hosts are disabled for this deployment")
	}
	client, err := httpoauth.New(app.Config, app.ClientSecret, app.RedirectURI, app.Destination, app.AllowPrivateHosts)
	if err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	return &httpOAuthProvider{app: app, client: client}, nil
}

func (p *httpOAuthProvider) OAuthScopePlan() OAuthScopePlan {
	return OAuthScopePlan{Required: append([]string(nil), p.app.Config.Scopes...)}
}

func (p *httpOAuthProvider) AuthorizationURL(_ context.Context, params AuthorizationURLParams) (string, error) {
	return p.client.AuthorizationURL(params.State, params.PKCEVerifier)
}

func (p *httpOAuthProvider) ExchangeCode(ctx context.Context, params ExchangeCodeParams) (OAuthTokenSet, error) {
	tokens, err := p.client.Exchange(ctx, params.Code, params.PKCEVerifier, params.Issuer)
	return p.tokenSet(tokens), httpOAuthError(err)
}

func (p *httpOAuthProvider) Refresh(ctx context.Context, params RefreshParams) (OAuthTokenSet, error) {
	tokens, err := p.client.Refresh(ctx, params.RefreshToken, params.Scopes)
	return p.tokenSet(tokens), httpOAuthError(err)
}

func (p *httpOAuthProvider) Revoke(ctx context.Context, params RevokeProviderGrantParams) error {
	token := params.RefreshToken
	if token == "" {
		token = params.AccessToken
	}
	return httpOAuthError(p.client.Revoke(ctx, token))
}

func (p *httpOAuthProvider) tokenSet(tokens httpoauth.TokenSet) OAuthTokenSet {
	app := p.app
	return OAuthTokenSet{HTTPApp: &app, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, ExpiresAt: tokens.ExpiresAt, Scopes: append([]string(nil), tokens.Scopes...)}
}

func httpOAuthError(err error) error {
	if err == nil {
		return nil
	}
	if xerror.Is(err, httpoauth.ErrGrant) {
		return ErrProviderInvalidGrant
	}
	return ErrProviderUnavailable
}

func grantRuntimeCredential(grant CredentialGrant, tokens OAuthTokenSet) (RuntimeCredential, error) {
	if grant.Provider == "slack" {
		payload, err := json.Marshal(map[string]string{"webhook_url": tokens.SlackWebhookURL})
		if err != nil {
			return RuntimeCredential{}, xerror.Unexpected().With(err)
		}
		if tokens.SlackWebhookURL == "" {
			return RuntimeCredential{}, ErrCredentialNotFound
		}
		return RuntimeCredential{Kind: CredentialKindOAuth2Grant, PlaintextJSON: payload}, nil
	}
	if grant.Provider != "http" {
		return accessTokenRuntime(tokens.AccessToken, grant.Scopes)
	}
	if tokens.HTTPApp == nil {
		return RuntimeCredential{}, ErrProviderNotConfigured
	}
	payload, err := json.Marshal(map[string]any{"bearer_token": tokens.AccessToken, "oauth_destination": tokens.HTTPApp.Destination})
	if err != nil {
		return RuntimeCredential{}, xerror.Unexpected().With(err)
	}
	return RuntimeCredential{Kind: CredentialKindOAuth2Grant, PlaintextJSON: payload}, nil
}

// HTTP providers may rotate refresh tokens, so only one worker may exchange a stored token at a time.
func (s *service) refreshGrant(ctx context.Context, grant CredentialGrant, now time.Time) (CredentialGrant, error) {
	if grant.Provider != "http" {
		return s.refreshGrantUnlocked(ctx, grant, now)
	}
	ctx, cancel := context.WithTimeout(postgres.WithoutTransaction(ctx), 65*time.Second)
	defer cancel()
	claimID, err := oauthflow.RandomState()
	if err != nil {
		return CredentialGrant{}, err
	}
	owner := toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: grant.Owner})
	for {
		claimed, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (bool, error) {
			return s.repo.ClaimHTTPOAuthRefresh(ctx, db, toRepoCredentialGrant(grant), claimID)
		})
		if err != nil {
			return CredentialGrant{}, err
		}
		if claimed {
			claimCtx, claimCancel := context.WithTimeout(ctx, 45*time.Second)
			defer claimCancel()
			ctx = claimCtx
			defer func() {
				cleanupCtx, cleanupCancel := credentialFinalizeContext(ctx)
				defer cleanupCancel()
				// Claim expiry recovers failed cleanup without replacing the refresh outcome.
				_ = s.txm.RunRead(cleanupCtx, func(ctx context.Context, db postgres.DBTX) error {
					return s.repo.ReleaseHTTPOAuthRefresh(ctx, db, owner, claimID)
				})
			}()
			current, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (repocredentials.CredentialGrant, error) {
				return s.repo.FindOAuthGrant(ctx, db, owner)
			})
			if err != nil {
				return CredentialGrant{}, err
			}
			if current.Slug != grant.Slug || !current.GrantUpdatedAt.Equal(grant.GrantUpdatedAt) || current.Status != CredentialStatusActive.String() {
				return CredentialGrant{}, ErrOAuthGrantConflict
			}
			return s.refreshGrantUnlocked(ctx, grant, s.now())
		}
		current, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (repocredentials.CredentialGrant, error) {
			return s.repo.FindOAuthGrant(ctx, db, owner)
		})
		if err != nil {
			return CredentialGrant{}, err
		}
		if current.Slug != grant.Slug || !current.GrantUpdatedAt.Equal(grant.GrantUpdatedAt) || current.Status != CredentialStatusActive.String() {
			return CredentialGrant{}, ErrOAuthGrantConflict
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return CredentialGrant{}, ErrProviderUnavailable
		}
	}
}

// ConnectHTTPOAuth selects the saved grant type without disclosing the encrypted app.
func (s *service) ConnectHTTPOAuth(ctx context.Context, params StartOAuthInstallParams) (*ProviderConnectorInstall, error) {
	provider, err := s.httpOAuthProvider(ctx, params.Owner)
	if err != nil {
		return nil, err
	}
	if provider.app.Config.GrantType == "client_credentials" {
		_, err := s.ConnectHTTPOAuthClientCredentials(ctx, ResolveRuntimeCredentialParams{Owner: params.Owner})
		return nil, err
	}
	params.Provider = "http"
	install, err := s.StartOAuthInstall(ctx, params)
	if err != nil {
		return nil, err
	}
	return &install, nil
}

// AcquireHTTPOAuthTestCredential never stores draft app settings or provider tokens.
func (s *service) AcquireHTTPOAuthTestCredential(ctx context.Context, app HTTPOAuthApp) (RuntimeCredential, error) {
	provider, err := s.newHTTPOAuthProvider(app)
	if err != nil {
		return RuntimeCredential{}, err
	}
	if app.Config.GrantType != "client_credentials" {
		return RuntimeCredential{}, ErrInvalidInput.Detail("Save the source and connect it to authorize this app before testing")
	}
	tokens, err := provider.client.ClientCredentials(postgres.WithoutTransaction(ctx))
	if err != nil {
		return RuntimeCredential{}, httpOAuthError(err)
	}
	return grantRuntimeCredential(CredentialGrant{Provider: "http"}, provider.tokenSet(tokens))
}
