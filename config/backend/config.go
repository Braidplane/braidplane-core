package backend

import (
	"encoding/hex"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"

	"github.com/Braidplane/braidplane-core/config"
	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	httpScheme                 = "http"
	httpsScheme                = "https"
	defaultFrontendURL         = "http://localhost:3002"
	defaultGoogleRedirectURI   = "http://localhost:3002/api/auth/google/callback"
	googleRedirectCallbackURL  = "/api/auth/google/callback"
	hubSpotRedirectCallbackURL = "/provider-connectors/hubspot/callback"
	gitLabRedirectCallbackURL  = "/provider-connectors/gitlab/callback"
	defaultEncryptionKeyHex    = "0101010101010101010101010101010101010101010101010101010101010101"
	defaultNoReplyAddress      = "no-reply@braidplane.com"
	postgresSSLModeVerifyFull  = "verify-full"
)

// Config represents the backend application runtime configuration.
type Config struct {
	Deployment    config.DeploymentConfig `toml:"deployment"`
	Server        ServerConfig            `toml:"server"`
	Database      DatabaseConfig          `toml:"database"`
	Session       SessionConfig           `toml:"session"`
	Security      SecurityConfig          `toml:"security"`
	Auth          AuthConfig              `toml:"auth"`
	AbuseControls AbuseControlsConfig     `toml:"abuse_controls"`

	PrivatePostgres   PrivatePostgresConfig   `toml:"private_postgres"`
	Proof             config.ProofConfig      `toml:"proof"`
	ProofObservations ProofObservationsConfig `toml:"proof_observations"`
	HTTPOAuth         HTTPOAuthConfig         `toml:"http_oauth"`
	HubSpot           HubSpotConfig           `toml:"hubspot"`
	Slack             SlackConfig             `toml:"slack"`
	GitLab            GitLabConfig            `toml:"gitlab"`
	GitHubProof       GitHubProofConfig       `toml:"github_proof"`
	BigQuery          BigQueryConfig          `toml:"bigquery"`
	GoogleWorkspace   GoogleWorkspaceConfig   `toml:"google_workspace"`
	Snowflake         SnowflakeConfig         `toml:"snowflake"`
	CORS              CORSConfig              `toml:"cors"`
	SMTP              config.SMTP             `toml:"smtp"`
	Logging           LoggingConfig           `toml:"logging"`
	Tracing           config.TracingConfig    `toml:"tracing"`
	Metrics           config.MetricsConfig    `toml:"metrics"`
	Execution         ExecutionConfig         `toml:"execution"`
	ExecutionLimits   ExecutionLimitsConfig   `toml:"execution_limits"`
	Worker            WorkerConfig            `toml:"worker"`
	FrontendURL       string                  `toml:"frontend_url"     env:"FRONTEND_URL"     flag:"frontend-url"     desc:"Canonical browser application URL used for links and authentication flows"`
	DefaultLanguage   string                  `toml:"default_language" env:"DEFAULT_LANGUAGE" flag:"default-language" desc:"Default language for the application"`
}

// PrivatePostgresConfig permits one operator-pinned endpoint for one organization.
// Empty fields disable the exception; it never enables customer private-host overrides.

type PrivatePostgresConfig struct {
	Organization string `toml:"organization" env:"PRIVATE_POSTGRES_ORGANIZATION" flag:"private-postgres-organization" desc:"Organization owning the pinned private PostgreSQL source"`
	Source       string `toml:"source" env:"PRIVATE_POSTGRES_SOURCE" flag:"private-postgres-source" desc:"Exact private PostgreSQL data source slug"`
	Host         string `toml:"host" env:"PRIVATE_POSTGRES_HOST" flag:"private-postgres-host" desc:"Private PostgreSQL TLS hostname"`
	Address      string `toml:"address" env:"PRIVATE_POSTGRES_ADDRESS" flag:"private-postgres-address" desc:"Pinned private IP and port for one PostgreSQL source"`
}

// Validate enforces the operator-owned private destination grant boundary.
func (c PrivatePostgresConfig) Validate(profile string) error {
	if c == (PrivatePostgresConfig{}) {
		return nil
	}
	if profile != config.DeploymentProfileSelfHosted {
		return xerror.ValidationFailed().Detail("private_postgres requires the self-hosted profile")
	}
	if _, err := slug.New(c.Organization); err != nil {
		return xerror.ValidationFailed().Detail("private_postgres.organization must be a valid slug")
	}
	if _, err := slug.New(c.Source); err != nil {
		return xerror.ValidationFailed().Detail("private_postgres.source must be a valid slug")
	}
	if err := config.ValidateNonLocalHost(c.Host, "private_postgres.host"); err != nil {
		return err
	}
	if strings.TrimSpace(c.Host) != c.Host || strings.ContainsAny(c.Host, "/:@?# \t\n") {
		return xerror.ValidationFailed().Detail("private_postgres.host must be a hostname or IPv4 literal")
	}
	address, err := netip.ParseAddrPort(c.Address)
	if err != nil || address.Port() == 0 || !address.Addr().IsPrivate() || address.Addr().Is4In6() {
		return xerror.ValidationFailed().Detail("private_postgres.address must be an RFC1918 or ULA IP with a nonzero port")
	}
	return nil
}

// ProofObservationsConfig controls explicitly authorized live metadata execution.
type ProofObservationsConfig struct {
	Enabled        bool   `toml:"enabled" env:"PROOF_OBSERVATIONS_ENABLED" flag:"proof-observations-enabled" desc:"Enable explicit read-only Proof observation admission"`
	EvidenceKeyHex string `toml:"evidence_key_hex" env:"PROOF_OBSERVATIONS_EVIDENCE_KEY_HEX" flag:"proof-observations-evidence-key-hex" desc:"Independent AES-256 Proof evidence key as hexadecimal"`
}

type GitHubProofConfig struct {
	AppID           int64  `toml:"app_id"           env:"GITHUB_PROOF_APP_ID"           flag:"github-proof-app-id"           desc:"Deployment-owned GitHub App numeric ID"`
	ClientID        string `toml:"client_id"        env:"GITHUB_PROOF_CLIENT_ID"        flag:"github-proof-client-id"        desc:"Deployment-owned GitHub App OAuth client ID"`
	ClientSecret    string `toml:"client_secret"    env:"GITHUB_PROOF_CLIENT_SECRET"    flag:"github-proof-client-secret"    desc:"Deployment-owned GitHub App OAuth client secret"`
	PrivateKeyFile  string `toml:"private_key_file" env:"GITHUB_PROOF_PRIVATE_KEY_FILE" flag:"github-proof-private-key-file" desc:"Path reference to the deployment-owned GitHub App private key"`
	CallbackURL     string `toml:"callback_url"     env:"GITHUB_PROOF_CALLBACK_URL"     flag:"github-proof-callback-url"     desc:"GitHub App user authorization callback URL"`
	InstallationURL string `toml:"installation_url" env:"GITHUB_PROOF_INSTALLATION_URL" flag:"github-proof-installation-url" desc:"GitHub-hosted App installation URL"`
	WebhookSecret   string `toml:"webhook_secret"   env:"GITHUB_PROOF_WEBHOOK_SECRET"   flag:"github-proof-webhook-secret"   desc:"GitHub App webhook secret for authenticated lifecycle and Proof intake"`
}

// ServerConfig contains backend HTTP server configuration.
type ServerConfig struct {
	Host              string          `toml:"host"                 env:"SERVER_HOST"                 flag:"server-host"                 desc:"Server host"`
	Port              int             `toml:"port"                 env:"SERVER_PORT"                 flag:"server-port"                 desc:"Server port"`
	ShutdownTimeout   config.Duration `toml:"shutdown_timeout"     env:"SERVER_SHUTDOWN_TIMEOUT"     flag:"server-shutdown-timeout"     desc:"Graceful shutdown timeout (e.g., 30s, 1m)"`
	DrainTimeout      config.Duration `toml:"drain_timeout"        env:"SERVER_DRAIN_TIMEOUT"        flag:"server-drain-timeout"        desc:"Readiness drain window before graceful shutdown starts (e.g., 5s)"`
	StaticCacheMaxAge uint            `toml:"static_cache_max_age" env:"SERVER_STATIC_CACHE_MAX_AGE" flag:"server-static-cache-max-age" desc:"Max-Age for static assets in seconds (0 = no cache, for dev)"`
	ProxyHeader       string          `toml:"proxy_header"         env:"SERVER_PROXY_HEADER"         flag:"server-proxy-header"         desc:"Trusted reverse proxy client IP header"`
	TrustedProxies    []string        `toml:"trusted_proxies"      env:"SERVER_TRUSTED_PROXIES"      flag:"server-trusted-proxies"      desc:"Comma-separated trusted reverse proxy IPs or CIDRs"`
}

