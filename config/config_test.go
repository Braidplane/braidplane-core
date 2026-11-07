package config_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/config"
	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	frontendconfig "github.com/Braidplane/braidplane-core/config/frontend"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var configEnvVars = []string{
	"DEPLOYMENT_PROFILE",
	"SERVER_HOST",
	"SERVER_PORT",
	"SERVER_SHUTDOWN_TIMEOUT",
	"SERVER_DRAIN_TIMEOUT",
	"SERVER_STATIC_CACHE_MAX_AGE",
	"SERVER_PROXY_HEADER",
	"SERVER_TRUSTED_PROXIES",
	"FRONTEND_SERVER_HOST",
	"FRONTEND_SERVER_PORT",
	"FRONTEND_SERVER_SHUTDOWN_TIMEOUT",
	"FRONTEND_SERVER_DRAIN_TIMEOUT",
	"FRONTEND_SERVER_STATIC_CACHE_MAX_AGE",
	"FRONTEND_SERVER_PROXY_HEADER",
	"FRONTEND_SERVER_TRUSTED_PROXIES",
	"DB_HOST",
	"DB_PORT",
	"DB_USER",
	"DB_PASSWORD",
	"DB_DATABASE",
	"DB_SSL_MODE",
	"DB_SSL_ROOT_CERT",
	"DB_MAX_CONNS",
	"DB_MIN_CONNS",
	"DB_MAX_CONN_LIFETIME",
	"DB_MAX_CONN_IDLE_TIME",
	"DB_HEALTH_CHECK_PERIOD",
	"SESSION_TABLE_NAME",
	"SESSION_COOKIE_SECURE",
	"FRONTEND_SESSION_TABLE_NAME",
	"FRONTEND_SESSION_COOKIE_SECURE",
	"ARGON2_MEMORY",
	"ARGON2_ITERATIONS",
	"ARGON2_PARALLELISM",
	"ARGON2_SALT_LENGTH",
	"ARGON2_KEY_LENGTH",
	"ENCRYPTION_KEY_HEX",
	"ALLOW_PRIVATE_DATA_SOURCE_HOSTS",
	"ALLOW_PLAINTEXT_CONNECTORS",
	"GOOGLE_CLIENT_ID",
	"GOOGLE_CLIENT_SECRET",
	"GOOGLE_REDIRECT_URI",
	"GITHUB_PROOF_APP_ID",
	"GITHUB_PROOF_CLIENT_ID",
	"GITHUB_PROOF_CLIENT_SECRET",
	"GITHUB_PROOF_PRIVATE_KEY_FILE",
	"GITHUB_PROOF_CALLBACK_URL",
	"GITHUB_PROOF_INSTALLATION_URL",
	"GITHUB_PROOF_WEBHOOK_SECRET",
	"PROOF_GIT_CHECKS_ENABLED",
	"SELF_SERVICE_REGISTRATION_ENABLED",
	"EXTERNAL_ACCESS_ENABLED",
	"TERMS_DOCUMENT_VERSION",
	"TERMS_URL",
	"PRIVACY_URL",
	"LEGAL_DOCUMENTS_FINAL",
	"CORS_ALLOWED_ORIGINS",
	"HUBSPOT_BASE_URL",
	"HUBSPOT_ALLOW_PRIVATE_HOSTS",
	"HUBSPOT_OAUTH_CLIENT_ID",
	"HUBSPOT_OAUTH_CLIENT_SECRET",
	"HUBSPOT_OAUTH_REDIRECT_URI",
	"HUBSPOT_OAUTH_BASE_URL",
	"SLACK_OAUTH_CLIENT_ID",
	"SLACK_OAUTH_CLIENT_SECRET",
	"SLACK_OAUTH_REDIRECT_URI",
	"GITLAB_OAUTH_CLIENT_ID",
	"GITLAB_OAUTH_CLIENT_SECRET",
	"GITLAB_OAUTH_REDIRECT_URI",
	"GITLAB_OAUTH_BASE_URL",
	"BIGQUERY_BASE_URL",
	"BIGQUERY_TOKEN_URL",
	"BIGQUERY_ALLOW_PRIVATE_HOSTS",
	"EXECUTION_LIMITS_QUERY_TIMEOUT",
	"EXECUTION_LIMITS_CONNECTOR_TIMEOUT",
	"EXECUTION_LIMITS_MAX_RESULT_ROWS",
	"EXECUTION_LIMITS_MAX_RESPONSE_BYTES",
	"EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_PER_DATA_SOURCE",
	"EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_QUEUE_TIMEOUT",
	"EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_MAX_QUEUED_PER_DATA_SOURCE",
	"EXECUTION_LIMITS_HTTP_TIMEOUT",
	"EXECUTION_LIMITS_HTTP_MAX_REDIRECTS",
	"EXECUTION_LIMITS_HTTP_MAX_REQUEST_BYTES",
	"EXECUTION_LIMITS_HTTP_MAX_RESPONSE_BYTES",
	"EXECUTION_LIMITS_HTTP_RETRY_ATTEMPTS",
	"EXECUTION_LIMITS_HTTP_RETRY_BACKOFF",
	"EXECUTION_LIMITS_TRANSFORM_TIMEOUT",
	"EXECUTION_LIMITS_TRANSFORM_MAX_WORK",
	"EXECUTION_LIMITS_TRANSFORM_MAX_INTERMEDIATE_ROWS",
	"EXECUTION_LIMITS_TRANSFORM_MAX_INTERMEDIATE_BYTES",
	"EXECUTION_LIMITS_TRANSFORM_MAX_OUTPUT_ROWS",
	"EXECUTION_LIMITS_TRANSFORM_MAX_OUTPUT_BYTES",
	"EXECUTION_LIMITS_TRANSFORM_MAX_DEPTH",
	"WORKER_PIPELINE_GIT_MAX_WORKERS",
	"WORKER_EMAIL_MAX_WORKERS",
	"WORKER_NOTIFICATION_DELIVERY_MAX_WORKERS",
	"WORKER_EVENTS_MAX_WORKERS",
	"WORKER_EVENT_MAINTENANCE_MAX_WORKERS",
	"WORKER_PIPELINE_SCHEDULE_MAX_WORKERS",
	"WORKER_PIPELINE_RETRY_MAX_WORKERS",
	"WORKER_PIPELINE_RUN_MAX_WORKERS",
	"WORKER_DATA_SOURCE_SCHEMA_IMPORT_MAX_WORKERS",
	"SMTP_HOST",
	"SMTP_USER",
	"SMTP_PASSWORD",
	"SMTP_CA_CERT",
	"SMTP_CA_CERT_FILE",
	"SMTP_INSECURE_SKIP_VERIFY",
	"SMTP_NO_REPLY_ADDRESS",
	"LOG_LEVEL",
	"LOG_BACKEND_FILE",
	"LOG_FRONTEND_FILE",
	"LOG_ROTATION_INTERVAL",
	"LOG_HTTP_REQUESTS",
	"LOG_MAX_FILE_SIZE_MB",
	"LOG_ENVIRONMENT",
	"LOG_OTLP_LOGS_ENDPOINT",
	"TRACING_OTLP_TRACES_ENDPOINT",
	"TRACING_SAMPLING_RATIO",
	"METRICS_ENABLED",
	"METRICS_PATH",
	"FRONTEND_URL",
	"BACKEND_API_URL",
	"DEFAULT_LANGUAGE",
}

