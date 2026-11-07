package datasourcessvc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	provider "github.com/Braidplane/braidplane-core/pkg/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

type WorkspacePickerSettings struct {
	ClientID     string
	DeveloperKey string
	AppID        string
}

type WorkspacePicker struct {
	WorkspacePickerSettings
	AccountEmail     string
	ExpectedRevision int64
}

// GetGoogleWorkspacePicker returns public browser settings; stored OAuth tokens never leave the backend.
func (s *service) GetGoogleWorkspacePicker(ctx context.Context, org, source slug.Slug) (*WorkspacePicker, error) {
	ds, err := s.findRepoDataSource(ctx, org, source)
	if err != nil {
		return nil, err
	}
	if ds.Type != model.DataSourceTypeGoogleWorkspace {
		return nil, ErrInvalidInput.Detail("Choose a Workspace data source")
	}
	var cfg workspace.Config
	if json.Unmarshal(ds.Config, &cfg) != nil || !ds.IsActive || ds.Credential == nil || ds.Credential.Status != "active" || ds.Credential.Kind != "oauth2_grant" || ds.Credential.Account.ID == "" || ds.Credential.Provider != "google_workspace" || !workspace.ValidFileScopes(ds.Credential.Scopes) || ds.Credential.Account.Email == "" {
		return nil, ErrInvalidInput.Detail("Reconnect Google Workspace with selected-file access")
	}
	settings := s.googleWorkspacePicker
	if settings.ClientID == "" || settings.DeveloperKey == "" || settings.AppID == "" {
		return nil, xerror.Unavailable(xerror.DataSource).Detail("Google Picker is not configured")
	}
	return &WorkspacePicker{WorkspacePickerSettings: settings, AccountEmail: ds.Credential.Account.Email, ExpectedRevision: ds.ConnectionTestRevision}, nil
}

type WorkspaceVerificationParams struct {
	FileIDs          []string
	ExpectedRevision int64
}

func decodeWorkspacePending(raw []byte) error {
	if raw = bytes.TrimSpace(raw); len(raw) == 0 || raw[0] != '{' {
		return ErrInvalidInput.Detail("Expected pending Workspace configuration")
	}
	var pending workspace.PendingInstallConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pending); err != nil {
		return ErrInvalidInput.Detail("Invalid pending Workspace configuration")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrInvalidInput.Detail("Expected one pending Workspace configuration")
	}
	if err := pending.Validate(); err != nil {
		return ErrInvalidInput.With(err)
	}
	return nil
}

func workspaceSelectionPending(ds *repods.DataSource) bool {
	if ds.Type != model.DataSourceTypeGoogleWorkspace {
		return false
	}
	var cfg workspace.Config
	return json.Unmarshal(ds.Config, &cfg) != nil || ds.Credential == nil || ds.Credential.Status != credentialssvc.CredentialStatusActive.String()
}

func workspaceConsentConfig(install credentialssvc.CredentialInstall, ds *repods.DataSource) (workspace.Config, error) {
	if ds.Type != model.DataSourceTypeGoogleWorkspace || install.ExpectedSourceRevision == nil || ds.ConnectionTestRevision != *install.ExpectedSourceRevision {
		return workspace.Config{}, ErrConnectionTestConflict
	}
	if !workspace.ValidFileScopes(install.RequestedScopes) {
		return workspace.Config{}, ErrInvalidInput.Detail("Workspace consent requires selected-file Drive access")
	}
	cfg := workspace.Config{AccessMode: workspace.SelectedFiles}
	return cfg, nil
}

func (s *service) validateWorkspaceConsent(ctx context.Context, install credentialssvc.CredentialInstall, tokens credentialssvc.OAuthTokenSet) error {
	ctx = postgres.WithoutTransaction(ctx)
	ds, err := s.findRepoDataSource(ctx, install.Owner.OrgSlug, install.Owner.DataSourceSlug)
	if err != nil {
		return err
	}
	cfg, err := workspaceConsentConfig(install, ds)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	credentials, err := json.Marshal(map[string]any{"access_token": tokens.AccessToken, "granted_scopes": tokens.Scopes})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	defer clear(credentials)
	return s.runConnectionTest(ctx, install.Owner.OrgSlug, ds.Slug, ds.Type, raw, credentials)
}