// DatabaseConfig contains backend database connection configuration.
type DatabaseConfig = config.DatabaseConfig

// SessionConfig contains backend session storage configuration.
type SessionConfig struct {
	TableName    string `toml:"table_name"    env:"SESSION_TABLE_NAME"    flag:"session-table-name"    desc:"Session table name"`
	CookieSecure bool   `toml:"cookie_secure" env:"SESSION_COOKIE_SECURE" flag:"session-cookie-secure" desc:"Set Secure flag on session cookies"`
}

// SecurityConfig contains backend security-related configuration.
type SecurityConfig struct {
	AllowPlaintextConnectors    bool   `toml:"allow_plaintext_connectors" env:"ALLOW_PLAINTEXT_CONNECTORS" flag:"allow-plaintext-connectors" desc:"Permit plaintext connector transport for local development only"`
	Argon2Memory                uint32 `toml:"argon2_memory"                    env:"ARGON2_MEMORY"                    flag:"argon2-memory"                    desc:"Argon2 memory (KB)"`
	Argon2Iterations            uint32 `toml:"argon2_iterations"                env:"ARGON2_ITERATIONS"                flag:"argon2-iterations"                desc:"Argon2 iterations"`
	Argon2Parallelism           uint8  `toml:"argon2_parallelism"               env:"ARGON2_PARALLELISM"               flag:"argon2-parallelism"               desc:"Argon2 parallelism"`
	Argon2SaltLength            uint32 `toml:"argon2_salt_length"               env:"ARGON2_SALT_LENGTH"               flag:"argon2-salt-length"               desc:"Argon2 salt length"`
	Argon2KeyLength             uint32 `toml:"argon2_key_length"                env:"ARGON2_KEY_LENGTH"                flag:"argon2-key-length"                desc:"Argon2 key length"`
	EncryptionKeyHex            string `toml:"encryption_key_hex"               env:"ENCRYPTION_KEY_HEX"               flag:"encryption-key-hex"               desc:"AES-256 encryption key in hex format (32 bytes = 64 hex chars)"`
	CredentialCurrentKeyID      string `toml:"credential_current_key_id" env:"CREDENTIAL_CURRENT_KEY_ID" flag:"credential-current-key-id" desc:"Current product-credential encryption key ID"`
	CredentialKeysHex           string `toml:"credential_keys_hex" env:"CREDENTIAL_KEYS_HEX" flag:"credential-keys-hex" desc:"Protected JSON object of credential key IDs to 32-byte hex keys"`
	CredentialDisableLegacy     bool   `toml:"credential_disable_legacy" env:"CREDENTIAL_DISABLE_LEGACY" flag:"credential-disable-legacy" desc:"Reject unversioned product-credential ciphertext after migration"`
	ExecutionRequestKeyHex      string `toml:"execution_request_key_hex"         env:"EXECUTION_REQUEST_KEY_HEX"         flag:"execution-request-key-hex"         desc:"Dedicated AES-256 key for short-lived execution request/result envelopes"`
	AllowPrivateDataSourceHosts bool   `toml:"allow_private_data_source_hosts" env:"ALLOW_PRIVATE_DATA_SOURCE_HOSTS" flag:"allow-private-data-source-hosts" desc:"Allow customer data source payloads to opt into private or loopback hosts"`
}

// AuthConfig contains backend authentication configuration.
type AuthConfig struct {
	GoogleClientID                 string `toml:"google_client_id"                  env:"GOOGLE_CLIENT_ID"                  flag:"google-client-id"                  desc:"Google OAuth client ID"`
	GoogleClientSecret             string `toml:"google_client_secret"              env:"GOOGLE_CLIENT_SECRET"              flag:"google-client-secret"              desc:"Google OAuth client secret"`
	GoogleRedirectURI              string `toml:"google_redirect_uri"               env:"GOOGLE_REDIRECT_URI"               flag:"google-redirect-uri"               desc:"Google OAuth redirect URI"`
	SelfServiceRegistrationEnabled bool   `toml:"self_service_registration_enabled" env:"SELF_SERVICE_REGISTRATION_ENABLED" flag:"self-service-registration-enabled" desc:"Enable public email/password signup and first-time OIDC user auto-creation"`
}

// AbuseControlsConfig defines shared PostgreSQL-backed request budgets. These
// values apply across API replicas and survive process restarts.
type AbuseControlsConfig struct {
	AuthAttempts            int             `toml:"auth_attempts"              env:"ABUSE_CONTROLS_AUTH_ATTEMPTS"              flag:"abuse-controls-auth-attempts"              desc:"Maximum authentication attempts per client and stable subject in one window"`
	AuthWindow              config.Duration `toml:"auth_window"                env:"ABUSE_CONTROLS_AUTH_WINDOW"                flag:"abuse-controls-auth-window"                desc:"Shared authentication throttle window (e.g., 1m)"`
	ExpensiveClientAttempts int             `toml:"expensive_client_attempts"  env:"ABUSE_CONTROLS_EXPENSIVE_CLIENT_ATTEMPTS"  flag:"abuse-controls-expensive-client-attempts"  desc:"Maximum expensive operations per client in one window"`
	ExpensiveTenantAttempts int             `toml:"expensive_tenant_attempts"  env:"ABUSE_CONTROLS_EXPENSIVE_TENANT_ATTEMPTS"  flag:"abuse-controls-expensive-tenant-attempts"  desc:"Maximum expensive operations per tenant in one window"`
	CredentialAttempts      int             `toml:"credential_attempts"        env:"ABUSE_CONTROLS_CREDENTIAL_ATTEMPTS"        flag:"abuse-controls-credential-attempts"        desc:"Maximum credential mutations per client and tenant in one window"`
	ExpensiveWindow         config.Duration `toml:"expensive_window"           env:"ABUSE_CONTROLS_EXPENSIVE_WINDOW"           flag:"abuse-controls-expensive-window"           desc:"Shared expensive-operation throttle window (e.g., 1m)"`
}

// HTTPOAuthConfig pins the callback independently of customer app endpoints.
type HTTPOAuthConfig struct {
	OAuthRedirectURI string `toml:"oauth_redirect_uri" env:"HTTP_OAUTH_REDIRECT_URI" flag:"http-oauth-redirect-uri" desc:"HTTPS callback URI for customer HTTP OAuth apps"`
}

func (c HTTPOAuthConfig) validate(profile string) error {
	if c.OAuthRedirectURI == "" {
		return nil
	}
	u, err := url.Parse(c.OAuthRedirectURI)
	if err != nil || u.Scheme != httpsScheme || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/provider-connectors/http/callback" {
		return xerror.Unexpected().Detail("http_oauth.oauth_redirect_uri must be an HTTPS URL ending in /provider-connectors/http/callback without user info, query, or fragment")
	}
	if config.IsNonLocalDeploymentProfile(profile) {
		_, err = config.ValidatePublicHTTPSURL(c.OAuthRedirectURI, "http_oauth.oauth_redirect_uri")
	}
	return err
}

// HubSpotConfig contains backend runtime overrides for the HubSpot connector.
type HubSpotConfig struct {
	BaseURL           string `toml:"base_url"            env:"HUBSPOT_BASE_URL"            flag:"hubspot-base-url"            desc:"Override the HubSpot API base URL for local development or mock testing"`
	AllowPrivateHosts bool   `toml:"allow_private_hosts" env:"HUBSPOT_ALLOW_PRIVATE_HOSTS" flag:"hubspot-allow-private-hosts" desc:"Allow the HubSpot connector to call private hosts; intended for local mock testing"`
	OAuthClientID     string `toml:"oauth_client_id"     env:"HUBSPOT_OAUTH_CLIENT_ID"     flag:"hubspot-oauth-client-id"     desc:"HubSpot developer app OAuth client ID"`
	OAuthClientSecret string `toml:"oauth_client_secret" env:"HUBSPOT_OAUTH_CLIENT_SECRET" flag:"hubspot-oauth-client-secret" desc:"HubSpot developer app OAuth client secret"`
	OAuthRedirectURI  string `toml:"oauth_redirect_uri"  env:"HUBSPOT_OAUTH_REDIRECT_URI"  flag:"hubspot-oauth-redirect-uri"  desc:"HubSpot developer app OAuth callback URI"`
	OAuthBaseURL      string `toml:"oauth_base_url"      env:"HUBSPOT_OAUTH_BASE_URL"      flag:"hubspot-oauth-base-url"      desc:"Override the HubSpot OAuth base URL for local mock testing"`
}

// SlackConfig contains deployment-owned settings for Slack notification installation.
type SlackConfig struct {
	OAuthClientID     string `toml:"oauth_client_id" env:"SLACK_OAUTH_CLIENT_ID" flag:"slack-oauth-client-id" desc:"Slack app OAuth client ID"`
	OAuthClientSecret string `toml:"oauth_client_secret" env:"SLACK_OAUTH_CLIENT_SECRET" flag:"slack-oauth-client-secret" desc:"Slack app OAuth client secret"`
	OAuthRedirectURI  string `toml:"oauth_redirect_uri" env:"SLACK_OAUTH_REDIRECT_URI" flag:"slack-oauth-redirect-uri" desc:"Slack app OAuth callback URI"`
}