const testEncryptionKeyHex = "0202020202020202020202020202020202020202020202020202020202020202"

func TestStartupSafetyHelpersValidateNonLocalURLsAndHosts(t *testing.T) {
	t.Parallel()

	t.Run("ValidatePublicHTTPSURL", func(t *testing.T) {
		t.Parallel()

		_, err := config.ValidatePublicHTTPSURL("https://app.example.com", "frontend_url")
		require.NoError(t, err)

		_, err = config.ValidatePublicHTTPSURL("http://app.example.com", "frontend_url")
		require.ErrorContains(t, err, "frontend_url must use https://")

		_, err = config.ValidatePublicHTTPSURL("https://10.0.0.5", "frontend_url")
		require.ErrorContains(t, err, "frontend_url must not use a local, private")
	})

	t.Run("ValidateNonLocalHTTPURL", func(t *testing.T) {
		t.Parallel()

		_, err := config.ValidateNonLocalHTTPURL("http://backend.internal:3001", "backend_api_url")
		require.NoError(t, err)

		_, err = config.ValidateNonLocalHTTPURL("http://localhost:3001", "backend_api_url")
		require.ErrorContains(t, err, "backend_api_url must not use a local")
	})

	t.Run("ValidateNonLocalHost", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, config.ValidateNonLocalHost("db.internal", "database.host"))
		require.ErrorContains(
			t,
			config.ValidateNonLocalHost("127.0.0.1", "database.host"),
			"database.host must not use a local",
		)
	})

	t.Run("IsLocalDevelopmentHost", func(t *testing.T) {
		t.Parallel()

		require.True(t, config.IsLocalDevelopmentHost("dev.localhost"))
		require.False(t, config.IsLocalDevelopmentHost("db.internal"))
	})
}

func TestValidateNonLocalPostgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		host          string
		user          string
		password      string
		sslMode       string
		wantErrSubstr string
	}{
		{
			name:     "Valid",
			host:     "db.internal",
			user:     "braidplane",
			password: "secret",
			sslMode:  "verify-full",
		},
		{
			name:          "LocalHost",
			host:          "localhost",
			user:          "braidplane",
			password:      "secret",
			sslMode:       "require",
			wantErrSubstr: "database.host must not use a local",
		},
		{
			name:          "EmptyUser",
			host:          "db.internal",
			password:      "secret",
			sslMode:       "require",
			wantErrSubstr: "database.user is required",
		},
		{
			name:          "EmptyPassword",
			host:          "db.internal",
			user:          "braidplane",
			sslMode:       "require",
			wantErrSubstr: "database.password is required",
		},
		{
			name:          "DefaultCredentials",
			host:          "db.internal",
			user:          "postgres",
			password:      "postgres",
			sslMode:       "require",
			wantErrSubstr: "must not use postgres/postgres defaults",
		},
		{
			name:          "InsecureSSL",
			host:          "db.internal",
			user:          "braidplane",
			password:      "secret",
			sslMode:       "disable",
			wantErrSubstr: "database.ssl_mode must be require, verify-ca, or verify-full",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := config.ValidateNonLocalPostgres(tt.host, tt.user, tt.password, tt.sslMode)
			if tt.wantErrSubstr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErrSubstr)
		})
	}
}

func TestValidateDeploymentPostgresAllowsOnlyPrivateStagingDisableTLS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		profile       string
		host          string
		sslMode       string
		wantErrSubstr string
	}{
		{
			name:          "StagingPrivatePostgresWithoutTLS",
			profile:       config.DeploymentProfileStaging,
			host:          "private-db",
			sslMode:       "disable",
			wantErrSubstr: "database.ssl_mode must be require, verify-ca, or verify-full",
		},
		{
			name:          "StagingExternalPostgresWithoutTLS",
			profile:       config.DeploymentProfileStaging,
			host:          "db.example.test",
			sslMode:       "disable",
			wantErrSubstr: "database.ssl_mode must be require, verify-ca, or verify-full",
		},
		{
			name:          "ProductionPrivatePostgresWithoutTLS",
			profile:       config.DeploymentProfileProduction,
			host:          "private-db",
			sslMode:       "disable",
			wantErrSubstr: "database.ssl_mode must be require, verify-ca, or verify-full",
		},
		{
			name:    "ProductionPrivatePostgresWithTLS",
			profile: config.DeploymentProfileProduction,
			host:    "private-db",
			sslMode: "require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := config.ValidateDeploymentPostgres(
				tt.profile,
				tt.host,
				"braidplane",
				"secret",
				tt.sslMode,
			)
			if tt.wantErrSubstr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErrSubstr)
		})
	}
}

func resetTestFlags(t *testing.T, args ...string) {
	t.Helper()

	oldCommandLine := flag.CommandLine
	oldArgs := os.Args

	programName := "config.test"
	if len(oldArgs) > 0 {
		programName = oldArgs[0]
	}

	flag.CommandLine = flag.NewFlagSet(programName, flag.ContinueOnError)
	os.Args = append([]string{programName}, args...)

	t.Cleanup(func() {
		flag.CommandLine = oldCommandLine
		os.Args = oldArgs
	})
}

func clearTestConfigEnv(t *testing.T) {
	t.Helper()

	for _, envVar := range configEnvVars {
		t.Setenv(envVar, "")
	}
}

func setTestBackendRequiredEnv(t *testing.T) {
	t.Helper()

	t.Setenv("ENCRYPTION_KEY_HEX", testEncryptionKeyHex)
	setTestGitHubAppEnv(t)
}

func setTestGitHubAppEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_PROOF_APP_ID", "1")
	t.Setenv("GITHUB_PROOF_CLIENT_ID", "test-client")
	t.Setenv("GITHUB_PROOF_CLIENT_SECRET", "test-secret")
	t.Setenv("GITHUB_PROOF_PRIVATE_KEY_FILE", "/test/github-app.pem")
	t.Setenv("GITHUB_PROOF_CALLBACK_URL", "https://app.example.test/github/callback")
	t.Setenv("GITHUB_PROOF_INSTALLATION_URL", "https://github.com/apps/braidplane-test/installations/new")
	t.Setenv("GITHUB_PROOF_WEBHOOK_SECRET", strings.Repeat("s", 32))
}

