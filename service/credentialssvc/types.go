package credentialssvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const DefaultInstallTTL = 10 * time.Minute

//go:generate -command enumer go tool -modfile=../../tools/go.mod enumer
//go:generate enumer -type=CredentialKind -transform=snake -output=credential_kind_string.go -text -json -linecomment
//go:generate enumer -type=CredentialOwnerKind -transform=snake -output=credential_owner_kind_string.go -text -json -linecomment
//go:generate enumer -type=CredentialStatus -transform=snake -output=credential_status_string.go -text -json -linecomment
//go:generate enumer -type=StaticCredentialType -transform=snake -output=static_credential_type_string.go -text -json -linecomment
//go:generate enumer -type=OAuthInstallIntent -transform=snake -output=oauth_install_intent_string.go -text -json -linecomment

type CredentialKind int

const (
	CredentialKindStaticSecret CredentialKind = iota // static_secret
	CredentialKindOAuth2Grant                        // oauth2_grant
)

type CredentialOwnerKind int

const (
	CredentialOwnerKindDataSource             CredentialOwnerKind = iota // data_source
	CredentialOwnerKindEnvironmentSecret                                 // environment_secret
	CredentialOwnerKindOrganizationGitLab                                // organization_gitlab
	CredentialOwnerKindNotificationConnection                            // notification_connection
)

type CredentialStatus int

const (
	CredentialStatusPending CredentialStatus = iota // pending
	CredentialStatusActive                          // active
	CredentialStatusFailed                          // failed
	CredentialStatusRevoked                         // revoked
	CredentialStatusRotated                         // rotated
)

type StaticCredentialType int

const (
	StaticCredentialTypePostgres          StaticCredentialType = iota // postgres
	StaticCredentialTypeMySQL                                         // mysql
	StaticCredentialTypeBigQuery                                      // bigquery
	StaticCredentialTypeHTTP                                          // http
	StaticCredentialTypeHubSpot                                       // hubspot
	StaticCredentialTypeMongoDB                                       // mongodb
	StaticCredentialTypeFTP                                           // ftp
	StaticCredentialTypeGit                                           // git
	StaticCredentialTypeS3                                            // s3
	StaticCredentialTypeEnvironmentSecret                             // environment_secret
	StaticCredentialTypeSnowflake                                     // snowflake
)

type OAuthInstallIntent int

const (
	OAuthInstallIntentCreate OAuthInstallIntent = iota // create_grant
	OAuthInstallIntentRotate                           // rotate_grant
)

type CredentialOwner struct {
	Kind                       CredentialOwnerKind
	OrgSlug                    slug.Slug `validate:"required"`
	DataSourceSlug             slug.Slug
	NotificationConnectionSlug slug.Slug
	EnvironmentSlug            slug.Slug
	EnvironmentSecretKey       string
}

type ProviderAccount struct {
	ID          string
	Name        string
	Email       string
	ChannelID   string `json:",omitempty"`
	ChannelName string `json:",omitempty"`
}

type OAuthTokenSet struct {
	SlackWebhookURL string          `json:"slack_webhook_url,omitempty"`
	HTTPApp         *HTTPOAuthApp   `json:"http_app,omitempty"`
	AccessToken     string          `json:"access_token"`
	RefreshToken    string          `json:"refresh_token"`
	ExpiresAt       time.Time       `json:"expires_at"`
	Scopes          []string        `json:"scopes"`
	Account         ProviderAccount `json:"account"`
}

type CredentialOAuthMetadata struct {
	Provider             string
	Scopes               []string
	Account              ProviderAccount
	AccessTokenExpiresAt time.Time
	LastRefreshedAt      *time.Time
	RevokedAt            *time.Time
	FailureCode          string
	FailureMessage       string
}

type CredentialMetadata struct {
	Slug      slug.Slug
	Kind      CredentialKind
	Status    CredentialStatus
	UpdatedAt time.Time
	OAuth     CredentialOAuthMetadata
}

type CredentialRecord struct {
	Slug             slug.Slug
	Owner            CredentialOwner
	Kind             CredentialKind
	CredentialType   StaticCredentialType
	Status           CredentialStatus
	EncryptedPayload []byte
	CreatedAt        time.Time
	UpdatedAt        time.Time
	RotatedAt        *time.Time
	RevokedAt        *time.Time
	FailureCode      string
	FailureMessage   string
}