// GitLabConfig contains deployment-owned GitLab OAuth application settings.
type GitLabConfig struct {
	OAuthClientID     string `toml:"oauth_client_id"     env:"GITLAB_OAUTH_CLIENT_ID"     flag:"gitlab-oauth-client-id"     desc:"GitLab OAuth application ID"`
	OAuthClientSecret string `toml:"oauth_client_secret" env:"GITLAB_OAUTH_CLIENT_SECRET" flag:"gitlab-oauth-client-secret" desc:"GitLab OAuth application secret"`
	OAuthRedirectURI  string `toml:"oauth_redirect_uri"  env:"GITLAB_OAUTH_REDIRECT_URI"  flag:"gitlab-oauth-redirect-uri"  desc:"GitLab OAuth callback URI"`
	OAuthBaseURL      string `toml:"oauth_base_url"      env:"GITLAB_OAUTH_BASE_URL"      flag:"gitlab-oauth-base-url"      desc:"GitLab OAuth instance base URL; defaults to GitLab.com"`
}

// GoogleWorkspaceConfig contains deployment-owned Workspace OAuth and Picker settings.
// OAuth settings are independent of BigQuery because callbacks and consent scopes differ.
type GoogleWorkspaceConfig struct {
	OAuthClientID     string `toml:"oauth_client_id" env:"GOOGLE_WORKSPACE_OAUTH_CLIENT_ID" flag:"google-workspace-oauth-client-id" desc:"Deployment-owned Google OAuth client ID for Workspace"`
	OAuthClientSecret string `toml:"oauth_client_secret" env:"GOOGLE_WORKSPACE_OAUTH_CLIENT_SECRET" flag:"google-workspace-oauth-client-secret" desc:"Deployment-owned Google OAuth client secret for Workspace"`
	OAuthRedirectURI  string `toml:"oauth_redirect_uri" env:"GOOGLE_WORKSPACE_OAUTH_REDIRECT_URI" flag:"google-workspace-oauth-redirect-uri" desc:"Google OAuth callback URI for Workspace"`
	PickerAPIKey      string `toml:"picker_api_key" env:"GOOGLE_WORKSPACE_PICKER_API_KEY" flag:"google-workspace-picker-api-key" desc:"Public browser key restricted to Google Picker/Drive APIs and permitted HTTP referrers"`
	ProjectNumber     string `toml:"project_number" env:"GOOGLE_WORKSPACE_PROJECT_NUMBER" flag:"google-workspace-project-number" desc:"Google Cloud project number containing the Workspace OAuth client and Picker key"`
}

// BigQueryConfig contains deployment-owned OAuth and runtime endpoint configuration.
type BigQueryConfig struct {
	OAuthClientID     string `toml:"oauth_client_id" env:"BIGQUERY_OAUTH_CLIENT_ID" flag:"bigquery-oauth-client-id" desc:"Deployment-owned Google OAuth client ID for BigQuery connector authorization"`
	OAuthClientSecret string `toml:"oauth_client_secret" env:"BIGQUERY_OAUTH_CLIENT_SECRET" flag:"bigquery-oauth-client-secret" desc:"Deployment-owned Google OAuth client secret for BigQuery connector authorization"`
	OAuthRedirectURI  string `toml:"oauth_redirect_uri" env:"BIGQUERY_OAUTH_REDIRECT_URI" flag:"bigquery-oauth-redirect-uri" desc:"Google OAuth callback URI for BigQuery connector authorization"`
	BaseURL           string `toml:"base_url"            env:"BIGQUERY_BASE_URL"            flag:"bigquery-base-url"            desc:"Override the BigQuery REST API base URL for private endpoint routing or local verification"`
	TokenURL          string `toml:"token_url"           env:"BIGQUERY_TOKEN_URL"           flag:"bigquery-token-url"           desc:"Override the BigQuery service-account token URL for private endpoint routing or local verification"`
	AllowPrivateHosts bool   `toml:"allow_private_hosts" env:"BIGQUERY_ALLOW_PRIVATE_HOSTS" flag:"bigquery-allow-private-hosts" desc:"Allow the BigQuery connector endpoint overrides to call private hosts; intended for trusted on-prem routing or local verification"`
}

// SnowflakeConfig contains backend runtime identity inputs for the Snowflake connector.
type SnowflakeConfig struct {
	WIFIssuer     string `toml:"wif_issuer" env:"SNOWFLAKE_WIF_ISSUER" flag:"snowflake-wif-issuer" desc:"HTTPS issuer enabling database-managed customer Snowflake workload identities"`
	WIFSocketPath string `toml:"wif_socket_path" env:"SNOWFLAKE_WIF_SOCKET_PATH" flag:"snowflake-wif-socket-path" desc:"Absolute path to the SPIRE Workload API socket for customer Snowflake identities"`
}

const (
	ExecutionBackendRiver  = "river"
	ExecutionBackendPoller = "poller"
)

// ExecutionConfig selects the deployment-wide execution placement.
type ExecutionConfig struct {
	Backend                   string          `toml:"backend"         env:"EXECUTION_BACKEND"         flag:"execution-backend"         desc:"Execution backend: river or poller"`
	RequestTimeout            config.Duration `toml:"request_timeout" env:"EXECUTION_REQUEST_TIMEOUT" flag:"execution-request-timeout" desc:"Maximum time to wait for a persisted execution request"`
	CompletionTriggersEnabled bool            `toml:"completion_triggers_enabled" env:"EXECUTION_COMPLETION_TRIGGERS_ENABLED" flag:"execution-completion-triggers-enabled" desc:"Enable completion trigger resources and dispatch deployment-wide"`
}