func TestLoad_AllFieldTypes(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
[database]
[session]
[security]
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err, "failed to write test config")

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, "local", cfg.Deployment.Profile)
	assert.Equal(t, 3000, cfg.Server.Port)
	assert.Empty(t, cfg.Server.ProxyHeader)
	assert.Empty(t, cfg.Server.TrustedProxies)
	assert.Equal(t, "localhost", cfg.Database.Host)
	assert.Equal(t, 35432, cfg.Database.Port)
	assert.Equal(t, 25, cfg.Database.MaxConns)
	assert.Equal(t, "local", cfg.Logging.Environment)
	assert.Empty(t, cfg.Logging.OTLPLogsEndpoint)
	assert.Empty(t, cfg.Tracing.OTLPTracesEndpoint)
	assert.InEpsilon(t, 1.0, cfg.Tracing.SamplingRatio, 0)
	assert.False(t, cfg.Metrics.Enabled)
	assert.Equal(t, "/metrics", cfg.Metrics.Path)
	assert.Equal(t, uint32(64*1024), cfg.Security.Argon2Memory)
	assert.False(t, cfg.Security.AllowPrivateDataSourceHosts)
	assert.False(t, cfg.Security.AllowPlaintextConnectors)
	assert.False(t, cfg.Auth.SelfServiceRegistrationEnabled)
	assert.Empty(t, cfg.HubSpot.BaseURL)
	assert.False(t, cfg.HubSpot.AllowPrivateHosts)
	assert.Empty(t, cfg.BigQuery.BaseURL)
	assert.Empty(t, cfg.BigQuery.TokenURL)
	assert.False(t, cfg.BigQuery.AllowPrivateHosts)
	assert.Equal(t, 30, int(cfg.ExecutionLimits.QueryTimeout.Seconds()))
	assert.Equal(t, 30, int(cfg.ExecutionLimits.ConnectorTimeout.Seconds()))
	assert.Equal(t, 1000, cfg.ExecutionLimits.MaxResultRows)
	assert.Equal(t, int64(10*1024*1024), cfg.ExecutionLimits.MaxResponseBytes)
	assert.Equal(t, 4, cfg.ExecutionLimits.ConnectorConcurrencyPerDataSource)
	assert.Equal(t, 5, int(cfg.ExecutionLimits.ConnectorConcurrencyQueueTimeout.Seconds()))
	assert.Equal(t, 16, cfg.ExecutionLimits.ConnectorConcurrencyMaxQueued)
	assert.Equal(t, 10, int(cfg.ExecutionLimits.HTTPTimeout.Seconds()))
	assert.Equal(t, 3, cfg.ExecutionLimits.HTTPMaxRedirects)
	assert.Equal(t, int64(1024*1024), cfg.ExecutionLimits.HTTPMaxRequestBytes)
	assert.Equal(t, int64(10*1024*1024), cfg.ExecutionLimits.HTTPMaxResponseBytes)
	assert.Equal(t, 1, cfg.ExecutionLimits.HTTPRetryAttempts)
	assert.Equal(t, 250, int(cfg.ExecutionLimits.HTTPRetryBackoff.Milliseconds()))
	assert.Equal(t, 10, int(cfg.ExecutionLimits.TransformTimeout.Seconds()))
	assert.Equal(t, int64(1_000_000), cfg.ExecutionLimits.TransformMaxWork)
	assert.Equal(t, 10_000, cfg.ExecutionLimits.TransformMaxIntermediateRows)
	assert.Equal(t, int64(32*1024*1024), cfg.ExecutionLimits.TransformMaxIntermediateBytes)
	assert.Equal(t, 1_000, cfg.ExecutionLimits.TransformMaxOutputRows)
	assert.Equal(t, int64(10*1024*1024), cfg.ExecutionLimits.TransformMaxOutputBytes)
	assert.Equal(t, 16, cfg.ExecutionLimits.TransformMaxDepth)
}

func TestLoad_TOMLConfig(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine which is global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
host = "127.0.0.1"
port = 3001
proxy_header = "X-Forwarded-For"
trusted_proxies = ["10.0.0.0/8", "127.0.0.1"]

[database]
host = "db.example.com"
port = 5433
user = "testuser"
password = "testpass"
database = "testdb"
ssl_mode = "require"
max_conns = 50
min_conns = 10

[session]
table_name = "user_sessions"

[security]
argon2_memory = 32768
argon2_iterations = 4
argon2_parallelism = 4
argon2_salt_length = 32
argon2_key_length = 64
allow_private_data_source_hosts = true
allow_plaintext_connectors = true

[auth]
self_service_registration_enabled = false

[hubspot]
base_url = "http://localhost:38082"
allow_private_hosts = true

[bigquery]
base_url = "http://localhost:38084"
token_url = "http://localhost:38084/token"
allow_private_hosts = true

[execution_limits]
query_timeout = "45s"
connector_timeout = "20s"
max_result_rows = 250
max_response_bytes = 2097152
connector_concurrency_per_data_source = 2
connector_concurrency_queue_timeout = "4s"
connector_concurrency_max_queued_per_data_source = 7
http_timeout = "3s"
http_max_redirects = 1
http_max_request_bytes = 65536
http_max_response_bytes = 1048576
http_retry_attempts = 3
http_retry_backoff = "100ms"
transform_timeout = "7s"
transform_max_work = 12345
transform_max_intermediate_rows = 2345
transform_max_intermediate_bytes = 34567
transform_max_output_rows = 456
transform_max_output_bytes = 56789
transform_max_depth = 6
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1", cfg.Server.Host)
	assert.Equal(t, 3001, cfg.Server.Port)
	assert.Equal(t, "X-Forwarded-For", cfg.Server.ProxyHeader)
	assert.Equal(t, []string{"10.0.0.0/8", "127.0.0.1"}, cfg.Server.TrustedProxies)
	assert.Equal(t, "db.example.com", cfg.Database.Host)
	assert.Equal(t, 5433, cfg.Database.Port)
	assert.Equal(t, "testuser", cfg.Database.User)
	assert.Equal(t, 50, cfg.Database.MaxConns)
	assert.Equal(t, "user_sessions", cfg.Session.TableName)
	assert.Equal(t, uint32(32768), cfg.Security.Argon2Memory)
	assert.True(t, cfg.Security.AllowPrivateDataSourceHosts)
	assert.True(t, cfg.Security.AllowPlaintextConnectors)
	assert.False(t, cfg.Auth.SelfServiceRegistrationEnabled)
	assert.Equal(t, "http://localhost:38082", cfg.HubSpot.BaseURL)
	assert.True(t, cfg.HubSpot.AllowPrivateHosts)
	assert.Equal(t, "http://localhost:38084", cfg.BigQuery.BaseURL)
	assert.Equal(t, "http://localhost:38084/token", cfg.BigQuery.TokenURL)
	assert.True(t, cfg.BigQuery.AllowPrivateHosts)
	assert.Equal(t, 45, int(cfg.ExecutionLimits.QueryTimeout.Seconds()))
	assert.Equal(t, 20, int(cfg.ExecutionLimits.ConnectorTimeout.Seconds()))
	assert.Equal(t, 250, cfg.ExecutionLimits.MaxResultRows)
	assert.Equal(t, int64(2097152), cfg.ExecutionLimits.MaxResponseBytes)
	assert.Equal(t, 2, cfg.ExecutionLimits.ConnectorConcurrencyPerDataSource)
	assert.Equal(t, 4, int(cfg.ExecutionLimits.ConnectorConcurrencyQueueTimeout.Seconds()))
	assert.Equal(t, 7, cfg.ExecutionLimits.ConnectorConcurrencyMaxQueued)
	assert.Equal(t, 3, int(cfg.ExecutionLimits.HTTPTimeout.Seconds()))
	assert.Equal(t, 1, cfg.ExecutionLimits.HTTPMaxRedirects)
	assert.Equal(t, int64(65536), cfg.ExecutionLimits.HTTPMaxRequestBytes)
	assert.Equal(t, int64(1048576), cfg.ExecutionLimits.HTTPMaxResponseBytes)
	assert.Equal(t, 3, cfg.ExecutionLimits.HTTPRetryAttempts)
	assert.Equal(t, 100, int(cfg.ExecutionLimits.HTTPRetryBackoff.Milliseconds()))
	assert.Equal(t, 7, int(cfg.ExecutionLimits.TransformTimeout.Seconds()))
	assert.Equal(t, int64(12345), cfg.ExecutionLimits.TransformMaxWork)
	assert.Equal(t, 2345, cfg.ExecutionLimits.TransformMaxIntermediateRows)
	assert.Equal(t, int64(34567), cfg.ExecutionLimits.TransformMaxIntermediateBytes)
	assert.Equal(t, 456, cfg.ExecutionLimits.TransformMaxOutputRows)
	assert.Equal(t, int64(56789), cfg.ExecutionLimits.TransformMaxOutputBytes)
	assert.Equal(t, 6, cfg.ExecutionLimits.TransformMaxDepth)
}

