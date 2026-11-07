package datasourcessvc

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/httpoauth"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
)

// HTTP app input is distinct from the connector's token-only runtime payload.
func (s *service) httpOAuthApp(typ model.DataSourceType, config json.RawMessage, credentials map[string]any) (*credentialssvc.HTTPOAuthApp, error) {
	input, ok := credentials["oauth"]
	if !ok {
		return nil, nil //nolint:nilnil // Absent OAuth settings select the existing static authentication path.
	}
	if typ != model.DataSourceTypeHTTP || len(credentials) != 1 || input == nil {
		return nil, ErrInvalidInput.Detail("HTTP OAuth settings cannot be combined with other credentials")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, ErrInvalidInput.Detail("Invalid HTTP OAuth settings")
	}
	var settings struct {
		httpoauth.Config
		ClientSecret string `json:"client_secret"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return nil, ErrInvalidInput.Detail("Invalid HTTP OAuth settings")
	}
	if err := s.validateConfigAndCredentials(typ, config, []byte(`{}`)); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	var httpConfig httpconnector.Config
	if err := json.Unmarshal(config, &httpConfig); err != nil {
		return nil, ErrInvalidInput.Detail("Invalid HTTP configuration")
	}
	app := &credentialssvc.HTTPOAuthApp{Config: settings.Config, ClientSecret: settings.ClientSecret, RedirectURI: s.httpOAuthRedirectURI, Destination: httpConfig.BaseURL, AllowPrivateHosts: httpConfig.AllowPrivateHosts}
	if settings.GrantType == "authorization_code" && app.RedirectURI == "" {
		return nil, ErrInvalidInput.Detail("HTTP OAuth authorization-code setup requires an operator-configured callback URI")
	}
	if _, err := httpoauth.New(app.Config, app.ClientSecret, app.RedirectURI, app.Destination, app.AllowPrivateHosts); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	return app, nil
}

func (s *service) createHTTPOAuth(ctx context.Context, org slug.Slug, actor model.EventActor, params *CreateParams, app credentialssvc.HTTPOAuthApp) (*DataSource, error) {
	owner := dataSourceCredentialOwner(org, params.Slug)
	err := s.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		credential, err := s.credentials.CreatePendingOAuthCredential(ctx, credentialssvc.CreatePendingOAuthCredentialParams{Owner: owner, Provider: "http"})
		if err != nil {
			return err
		}
		ds, err := s.repo.Create(ctx, tx, org, &repods.CreateParams{Slug: params.Slug, Name: params.Name, Type: params.Type, ConfigJSON: params.ConfigJSON, ReadOnly: params.ReadOnly, CredentialSlug: credential.Slug})
		if err != nil {
			return err
		}
		if err := s.credentials.ConfigureHTTPOAuthApp(ctx, credentialssvc.ConfigureHTTPOAuthAppParams{Owner: owner, App: app}); err != nil {
			return err
		}
		return s.recordDataSourceEvent(ctx, tx, org, actor, ds, model.EventActionCreate)
	})
	if err != nil {
		return nil, err
	}
	return s.FindBySlug(ctx, org, params.Slug)
}

func (s *service) replaceHTTPOAuth(ctx context.Context, tx postgres.DBTX, org slug.Slug, actor model.EventActor, ds *repods.DataSource, app credentialssvc.HTTPOAuthApp) (*repods.DataSource, error) {
	owner := dataSourceCredentialOwner(org, ds.Slug)
	credential, err := s.credentials.CreatePendingOAuthCredential(ctx, credentialssvc.CreatePendingOAuthCredentialParams{Owner: owner, Provider: "http"})
	if err != nil {
		return nil, err
	}
	ds, err = s.repo.UpdateCredentials(ctx, tx, org, ds.Slug, &repods.UpdateCredentialsParams{CredentialSlug: credential.Slug})
	if err != nil {
		return nil, err
	}
	inactive := false
	ds, err = s.repo.Update(ctx, tx, org, ds.Slug, &repods.UpdateParams{Name: ds.Name, ConfigJSON: ds.Config, IsActive: &inactive, ReadOnly: ds.ReadOnly})
	if err != nil {
		return nil, err
	}
	if err := s.credentials.ConfigureHTTPOAuthApp(ctx, credentialssvc.ConfigureHTTPOAuthAppParams{Owner: owner, App: app}); err != nil {
		return nil, err
	}
	return ds, s.recordDataSourceEvent(ctx, tx, org, actor, ds, model.EventActionRotate)
}

func (s *service) ConnectHTTPOAuth(ctx context.Context, org, source slug.Slug, actor model.EventActor) (*HTTPOAuthConnection, error) {
	ds, err := s.FindBySlug(ctx, org, source)
	if err != nil {
		return nil, err
	}
	if ds.Type != model.DataSourceTypeHTTP || ds.Credential == nil || ds.Credential.Kind != credentialssvc.CredentialKindOAuth2Grant {
		return nil, ErrInvalidInput.Detail("This source does not use HTTP OAuth")
	}
	intent := credentialssvc.OAuthInstallIntentRotate
	if ds.Credential.Status == credentialssvc.CredentialStatusPending {
		intent = credentialssvc.OAuthInstallIntentCreate
	}
	install, err := s.credentials.ConnectHTTPOAuth(ctx, credentialssvc.StartOAuthInstallParams{Owner: dataSourceCredentialOwner(org, source), Provider: "http", Intent: intent, Actor: actor})
	if err != nil {
		return nil, err
	}
	if install == nil {
		action := model.EventActionRotate
		if intent == credentialssvc.OAuthInstallIntentCreate {
			action = model.EventActionCreate
		}
		ds, err = s.ValidateCredentialLifecycle(ctx, org, source, actor, action)
		if err != nil {
			return nil, err
		}
	}
	return &HTTPOAuthConnection{DataSource: ds, Install: install}, nil
}
