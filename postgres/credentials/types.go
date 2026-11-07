package credentials

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

const (
	OwnerKindDataSource             = "data_source"
	OwnerKindEnvironmentSecret      = "environment_secret"
	OwnerKindOrganizationGitLab     = "organization_gitlab"
	OwnerKindNotificationConnection = "notification_connection"
)

type CredentialRecord struct {
	Slug                       slug.Slug
	OrgSlug                    slug.Slug
	OwnerKind                  string
	NotificationConnectionSlug slug.Slug
	DataSourceSlug             slug.Slug
	EnvironmentSlug            slug.Slug
	EnvironmentSecretKey       string
	Kind                       string
	CredentialType             string
	Status                     string
	EncryptedPayload           []byte
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
	RotatedAt                  *time.Time
	RevokedAt                  *time.Time
	FailureCode                string
	FailureMessage             string
}

type CredentialInstall struct {
	RepositorySetupSlug        slug.Slug
	RepositorySetupProjectSlug slug.Slug
	EncryptedPKCEVerifier      []byte
	ExpectedSourceRevision     *int64
	Slug                       slug.Slug
	OrgSlug                    slug.Slug
	OwnerKind                  string
	NotificationConnectionSlug slug.Slug
	DataSourceSlug             slug.Slug
	Provider                   string
	StateHash                  string
	Intent                     string
	ExpectedCredentialSlug     slug.Slug
	ExpectedCredentialStatus   string
	ActorType                  string
	ActorSlug                  *slug.Slug
	RequestedScopes            []string
	ExpiresAt                  time.Time
	ConsumedAt                 *time.Time
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

type CredentialGrant struct {
	Slug                       slug.Slug
	OrgSlug                    slug.Slug
	OwnerKind                  string
	NotificationConnectionSlug slug.Slug
	DataSourceSlug             slug.Slug
	Kind                       string
	Provider                   string
	Status                     string
	EncryptedTokenSet          []byte
	AccessTokenExpires         time.Time
	Scopes                     []string
	Account                    json.RawMessage
	LastRefreshedAt            *time.Time
	RevokedAt                  *time.Time
	RotatedAt                  *time.Time
	FailureCode                string
	FailureMessage             string
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
	GrantUpdatedAt             time.Time
}

// OAuthGrantIntentState contains the locked credential state needed to validate a callback intent.
type OAuthGrantIntentState struct {
	SourceRevision int64
	CredentialSlug slug.Slug
	Kind           string
	Status         string
	HasGrant       bool
}

type ResolveRuntimeCredentialParams struct {
	OwnerKind                  string
	OrgSlug                    slug.Slug
	NotificationConnectionSlug slug.Slug
	DataSourceSlug             slug.Slug
	EnvironmentSlug            slug.Slug
	EnvironmentSecretKey       string
}

type PipelineWebhookOwner struct {
	OrgSlug, ProjectSlug, EnvironmentSlug, TriggerSlug slug.Slug
}

type NotificationConnectionCredential struct {
	CredentialRecord
	Provider string
}

type NotificationConnection struct {
	Slug      slug.Slug
	Name      string
	Provider  string
	Status    string
	Account   json.RawMessage
	UpdatedAt time.Time
}

type Repository interface {
	CreateNotificationConnection(context.Context, postgres.DBTX, CredentialRecord, string, string) (CredentialRecord, error)
	FindNotificationConnectionCredential(context.Context, postgres.DBTX, ResolveRuntimeCredentialParams) (NotificationConnectionCredential, error)
	ListNotificationConnections(context.Context, postgres.DBTX, slug.Slug) ([]NotificationConnection, error)
	ClaimHTTPOAuthRefresh(context.Context, postgres.DBTX, CredentialGrant, string) (bool, error)
	ReleaseHTTPOAuthRefresh(context.Context, postgres.DBTX, ResolveRuntimeCredentialParams, string) error
	SaveHTTPOAuthApp(context.Context, postgres.DBTX, ResolveRuntimeCredentialParams, []byte) error
	FindHTTPOAuthApp(context.Context, postgres.DBTX, ResolveRuntimeCredentialParams) ([]byte, error)
	CreateStaticSecret(ctx context.Context, tx postgres.DBTX, record CredentialRecord) (CredentialRecord, error)
	CreatePendingOAuthCredential(ctx context.Context, tx postgres.DBTX, record CredentialRecord) (CredentialRecord, error)
	CreateOrganizationGitLabConnection(ctx context.Context, tx postgres.DBTX, record CredentialRecord) (CredentialRecord, error)
	AttachOrganizationGitLabConnection(ctx context.Context, tx postgres.DBTX, orgSlug, dataSourceSlug, credentialSlug slug.Slug, repositoryID string) error
	FindOrganizationGitLabConnection(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug) (CredentialRecord, error)
	FindStaticSecretByDataSource(ctx context.Context, tx postgres.DBTX, params ResolveRuntimeCredentialParams) (CredentialRecord, error)
	FindStaticSecretByEnvironmentSecret(ctx context.Context, tx postgres.DBTX, params ResolveRuntimeCredentialParams) (CredentialRecord, error)
	MarkStaticEnvironmentSecretRotated(ctx context.Context, tx postgres.DBTX, params ResolveRuntimeCredentialParams, replacementSlug slug.Slug, rotatedAt time.Time) ([]CredentialRecord, error)
	MarkStaticEnvironmentSecretRevoked(ctx context.Context, tx postgres.DBTX, params ResolveRuntimeCredentialParams, revokedAt time.Time) (CredentialRecord, error)
	CreatePipelineWebhookCredential(ctx context.Context, tx postgres.DBTX, owner PipelineWebhookOwner, record CredentialRecord, rotatedAt time.Time) (CredentialRecord, int, error)
	LockPipelineWebhookCredentialOwner(ctx context.Context, tx postgres.DBTX, owner PipelineWebhookOwner) error
	RevokePipelineWebhookCredentials(ctx context.Context, tx postgres.DBTX, owner PipelineWebhookOwner, revokedAt time.Time) error
	ResolvePipelineWebhookCredential(ctx context.Context, tx postgres.DBTX, owner PipelineWebhookOwner, keyVersion int) (CredentialRecord, error)
	CreateOAuthInstall(ctx context.Context, tx postgres.DBTX, install CredentialInstall) (CredentialInstall, error)
	FindOAuthInstallByState(ctx context.Context, tx postgres.DBTX, provider, stateHash string, now time.Time) (CredentialInstall, error)
	ConsumeOAuthInstall(ctx context.Context, tx postgres.DBTX, provider, stateHash string, now time.Time) (CredentialInstall, error)
	LockOAuthGrantIntent(ctx context.Context, tx postgres.DBTX, owner ResolveRuntimeCredentialParams) (OAuthGrantIntentState, error)
	CreateOAuthGrant(ctx context.Context, tx postgres.DBTX, grant CredentialGrant, intent string) (CredentialGrant, error)
	UpsertOAuthGrant(ctx context.Context, tx postgres.DBTX, grant CredentialGrant) (CredentialGrant, error)
	FindOAuthGrant(ctx context.Context, tx postgres.DBTX, params ResolveRuntimeCredentialParams) (CredentialGrant, error)
	MarkOAuthGrantRevoked(ctx context.Context, tx postgres.DBTX, grant CredentialGrant) (CredentialGrant, error)
}