// The credential service locks the owner before calling this in its finalization transaction.
func (s *service) finalizeWorkspaceConsent(ctx context.Context, install credentialssvc.CredentialInstall, _ credentialssvc.OAuthTokenSet) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		ds, err := s.repo.FindBySlug(ctx, tx, install.Owner.OrgSlug, install.Owner.DataSourceSlug)
		if err != nil {
			return err
		}
		cfg, err := workspaceConsentConfig(install, ds)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		active := true
		ds, err = s.repo.Update(ctx, tx, install.Owner.OrgSlug, ds.Slug, &repods.UpdateParams{Name: ds.Name, ReadOnly: ds.ReadOnly, ConfigJSON: raw, IsActive: &active})
		if err != nil {
			return err
		}
		if _, err := s.schemaRepo.ResetImportByDataSourceSlug(ctx, tx, install.Owner.OrgSlug, ds.Slug); err != nil {
			return err
		}
		{
			_, err = s.recordConnectionSuccess(ctx, tx, install.Owner.OrgSlug, ds.Slug, &repods.MarkTestSuccessParams{ExpectedRevision: ds.ConnectionTestRevision, TestedAt: time.Now().UTC()})
			return err
		}
	})
}

func (s *service) workspaceClient(ctx context.Context, org, source slug.Slug) (*provider.Client, *repods.DataSource, error) {
	ds, err := s.findRepoDataSource(ctx, org, source)
	if err != nil {
		return nil, nil, err
	}
	if ds.Type != model.DataSourceTypeGoogleWorkspace {
		return nil, nil, ErrInvalidInput.Detail("Choose a Workspace data source")
	}

	var cfg workspace.Config
	if err := json.Unmarshal(ds.Config, &cfg); err != nil {
		return nil, nil, err
	}
	raw, _, err := s.resolveCredentialPayload(ctx, org, ds)
	if err != nil {
		return nil, nil, err
	}
	defer clear(raw)
	var creds provider.Credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, nil, xerror.InvalidCredentials()
	}
	client, err := provider.New(ctx, creds)
	return client, ds, err
}

func (s *service) ListGoogleWorkspaceFiles(ctx context.Context, org, source slug.Slug, options workspace.ListOptions) (*workspace.Discovery, error) {
	if err := options.Validate(); err != nil {
		return nil, xerror.Wrapf("validate Workspace list options: %w", err)
	}
	client, _, err := s.workspaceClient(ctx, org, source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	ctx = schemaquota.WithQuota(ctx, s.schemaMaxItems, s.schemaMaxBytes)
	return client.Discovery(ctx, options, provider.Limits{}, s.schemaMaxItems)
}

func (s *service) GetGoogleWorkspaceFile(ctx context.Context, org, source slug.Slug, id string) (*workspace.File, error) {
	client, _, err := s.workspaceClient(ctx, org, source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	return client.FileMetadata(ctx, id, provider.Limits{})
}

func (s *service) GetGoogleWorkspaceWorksheets(ctx context.Context, org, source slug.Slug, id string) (*workspace.Metadata, error) {
	client, _, err := s.workspaceClient(ctx, org, source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	ctx = schemaquota.WithQuota(ctx, s.schemaMaxItems, s.schemaMaxBytes)
	return client.Metadata(ctx, id, provider.Limits{})
}

func (s *service) VerifyGoogleWorkspaceFiles(ctx context.Context, org, source slug.Slug, _ model.EventActor, params WorkspaceVerificationParams) (*workspace.Discovery, error) {
	if len(params.FileIDs) == 0 || len(params.FileIDs) > 100 {
		return nil, ErrInvalidInput.Detail("Choose 1 to 100 file IDs")
	}
	seen := map[string]bool{}
	for _, id := range params.FileIDs {
		if err := workspace.ValidateID(id); err != nil {
			return nil, xerror.Wrapf("validate Workspace target ID: %w", err)
		}
		if seen[id] {
			return nil, ErrInvalidInput.Detail("File IDs must be unique")
		}
		seen[id] = true
	}
	client, ds, err := s.workspaceClient(ctx, org, source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	if ds.ConnectionTestRevision != params.ExpectedRevision {
		return nil, ErrConnectionTestConflict
	}
	result := &workspace.Discovery{Files: []workspace.File{}}
	ctx = schemaquota.WithQuota(ctx, s.schemaMaxItems, s.schemaMaxBytes)
	for _, id := range params.FileIDs {
		f, err := client.FileMetadata(ctx, id, provider.Limits{})
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(f)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		if err := schemaquota.Charge(ctx, 1, len(raw)); err != nil {
			return nil, err
		}
		result.Files = append(result.Files, *f)
	}
	current, err := s.findRepoDataSource(ctx, org, source)
	if err != nil {
		return nil, err
	}
	if current.ConnectionTestRevision != params.ExpectedRevision {
		return nil, ErrConnectionTestConflict
	}
	return result, nil
}