// ExecutionLimitsConfig contains backend runtime guardrails for user-triggered execution.
type ExecutionLimitsConfig struct {
	QueryTimeout                      config.Duration `toml:"query_timeout"                         env:"EXECUTION_LIMITS_QUERY_TIMEOUT"                         flag:"execution-limits-query-timeout"                         desc:"Maximum saved or inline query execution duration (e.g., 30s, 1m)"`
	ConnectorTimeout                  config.Duration `toml:"connector_timeout"                     env:"EXECUTION_LIMITS_CONNECTOR_TIMEOUT"                     flag:"execution-limits-connector-timeout"                     desc:"Maximum connector operation duration (e.g., 30s, 1m)"`
	MaxResultRows                     int             `toml:"max_result_rows"                       env:"EXECUTION_LIMITS_MAX_RESULT_ROWS"                       flag:"execution-limits-max-result-rows"                       desc:"Maximum rows/documents returned by a query execution"`
	MaxFileBytes                      int64           `toml:"max_file_bytes" env:"EXECUTION_LIMITS_MAX_FILE_BYTES" flag:"execution-limits-max-file-bytes" desc:"Maximum bytes per native file"`
	MaxResponseBytes                  int64           `toml:"max_response_bytes"                    env:"EXECUTION_LIMITS_MAX_RESPONSE_BYTES"                    flag:"execution-limits-max-response-bytes"                    desc:"Maximum response payload size in bytes for query execution"`
	ConnectorConcurrencyPerDataSource int             `toml:"connector_concurrency_per_data_source" env:"EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_PER_DATA_SOURCE" flag:"execution-limits-connector-concurrency-per-data-source" desc:"Deployment-wide maximum concurrent connector executions per organization/data source"`
	ConnectorConcurrencyQueueTimeout  config.Duration `toml:"connector_concurrency_queue_timeout"  env:"EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_QUEUE_TIMEOUT"  flag:"execution-limits-connector-concurrency-queue-timeout"  desc:"Maximum time a connector execution may wait for its organization/data-source slot"`
	ConnectorConcurrencyMaxQueued     int             `toml:"connector_concurrency_max_queued_per_data_source" env:"EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_MAX_QUEUED_PER_DATA_SOURCE" flag:"execution-limits-connector-concurrency-max-queued-per-data-source" desc:"Maximum queued connector executions per organization/data source"`
	HTTPTimeout                       config.Duration `toml:"http_timeout"                          env:"EXECUTION_LIMITS_HTTP_TIMEOUT"                          flag:"execution-limits-http-timeout"                          desc:"Maximum outbound HTTP connector request duration (e.g., 10s, 1m)"`
	HTTPMaxRedirects                  int             `toml:"http_max_redirects"                    env:"EXECUTION_LIMITS_HTTP_MAX_REDIRECTS"                    flag:"execution-limits-http-max-redirects"                    desc:"Maximum redirects followed by HTTP connector execution"`
	HTTPMaxRequestBytes               int64           `toml:"http_max_request_bytes"                env:"EXECUTION_LIMITS_HTTP_MAX_REQUEST_BYTES"                flag:"execution-limits-http-max-request-bytes"                desc:"Maximum outbound HTTP connector request body size in bytes"`
	HTTPMaxResponseBytes              int64           `toml:"http_max_response_bytes"               env:"EXECUTION_LIMITS_HTTP_MAX_RESPONSE_BYTES"               flag:"execution-limits-http-max-response-bytes"               desc:"Maximum outbound HTTP connector response body size in bytes"`
	HTTPRetryAttempts                 int             `toml:"http_retry_attempts"                   env:"EXECUTION_LIMITS_HTTP_RETRY_ATTEMPTS"                   flag:"execution-limits-http-retry-attempts"                   desc:"Maximum HTTP connector attempts, including the first attempt"`
	HTTPRetryBackoff                  config.Duration `toml:"http_retry_backoff"                    env:"EXECUTION_LIMITS_HTTP_RETRY_BACKOFF"                    flag:"execution-limits-http-retry-backoff"                    desc:"Backoff between HTTP connector retry attempts (e.g., 250ms)"`
	TransformTimeout                  config.Duration `toml:"transform_timeout"                     env:"EXECUTION_LIMITS_TRANSFORM_TIMEOUT"                     flag:"execution-limits-transform-timeout"                     desc:"Maximum SQL transform execution duration (e.g., 10s, 30s)"`
	TransformMaxWork                  int64           `toml:"transform_max_work"                    env:"EXECUTION_LIMITS_TRANSFORM_MAX_WORK"                    flag:"execution-limits-transform-max-work"                    desc:"Maximum cumulative SQL transform work units, including join candidate pairs"`
	TransformMaxIntermediateRows      int             `toml:"transform_max_intermediate_rows"       env:"EXECUTION_LIMITS_TRANSFORM_MAX_INTERMEDIATE_ROWS"       flag:"execution-limits-transform-max-intermediate-rows"       desc:"Maximum cumulative rows materialized during one SQL transform"`
	TransformMaxIntermediateBytes     int64           `toml:"transform_max_intermediate_bytes"      env:"EXECUTION_LIMITS_TRANSFORM_MAX_INTERMEDIATE_BYTES"      flag:"execution-limits-transform-max-intermediate-bytes"      desc:"Maximum cumulative estimated bytes materialized during one SQL transform"`
	TransformMaxOutputRows            int             `toml:"transform_max_output_rows"             env:"EXECUTION_LIMITS_TRANSFORM_MAX_OUTPUT_ROWS"             flag:"execution-limits-transform-max-output-rows"             desc:"Maximum rows returned by one SQL transform"`
	TransformMaxOutputBytes           int64           `toml:"transform_max_output_bytes"            env:"EXECUTION_LIMITS_TRANSFORM_MAX_OUTPUT_BYTES"            flag:"execution-limits-transform-max-output-bytes"            desc:"Maximum serialized bytes returned by one SQL transform"`
	TransformMaxDepth                 int             `toml:"transform_max_depth"                   env:"EXECUTION_LIMITS_TRANSFORM_MAX_DEPTH"                   flag:"execution-limits-transform-max-depth"                   desc:"Maximum nested SQL transform relation and expression depth"`
}

// WorkerConfig contains per-process River queue concurrency limits.
type WorkerConfig struct {
	PipelineGitMaxWorkers            int `toml:"pipeline_git_max_workers"              env:"WORKER_PIPELINE_GIT_MAX_WORKERS"              flag:"worker-pipeline-git-max-workers"              desc:"Maximum concurrent pipeline Git jobs in this worker process"`
	EmailMaxWorkers                  int `toml:"email_max_workers"                     env:"WORKER_EMAIL_MAX_WORKERS"                     flag:"worker-email-max-workers"                     desc:"Maximum concurrent email jobs in this worker process"`
	NotificationDeliveryMaxWorkers   int `toml:"notification_delivery_max_workers"     env:"WORKER_NOTIFICATION_DELIVERY_MAX_WORKERS"     flag:"worker-notification-delivery-max-workers"     desc:"Maximum concurrent notification delivery jobs in this worker process"`
	EventsMaxWorkers                 int `toml:"events_max_workers"                    env:"WORKER_EVENTS_MAX_WORKERS"                    flag:"worker-events-max-workers"                    desc:"Maximum concurrent durable event jobs in this worker process"`
	EventMaintenanceMaxWorkers       int `toml:"event_maintenance_max_workers"         env:"WORKER_EVENT_MAINTENANCE_MAX_WORKERS"         flag:"worker-event-maintenance-max-workers"         desc:"Maximum concurrent event maintenance jobs in this worker process"`
	PipelineScheduleMaxWorkers       int `toml:"pipeline_schedule_max_workers"         env:"WORKER_PIPELINE_SCHEDULE_MAX_WORKERS"         flag:"worker-pipeline-schedule-max-workers"         desc:"Maximum concurrent pipeline schedule jobs in this worker process"`
	PipelineRetryMaxWorkers          int `toml:"pipeline_retry_max_workers"            env:"WORKER_PIPELINE_RETRY_MAX_WORKERS"            flag:"worker-pipeline-retry-max-workers"            desc:"Maximum concurrent pipeline retry jobs in this worker process"`
	PipelineRunMaxWorkers            int `toml:"pipeline_run_max_workers"              env:"WORKER_PIPELINE_RUN_MAX_WORKERS"              flag:"worker-pipeline-run-max-workers"              desc:"Maximum concurrent pipeline run jobs in this worker process"`
	DataSourceSchemaImportMaxWorkers int `toml:"data_source_schema_import_max_workers" env:"WORKER_DATA_SOURCE_SCHEMA_IMPORT_MAX_WORKERS" flag:"worker-data-source-schema-import-max-workers" desc:"Maximum concurrent data-source schema import jobs in this worker process"`
	ExecutionMaxWorkers              int `toml:"execution_max_workers"                 env:"WORKER_EXECUTION_MAX_WORKERS"                 flag:"worker-execution-max-workers"                 desc:"Maximum concurrent persisted execution jobs in this worker process"`
}

// CORSConfig contains backend CORS configuration.
type CORSConfig struct {
	AllowedOrigins []string `toml:"allowed_origins" env:"CORS_ALLOWED_ORIGINS" flag:"cors-allowed-origins" desc:"Comma-separated allowed CORS origins"`
}

// LoggingConfig contains backend logging configuration.
type LoggingConfig struct {
	Level            string          `toml:"level"             env:"LOG_LEVEL"             flag:"log-level"             desc:"Log level (none, debug, info, warn, error)"`
	File             string          `toml:"backend_file"      env:"LOG_BACKEND_FILE"      flag:"log-backend-file"      desc:"Backend log file path (empty disables file logging)"`
	RotationInterval config.Duration `toml:"rotation_interval" env:"LOG_ROTATION_INTERVAL" flag:"log-rotation-interval" desc:"Log file rotation interval (e.g., 1h, 24h)"`
	LogHTTPRequests  bool            `toml:"log_http_requests" env:"LOG_HTTP_REQUESTS"     flag:"log-http-requests"     desc:"Log full HTTP request/response bodies"`
	MaxFileSizeMB    int             `toml:"max_file_size_mb"  env:"LOG_MAX_FILE_SIZE_MB"  flag:"log-max-file-size-mb"  desc:"Maximum log file size in MB before rotation"`
	Environment      string          `toml:"environment"       env:"LOG_ENVIRONMENT"       flag:"log-environment"       desc:"Deployment environment for structured logs"`
	OTLPLogsEndpoint string          `toml:"otlp_logs_endpoint" env:"LOG_OTLP_LOGS_ENDPOINT" flag:"log-otlp-logs-endpoint" desc:"Full OTLP/HTTP logs endpoint (empty disables direct log export)"`
}

// Load reads and parses backend configuration.
func Load(path string) (*Config, error) {
	return load(path, func(path string, cfg *Config) error {
		return config.LoadInto(path, cfg)
	})
}

// LoadWithArgs reads backend configuration using an isolated set of shared config flags.
func LoadWithArgs(path string, args []string) (*Config, error) {
	return load(path, func(path string, cfg *Config) error {
		return config.LoadIntoWithArgs(path, cfg, args)
	})
}

// LoadExecutorWithArgs loads only the configuration required by the dedicated
// execution worker. It deliberately does not require application encryption,
// auth, SMTP, session, or control-plane configuration.
func LoadExecutorWithArgs(path string, args []string) (*Config, error) {
	return loadWithValidator(path, func(path string, cfg *Config) error {
		return config.LoadIntoWithArgs(path, cfg, args)
	}, (*Config).validateExecutor)
}

// load applies shared config sources, derived values, and backend validation.
func load(path string, loadInto func(string, *Config) error) (*Config, error) {
	return loadWithValidator(path, loadInto, (*Config).validate)
}