func TestLoad_IgnoresRemovedPipelineRepoPathConfig(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine which is global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
[database]
[session]
[security]

[pipelines]
repo_path = ""
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	_, err = backendconfig.Load(configPath)
	require.NoError(t, err)
}

func TestLoad_EnvOverrides(t *testing.T) {
	setTestGitHubAppEnv(t)
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine which is global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
host = "0.0.0.0"
port = 3000

[database]
host = "localhost"
port = 5432
user = "postgres"
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	t.Setenv("SERVER_HOST", "192.168.1.1")
	t.Setenv("SERVER_PORT", "9000")
	t.Setenv("SERVER_PROXY_HEADER", "X-Real-IP")
	t.Setenv("SERVER_TRUSTED_PROXIES", "10.10.0.0/16,127.0.0.1")
	t.Setenv("DEPLOYMENT_PROFILE", "local")
	t.Setenv("DB_HOST", "prod-db.example.com")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("DB_USER", "produser")
	t.Setenv("DB_PASSWORD", "secretpass")
	t.Setenv("DB_MAX_CONNS", "100")
	t.Setenv("SESSION_TABLE_NAME", "user_sessions")
	t.Setenv("ARGON2_MEMORY", "131072")
	t.Setenv("ALLOW_PRIVATE_DATA_SOURCE_HOSTS", "true")
	t.Setenv("ALLOW_PLAINTEXT_CONNECTORS", "true")
	t.Setenv("SELF_SERVICE_REGISTRATION_ENABLED", "false")
	t.Setenv("HUBSPOT_BASE_URL", "http://localhost:39090")
	t.Setenv("HUBSPOT_ALLOW_PRIVATE_HOSTS", "true")
	t.Setenv("BIGQUERY_BASE_URL", "http://localhost:39091")
	t.Setenv("BIGQUERY_TOKEN_URL", "http://localhost:39091/token")
	t.Setenv("BIGQUERY_ALLOW_PRIVATE_HOSTS", "true")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com,http://localhost:39092")
	t.Setenv("EXECUTION_LIMITS_QUERY_TIMEOUT", "11s")
	t.Setenv("EXECUTION_LIMITS_CONNECTOR_TIMEOUT", "12s")
	t.Setenv("EXECUTION_LIMITS_MAX_RESULT_ROWS", "333")
	t.Setenv("EXECUTION_LIMITS_MAX_RESPONSE_BYTES", "4444")
	t.Setenv("EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_PER_DATA_SOURCE", "5")
	t.Setenv("EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_QUEUE_TIMEOUT", "6s")
	t.Setenv("EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_MAX_QUEUED_PER_DATA_SOURCE", "9")
	t.Setenv("EXECUTION_LIMITS_HTTP_TIMEOUT", "13s")
	t.Setenv("EXECUTION_LIMITS_HTTP_MAX_REDIRECTS", "2")
	t.Setenv("EXECUTION_LIMITS_HTTP_MAX_REQUEST_BYTES", "5555")
	t.Setenv("EXECUTION_LIMITS_HTTP_MAX_RESPONSE_BYTES", "6666")
	t.Setenv("EXECUTION_LIMITS_HTTP_RETRY_ATTEMPTS", "4")
	t.Setenv("EXECUTION_LIMITS_HTTP_RETRY_BACKOFF", "150ms")
	t.Setenv("ENCRYPTION_KEY_HEX", testEncryptionKeyHex)

	resetTestFlags(t)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "192.168.1.1", cfg.Server.Host)
	assert.Equal(t, "local", cfg.Deployment.Profile)
	assert.Equal(t, 9000, cfg.Server.Port)
	assert.Equal(t, "X-Real-IP", cfg.Server.ProxyHeader)
	assert.Equal(t, []string{"10.10.0.0/16", "127.0.0.1"}, cfg.Server.TrustedProxies)
	assert.Equal(t, "prod-db.example.com", cfg.Database.Host)
	assert.Equal(t, 5433, cfg.Database.Port)
	assert.Equal(t, "produser", cfg.Database.User)
	assert.Equal(t, "secretpass", cfg.Database.Password)
	assert.Equal(t, 100, cfg.Database.MaxConns)
	assert.Equal(t, "user_sessions", cfg.Session.TableName)
	assert.Equal(t, uint32(131072), cfg.Security.Argon2Memory)
	assert.True(t, cfg.Security.AllowPrivateDataSourceHosts)
	assert.True(t, cfg.Security.AllowPlaintextConnectors)
	assert.False(t, cfg.Auth.SelfServiceRegistrationEnabled)
	assert.Equal(t, "http://localhost:39090", cfg.HubSpot.BaseURL)
	assert.True(t, cfg.HubSpot.AllowPrivateHosts)
	assert.Equal(t, "http://localhost:39091", cfg.BigQuery.BaseURL)
	assert.Equal(t, "http://localhost:39091/token", cfg.BigQuery.TokenURL)
	assert.True(t, cfg.BigQuery.AllowPrivateHosts)
	assert.Equal(t, []string{"https://app.example.com", "http://localhost:39092"}, cfg.CORS.AllowedOrigins)
	assert.Equal(t, 11, int(cfg.ExecutionLimits.QueryTimeout.Seconds()))
	assert.Equal(t, 12, int(cfg.ExecutionLimits.ConnectorTimeout.Seconds()))
	assert.Equal(t, 333, cfg.ExecutionLimits.MaxResultRows)
	assert.Equal(t, int64(4444), cfg.ExecutionLimits.MaxResponseBytes)
	assert.Equal(t, 5, cfg.ExecutionLimits.ConnectorConcurrencyPerDataSource)
	assert.Equal(t, 6, int(cfg.ExecutionLimits.ConnectorConcurrencyQueueTimeout.Seconds()))
	assert.Equal(t, 9, cfg.ExecutionLimits.ConnectorConcurrencyMaxQueued)
	assert.Equal(t, 13, int(cfg.ExecutionLimits.HTTPTimeout.Seconds()))
	assert.Equal(t, 2, cfg.ExecutionLimits.HTTPMaxRedirects)
	assert.Equal(t, int64(5555), cfg.ExecutionLimits.HTTPMaxRequestBytes)
	assert.Equal(t, int64(6666), cfg.ExecutionLimits.HTTPMaxResponseBytes)
	assert.Equal(t, 4, cfg.ExecutionLimits.HTTPRetryAttempts)
	assert.Equal(t, 150, int(cfg.ExecutionLimits.HTTPRetryBackoff.Milliseconds()))
}

func TestLoad_LoggingEnvironmentPrecedence(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
[database]
[session]
[security]

[logging]
environment = "toml-env"
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	t.Setenv("LOG_ENVIRONMENT", "env-env")
	setTestBackendRequiredEnv(t)
	resetTestFlags(t, "--log-environment=flag-env")

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "flag-env", cfg.Logging.Environment)
}

