package datasourcessvc

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

// DataSource represents a data source in the domain model (service layer).
type DataSource struct {
	ConnectionRevision           int64
	OrganizationGitLabConnection bool
	Slug                         slug.Slug
	Name                         string
	Type                         model.DataSourceType
	Config                       json.RawMessage
	Schema                       SchemaResult
	SchemaCached                 bool
	SchemaImportStatus           model.DataSourceSchemaImportStatus
	SchemaImportDiagnostic       *SchemaImportDiagnostic
	SchemaImportStartedAt        *time.Time
	SchemaImportFinishedAt       *time.Time
	EncryptedCredentials         []byte
	Credential                   *credentialssvc.CredentialMetadata
	IsActive                     bool
	ReadOnly                     bool
	LastTestedAt                 *time.Time
	TestError                    *string
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

// StartProviderDataSourceInstallParams defines a new provider-backed data source before authorization.
type StartProviderDataSourceInstallParams struct {
	Name       string `validate:"required,min=1,max=100"`
	Slug       slug.Slug
	Type       model.DataSourceType `validate:"required,datasourcetype"`
	ConfigJSON json.RawMessage      `validate:"required"`
	ReadOnly   *bool
}

// ProviderDataSourceInstall returns the pending source and its provider authorization metadata.
type ProviderDataSourceInstall struct {
	DataSource *DataSource
	Install    credentialssvc.ProviderConnectorInstall
}

// SchemaImportDiagnostic is safe to expose to operators after a failed import.
type SchemaImportDiagnostic struct {
	Code    string
	Message string
}

// QueuedSchemaImportJob reports durable queue acceptance metadata for an import request.
type QueuedSchemaImportJob struct {
	Kind  string
	Queue string
	State string
}

// SchemaImportResult reports the durable status after requesting a schema import.
type SchemaImportResult struct {
	DataSourceSlug slug.Slug
	Status         model.DataSourceSchemaImportStatus
	Diagnostic     *SchemaImportDiagnostic
	Skipped        bool
	StartedAt      *time.Time
	FinishedAt     *time.Time
	Enqueued       bool
	Job            *QueuedSchemaImportJob
}

// DataSourceBranch describes a remote branch visible through a git-compatible data source.
type DataSourceBranch struct {
	Name      string
	IsDefault bool
}

type ProviderRepository struct {
	ID            string
	Name          string
	FullName      string
	WebURL        string
	DefaultBranch string
}

type SelectProviderRepositoryParams struct {
	RepositoryID string `validate:"required"`
}

// CustomerConfigPolicy controls which customer-supplied data source config options are accepted.
type CustomerConfigPolicy struct {
	AllowPlaintextConnectors    bool
	AllowPrivateDataSourceHosts bool
}

// ServiceConfig configures data source execution dependencies and limits.
type ServiceConfig struct {
	HTTPOAuthRedirectURI        string
	GoogleWorkspacePicker       WorkspacePickerSettings
	SnowflakeWIFIssuer          string
	CustomerConfigPolicy        CustomerConfigPolicy
	ConnectionTestExecutor      ConnectionTestExecutor
	SchemaIntrospectionExecutor SchemaIntrospectionExecutor
	SchemaMaxBytes              int
	SchemaMaxItems              int
	GitBranchExecutor           GitBranchExecutor
	GitBranchMaxBytes           int
	GitBranchMaxItems           int
}

// CreateParams are the parameters for creating a new data source.
type CreateParams struct {
	Name        string               `validate:"required,min=1,max=100"`
	Slug        slug.Slug            // Empty slug will be generated from name
	Type        model.DataSourceType `validate:"required,datasourcetype"`
	ConfigJSON  json.RawMessage      `validate:"required"`
	Credentials map[string]any
	ReadOnly    bool
}

// TestConnectionParams are the parameters for testing a draft data source connection.
type TestConnectionParams struct {
	Type        model.DataSourceType `validate:"required,datasourcetype"`
	ConfigJSON  json.RawMessage      `validate:"required"`
	Credentials map[string]any
}

// UpdateParams are the parameters for updating an existing data source.
type UpdateParams struct {
	Name               string `validate:"omitempty,min=1,max=100"`
	ConfigJSON         json.RawMessage
	ConfigFromCustomer bool
	IsActive           *bool
	ReadOnly           bool
	ReadOnlySet        bool
}

// UpdateCredentialsParams are the parameters for updating data source credentials.
type UpdateCredentialsParams struct {
	Credentials map[string]any `validate:"required"`
}

// HTTPOAuthConnection returns consent metadata only when a browser round trip is required.
type HTTPOAuthConnection struct {
	DataSource *DataSource
	Install    *credentialssvc.ProviderConnectorInstall
}
