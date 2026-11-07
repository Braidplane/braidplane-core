package datasources

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// DataSource represents a registered data source.
type DataSource struct {
	OrganizationGitLabConnection bool
	Slug                         slug.Slug
	Name                         string
	Type                         model.DataSourceType
	Config                       json.RawMessage
	EncryptedCredentials         []byte
	IsActive                     bool
	ReadOnly                     bool
	SchemaImportStatus           model.DataSourceSchemaImportStatus
	SchemaImportDiagnostic       *SchemaImportDiagnostic
	SchemaImportStartedAt        *time.Time
	SchemaImportFinishedAt       *time.Time
	LastTestedAt                 *time.Time
	TestError                    *string
	ConnectionTestRevision       int64
	Schema                       json.RawMessage
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
	Credential                   *CredentialMetadata
}

// CredentialMetadata contains only the safe lifecycle fields needed with persisted data sources.
type CredentialMetadata struct {
	Slug                 slug.Slug       `json:"slug"`
	Kind                 string          `json:"kind"`
	Status               string          `json:"status"`
	Provider             string          `json:"provider"`
	UpdatedAt            time.Time       `json:"updated_at"`
	AccessTokenExpiresAt *time.Time      `json:"access_token_expires_at"`
	Scopes               []string        `json:"scopes"`
	Account              ProviderAccount `json:"account"`
	LastRefreshedAt      *time.Time      `json:"last_refreshed_at"`
	RevokedAt            *time.Time      `json:"revoked_at"`
	FailureCode          string          `json:"failure_code"`
	FailureMessage       string          `json:"failure_message"`
}

// ProviderAccount contains the safe provider identity metadata persisted with an OAuth grant.
type ProviderAccount struct {
	ID    string `json:"ID"`
	Name  string `json:"Name"`
	Email string `json:"Email"`
}

// CreateParams contains parameters for creating a data source.
type CreateParams struct {
	Slug           slug.Slug
	Name           string
	Type           model.DataSourceType
	ConfigJSON     json.RawMessage
	CredentialSlug slug.Slug
	ReadOnly       bool
}

// UpdateParams contains parameters for updating a data source.
type UpdateParams struct {
	Name        string
	ConfigJSON  json.RawMessage
	IsActive    *bool
	ReadOnly    bool
	ReadOnlySet bool
}

// UpdateCredentialsParams contains parameters for updating data source credentials.
type UpdateCredentialsParams struct {
	CredentialSlug slug.Slug
}

type MarkTestSuccessParams struct {
	ExpectedRevision int64
	TestedAt         time.Time
}
