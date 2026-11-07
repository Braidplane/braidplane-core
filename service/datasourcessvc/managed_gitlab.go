package datasourcessvc

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	gitconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/git"
)

func (s *service) AttachOrganizationGitLabConnection(ctx context.Context, org, ds slug.Slug, actor model.EventActor) error {
	source, err := s.findRepoDataSource(ctx, org, ds)
	if err != nil {
		return err
	}
	if source.Type != model.DataSourceTypeGitlab {
		return ErrInvalidInput.Detail("Select a managed GitLab source.")
	}
	var config gitconnector.ProviderConfig
	if err := json.Unmarshal(source.Config, &config); err != nil || config.Repository.ID == "" {
		return ErrInvalidInput.Detail("Select a verified GitLab repository before attaching the organization connection.")
	}
	selection, err := json.Marshal(map[string]string{"project_id": config.Repository.ID, "base_path": config.BasePath})
	if err != nil {
		return err
	}
	_, _, credentialSlug, err := s.normalizeGitLabCreate(ctx, org, &CreateParams{ConfigJSON: selection, ReadOnly: source.ReadOnly})
	if err != nil {
		return err
	}
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.credentials.AttachOrganizationGitLabConnection(ctx, credentialssvc.AttachOrganizationGitLabConnectionParams{OrganizationSlug: org, DataSourceSlug: ds, ExpectedRepositoryID: config.Repository.ID, ExpectedCredentialSlug: credentialSlug}); err != nil {
			return err
		}
		updated, err := s.repo.FindBySlug(ctx, tx, org, ds)
		if err != nil {
			return err
		}
		return s.recordDataSourceEvent(ctx, tx, org, actor, updated, model.EventActionUpdate)
	})
	if err != nil {
		return err
	}
	return s.TestExistingConnection(ctx, org, ds)
}

func (s *service) normalizeGitLabCreate(ctx context.Context, org slug.Slug, params *CreateParams) ([]byte, []byte, slug.Slug, error) {
	var config struct {
		ProjectID string `json:"project_id"`
		BasePath  string `json:"base_path"`
	}
	if len(params.Credentials) != 0 {
		return nil, nil, "", ErrInvalidInput.Detail("Use Generic Git for manual GitLab credentials.")
	}
	if err := json.Unmarshal(params.ConfigJSON, &config); err != nil || strings.TrimSpace(config.ProjectID) == "" {
		return nil, nil, "", ErrInvalidInput.Detail("Select a GitLab repository from the organization connection.")
	}
	connection, err := s.credentials.GetOrganizationGitLabConnection(ctx, org)
	if err != nil {
		return nil, nil, "", err
	}
	if connection.Credential.Status != credentialssvc.CredentialStatusActive {
		return nil, nil, "", ErrInvalidInput.Detail("Connect GitLab in organization settings before creating a managed source.")
	}
	if !params.ReadOnly && !slices.Contains(connection.Credential.OAuth.Scopes, "write_repository") && !slices.Contains(connection.Credential.OAuth.Scopes, "api") {
		return nil, nil, "", ErrInvalidInput.Detail("The organization GitLab connection permits reads only. Choose read-only or reconnect with Git write permission.")
	}
	owner := credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindOrganizationGitLab, OrgSlug: org}
	repository, err := s.credentials.ResolveProviderRepository(ctx, credentialssvc.ResolveProviderRepositoryParams{Owner: owner, Provider: "gitlab", RepositoryID: config.ProjectID})
	if err != nil {
		return nil, nil, "", err
	}
	runtime, err := s.credentials.ResolveRuntimeCredential(ctx, credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
	if err != nil {
		return nil, nil, "", err
	}
	var tokens credentialssvc.OAuthTokenSet
	if err := json.Unmarshal(runtime.PlaintextJSON, &tokens); err != nil || tokens.AccessToken == "" {
		return nil, nil, "", xerror.InvalidCredentials()
	}
	params.ConfigJSON, err = json.Marshal(gitconnector.ProviderConfig{Repository: gitconnector.ProviderRepository{ID: repository.ID, Name: repository.Name, FullName: repository.FullName, WebURL: repository.WebURL, DefaultBranch: repository.DefaultBranch}, BasePath: config.BasePath})
	if err != nil {
		return nil, nil, "", err
	}
	configJSON, err := json.Marshal(gitconnector.Config{RepoURL: repository.CloneURL, DefaultBranch: repository.DefaultBranch, BasePath: config.BasePath})
	if err != nil {
		return nil, nil, "", err
	}
	credentialsJSON, err := json.Marshal(gitconnector.Credentials{AuthType: gitconnector.AuthTypeHTTPS, Username: "oauth2", Token: tokens.AccessToken})
	return configJSON, credentialsJSON, connection.Credential.Slug, err
}