func TestLoad_LoggingOTLPLogsEndpointPrecedence(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
[database]
[session]
[security]

[logging]
otlp_logs_endpoint = "http://toml-alloy:4318/v1/logs"
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	t.Setenv("LOG_OTLP_LOGS_ENDPOINT", "http://env-alloy:4318/v1/logs")
	setTestBackendRequiredEnv(t)
	resetTestFlags(t, "--log-otlp-logs-endpoint=http://flag-alloy:4318/v1/logs")

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "http://flag-alloy:4318/v1/logs", cfg.Logging.OTLPLogsEndpoint)
}

func TestLoad_TracingConfigPrecedence(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
[database]
[session]
[security]

[tracing]
otlp_traces_endpoint = "http://toml-alloy:4318/v1/traces"
sampling_ratio = 0.25
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	t.Setenv("TRACING_OTLP_TRACES_ENDPOINT", "http://env-alloy:4318/v1/traces")
	t.Setenv("TRACING_SAMPLING_RATIO", "0.5")
	resetTestFlags(
		t,
		"--tracing-otlp-traces-endpoint=http://flag-alloy:4318/v1/traces",
		"--tracing-sampling-ratio=0.75",
	)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "http://flag-alloy:4318/v1/traces", cfg.Tracing.OTLPTracesEndpoint)
	assert.InEpsilon(t, 0.75, cfg.Tracing.SamplingRatio, 0)
}

func TestLoad_MetricsConfigPrecedence(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
[database]
[session]
[security]

[metrics]
enabled = false
path = "/toml-metrics"
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	t.Setenv("METRICS_ENABLED", "true")
	t.Setenv("METRICS_PATH", "/env-metrics")
	resetTestFlags(
		t,
		"--metrics-enabled=true",
		"--metrics-path=/flag-metrics",
	)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.True(t, cfg.Metrics.Enabled)
	assert.Equal(t, "/flag-metrics", cfg.Metrics.Path)
}

func TestLoad_InvalidMetricsPath(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	tests := []struct {
		name string
		path string
	}{
		{name: "MissingSlash", path: "metrics"},
		{name: "Root", path: "/"},
		{name: "WithQuery", path: "/metrics?debug=true"},
		{name: "APIRoot", path: "/api"},
		{name: "APIPrefix", path: "/api/metrics"},
	}

	for _, tt := range tests { // This test mutates process-global config flags and environment.
		t.Run(tt.name, func(t *testing.T) {
			content := `[server]
[database]
[session]
[security]

[metrics]
enabled = true
path = "` + tt.path + `"
`
			err := os.WriteFile(configPath, []byte(content), 0o600)
			require.NoError(t, err)

			clearTestConfigEnv(t)
			setTestBackendRequiredEnv(t)
			resetTestFlags(t)

			_, err = backendconfig.Load(configPath)
			require.Error(t, err)
			require.ErrorContains(t, err, "metrics.path")
		})
	}
}

func TestLoad_InvalidTracingSamplingRatio(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	tests := []struct {
		name  string
		ratio string
	}{
		{name: "BelowZero", ratio: "-0.01"},
		{name: "AboveOne", ratio: "1.01"},
	}

	for _, tt := range tests { // This test mutates process-global config flags and environment.
		t.Run(tt.name, func(t *testing.T) {
			content := `[server]
[database]
[session]
[security]

[tracing]
sampling_ratio = ` + tt.ratio + `
`
			err := os.WriteFile(configPath, []byte(content), 0o600)
			require.NoError(t, err)

			clearTestConfigEnv(t)
			setTestBackendRequiredEnv(t)
			resetTestFlags(t)

			_, err = backendconfig.Load(configPath)
			require.Error(t, err)
			require.ErrorContains(t, err, "tracing.sampling_ratio")
			require.ErrorContains(t, err, "between 0.0 and 1.0")
		})
	}
}

func TestLoad_ExecutionLimitsPrecedence(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
[database]
[session]
[security]

[execution_limits]
query_timeout = "40s"
max_result_rows = 200
connector_concurrency_queue_timeout = "2s"
connector_concurrency_max_queued_per_data_source = 8
http_retry_backoff = "75ms"
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	t.Setenv("EXECUTION_LIMITS_QUERY_TIMEOUT", "50s")
	t.Setenv("EXECUTION_LIMITS_MAX_RESULT_ROWS", "300")
	t.Setenv("EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_QUEUE_TIMEOUT", "3s")
	t.Setenv("EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_MAX_QUEUED_PER_DATA_SOURCE", "10")
	t.Setenv("EXECUTION_LIMITS_HTTP_RETRY_BACKOFF", "125ms")
	resetTestFlags(
		t,
		"--execution-limits-query-timeout=1m",
		"--execution-limits-max-result-rows=400",
		"--execution-limits-connector-concurrency-queue-timeout=4s",
		"--execution-limits-connector-concurrency-max-queued-per-data-source=12",
		"--execution-limits-http-retry-backoff=250ms",
	)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, 60, int(cfg.ExecutionLimits.QueryTimeout.Seconds()))
	assert.Equal(t, 400, cfg.ExecutionLimits.MaxResultRows)
	assert.Equal(t, 4, int(cfg.ExecutionLimits.ConnectorConcurrencyQueueTimeout.Seconds()))
	assert.Equal(t, 12, cfg.ExecutionLimits.ConnectorConcurrencyMaxQueued)
	assert.Equal(t, 250, int(cfg.ExecutionLimits.HTTPRetryBackoff.Milliseconds()))
}

func TestLoadBackend_ServerPortDoesNotSetFrontendURL(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine which is global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
port = 3001

[database]

[session]

[security]
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	t.Setenv("SERVER_PORT", "49912")
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, 49912, cfg.Server.Port)
	assert.Equal(t, "http://localhost:3002", cfg.FrontendURL)
	assert.Equal(t, "http://localhost:3002/api/auth/google/callback", cfg.Auth.GoogleRedirectURI)
}

func TestLoad_DerivesGoogleRedirectURIFromExplicitFrontendURL(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine which is global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]

[database]

[session]

[security]
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	t.Setenv("FRONTEND_URL", "http://localhost:49912")
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "http://localhost:49912", cfg.FrontendURL)
	assert.Equal(t, "http://localhost:49912/api/auth/google/callback", cfg.Auth.GoogleRedirectURI)
}

func TestLoadFrontend_UsesExplicitBackendAPIURL(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine which is global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[frontend_server]
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	t.Setenv("BACKEND_API_URL", "http://localhost:49913")
	resetTestFlags(t)

	cfg, err := frontendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "http://localhost:49913", cfg.BackendAPIURL)
}