func loadWithValidator(
	path string,
	loadInto func(string, *Config) error,
	validator func(*Config) error,
) (*Config, error) {
	cfg := defaults()
	if err := loadInto(path, cfg); err != nil {
		return nil, err
	}

	cfg.applyDerivedURLs()

	if err := validator(cfg); err != nil {
		return nil, xerror.Wrapf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// ValidateExecutionRequestKey rejects missing, placeholder, malformed, or
// application-key-reused execution envelope keys.
func (c *Config) ValidateExecutionRequestKey() error {
	key := strings.TrimSpace(c.Security.ExecutionRequestKeyHex)
	if len(key) != 64 {
		return xerror.Unexpected().Detail("security.execution_request_key_hex must be a 64-character hex string")
	}
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		return xerror.Unexpected().Detail("security.execution_request_key_hex must be a 64-character hex string")
	}
	if isAllZeroHexKey(key) || strings.EqualFold(key, defaultEncryptionKeyHex) {
		return xerror.Unexpected().Detail("security.execution_request_key_hex must not use a placeholder key")
	}
	if applicationKey := strings.TrimSpace(c.Security.EncryptionKeyHex); applicationKey != "" && strings.EqualFold(key, applicationKey) {
		return xerror.Unexpected().Detail("security.execution_request_key_hex must differ from security.encryption_key_hex")
	}
	return nil
}

func (c *Config) validateExecutor() error {
	if err := c.Snowflake.validate(); err != nil {
		return err
	}
	if err := config.ValidateDeploymentProfile(&c.Deployment.Profile); err != nil {
		return err
	}
	if err := c.PrivatePostgres.Validate(c.Deployment.Profile); err != nil {
		return err
	}
	if err := c.validateConnectorTransport(); err != nil {
		return err
	}
	if err := config.ValidateRuntime(c.Logging.Level, c.Logging.Environment, &c.Tracing, &c.Metrics); err != nil {
		return err
	}
	if c.Server.ShutdownTimeout <= 0 || c.Server.DrainTimeout < 0 {
		return xerror.Unexpected().Detail("executor server timeouts are invalid")
	}
	if err := config.ValidateDeploymentPostgres(
		c.Deployment.Profile,
		c.Database.Host,
		c.Database.User,
		c.Database.Password,
		c.Database.SSLMode,
	); err != nil {
		return err
	}
	if c.Database.SSLMode != postgresSSLModeVerifyFull {
		return xerror.Unexpected().Detail("executor database.ssl_mode must be verify-full")
	}
	if strings.TrimSpace(c.Database.SSLRootCert) == "" {
		return xerror.Unexpected().Detail("executor database.ssl_root_cert is required")
	}
	if err := c.validateExecutionLimits(); err != nil {
		return err
	}
	if err := c.Execution.validate(); err != nil {
		return err
	}
	return c.ValidateExecutionRequestKey()
}

func (c *Config) validateExecutionLimits() error {
	checks := []struct {
		invalid     bool
		name        string
		requirement string
	}{
		{c.ExecutionLimits.QueryTimeout <= 0, "query_timeout", "greater than 0"},
		{c.ExecutionLimits.ConnectorTimeout <= 0, "connector_timeout", "greater than 0"},
		{c.ExecutionLimits.MaxResultRows <= 0, "max_result_rows", "greater than 0"},
		{c.ExecutionLimits.MaxFileBytes <= 0 || c.ExecutionLimits.MaxFileBytes > filestream.MaxRetainedBytes, "max_file_bytes", "positive and within retained storage bounds"},
		{c.ExecutionLimits.MaxResponseBytes <= 0, "max_response_bytes", "greater than 0"},
		{c.ExecutionLimits.ConnectorConcurrencyPerDataSource <= 0, "connector_concurrency_per_data_source", "greater than 0"},
		{c.ExecutionLimits.ConnectorConcurrencyQueueTimeout <= 0, "connector_concurrency_queue_timeout", "greater than 0"},
		{c.ExecutionLimits.ConnectorConcurrencyMaxQueued <= 0, "connector_concurrency_max_queued_per_data_source", "greater than 0"},
		{c.ExecutionLimits.HTTPTimeout <= 0, "http_timeout", "greater than 0"},
		{c.ExecutionLimits.HTTPMaxRedirects <= 0, "http_max_redirects", "greater than 0"},
		{c.ExecutionLimits.HTTPMaxRequestBytes <= 0, "http_max_request_bytes", "greater than 0"},
		{c.ExecutionLimits.HTTPMaxResponseBytes <= 0, "http_max_response_bytes", "greater than 0"},
		{c.ExecutionLimits.HTTPRetryAttempts <= 0, "http_retry_attempts", "greater than 0"},
		{c.ExecutionLimits.HTTPRetryBackoff < 0, "http_retry_backoff", "greater than or equal to 0"},
		{c.ExecutionLimits.TransformTimeout <= 0, "transform_timeout", "greater than 0"},
		{c.ExecutionLimits.TransformMaxWork <= 0, "transform_max_work", "greater than 0"},
		{c.ExecutionLimits.TransformMaxIntermediateRows <= 0, "transform_max_intermediate_rows", "greater than 0"},
		{c.ExecutionLimits.TransformMaxIntermediateBytes <= 0, "transform_max_intermediate_bytes", "greater than 0"},
		{c.ExecutionLimits.TransformMaxOutputRows <= 0, "transform_max_output_rows", "greater than 0"},
		{c.ExecutionLimits.TransformMaxOutputBytes <= 0, "transform_max_output_bytes", "greater than 0"},
		{c.ExecutionLimits.TransformMaxDepth <= 0, "transform_max_depth", "greater than 0"},
	}
	for _, check := range checks {
		if check.invalid {
			return xerror.Unexpected().Detailf("execution_limits.%s must be %s", check.name, check.requirement)
		}
	}
	return nil
}

func defaults() *Config {
	return &Config{
		Deployment: config.DeploymentConfig{
			Profile: config.DeploymentProfileLocal,
		},
		Server: ServerConfig{
			Host:            "0.0.0.0",
			Port:            3000,
			ShutdownTimeout: config.NewDuration(30 * time.Second),
		},
		Database: config.DefaultDatabaseConfig(),
		Session: SessionConfig{
			TableName:    "user_sessions",
			CookieSecure: true,
		},
		Security: SecurityConfig{
			Argon2Memory:                64 * 1024,
			Argon2Iterations:            3,
			Argon2Parallelism:           2,
			Argon2SaltLength:            16,
			Argon2KeyLength:             32,
			AllowPrivateDataSourceHosts: false,
		},
		Auth: AuthConfig{
			GoogleClientID:                 "",
			GoogleClientSecret:             "",
			GoogleRedirectURI:              defaultGoogleRedirectURI,
			SelfServiceRegistrationEnabled: false,
		},
		AbuseControls: AbuseControlsConfig{
			AuthAttempts:            5,
			AuthWindow:              config.NewDuration(time.Minute),
			ExpensiveClientAttempts: 20,
			ExpensiveTenantAttempts: 40,
			CredentialAttempts:      10,
			ExpensiveWindow:         config.NewDuration(time.Minute),
		},
		CORS: CORSConfig{
			AllowedOrigins: []string{"http://localhost:3000"},
		},
		SMTP: config.SMTP{
			Host:           "localhost:31025",
			User:           "",
			Password:       "",
			CACert:         "",
			CACertFile:     "",
			Insecure:       true,
			NoReplyAddress: defaultNoReplyAddress,
		},
		Logging: LoggingConfig{
			Level:            "debug",
			File:             "logs/backend.log",
			RotationInterval: config.NewDuration(1 * time.Hour),
			LogHTTPRequests:  true,
			MaxFileSizeMB:    100,
			Environment:      "local",
		},
		Tracing: config.TracingConfig{
			SamplingRatio: 1,
		},
		Metrics: config.MetricsConfig{
			Path: "/metrics",
		},
		Execution: ExecutionConfig{
			CompletionTriggersEnabled: false,
			Backend:                   ExecutionBackendRiver,
			RequestTimeout:            config.NewDuration(30 * time.Second),
		},
		ExecutionLimits: ExecutionLimitsConfig{
			QueryTimeout:                      config.NewDuration(30 * time.Second),
			ConnectorTimeout:                  config.NewDuration(30 * time.Second),
			MaxResultRows:                     1000,
			MaxFileBytes:                      128 * 1024 * 1024,
			MaxResponseBytes:                  10 * 1024 * 1024,
			ConnectorConcurrencyPerDataSource: 4,
			ConnectorConcurrencyQueueTimeout:  config.NewDuration(5 * time.Second),
			ConnectorConcurrencyMaxQueued:     16,
			HTTPTimeout:                       config.NewDuration(10 * time.Second),
			HTTPMaxRedirects:                  3,
			HTTPMaxRequestBytes:               1024 * 1024,
			HTTPMaxResponseBytes:              10 * 1024 * 1024,
			HTTPRetryAttempts:                 1,
			HTTPRetryBackoff:                  config.NewDuration(250 * time.Millisecond),
			TransformTimeout:                  config.NewDuration(10 * time.Second),
			TransformMaxWork:                  1_000_000,
			TransformMaxIntermediateRows:      10_000,
			TransformMaxIntermediateBytes:     32 * 1024 * 1024,
			TransformMaxOutputRows:            1_000,
			TransformMaxOutputBytes:           10 * 1024 * 1024,
			TransformMaxDepth:                 16,
		},
		Worker: WorkerConfig{
			PipelineGitMaxWorkers:            1,
			EmailMaxWorkers:                  1,
			NotificationDeliveryMaxWorkers:   1,
			EventsMaxWorkers:                 1,
			EventMaintenanceMaxWorkers:       1,
			PipelineScheduleMaxWorkers:       4,
			PipelineRetryMaxWorkers:          4,
			PipelineRunMaxWorkers:            4,
			DataSourceSchemaImportMaxWorkers: 2,
			ExecutionMaxWorkers:              4,
		},
		FrontendURL:     defaultFrontendURL,
		DefaultLanguage: "en",
	}
}

func (cfg *Config) applyDerivedURLs() {
	if cfg.Auth.GoogleRedirectURI == defaultGoogleRedirectURI && cfg.FrontendURL != "" {
		cfg.Auth.GoogleRedirectURI = strings.TrimRight(
			cfg.FrontendURL,
			"/",
		) + googleRedirectCallbackURL
	}
}

// Address returns the server address string.
func (s *ServerConfig) Address() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

func (c *Config) validate() error {
	if err := c.SMTP.ValidateCredentials(); err != nil {
		return err
	}
	if err := config.ValidateDeploymentProfile(&c.Deployment.Profile); err != nil {
		return err
	}
	if err := c.PrivatePostgres.Validate(c.Deployment.Profile); err != nil {
		return err
	}
	if err := c.validateConnectorTransport(); err != nil {
		return err
	}
	if err := config.ValidateRuntime(
		c.Logging.Level,
		c.Logging.Environment,
		&c.Tracing,
		&c.Metrics,
	); err != nil {
		return err
	}
	if err := config.ValidateProxyConfig(
		"server",
		&c.Server.ProxyHeader,
		c.Server.TrustedProxies,
		func(i int, value string) {
			c.Server.TrustedProxies[i] = value
		},
	); err != nil {
		return err
	}
	if c.Server.ShutdownTimeout <= 0 {
		return xerror.Unexpected().Detail("server.shutdown_timeout must be greater than 0")
	}
	if c.Server.DrainTimeout < 0 {
		return xerror.Unexpected().Detail("server.drain_timeout must be greater than or equal to 0")
	}

	if len(c.CORS.AllowedOrigins) == 0 {
		return xerror.Unexpected().Detail("at least one CORS origin is required")
	}

	for i, origin := range c.CORS.AllowedOrigins {
		origin = strings.TrimSpace(origin)
		c.CORS.AllowedOrigins[i] = origin
		if origin == "" {
			return xerror.Unexpected().Detail("CORS origin cannot be empty")
		}
		parsedURL, err := config.ValidateHTTPOrigin(origin, "CORS origin")
		if err != nil {
			return xerror.Unexpected().Detailf("invalid CORS origin %q: %w", origin, err)
		}
		if parsedURL.Scheme != httpScheme && parsedURL.Scheme != httpsScheme {
			return xerror.Unexpected().Detailf("CORS origin %q must include a scheme", origin)
		}
	}

	if err := c.validateExecutionLimits(); err != nil {
		return err
	}
	if err := c.Execution.validate(); err != nil {
		return err
	}
	if err := c.AbuseControls.validate(); err != nil {
		return err
	}
	if (strings.TrimSpace(c.Auth.GoogleClientID) == "") != (strings.TrimSpace(c.Auth.GoogleClientSecret) == "") {
		return xerror.ValidationFailed().Detail("auth Google client ID and secret must be configured together")
	}
	if err := c.Worker.validate(); err != nil {
		return err
	}

	if err := validateOptionalHTTPURL(c.HubSpot.BaseURL, "hubspot.base_url"); err != nil {
		return err
	}
	if err := c.HTTPOAuth.validate(c.Deployment.Profile); err != nil {
		return err
	}
	if err := validateHubSpotOAuth(c.HubSpot, c.Deployment.Profile); err != nil {
		return err
	}
	if err := validateSlackOAuth(c.Slack, c.Deployment.Profile); err != nil {
		return err
	}
	if err := validateGitLabOAuth(c.GitLab, c.Deployment.Profile); err != nil {
		return err
	}
	if c.ProofObservations.Enabled {
		c.ProofObservations.EvidenceKeyHex = strings.TrimSpace(c.ProofObservations.EvidenceKeyHex)
		key, err := hex.DecodeString(c.ProofObservations.EvidenceKeyHex)
		clear(key)
		if err != nil || len(key) != 32 || isAllZeroHexKey(c.ProofObservations.EvidenceKeyHex) || strings.EqualFold(c.ProofObservations.EvidenceKeyHex, defaultEncryptionKeyHex) || c.Security.credentialKeyMatches(c.ProofObservations.EvidenceKeyHex) || strings.EqualFold(c.ProofObservations.EvidenceKeyHex, strings.TrimSpace(c.Security.ExecutionRequestKeyHex)) {
			return xerror.ValidationFailed().Detail("live Proof observations require an independent non-default evidence key")
		}
	}
	if err := validateGitHubProof(c.GitHubProof, c.Deployment.Profile); err != nil {
		return err
	}
	if err := validateGoogleWorkspaceOAuth(&c.GoogleWorkspace, c.Deployment.Profile); err != nil {
		return err
	}
	if err := validateBigQueryOAuth(c.BigQuery, c.Deployment.Profile); err != nil {
		return err
	}
	if err := validateOptionalHTTPURL(c.BigQuery.BaseURL, "bigquery.base_url"); err != nil {
		return err
	}
	if err := validateOptionalHTTPURL(c.BigQuery.TokenURL, "bigquery.token_url"); err != nil {
		return err
	}
	if err := c.Snowflake.validate(); err != nil {
		return err
	}

	const encryptionKeyErrMsg = "security.encryption_key_hex must be a 64-character hex string (32 bytes for AES-256)"
	key := strings.TrimSpace(c.Security.EncryptionKeyHex)
	if key == "" {
		return xerror.Unexpected().Detail(encryptionKeyErrMsg)
	}
	if len(key) != 64 {
		return xerror.Unexpected().Detail(encryptionKeyErrMsg)
	}
	if _, err := hex.DecodeString(key); err != nil {
		return xerror.Unexpected().Detail(encryptionKeyErrMsg)
	}
	if isAllZeroHexKey(key) {
		return xerror.Unexpected().Detail(
			"security.encryption_key_hex must not use an all-zero placeholder key",
		)
	}
	if strings.EqualFold(key, defaultEncryptionKeyHex) {
		return xerror.Unexpected().Detail(
			"security.encryption_key_hex must not use the repository default key",
		)
	}
	if _, err := c.Security.CredentialKeyring(); err != nil {
		return err
	}

	if config.IsNonLocalDeploymentProfile(c.Deployment.Profile) {
		if err := c.validateNonLocalDeployment(); err != nil {
			return err
		}
	}

	return nil
}

func validateGitHubProof(github GitHubProofConfig, profile string) error {
	if github == (GitHubProofConfig{}) {
		return nil
	}

	if github.AppID <= 0 {
		return xerror.Unexpected().Detail("github_proof.app_id must be positive")
	}

	values := []bool{
		strings.TrimSpace(github.ClientID) != "",
		strings.TrimSpace(github.ClientSecret) != "",
		strings.TrimSpace(github.PrivateKeyFile) != "",
		strings.TrimSpace(github.CallbackURL) != "",
		strings.TrimSpace(github.InstallationURL) != "",
		strings.TrimSpace(github.WebhookSecret) != "",
	}
	for _, configured := range values {
		if !configured {
			return xerror.Unexpected().Detail("managed github_proof App configuration must be complete")
		}
	}
	if len(strings.TrimSpace(github.WebhookSecret)) < 32 {
		return xerror.Unexpected().Detail("github_proof.webhook_secret must be at least 32 characters")
	}
	if profile == config.DeploymentProfileLocal {
		if err := validateOptionalHTTPURL(github.CallbackURL, "github_proof.callback_url"); err != nil {
			return err
		}
		return validateOptionalHTTPURL(github.InstallationURL, "github_proof.installation_url")
	}
	if _, err := config.ValidatePublicHTTPSURL(github.CallbackURL, "github_proof.callback_url"); err != nil {
		return err
	}
	_, err := config.ValidatePublicHTTPSURL(github.InstallationURL, "github_proof.installation_url")
	return err
}

func (c AbuseControlsConfig) validate() error {
	if c.AuthAttempts <= 0 {
		return xerror.Unexpected().Detail("abuse_controls.auth_attempts must be greater than 0")
	}
	if c.AuthWindow <= 0 {
		return xerror.Unexpected().Detail("abuse_controls.auth_window must be greater than 0")
	}
	if c.ExpensiveClientAttempts <= 0 {
		return xerror.Unexpected().Detail("abuse_controls.expensive_client_attempts must be greater than 0")
	}
	if c.ExpensiveTenantAttempts <= 0 {
		return xerror.Unexpected().Detail("abuse_controls.expensive_tenant_attempts must be greater than 0")
	}
	if c.CredentialAttempts <= 0 {
		return xerror.Unexpected().Detail("abuse_controls.credential_attempts must be greater than 0")
	}
	if c.ExpensiveWindow <= 0 {
		return xerror.Unexpected().Detail("abuse_controls.expensive_window must be greater than 0")
	}
	return nil
}

// validateHubSpotOAuth keeps incomplete developer-app configuration from starting a misleading runtime.
func validateHubSpotOAuth(hubSpot HubSpotConfig, profile string) error {
	values := []string{strings.TrimSpace(hubSpot.OAuthClientID), strings.TrimSpace(hubSpot.OAuthClientSecret), strings.TrimSpace(hubSpot.OAuthRedirectURI)}
	configured := 0
	for _, value := range values {
		if value != "" {
			configured++
		}
	}
	if configured != 0 && configured != len(values) {
		return xerror.Unexpected().Detail("hubspot OAuth client ID, client secret, and redirect URI must be configured together")
	}
	if strings.TrimSpace(hubSpot.OAuthBaseURL) != "" && config.IsNonLocalDeploymentProfile(profile) {
		return xerror.Unexpected().Detail("hubspot.oauth_base_url is only supported in the local deployment profile")
	}
	if err := validateOptionalHTTPURL(hubSpot.OAuthBaseURL, "hubspot.oauth_base_url"); err != nil {
		return err
	}
	if configured == 0 {
		return nil
	}
	redirectURL, err := url.Parse(strings.TrimSpace(hubSpot.OAuthRedirectURI))
	if err != nil || (redirectURL.Scheme != httpScheme && redirectURL.Scheme != httpsScheme) || redirectURL.Host == "" {
		return xerror.Unexpected().Detail("hubspot.oauth_redirect_uri must be an absolute HTTP URL")
	}
	if redirectURL.Path != hubSpotRedirectCallbackURL {
		return xerror.Unexpected().Detailf("hubspot.oauth_redirect_uri must end with %s", hubSpotRedirectCallbackURL)
	}
	if config.IsNonLocalDeploymentProfile(profile) {
		if _, err := config.ValidatePublicHTTPSURL(hubSpot.OAuthRedirectURI, "hubspot.oauth_redirect_uri"); err != nil {
			return err
		}
	}
	return nil
}

func validateGoogleWorkspaceOAuth(workspace *GoogleWorkspaceConfig, profile string) error {
	workspace.PickerAPIKey = strings.TrimSpace(workspace.PickerAPIKey)
	workspace.ProjectNumber = strings.TrimSpace(workspace.ProjectNumber)
	if err := validateGoogleConnectorOAuth("google_workspace", workspace.OAuthClientID, workspace.OAuthClientSecret, workspace.OAuthRedirectURI, profile); err != nil {
		return err
	}
	if (workspace.PickerAPIKey == "") != (workspace.ProjectNumber == "") {
		return xerror.Unexpected().Detail("google_workspace Picker API key and project number must be configured together")
	}
	if workspace.ProjectNumber != "" {
		for _, digit := range workspace.ProjectNumber {
			if digit < '0' || digit > '9' {
				return xerror.Unexpected().Detail("google_workspace project number must contain only digits")
			}
		}
	}
	if strings.TrimSpace(workspace.OAuthClientID) != "" && workspace.PickerAPIKey == "" {
		return xerror.Unexpected().Detail("google_workspace requires a restricted Picker API key and project number when OAuth is configured")
	}
	return nil
}

func validateBigQueryOAuth(bigQuery BigQueryConfig, profile string) error {
	return validateGoogleConnectorOAuth("bigquery", bigQuery.OAuthClientID, bigQuery.OAuthClientSecret, bigQuery.OAuthRedirectURI, profile)
}

func validateGoogleConnectorOAuth(provider, clientID, clientSecret, redirectURI, profile string) error {
	id, secret, redirect := strings.TrimSpace(clientID), strings.TrimSpace(clientSecret), strings.TrimSpace(redirectURI)
	if id == "" && secret == "" && redirect == "" {
		return nil
	}
	if id == "" || secret == "" || redirect == "" {
		return xerror.Unexpected().Detailf("%s OAuth client ID, client secret, and redirect URI must be configured together", provider)
	}
	u, err := url.Parse(redirect)
	if err != nil || (u.Scheme != httpScheme && u.Scheme != httpsScheme) || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return xerror.Unexpected().Detailf("%s.oauth_redirect_uri must be an absolute HTTP URL without user info, query, or fragment", provider)
	}
	if u.Path != "/provider-connectors/"+provider+"/callback" {
		return xerror.Unexpected().Detailf("%s.oauth_redirect_uri must end with /provider-connectors/%s/callback", provider, provider)
	}
	if config.IsNonLocalDeploymentProfile(profile) {
		_, err := config.ValidatePublicHTTPSURL(redirect, provider+".oauth_redirect_uri")
		return err
	}
	return nil
}