type CredentialInstall struct {
	RepositorySetupSlug        slug.Slug
	RepositorySetupProjectSlug slug.Slug
	EncryptedPKCEVerifier      []byte
	ExpectedSourceRevision     *int64
	Slug                       slug.Slug
	Owner                      CredentialOwner
	Provider                   string
	StateHash                  string
	Intent                     OAuthInstallIntent
	ExpectedCredentialSlug     slug.Slug
	ExpectedCredentialStatus   CredentialStatus
	Actor                      model.EventActor
	RequestedScopes            []string
	ExpiresAt                  time.Time
	ConsumedAt                 *time.Time
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
}

type CredentialGrant struct {
	Slug               slug.Slug
	Owner              CredentialOwner
	Kind               CredentialKind
	Provider           string
	Status             CredentialStatus
	EncryptedTokenSet  []byte
	AccessTokenExpires time.Time
	Scopes             []string
	Account            ProviderAccount
	LastRefreshedAt    *time.Time
	RevokedAt          *time.Time
	RotatedAt          *time.Time
	FailureCode        string
	FailureMessage     string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	GrantUpdatedAt     time.Time
}

type RuntimeCredential struct {
	Kind          CredentialKind
	Version       string
	PlaintextJSON []byte
}

type CreateStaticSecretParams struct {
	Owner          CredentialOwner `validate:"required"`
	CredentialType StaticCredentialType
	PlaintextJSON  []byte `validate:"required"`
	Actor          model.EventActor
}

type PipelineWebhookSecretParams struct {
	OrgSlug, ProjectSlug, EnvironmentSlug, TriggerSlug slug.Slug `validate:"required"`
}

type IssuedPipelineWebhookSecret struct {
	KeyVersion int
	Value      string
}

type ResolvePipelineWebhookSecretParams struct {
	OrgSlug, ProjectSlug, EnvironmentSlug, TriggerSlug slug.Slug `validate:"required"`
	KeyVersion                                         int       `validate:"required,min=1"`
}

type CreatePendingOAuthCredentialParams struct {
	Owner    CredentialOwner `validate:"required"`
	Provider string          `validate:"required"`
}

type (
	OrganizationGitLabConnection struct {
		OrganizationSlug slug.Slug
		Credential       CredentialMetadata
	}
	CreateOrganizationGitLabConnectionParams struct {
		OrganizationSlug slug.Slug `validate:"required"`
		Provider         string    `validate:"required"`
	}
	AttachOrganizationGitLabConnectionParams struct {
		OrganizationSlug       slug.Slug `validate:"required"`
		DataSourceSlug         slug.Slug `validate:"required"`
		ExpectedRepositoryID   string    `validate:"required"`
		ExpectedCredentialSlug slug.Slug `validate:"required"`
	}
)

type StartOAuthInstallParams struct {
	RepositorySetupSlug        slug.Slug
	RepositorySetupProjectSlug slug.Slug
	Owner                      CredentialOwner `validate:"required"`
	Provider                   string          `validate:"required"`
	RequestedScopes            []string
	IncludeWriteScopes         bool
	AccessMode                 string `validate:"omitempty,oneof=selected_files"`
	Intent                     OAuthInstallIntent
	Actor                      model.EventActor
}

type ProviderConnectorInstall struct {
	Slug             slug.Slug
	Provider         string
	AuthorizationURL string
	State            string
	ExpiresAt        time.Time
	Intent           OAuthInstallIntent
	RequestedScopes  []string
}

type CompleteOAuthCallbackParams struct {
	Issuer string
	// ValidateWorkspace checks provider access before opening the finalization transaction.
	ValidateWorkspace func(context.Context, CredentialInstall, OAuthTokenSet) error `validate:"-"`
	// FinalizeWorkspace updates the source atomically with its grant.
	FinalizeWorkspace func(context.Context, CredentialInstall, OAuthTokenSet) error `validate:"-"`

	Provider string `validate:"required"`
	State    string `validate:"required"`
	Code     string `validate:"required"`
	Actor    model.EventActor
}

type FindOAuthInstallParams struct {
	Provider string `validate:"required"`
	State    string `validate:"required"`
}

// ConsumeOAuthInstallParams identifies a terminal OAuth callback that must not exchange a grant.
type ConsumeOAuthInstallParams struct {
	Provider string `validate:"required"`
	State    string `validate:"required"`
}