func TestLoad_PrecedenceOrder(t *testing.T) {
	setTestGitHubAppEnv(t)
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
host = "0.0.0.0"
port = 3000
drain_timeout = "1s"

[database]
host = "localhost"
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	t.Setenv("SERVER_HOST", "env-host")
	t.Setenv("SERVER_PORT", "8000")
	t.Setenv("SERVER_DRAIN_TIMEOUT", "2s")
	t.Setenv("DEPLOYMENT_PROFILE", "staging")
	t.Setenv("ENCRYPTION_KEY_HEX", testEncryptionKeyHex)

	resetTestFlags(
		t,
		"--deployment-profile=local",
		"--server-host=flag-host",
		"--server-port=7000",
		"--server-drain-timeout=3s",
		"--server-proxy-header=X-Forwarded-For",
		"--server-trusted-proxies=192.0.2.10,192.0.2.0/24",
		"--db-host=flag-db.example.com",
		"--db-port=5433",
		"--db-user=flaguser",
		"--db-max-conns=200",
		"--argon2-memory=262144",
		"--allow-private-data-source-hosts=true",
		"--allow-plaintext-connectors=true",
		"--hubspot-base-url=http://localhost:39999",
		"--hubspot-allow-private-hosts=true",
		"--bigquery-base-url=http://localhost:39998",
		"--bigquery-token-url=http://localhost:39998/token",
		"--bigquery-allow-private-hosts=true",
	)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "flag-host", cfg.Server.Host)
	assert.Equal(t, "local", cfg.Deployment.Profile)
	assert.Equal(t, 7000, cfg.Server.Port)
	assert.Equal(t, config.NewDuration(3*time.Second), cfg.Server.DrainTimeout)
	assert.Equal(t, "X-Forwarded-For", cfg.Server.ProxyHeader)
	assert.Equal(t, []string{"192.0.2.10", "192.0.2.0/24"}, cfg.Server.TrustedProxies)
	assert.Equal(t, "flag-db.example.com", cfg.Database.Host)
	assert.Equal(t, 5433, cfg.Database.Port)
	assert.Equal(t, "flaguser", cfg.Database.User)
	assert.Equal(t, 200, cfg.Database.MaxConns)
	assert.Equal(t, uint32(262144), cfg.Security.Argon2Memory)
	assert.True(t, cfg.Security.AllowPrivateDataSourceHosts)
	assert.True(t, cfg.Security.AllowPlaintextConnectors)
	assert.Equal(t, "http://localhost:39999", cfg.HubSpot.BaseURL)
	assert.True(t, cfg.HubSpot.AllowPrivateHosts)
	assert.Equal(t, "http://localhost:39998", cfg.BigQuery.BaseURL)
	assert.Equal(t, "http://localhost:39998/token", cfg.BigQuery.TokenURL)
	assert.True(t, cfg.BigQuery.AllowPrivateHosts)
}

func TestLoadIntoWithArgs_InvalidFlagValueIsValidationError(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	err := os.WriteFile(configPath, []byte("port = 15432\n"), 0o600)
	require.NoError(t, err)

	cfg := struct {
		Port int `toml:"port" flag:"db-port" desc:"Database port"`
	}{
		Port: 15432,
	}
	err = config.LoadIntoWithArgs(configPath, &cfg, []string{"--db-port=not-a-number"})

	require.Error(t, err)
	var typedErr *xerror.Error
	require.True(t, xerror.As(err, &typedErr))
	assert.Equal(t, xerror.KindInvalidInput, typedErr.Kind())
	assert.Equal(t, "VALIDATION_FAILED", typedErr.Code())
}

func TestLoadBackend_IgnoresFrontendServerPort(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
port = 3100
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestGitHubAppEnv(t)
	t.Setenv("SERVER_PORT", "3200")
	t.Setenv("FRONTEND_SERVER_PORT", "9200")
	t.Setenv("ENCRYPTION_KEY_HEX", testEncryptionKeyHex)
	resetTestFlags(t, "--server-port=3300")

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, 3300, cfg.Server.Port)
	assert.Equal(t, "http://localhost:3002", cfg.FrontendURL)
}

func TestLoadFrontend_PrecedenceOrder(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[frontend_server]
host = "127.0.0.1"
port = 9100
drain_timeout = "1s"
proxy_header = " X-Forwarded-For "
trusted_proxies = [" 127.0.0.1 "]

[deployment]
profile = "staging"

[logging]
environment = "toml-env"

[security]
allow_private_data_source_hosts = false
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	t.Setenv("SERVER_PORT", "8200")
	t.Setenv("DEPLOYMENT_PROFILE", "production")
	t.Setenv("FRONTEND_SERVER_PORT", "9200")
	t.Setenv("FRONTEND_SERVER_DRAIN_TIMEOUT", "2s")
	t.Setenv("BACKEND_API_URL", "http://localhost:8200")
	t.Setenv("ALLOW_PRIVATE_DATA_SOURCE_HOSTS", "false")
	t.Setenv("LOG_ENVIRONMENT", "env-env")
	resetTestFlags(
		t,
		"--deployment-profile=local",
		"--frontend-server-port=9300",
		"--frontend-server-drain-timeout=3s",
		"--backend-api-url=http://localhost:8300",
		"--allow-private-data-source-hosts=true",
		"--log-environment=flag-env",
	)

	cfg, err := frontendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1", cfg.Server.Host)
	assert.Equal(t, "local", cfg.Deployment.Profile)
	assert.Equal(t, 9300, cfg.Server.Port)
	assert.Equal(t, config.NewDuration(3*time.Second), cfg.Server.DrainTimeout)
	assert.Equal(t, "X-Forwarded-For", cfg.Server.ProxyHeader)
	assert.Equal(t, []string{"127.0.0.1"}, cfg.Server.TrustedProxies)
	assert.Equal(t, "http://localhost:8300", cfg.BackendAPIURL)
	assert.True(t, cfg.Security.AllowPrivateDataSourceHosts)
	assert.Equal(t, "flag-env", cfg.Logging.Environment)
}

func TestLoad_SelfServiceRegistrationEnabledPrecedence(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[auth]
self_service_registration_enabled = false

[security]
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	t.Setenv("SELF_SERVICE_REGISTRATION_ENABLED", "true")
	resetTestFlags(t, "--self-service-registration-enabled=false")

	backendCfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)
	assert.False(t, backendCfg.Auth.SelfServiceRegistrationEnabled)

	clearTestConfigEnv(t)
	t.Setenv("SELF_SERVICE_REGISTRATION_ENABLED", "true")
	resetTestFlags(t, "--self-service-registration-enabled=false")

	frontendCfg, err := frontendconfig.Load(configPath)
	require.NoError(t, err)
	assert.False(t, frontendCfg.Auth.SelfServiceRegistrationEnabled)
}

func TestLoad_ProofGitChecksEnabledPrecedence(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[proof]\ngit_checks_enabled = false\n"), 0o600))
	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	t.Setenv("PROOF_GIT_CHECKS_ENABLED", "true")
	resetTestFlags(t, "--proof-git-checks-enabled=false")
	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)
	assert.False(t, cfg.Proof.GitChecksEnabled)
}

func TestLoadFrontend_UsesFrontendSessionSettings(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[frontend_server]

[database]
host = "frontend-db.example.test"
database = "frontend_db"

[session]
table_name = "backend_sessions"

[frontend_session]
table_name = "user_sessions"
cookie_secure = true
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	t.Setenv("DB_HOST", "frontend-env-db.example.test")
	t.Setenv("SESSION_TABLE_NAME", "backend_env_sessions")
	t.Setenv("FRONTEND_SESSION_TABLE_NAME", "user_sessions")
	resetTestFlags(
		t,
		"--frontend-session-cookie-secure=false",
	)

	cfg, err := frontendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "frontend-env-db.example.test", cfg.Database.Host)
	assert.Equal(t, "frontend_db", cfg.Database.Database)
	assert.Equal(t, "user_sessions", cfg.Session.TableName)
	assert.False(t, cfg.Session.CookieSecure)
}