func validateGitLabOAuth(gitLab GitLabConfig, profile string) error {
	values := []string{strings.TrimSpace(gitLab.OAuthClientID), strings.TrimSpace(gitLab.OAuthClientSecret), strings.TrimSpace(gitLab.OAuthRedirectURI)}
	configured := 0
	for _, value := range values {
		if value != "" {
			configured++
		}
	}
	if configured != 0 && configured != len(values) {
		return xerror.Unexpected().Detail("gitlab OAuth client ID, client secret, and redirect URI must be configured together")
	}
	if err := validateOptionalHTTPURL(gitLab.OAuthBaseURL, "gitlab.oauth_base_url"); err != nil {
		return err
	}
	if configured == 0 {
		return nil
	}
	redirectURL, err := url.Parse(strings.TrimSpace(gitLab.OAuthRedirectURI))
	if err != nil || (redirectURL.Scheme != httpScheme && redirectURL.Scheme != httpsScheme) || redirectURL.Host == "" {
		return xerror.Unexpected().Detail("gitlab.oauth_redirect_uri must be an absolute HTTP URL")
	}
	if redirectURL.Path != gitLabRedirectCallbackURL {
		return xerror.Unexpected().Detailf("gitlab.oauth_redirect_uri must end with %s", gitLabRedirectCallbackURL)
	}
	if config.IsNonLocalDeploymentProfile(profile) {
		if _, err := config.ValidatePublicHTTPSURL(gitLab.OAuthRedirectURI, "gitlab.oauth_redirect_uri"); err != nil {
			return err
		}
		if strings.TrimSpace(gitLab.OAuthBaseURL) != "" {
			if _, err := config.ValidatePublicHTTPSURL(gitLab.OAuthBaseURL, "gitlab.oauth_base_url"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *ExecutionConfig) validate() error {
	c.Backend = strings.TrimSpace(c.Backend)
	switch c.Backend {
	case ExecutionBackendRiver, ExecutionBackendPoller:
	default:
		return xerror.Unexpected().Detail("execution.backend must be river or poller")
	}
	if c.RequestTimeout <= 0 {
		return xerror.Unexpected().Detail("execution.request_timeout must be greater than 0")
	}
	return nil
}

// validate rejects disabled River queues because each registered queue must have positive capacity.
func (c WorkerConfig) validate() error {
	limits := []struct {
		name  string
		value int
	}{
		{name: "worker.pipeline_git_max_workers", value: c.PipelineGitMaxWorkers},
		{name: "worker.email_max_workers", value: c.EmailMaxWorkers},
		{name: "worker.notification_delivery_max_workers", value: c.NotificationDeliveryMaxWorkers},
		{name: "worker.events_max_workers", value: c.EventsMaxWorkers},
		{name: "worker.event_maintenance_max_workers", value: c.EventMaintenanceMaxWorkers},
		{name: "worker.pipeline_schedule_max_workers", value: c.PipelineScheduleMaxWorkers},
		{name: "worker.pipeline_retry_max_workers", value: c.PipelineRetryMaxWorkers},
		{name: "worker.pipeline_run_max_workers", value: c.PipelineRunMaxWorkers},
		{name: "worker.data_source_schema_import_max_workers", value: c.DataSourceSchemaImportMaxWorkers},
		{name: "worker.execution_max_workers", value: c.ExecutionMaxWorkers},
	}
	for _, limit := range limits {
		if limit.value <= 0 {
			return xerror.Unexpected().Detailf("%s must be greater than 0", limit.name)
		}
	}

	return nil
}

// validateNonLocalDeployment rejects local development defaults when the deployment profile is non-local.
func (c *Config) validateNonLocalDeployment() error {
	if c.Deployment.Profile == config.DeploymentProfileSelfHosted && c.Logging.File != "" && c.Logging.MaxFileSizeMB <= 0 {
		return xerror.ValidationFailed().Detail("logging.max_file_size_mb must be positive for self-hosted file logging")
	}

	if err := config.ValidateDeploymentPostgres(
		c.Deployment.Profile,
		c.Database.Host,
		c.Database.User,
		c.Database.Password,
		c.Database.SSLMode,
	); err != nil {
		return err
	}
	if !c.Session.CookieSecure {
		return xerror.Unexpected().Detail(
			"session.cookie_secure must be true in non-local deployment profiles",
		)
	}
	if c.Server.DrainTimeout <= 0 {
		return xerror.Unexpected().Detail(
			"server.drain_timeout must be greater than 0 in non-local deployment profiles",
		)
	}
	frontendURL, err := config.ValidateDeploymentHTTPSURL(c.Deployment.Profile, c.FrontendURL, "frontend_url")
	if err != nil {
		return err
	}
	redirectURL, err := config.ValidateDeploymentHTTPSURL(c.Deployment.Profile,
		c.Auth.GoogleRedirectURI,
		"auth.google_redirect_uri",
	)
	if err != nil {
		return err
	}
	if !sameURLOrigin(frontendURL, redirectURL) {
		return xerror.Unexpected().Detail(
			"auth.google_redirect_uri must use the same origin as frontend_url in non-local deployment profiles",
		)
	}
	if redirectURL.Path != googleRedirectCallbackURL {
		return xerror.Unexpected().Detailf(
			"auth.google_redirect_uri must end with %s in non-local deployment profiles",
			googleRedirectCallbackURL,
		)
	}
	if len(c.CORS.AllowedOrigins) != 1 {
		return xerror.Unexpected().Detail(
			"cors.allowed_origins must contain exactly one origin in non-local deployment profiles",
		)
	}
	corsOrigin, err := config.ValidateHTTPOrigin(
		c.CORS.AllowedOrigins[0],
		"cors.allowed_origins[0]",
	)
	if err != nil {
		return err
	}
	if _, err := config.ValidateDeploymentHTTPSURL(c.Deployment.Profile,
		c.CORS.AllowedOrigins[0],
		"cors.allowed_origins[0]",
	); err != nil {
		return err
	}
	if !sameURLOrigin(frontendURL, corsOrigin) {
		return xerror.Unexpected().Detail(
			"cors.allowed_origins must match the frontend_url origin in non-local deployment profiles",
		)
	}
	if err := validateNonLocalSMTP(c.SMTP); err != nil {
		return err
	}
	if c.Logging.LogHTTPRequests {
		return xerror.Unexpected().Detail(
			"logging.log_http_requests must be false in non-local deployment profiles",
		)
	}
	if strings.EqualFold(strings.TrimSpace(c.Logging.Level), "debug") {
		return xerror.Unexpected().Detail(
			"logging.level must not be debug in non-local deployment profiles",
		)
	}

	return nil
}

// validateNonLocalSMTP rejects unsafe SMTP settings outside local development.
func validateNonLocalSMTP(smtp config.SMTP) error {
	if smtp.Insecure {
		return xerror.Unexpected().Detail(
			"smtp.insecure_skip_verify must be false in non-local deployment profiles",
		)
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(smtp.Host))
	if err != nil {
		return xerror.Unexpected().Detailf("smtp.host must include host and port: %w", err)
	}
	if err := config.ValidateNonLocalHost(host, "smtp.host"); err != nil {
		return err
	}
	noReplyAddress := strings.ToLower(strings.TrimSpace(smtp.NoReplyAddress))
	if noReplyAddress == "" {
		return xerror.Unexpected().Detail(
			"smtp.no_reply_address is required in non-local deployment profiles",
		)
	}
	if noReplyAddress == defaultNoReplyAddress {
		return xerror.Unexpected().Detail(
			"smtp.no_reply_address must not use the repository default in non-local deployment profiles",
		)
	}
	if noReplyAddress == "no-reply@example.test" {
		return xerror.Unexpected().Detail(
			"smtp.no_reply_address must not use a development placeholder in non-local deployment profiles",
		)
	}
	return nil
}

// sameURLOrigin reports whether two URLs share the same scheme, host, and effective port.
func sameURLOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectiveURLPort(a) == effectiveURLPort(b)
}

// effectiveURLPort returns the explicit port or the default port for HTTP(S) URLs.
func effectiveURLPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case httpsScheme:
		return "443"
	case httpScheme:
		return "80"
	default:
		return ""
	}
}