type CompleteOAuthCallbackResult struct {
	InstallSlug slug.Slug
	Owner       CredentialOwner
	Intent      OAuthInstallIntent
	Actor       model.EventActor
	Credential  CredentialMetadata
}

type ResolveRuntimeCredentialParams struct {
	Owner CredentialOwner `validate:"required"`
}

type ProviderRepository struct {
	ID            string
	Name          string
	FullName      string
	WebURL        string
	CloneURL      string
	DefaultBranch string
}

type ListProviderRepositoriesParams struct {
	Owner    CredentialOwner `validate:"required"`
	Provider string          `validate:"required"`
}

type ResolveProviderRepositoryParams struct {
	Owner        CredentialOwner `validate:"required"`
	Provider     string          `validate:"required"`
	RepositoryID string          `validate:"required"`
}

type ResolveProviderRepositoryWithTokenParams struct {
	Provider     string `validate:"required"`
	AccessToken  string `validate:"required"`
	RepositoryID string `validate:"required"`
}

type ProviderRepositoryService interface {
	ListProviderRepositories(context.Context, ListProviderRepositoriesParams) ([]ProviderRepository, error)
	ResolveProviderRepository(context.Context, ResolveProviderRepositoryParams) (ProviderRepository, error)
	ResolveProviderRepositoryWithToken(context.Context, ResolveProviderRepositoryWithTokenParams) (ProviderRepository, error)
}

type RefreshGrantParams struct {
	Owner CredentialOwner `validate:"required"`
	Actor model.EventActor
}

type RevokeParams struct {
	Owner CredentialOwner `validate:"required"`
	Actor model.EventActor
}

type RotateParams struct {
	Owner              CredentialOwner `validate:"required"`
	Actor              model.EventActor
	IncludeWriteScopes bool
	AccessMode         string `validate:"omitempty,oneof=selected_files"`
}

type AuthorizationURLParams struct {
	PKCEVerifier   string
	State          string
	Scopes         []string
	OptionalScopes []string
}

type ExchangeCodeParams struct {
	PKCEVerifier string
	Issuer       string
	Code         string
	// RequestedScopes comes from the stored install, never from the callback.
	RequestedScopes []string
}

type RefreshParams struct {
	RefreshToken string
	Scopes       []string
}

type RevokeProviderGrantParams struct {
	AccessToken  string
	RefreshToken string
}

type ProviderAdapter interface {
	AuthorizationURL(ctx context.Context, params AuthorizationURLParams) (string, error)
	ExchangeCode(ctx context.Context, params ExchangeCodeParams) (OAuthTokenSet, error)
	Refresh(ctx context.Context, params RefreshParams) (OAuthTokenSet, error)
	Revoke(ctx context.Context, params RevokeProviderGrantParams) error
}

type RepositoryProvider interface {
	ProviderAdapter
	ListRepositories(context.Context, string) ([]ProviderRepository, error)
	ResolveRepository(context.Context, string, string) (ProviderRepository, error)
}

// ScopePlanningProvider supplies a provider-owned authorization scope plan.
// It prevents connector install callers from escalating provider permissions.
type ScopePlanningProvider interface {
	ProviderAdapter
	OAuthScopePlan() OAuthScopePlan
}

// ContextualScopePlanningProvider narrows provider-owned consent scopes for the
// requested access mode without allowing callers to name arbitrary scopes.
type ContextualScopePlanningProvider interface {
	ProviderAdapter
	OAuthScopePlanForInstall(OAuthScopePlanParams) OAuthScopePlan
}

type OAuthScopePlanParams struct {
	IncludeWriteScopes bool
	AccessMode         string
}

// OAuthScopePlan separates consent-blocking scopes from best-effort scopes.
type OAuthScopePlan struct {
	Required []string
	Optional []string
}

type ProviderRegistry map[string]ProviderAdapter

type NotificationConnection struct {
	Slug      slug.Slug
	Name      string
	Provider  string
	Status    CredentialStatus
	Account   ProviderAccount
	UpdatedAt time.Time
}

type CreateNotificationConnectionParams struct {
	Provider string    `validate:"required,oneof=slack"`
	OrgSlug  slug.Slug `validate:"required"`
	Slug     slug.Slug
	Name     string `validate:"required,max=255"`
}