func TestRepositoryConfigTOML_SplitRuntimePortsAreConsistent(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine and os.Args which are
	// global state.
	configPath := filepath.Join("..", "config.toml")

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	backendCfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	resetTestFlags(t)

	frontendCfg, err := frontendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, 3001, backendCfg.Server.Port)
	assert.Equal(t, "local", backendCfg.Deployment.Profile)
	assert.False(t, backendCfg.Auth.SelfServiceRegistrationEnabled)
	assert.Equal(t, config.NewDuration(0), backendCfg.Server.DrainTimeout)
	assert.Equal(t, 3002, frontendCfg.Server.Port)
	assert.Equal(t, "local", frontendCfg.Deployment.Profile)
	assert.False(t, frontendCfg.Auth.SelfServiceRegistrationEnabled)
	assert.Equal(t, config.NewDuration(0), frontendCfg.Server.DrainTimeout)
	assert.Equal(t, "http://localhost:3002", backendCfg.FrontendURL)
	assert.Equal(
		t,
		"http://localhost:3002/api/auth/google/callback",
		backendCfg.Auth.GoogleRedirectURI,
	)
	assert.Equal(t, "http://localhost:3001", frontendCfg.BackendAPIURL)
}

func TestLoad_InvalidConfigFile(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine which is global state
	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	_, err := backendconfig.Load("nonexistent-config.toml")
	require.Error(t, err, "should return error for nonexistent file")
	assert.Contains(t, err.Error(), "failed to read config file")
}

func TestLoad_InvalidTOML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server
host = invalid toml syntax
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	_, err = backendconfig.Load(configPath)
	require.Error(t, err, "should return error for invalid TOML")
	assert.Contains(t, err.Error(), "failed to parse config file")
}

func TestLoad_InvalidEnvValues(t *testing.T) {
	// Cannot use t.Parallel() because subtests use t.Setenv() which manipulates global environment
	// state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
[database]
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	tests := []struct {
		name   string
		envVar string
		value  string
	}{
		{"InvalidPort", "SERVER_PORT", "not-a-number"},
		{"InvalidDbPort", "DB_PORT", "invalid"},
		{"InvalidMaxConns", "DB_MAX_CONNS", "abc"},
		{"InvalidArgon2Memory", "ARGON2_MEMORY", "xyz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envVar, tt.value)

			resetTestFlags(t)

			_, err := backendconfig.Load(configPath)
			require.Error(t, err, "should return error for invalid %s", tt.envVar)
			assert.Contains(t, err.Error(), "failed to apply env overrides")
		})
	}
}

func TestBackendConnectionString(t *testing.T) {
	t.Parallel()

	cfg := &backendconfig.DatabaseConfig{
		Host:              "db.example.com",
		Port:              5432,
		User:              "testuser",
		Password:          "testpass",
		Database:          "testdb",
		SSLMode:           "require",
		SSLRootCert:       "/etc/braidplane/postgres-ca.crt",
		MaxConns:          50,
		MinConns:          10,
		MaxConnLifetime:   7200,
		MaxConnIdleTime:   600,
		HealthCheckPeriod: 120,
	}

	connStr := cfg.ConnectionString()
	expected := "postgres://testuser:testpass@db.example.com:5432/testdb?sslmode=require&sslrootcert=%2Fetc%2Fbraidplane%2Fpostgres-ca.crt&pool_max_conns=50&pool_min_conns=10&pool_max_conn_lifetime=7200s&pool_max_conn_idle_time=600s&pool_health_check_period=120s"

	assert.Equal(t, expected, connStr)
}