func validateOptionalHTTPURL(value, field string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}

	_, err := config.ValidateHTTPURL(trimmed, field)
	return err
}

// isAllZeroHexKey reports whether a decoded hex key would contain only zero bytes.
func isAllZeroHexKey(key string) bool {
	return strings.Trim(key, "0") == ""
}

func (s *SnowflakeConfig) validate() error {
	socketPath := strings.TrimSpace(s.WIFSocketPath)
	issuer := strings.TrimSpace(s.WIFIssuer)
	if issuer != "" {
		if _, err := snowflakewif.NewIdentity(issuer, "validation", "validation", "validation"); err != nil {
			return xerror.Unexpected().Detail("snowflake.wif_issuer must be an HTTPS issuer origin without a port or path")
		}
	}
	if (socketPath == "") != (issuer == "") {
		return xerror.Unexpected().Detail("Snowflake workload socket and issuer must be configured together")
	}
	if socketPath != "" {
		if !filepath.IsAbs(socketPath) {
			return xerror.Unexpected().Detail("Snowflake workload API socket must be an absolute path")
		}
		socketPath = filepath.Clean(socketPath)
	}
	s.WIFSocketPath, s.WIFIssuer = socketPath, issuer

	return nil
}

func validateSlackOAuth(slack SlackConfig, profile string) error {
	values := []string{strings.TrimSpace(slack.OAuthClientID), strings.TrimSpace(slack.OAuthClientSecret), strings.TrimSpace(slack.OAuthRedirectURI)}
	configured := 0
	for _, value := range values {
		if value != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil
	}
	if configured != len(values) {
		return xerror.Unexpected().Detail("slack OAuth client ID, client secret, and redirect URI must be configured together")
	}
	callback, err := url.Parse(values[2])
	if err != nil || callback.Scheme != "https" || callback.Host == "" || callback.User != nil || callback.RawQuery != "" || callback.Fragment != "" || callback.Path != "/provider-connectors/slack/callback" {
		return xerror.Unexpected().Detail("slack.oauth_redirect_uri must be an HTTPS URL ending in /provider-connectors/slack/callback")
	}
	if config.IsNonLocalDeploymentProfile(profile) {
		if _, err := config.ValidatePublicHTTPSURL(values[2], "slack.oauth_redirect_uri"); err != nil {
			return err
		}
	}
	return nil
}

