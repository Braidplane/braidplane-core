package datasourcessvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

// toDataSourceFromRepo converts a repository DataSource to a service DataSource.
func toDataSourceFromRepo(repoDS *repods.DataSource) (*DataSource, error) {
	var credential *credentialssvc.CredentialMetadata
	if repoDS.Credential != nil {
		var err error
		credential, err = toCredentialMetadataFromRepo(repoDS.Credential)
		if err != nil {
			return nil, err
		}
	}
	return &DataSource{
		OrganizationGitLabConnection: repoDS.OrganizationGitLabConnection,
		Slug:                         repoDS.Slug,
		ConnectionRevision:           repoDS.ConnectionTestRevision,
		Name:                         repoDS.Name,
		Type:                         repoDS.Type,
		Config:                       repoDS.Config,
		Schema:                       nil, // Schema is not stored in the repository DataSource, it will be populated separately
		SchemaImportStatus:           repoDS.SchemaImportStatus,
		SchemaImportDiagnostic:       toServiceSchemaImportDiagnostic(repoDS.SchemaImportDiagnostic),
		SchemaImportStartedAt:        repoDS.SchemaImportStartedAt,
		SchemaImportFinishedAt:       repoDS.SchemaImportFinishedAt,
		IsActive:                     repoDS.IsActive,
		ReadOnly:                     repoDS.ReadOnly,
		LastTestedAt:                 repoDS.LastTestedAt,
		TestError:                    repoDS.TestError,
		CreatedAt:                    repoDS.CreatedAt,
		UpdatedAt:                    repoDS.UpdatedAt,
		Credential:                   credential,
	}, nil
}

// toCredentialMetadataFromRepo converts persisted safe lifecycle fields without exposing credential payloads.
func toCredentialMetadataFromRepo(metadata *repods.CredentialMetadata) (*credentialssvc.CredentialMetadata, error) {
	kind, err := credentialssvc.CredentialKindString(metadata.Kind)
	if err != nil {
		return nil, xerror.Unexpected().Detailf("convert persisted credential kind: %w", err)
	}
	status, err := credentialssvc.CredentialStatusString(metadata.Status)
	if err != nil {
		return nil, xerror.Unexpected().Detailf("convert persisted credential status: %w", err)
	}
	credential := &credentialssvc.CredentialMetadata{
		Slug:      metadata.Slug,
		Kind:      kind,
		Status:    status,
		UpdatedAt: metadata.UpdatedAt,
		OAuth: credentialssvc.CredentialOAuthMetadata{
			Provider: metadata.Provider,
			Scopes:   append([]string{}, metadata.Scopes...),
			Account: credentialssvc.ProviderAccount{
				ID: metadata.Account.ID, Name: metadata.Account.Name, Email: metadata.Account.Email,
			},
			LastRefreshedAt: metadata.LastRefreshedAt,
			RevokedAt:       metadata.RevokedAt,
			FailureCode:     metadata.FailureCode,
			FailureMessage:  metadata.FailureMessage,
		},
	}
	if metadata.AccessTokenExpiresAt != nil {
		credential.OAuth.AccessTokenExpiresAt = *metadata.AccessTokenExpiresAt
	}
	return credential, nil
}

func toServiceSchemaImportDiagnostic(
	diagnostic *repods.SchemaImportDiagnostic,
) *SchemaImportDiagnostic {
	if diagnostic == nil {
		return nil
	}
	return &SchemaImportDiagnostic{
		Code:    diagnostic.Code,
		Message: diagnostic.Message,
	}
}

// toRepoUpdateParams converts service UpdateParams to repository UpdateParams.
func toRepoUpdateParams(p *UpdateParams, current *repods.DataSource) *repods.UpdateParams {
	name := current.Name
	if p.Name != "" {
		name = p.Name
	}

	configJSON := current.Config
	if len(p.ConfigJSON) > 0 {
		configJSON = p.ConfigJSON
	}

	isActive := current.IsActive
	if p.IsActive != nil {
		isActive = *p.IsActive
	}

	readOnly := current.ReadOnly
	if p.ReadOnlySet {
		readOnly = p.ReadOnly
	}

	return &repods.UpdateParams{
		Name:        name,
		ConfigJSON:  configJSON,
		IsActive:    &isActive,
		ReadOnly:    readOnly,
		ReadOnlySet: true,
	}
}