func TestLoad_InvalidEncryptionKeyHex(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	tests := []struct {
		name    string
		key     string
		message string
	}{
		{
			name:    "MissingKey",
			key:     "",
			message: "security.encryption_key_hex must be a 64-character hex string",
		},
		{
			name:    "ShortKey",
			key:     "abcd",
			message: "security.encryption_key_hex must be a 64-character hex string",
		},
		{
			name:    "InvalidHex",
			key:     "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
			message: "security.encryption_key_hex must be a 64-character hex string",
		},
		{
			name:    "AllZeroPlaceholderKey",
			key:     "0000000000000000000000000000000000000000000000000000000000000000",
			message: "security.encryption_key_hex must not use an all-zero placeholder key",
		},
		{
			name:    "SharedRepositoryDefaultKey",
			key:     "0101010101010101010101010101010101010101010101010101010101010101",
			message: "security.encryption_key_hex must not use the repository default key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := `[security]
encryption_key_hex = "` + tt.key + `"
`
			err := os.WriteFile(configPath, []byte(content), 0o600)
			require.NoError(t, err)

			clearTestConfigEnv(t)
			setTestGitHubAppEnv(t)
			resetTestFlags(t)

			_, err = backendconfig.Load(configPath)
			require.Error(t, err)
			require.ErrorContains(t, err, tt.message)
		})
	}
}

func TestLoad_InvalidHubSpotBaseURL(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
[database]
[session]
[security]
[hubspot]
base_url = "ftp://localhost:38082"
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	_, err = backendconfig.Load(configPath)
	require.Error(t, err)
	require.ErrorContains(t, err, "hubspot.base_url")
	require.ErrorContains(t, err, "http:// or https://")
}

func TestLoad_InvalidBigQueryURLs(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantKey       string
		wantErrSubstr string
	}{
		{
			name: "BaseURL",
			body: `[bigquery]
base_url = "ftp://localhost:38084"
`,
			wantKey:       "bigquery.base_url",
			wantErrSubstr: "http:// or https://",
		},
		{
			name: "TokenURL",
			body: `[bigquery]
token_url = "ftp://localhost:38084/token"
`,
			wantKey:       "bigquery.token_url",
			wantErrSubstr: "http:// or https://",
		},
		{
			name: "BaseURLMissingHost",
			body: `[bigquery]
base_url = "https:///bigquery"
`,
			wantKey:       "bigquery.base_url",
			wantErrSubstr: "must include a host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.toml")

			content := `[server]
[database]
[session]
[security]
` + tt.body
			err := os.WriteFile(configPath, []byte(content), 0o600)
			require.NoError(t, err)

			clearTestConfigEnv(t)
			setTestBackendRequiredEnv(t)
			resetTestFlags(t)

			_, err = backendconfig.Load(configPath)
			require.Error(t, err)
			require.ErrorContains(t, err, tt.wantKey)
			require.ErrorContains(t, err, tt.wantErrSubstr)
		})
	}
}

func TestLoad_ServerProxyHeaderRequiresTrustedProxies(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
proxy_header = "X-Forwarded-For"

[database]
[session]
[security]
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	_, err = backendconfig.Load(configPath)
	require.Error(t, err)
	require.ErrorContains(t, err, "server.proxy_header")
	require.ErrorContains(t, err, "server.trusted_proxies")
}

func TestLoad_ServerTrustedProxiesValidateIPOrCIDR(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
proxy_header = "X-Forwarded-For"
trusted_proxies = ["10.0.0.0/8", "not-an-ip"]

[database]
[session]
[security]
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	_, err = backendconfig.Load(configPath)
	require.Error(t, err)
	require.ErrorContains(t, err, "server.trusted_proxies")
	require.ErrorContains(t, err, "not-an-ip")
}

func TestLoad_ServerProxyConfigTrimsWhitespace(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
proxy_header = " X-Forwarded-For "
trusted_proxies = [" 10.0.0.0/8 ", " 127.0.0.1 "]

[database]
[session]
[security]
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Equal(t, "X-Forwarded-For", cfg.Server.ProxyHeader)
	assert.Equal(t, []string{"10.0.0.0/8", "127.0.0.1"}, cfg.Server.TrustedProxies)
}

func TestLoad_InvalidExecutionLimits(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	tests := []struct {
		name    string
		body    string
		message string
	}{
		{
			name: "ZeroQueryTimeout",
			body: `[execution_limits]
query_timeout = "0s"
`,
			message: "execution_limits.query_timeout must be greater than 0",
		},
		{
			name: "ZeroMaxResultRows",
			body: `[execution_limits]
max_result_rows = 0
`,
			message: "execution_limits.max_result_rows must be greater than 0",
		},
		{
			name: "ZeroConcurrency",
			body: `[execution_limits]
connector_concurrency_per_data_source = 0
`,
			message: "execution_limits.connector_concurrency_per_data_source must be greater than 0",
		},
		{
			name: "ZeroConcurrencyQueueTimeout",
			body: `[execution_limits]
connector_concurrency_queue_timeout = "0s"
`,
			message: "execution_limits.connector_concurrency_queue_timeout must be greater than 0",
		},
		{
			name: "ZeroConcurrencyMaxQueued",
			body: `[execution_limits]
connector_concurrency_max_queued_per_data_source = 0
`,
			message: "execution_limits.connector_concurrency_max_queued_per_data_source must be greater than 0",
		},
		{
			name: "ZeroHTTPRetryAttempts",
			body: `[execution_limits]
http_retry_attempts = 0
`,
			message: "execution_limits.http_retry_attempts must be greater than 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := `[server]
[database]
[session]
[security]
` + tt.body
			err := os.WriteFile(configPath, []byte(content), 0o600)
			require.NoError(t, err)

			clearTestConfigEnv(t)
			setTestBackendRequiredEnv(t)
			resetTestFlags(t)

			_, err = backendconfig.Load(configPath)
			require.Error(t, err)
			require.ErrorContains(t, err, tt.message)
		})
	}
}

func TestServerAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		host     string
		port     int
		expected string
	}{
		{"Localhost", "localhost", 3000, "localhost:3000"},
		{"AllInterfaces", "0.0.0.0", 3001, "0.0.0.0:3001"},
		{"SpecificIP", "192.168.1.1", 9000, "192.168.1.1:9000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &backendconfig.ServerConfig{
				Host: tt.host,
				Port: tt.port,
			}

			addr := cfg.Address()
			assert.Equal(t, tt.expected, addr)
		})
	}
}

func TestLoad_CORS_ValidOrigins(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine which is global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
[database]
[session]
[security]
[cors]
allowed_origins = ["http://localhost:3000", "https://app.example.com", "http://localhost:5173"]
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Len(t, cfg.CORS.AllowedOrigins, 3)
	assert.Equal(t, "http://localhost:3000", cfg.CORS.AllowedOrigins[0])
	assert.Equal(t, "https://app.example.com", cfg.CORS.AllowedOrigins[1])
	assert.Equal(t, "http://localhost:5173", cfg.CORS.AllowedOrigins[2])
}

func TestLoad_CORS_DefaultOrigin(t *testing.T) {
	// Cannot use t.Parallel() because this test manipulates flag.CommandLine which is global state
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
[database]
[session]
[security]
`
	err := os.WriteFile(configPath, []byte(content), 0o600)
	require.NoError(t, err)

	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)

	cfg, err := backendconfig.Load(configPath)
	require.NoError(t, err)

	assert.Len(t, cfg.CORS.AllowedOrigins, 1)
	assert.Equal(t, "http://localhost:3000", cfg.CORS.AllowedOrigins[0])
}

func TestLoad_CORS_ValidationErrors(t *testing.T) {
	// Cannot use t.Parallel() because subtests manipulate flag.CommandLine which is global state
	tmpDir := t.TempDir()

	tests := []struct {
		name            string
		corsConfig      string
		wantErrContains string
	}{
		{
			name:            "EmptyOrigins",
			corsConfig:      "[cors]\nallowed_origins = []",
			wantErrContains: "at least one CORS origin is required",
		},
		{
			name:            "EmptyString",
			corsConfig:      "[cors]\nallowed_origins = [\"\"]",
			wantErrContains: "CORS origin cannot be empty",
		},
		{
			name:            "NoScheme",
			corsConfig:      "[cors]\nallowed_origins = [\"localhost:3000\"]",
			wantErrContains: "must include http:// or https://",
		},
		{
			name:            "TrailingSlash",
			corsConfig:      "[cors]\nallowed_origins = [\"https://app.example.com/\"]",
			wantErrContains: "must be an origin without path, query, or fragment",
		},
		{
			name:            "InvalidURL",
			corsConfig:      "[cors]\nallowed_origins = [\"ht!tp://invalid\"]",
			wantErrContains: "invalid CORS origin",
		},
		{
			name:            "MixedValidAndInvalid",
			corsConfig:      "[cors]\nallowed_origins = [\"http://localhost:3000\", \"no-scheme.com\"]",
			wantErrContains: "must include http:// or https://",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(tmpDir, tt.name+"-config.toml")

			content := `[server]
[database]
[session]
[security]
` + tt.corsConfig

			err := os.WriteFile(configPath, []byte(content), 0o600)
			require.NoError(t, err)

			clearTestConfigEnv(t)
			resetTestFlags(t)

			_, err = backendconfig.Load(configPath)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrContains)
		})
	}
}

func TestCheckedInConfigKeepsConnectorPlaintextOptIn(t *testing.T) {
	clearTestConfigEnv(t)
	setTestBackendRequiredEnv(t)
	resetTestFlags(t)
	cfg, err := backendconfig.Load(filepath.Join("..", "config.toml"))
	require.NoError(t, err)
	require.False(t, cfg.Security.AllowPlaintextConnectors)
}

func TestSelfHostedHTTPSOrigins(t *testing.T) {
	for _, tc := range []struct {
		profile, target string
		invalid         bool
	}{
		{profile: "self-hosted", target: "https://10.20.0.5"},
		{profile: "self-hosted", target: "https://[fd00::5]"},
		{profile: "self-hosted", target: "https://app.customer.internal"},
		{profile: "self-hosted", target: "http://10.20.0.5", invalid: true},
		{profile: "self-hosted", target: "https://user:synthetic-secret@10.20.0.5", invalid: true},
		{profile: "self-hosted", target: "https://127.0.0.1", invalid: true},
		{profile: "self-hosted", target: "https://0.0.0.0", invalid: true},
		{profile: "production", target: "https://10.20.0.5", invalid: true},
		{profile: "staging", target: "https://10.20.0.5", invalid: true},
	} {
		t.Run(tc.profile+tc.target, func(t *testing.T) {
			_, err := config.ValidateDeploymentHTTPSURL(tc.profile, tc.target, "origin")
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSMTPCredentialsMustBeConfiguredTogether(t *testing.T) {
	for _, tc := range []struct {
		user, password string
		invalid        bool
	}{
		{"", "", false}, {"synthetic-user", "synthetic-password", false}, {"synthetic-user", "", true}, {"", "synthetic-password", true},
	} {
		err := (config.SMTP{User: tc.user, Password: tc.password}).ValidateCredentials()
		if tc.invalid {
			require.ErrorContains(t, err, "configured together")
		} else {
			require.NoError(t, err)
		}
	}
}