// validateConnectorTransport applies to API, worker, and executor startup equally.
func (c *Config) validateConnectorTransport() error {
	if c.Deployment.Profile == config.DeploymentProfileSelfHosted && c.Security.AllowPrivateDataSourceHosts {
		return xerror.ValidationFailed().Detail("self-hosted deployments require explicit private destinations; security.allow_private_data_source_hosts must be false")
	}

	if c.Security.AllowPlaintextConnectors && config.IsNonLocalDeploymentProfile(c.Deployment.Profile) {
		return xerror.Unexpected().Detail("security.allow_plaintext_connectors is restricted to the local deployment profile")
	}
	for field, endpoint := range map[string]string{
		"hubspot.base_url":       c.HubSpot.BaseURL,
		"hubspot.oauth_base_url": c.HubSpot.OAuthBaseURL,
		"gitlab.oauth_base_url":  c.GitLab.OAuthBaseURL,
		"bigquery.base_url":      c.BigQuery.BaseURL,
		"bigquery.token_url":     c.BigQuery.TokenURL,
	} {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			continue
		}
		if err := validateOptionalHTTPURL(endpoint, field); err != nil {
			return err
		}
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "https" && (!c.Security.AllowPlaintextConnectors || parsed.Scheme != "http")) {
			return xerror.Unexpected().Detailf("%s requires HTTPS unless local plaintext connector policy is enabled", field)
		}
	}
	return nil
}
