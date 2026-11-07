package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/config"
)

const (
	testEncryptionKeyHex       = "0202020202020202020202020202020202020202020202020202020202020202"
	testExecutionRequestKeyHex = "0303030303030303030303030303030303030303030303030303030303030303"
)

func TestValidateExecutionRequestKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		appKey  string
		wantErr string
	}{
		{name: "DistinctKey", key: testExecutionRequestKeyHex},
		{name: "Missing", wantErr: "64-character"},
		{name: "Malformed", key: strings.Repeat("z", 64), wantErr: "64-character"},
		{name: "ZeroPlaceholder", key: strings.Repeat("0", 64), wantErr: "placeholder"},
		{name: "ApplicationKeyReuse", key: testExecutionRequestKeyHex, appKey: testExecutionRequestKeyHex, wantErr: "must differ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validBackendConfig()
			cfg.Security.ExecutionRequestKeyHex = tt.key
			cfg.Security.EncryptionKeyHex = tt.appKey
			err := cfg.ValidateExecutionRequestKey()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateGoogleCredentials(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, id, secret string
		invalid          bool
	}{
		{name: "disabled"},
		{name: "whitespace disabled", id: " ", secret: "\t"},
		{name: "configured", id: "synthetic-client", secret: "synthetic-secret"},
		{name: "missing secret", id: "synthetic-client", invalid: true},
		{name: "missing ID", secret: "synthetic-secret", invalid: true},
		{name: "blank secret", id: "synthetic-client", secret: " ", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validBackendConfig()
			cfg.Auth.GoogleClientID, cfg.Auth.GoogleClientSecret = tc.id, tc.secret
			err := cfg.validate()
			if tc.invalid {
				require.ErrorContains(t, err, "Google client ID and secret must be configured together")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCustomSessionTable(t *testing.T) {
	t.Parallel()
	cfg := validBackendConfig()
	cfg.Session.TableName = "unmanaged_sessions"
	require.NoError(t, cfg.validate())
}

func TestValidateExecutorRejectsInvalidExecutionLimits(t *testing.T) {
	t.Parallel()

	cfg := validProductionBackendConfig()
	cfg.Security.ExecutionRequestKeyHex = testExecutionRequestKeyHex
	cfg.Database.SSLMode = "verify-full"
	cfg.Database.SSLRootCert = "/tmp/test-postgres-ca.crt"
	cfg.ExecutionLimits.TransformMaxWork = 0

	require.ErrorContains(t, cfg.validateExecutor(), "execution_limits.transform_max_work")
}

func TestValidateExecutorRequiresVerifiedDatabaseIdentity(t *testing.T) {
	t.Parallel()

	cfg := validProductionBackendConfig()
	cfg.Security.ExecutionRequestKeyHex = testExecutionRequestKeyHex
	cfg.Database.SSLMode = "require"
	require.ErrorContains(t, cfg.validateExecutor(), "ssl_mode must be verify-full")
	cfg.Database.SSLMode = "verify-full"
	cfg.Database.SSLRootCert = ""
	require.ErrorContains(t, cfg.validateExecutor(), "ssl_root_cert is required")
}

func TestBackendConfigValidateAcceptsDefaultsWithNonDefaultEncryptionKey(t *testing.T) {
	t.Parallel()

	cfg := validBackendConfig()
	cfg.FrontendURL = "https://app.example.test/"
	cfg.Auth.GoogleRedirectURI = defaultGoogleRedirectURI
	cfg.applyDerivedURLs()

	require.NoError(t, cfg.validate())
	require.Equal(t, "https://app.example.test/api/auth/google/callback", cfg.Auth.GoogleRedirectURI)
	require.False(t, cfg.Auth.SelfServiceRegistrationEnabled)
	require.Equal(t, "0.0.0.0:3000", cfg.Server.Address())
	require.Equal(t, 1, cfg.Worker.PipelineGitMaxWorkers)
	require.Equal(t, 1, cfg.Worker.EmailMaxWorkers)
	require.Equal(t, 1, cfg.Worker.NotificationDeliveryMaxWorkers)
	require.Equal(t, 1, cfg.Worker.EventsMaxWorkers)
	require.Equal(t, 1, cfg.Worker.EventMaintenanceMaxWorkers)
	require.Equal(t, 4, cfg.Worker.PipelineScheduleMaxWorkers)
	require.Equal(t, 4, cfg.Worker.PipelineRetryMaxWorkers)
	require.Equal(t, 4, cfg.Worker.PipelineRunMaxWorkers)
	require.Equal(t, 2, cfg.Worker.DataSourceSchemaImportMaxWorkers)
	require.Equal(t, 4, cfg.Worker.ExecutionMaxWorkers)
	require.Equal(t, ExecutionBackendRiver, cfg.Execution.Backend)
	require.Equal(t, 30*time.Second, cfg.Execution.RequestTimeout.TimeDuration())
}

func TestBackendConfigAllowsOptionalCompleteGitHubApp(t *testing.T) {
	t.Parallel()
	cfg := validBackendConfig()
	cfg.GitHubProof = GitHubProofConfig{}
	require.NoError(t, cfg.validate())
	cfg.GitHubProof.AppID = 1
	require.ErrorContains(t, cfg.validate(), "must be complete")
	cfg.GitHubProof = validGitHubProofConfig()
	require.NoError(t, cfg.validate())
	cfg.GitHubProof.AppID = -1
	require.ErrorContains(t, cfg.validate(), "app_id must be positive")
}

func TestBackendConfigValidatesGitHubProofConfiguration(t *testing.T) {
	t.Parallel()
	cfg := validBackendConfig()
	cfg.GitHubProof = validGitHubProofConfig()
	cfg.GitHubProof.WebhookSecret = "short"
	require.ErrorContains(t, cfg.validate(), "github_proof.webhook_secret")

	for _, value := range []string{"http://app.example.test/github/callback", "https://10.0.0.5/github/callback"} {
		cfg := validProductionBackendConfig()
		cfg.GitHubProof = validGitHubProofConfig()
		cfg.GitHubProof.CallbackURL = value
		require.ErrorContains(t, cfg.validate(), "github_proof.callback_url")
	}
}

func validGitHubProofConfig() GitHubProofConfig {
	return GitHubProofConfig{
		AppID: 1, ClientID: "client", ClientSecret: "secret", PrivateKeyFile: "/run/secrets/github-app.pem",
		CallbackURL: "https://app.example.test/github/callback", InstallationURL: "https://github.com/apps/braidplane/installations/new",
		WebhookSecret: strings.Repeat("s", 32),
	}
}

// TestBackendConfigWorkerConcurrencyPrecedence verifies flags override environment and TOML values for queue capacity.
func TestBackendConfigWorkerConcurrencyPrecedence(t *testing.T) {
	setTestGitHubAppEnv(t)
	clearWorkerConfigEnv(t)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(`[security]
encryption_key_hex = "0202020202020202020202020202020202020202020202020202020202020202"

[worker]
pipeline_run_max_workers = 2
email_max_workers = 3
execution_max_workers = 4
`), 0o600))
	t.Setenv("WORKER_PIPELINE_RUN_MAX_WORKERS", "6")
	t.Setenv("WORKER_EXECUTION_MAX_WORKERS", "7")

	cfg, err := LoadWithArgs(configPath, []string{
		"--worker-pipeline-run-max-workers=9",
		"--worker-execution-max-workers=10",
	})
	require.NoError(t, err)
	require.Equal(t, 9, cfg.Worker.PipelineRunMaxWorkers)
	require.Equal(t, 3, cfg.Worker.EmailMaxWorkers)
	require.Equal(t, 10, cfg.Worker.ExecutionMaxWorkers)
}

func TestBackendConfigExecutionPrecedence(t *testing.T) {
	setTestGitHubAppEnv(t)
	t.Setenv("EXECUTION_BACKEND", "poller")
	t.Setenv("EXECUTION_REQUEST_TIMEOUT", "45s")

	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(`[security]
encryption_key_hex = "0202020202020202020202020202020202020202020202020202020202020202"

[execution]
backend = "river"
request_timeout = "40s"
`), 0o600))

	cfg, err := LoadWithArgs(configPath, []string{
		"--execution-backend=river",
		"--execution-request-timeout=50s",
	})
	require.NoError(t, err)
	require.Equal(t, ExecutionBackendRiver, cfg.Execution.Backend)
	require.Equal(t, 50*time.Second, cfg.Execution.RequestTimeout.TimeDuration())
}

func TestBackendConfigValidateRejectsInvalidExecution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*ExecutionConfig)
		wantErr string
	}{
		{name: "Backend", mutate: func(c *ExecutionConfig) { c.Backend = "automatic" }, wantErr: "execution.backend"},
		{name: "RequestTimeout", mutate: func(c *ExecutionConfig) { c.RequestTimeout = 0 }, wantErr: "execution.request_timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validBackendConfig()
			tt.mutate(&cfg.Execution)
			require.ErrorContains(t, cfg.validate(), tt.wantErr)
		})
	}
}

func TestBackendConfigAbuseControlsPrecedence(t *testing.T) {
	setTestGitHubAppEnv(t)
	for _, name := range []string{
		"ABUSE_CONTROLS_AUTH_ATTEMPTS",
		"ABUSE_CONTROLS_AUTH_WINDOW",
		"ABUSE_CONTROLS_EXPENSIVE_CLIENT_ATTEMPTS",
		"ABUSE_CONTROLS_EXPENSIVE_TENANT_ATTEMPTS",
		"ABUSE_CONTROLS_CREDENTIAL_ATTEMPTS",
		"ABUSE_CONTROLS_EXPENSIVE_WINDOW",
	} {
		t.Setenv(name, "")
	}
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(`[security]
encryption_key_hex = "0202020202020202020202020202020202020202020202020202020202020202"

[abuse_controls]
auth_attempts = 7
auth_window = "2m"
expensive_client_attempts = 21
expensive_tenant_attempts = 41
credential_attempts = 11
expensive_window = "3m"
`), 0o600))
	t.Setenv("ABUSE_CONTROLS_AUTH_ATTEMPTS", "8")
	t.Setenv("ABUSE_CONTROLS_EXPENSIVE_CLIENT_ATTEMPTS", "22")

	cfg, err := LoadWithArgs(configPath, []string{"--abuse-controls-auth-attempts=9"})
	require.NoError(t, err)
	require.Equal(t, 9, cfg.AbuseControls.AuthAttempts)
	require.Equal(t, 2*time.Minute, cfg.AbuseControls.AuthWindow.TimeDuration())
	require.Equal(t, 22, cfg.AbuseControls.ExpensiveClientAttempts)
	require.Equal(t, 41, cfg.AbuseControls.ExpensiveTenantAttempts)
	require.Equal(t, 11, cfg.AbuseControls.CredentialAttempts)
	require.Equal(t, 3*time.Minute, cfg.AbuseControls.ExpensiveWindow.TimeDuration())
}

func TestBackendConfigValidateRejectsInvalidAbuseControls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*AbuseControlsConfig)
		wantErr string
	}{
		{"auth attempts", func(cfg *AbuseControlsConfig) { cfg.AuthAttempts = 0 }, "auth_attempts"},
		{"auth window", func(cfg *AbuseControlsConfig) { cfg.AuthWindow = 0 }, "auth_window"},
		{"expensive client attempts", func(cfg *AbuseControlsConfig) { cfg.ExpensiveClientAttempts = 0 }, "expensive_client_attempts"},
		{"expensive tenant attempts", func(cfg *AbuseControlsConfig) { cfg.ExpensiveTenantAttempts = 0 }, "expensive_tenant_attempts"},
		{"credential attempts", func(cfg *AbuseControlsConfig) { cfg.CredentialAttempts = 0 }, "credential_attempts"},
		{"expensive window", func(cfg *AbuseControlsConfig) { cfg.ExpensiveWindow = 0 }, "expensive_window"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validBackendConfig()
			tt.mutate(&cfg.AbuseControls)
			require.ErrorContains(t, cfg.validate(), "abuse_controls."+tt.wantErr+" must be greater than 0")
		})
	}
}

// clearWorkerConfigEnv prevents developer shell overrides from changing config precedence assertions.
func clearWorkerConfigEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"WORKER_PIPELINE_GIT_MAX_WORKERS",
		"WORKER_EMAIL_MAX_WORKERS",
		"WORKER_NOTIFICATION_DELIVERY_MAX_WORKERS",
		"WORKER_EVENTS_MAX_WORKERS",
		"WORKER_EVENT_MAINTENANCE_MAX_WORKERS",
		"WORKER_PIPELINE_SCHEDULE_MAX_WORKERS",
		"WORKER_PIPELINE_RETRY_MAX_WORKERS",
		"WORKER_PIPELINE_RUN_MAX_WORKERS",
		"WORKER_DATA_SOURCE_SCHEMA_IMPORT_MAX_WORKERS",
		"WORKER_EXECUTION_MAX_WORKERS",
	} {
		t.Setenv(name, "")
	}
}

func TestBackendConfigValidateAcceptsNonLocalDeploymentProfile(t *testing.T) {
	t.Parallel()

	cfg := validProductionBackendConfig()
	cfg.Deployment.Profile = " production "
	cfg.FrontendURL = "https://APP.example.test"
	cfg.Auth.GoogleRedirectURI = "https://app.example.test:443/api/auth/google/callback"

	require.NoError(t, cfg.validate())
	require.Equal(t, config.DeploymentProfileProduction, cfg.Deployment.Profile)
}

func TestBackendConfigValidateRejectsPrivateStagingPostgresWithoutTLS(t *testing.T) {
	t.Parallel()

	cfg := validProductionBackendConfig()
	cfg.Deployment.Profile = config.DeploymentProfileStaging
	cfg.Database.Host = "private-db"
	cfg.Database.SSLMode = "disable"

	require.ErrorContains(t, cfg.validate(), "database.ssl_mode")
}

func TestBackendConfigValidateRejectsStagingPostgresWithoutTLSOutsidePrivateRuntimeNetwork(t *testing.T) {
	t.Parallel()

	cfg := validProductionBackendConfig()
	cfg.Deployment.Profile = config.DeploymentProfileStaging
	cfg.Database.Host = "db.example.test"
	cfg.Database.SSLMode = "disable"

	err := cfg.validate()
	require.ErrorContains(t, err, "database.ssl_mode")
}

func TestBackendConfigValidateRejectsSMTPPlaceholderNoReplyAddressInNonLocalDeployment(t *testing.T) {
	t.Parallel()

	cfg := validProductionBackendConfig()
	cfg.SMTP.NoReplyAddress = defaultNoReplyAddress

	err := cfg.validate()
	require.ErrorContains(t, err, "smtp.no_reply_address must not use the repository default")
}

func TestBackendConfigValidateRejectsUnsafeNonLocalDeployment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mutate        func(*Config)
		wantErrSubstr string
	}{
		{
			name:          "invalid deployment profile",
			mutate:        func(cfg *Config) { cfg.Deployment.Profile = "prod" },
			wantErrSubstr: "deployment.profile must be one of local, staging, production",
		},
		{
			name:          "local database host",
			mutate:        func(cfg *Config) { cfg.Database.Host = "localhost" },
			wantErrSubstr: "database.host must not use a local",
		},
		{
			name:          "default database credentials",
			mutate:        func(cfg *Config) { cfg.Database.User, cfg.Database.Password = "postgres", "postgres" },
			wantErrSubstr: "must not use postgres/postgres defaults",
		},
		{
			name:          "database tls disabled",
			mutate:        func(cfg *Config) { cfg.Database.SSLMode = "disable" },
			wantErrSubstr: "database.ssl_mode must be require, verify-ca, or verify-full",
		},
		{
			name:          "insecure session cookie",
			mutate:        func(cfg *Config) { cfg.Session.CookieSecure = false },
			wantErrSubstr: "session.cookie_secure must be true",
		},
		{
			name:          "MissingDrainTimeout",
			mutate:        func(cfg *Config) { cfg.Server.DrainTimeout = 0 },
			wantErrSubstr: "server.drain_timeout must be greater than 0 in non-local deployment profiles",
		},
		{
			name:          "http frontend url",
			mutate:        func(cfg *Config) { cfg.FrontendURL = "http://app.example.test" },
			wantErrSubstr: "frontend_url must use https://",
		},
		{
			name:          "local frontend url",
			mutate:        func(cfg *Config) { cfg.FrontendURL = "https://localhost:3002" },
			wantErrSubstr: "frontend_url must not use a local",
		},
		{
			name:          "private frontend url",
			mutate:        func(cfg *Config) { cfg.FrontendURL = "https://10.0.0.5" },
			wantErrSubstr: "frontend_url must not use a local, private",
		},
		{
			name:          "oauth redirect origin mismatch",
			mutate:        func(cfg *Config) { cfg.Auth.GoogleRedirectURI = "https://auth.example.test/api/auth/google/callback" },
			wantErrSubstr: "auth.google_redirect_uri must use the same origin",
		},
		{
			name:          "local cors origin",
			mutate:        func(cfg *Config) { cfg.CORS.AllowedOrigins = []string{"https://localhost:3002"} },
			wantErrSubstr: "cors.allowed_origins[0] must not use a local",
		},
		{
			name:          "private cors origin",
			mutate:        func(cfg *Config) { cfg.CORS.AllowedOrigins = []string{"https://192.168.1.10"} },
			wantErrSubstr: "cors.allowed_origins[0] must not use a local, private",
		},
		{
			name: "multiple cors origins",
			mutate: func(cfg *Config) {
				cfg.CORS.AllowedOrigins = []string{
					"https://app.example.test",
					"https://docs.example.test",
				}
			},
			wantErrSubstr: "cors.allowed_origins must contain exactly one origin",
		},
		{
			name:          "cors origin differs from frontend",
			mutate:        func(cfg *Config) { cfg.CORS.AllowedOrigins = []string{"https://example.test"} },
			wantErrSubstr: "cors.allowed_origins must match the frontend_url origin",
		},
		{
			name:          "insecure smtp tls",
			mutate:        func(cfg *Config) { cfg.SMTP.Insecure = true },
			wantErrSubstr: "smtp.insecure_skip_verify must be false",
		},
		{
			name:          "local smtp host",
			mutate:        func(cfg *Config) { cfg.SMTP.Host = "localhost:587" },
			wantErrSubstr: "smtp.host must not use a local",
		},
		{
			name:          "development smtp no reply",
			mutate:        func(cfg *Config) { cfg.SMTP.NoReplyAddress = "no-reply@example.test" },
			wantErrSubstr: "smtp.no_reply_address must not use a development placeholder",
		},
		{
			name:          "http body logging",
			mutate:        func(cfg *Config) { cfg.Logging.LogHTTPRequests = true },
			wantErrSubstr: "logging.log_http_requests must be false",
		},
		{
			name:          "debug logging",
			mutate:        func(cfg *Config) { cfg.Logging.Level = "debug" },
			wantErrSubstr: "logging.level must not be debug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validProductionBackendConfig()
			tt.mutate(cfg)

			err := cfg.validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErrSubstr)
		})
	}
}

func TestBackendConfigValidateRejectsInvalidCORSOrigins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		origins       []string
		wantErrSubstr string
	}{
		{
			name:          "empty list",
			origins:       nil,
			wantErrSubstr: "at least one CORS origin is required",
		},
		{
			name:          "empty origin",
			origins:       []string{""},
			wantErrSubstr: "CORS origin cannot be empty",
		},
		{
			name:          "missing scheme",
			origins:       []string{"localhost:3000"},
			wantErrSubstr: "must include http:// or https://",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validBackendConfig()
			cfg.CORS.AllowedOrigins = tt.origins

			err := cfg.validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErrSubstr)
		})
	}
}

func TestBackendConfigValidateRejectsInvalidExecutionLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mutate        func(*Config)
		wantErrSubstr string
	}{
		{
			name:          "query timeout",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.QueryTimeout = 0 },
			wantErrSubstr: "execution_limits.query_timeout must be greater than 0",
		},
		{
			name:          "connector timeout",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.ConnectorTimeout = 0 },
			wantErrSubstr: "execution_limits.connector_timeout must be greater than 0",
		},
		{
			name:          "max result rows",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.MaxResultRows = 0 },
			wantErrSubstr: "execution_limits.max_result_rows must be greater than 0",
		},
		{
			name:          "max response bytes",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.MaxResponseBytes = 0 },
			wantErrSubstr: "execution_limits.max_response_bytes must be greater than 0",
		},
		{
			name:          "connector concurrency",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.ConnectorConcurrencyPerDataSource = 0 },
			wantErrSubstr: "execution_limits.connector_concurrency_per_data_source must be greater than 0",
		},
		{
			name:          "connector concurrency queue timeout",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.ConnectorConcurrencyQueueTimeout = 0 },
			wantErrSubstr: "execution_limits.connector_concurrency_queue_timeout must be greater than 0",
		},
		{
			name:          "connector concurrency max queued",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.ConnectorConcurrencyMaxQueued = 0 },
			wantErrSubstr: "execution_limits.connector_concurrency_max_queued_per_data_source must be greater than 0",
		},
		{
			name:          "http timeout",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.HTTPTimeout = 0 },
			wantErrSubstr: "execution_limits.http_timeout must be greater than 0",
		},
		{
			name:          "http redirects",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.HTTPMaxRedirects = 0 },
			wantErrSubstr: "execution_limits.http_max_redirects must be greater than 0",
		},
		{
			name:          "http request bytes",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.HTTPMaxRequestBytes = 0 },
			wantErrSubstr: "execution_limits.http_max_request_bytes must be greater than 0",
		},
		{
			name:          "http response bytes",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.HTTPMaxResponseBytes = 0 },
			wantErrSubstr: "execution_limits.http_max_response_bytes must be greater than 0",
		},
		{
			name:          "http retry attempts",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.HTTPRetryAttempts = 0 },
			wantErrSubstr: "execution_limits.http_retry_attempts must be greater than 0",
		},
		{
			name:          "http retry backoff",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.HTTPRetryBackoff = config.NewDuration(-1) },
			wantErrSubstr: "execution_limits.http_retry_backoff must be greater than or equal to 0",
		},
		{
			name:          "transform timeout",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.TransformTimeout = 0 },
			wantErrSubstr: "execution_limits.transform_timeout must be greater than 0",
		},
		{
			name:          "transform max work",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.TransformMaxWork = 0 },
			wantErrSubstr: "execution_limits.transform_max_work must be greater than 0",
		},
		{
			name:          "transform intermediate rows",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.TransformMaxIntermediateRows = 0 },
			wantErrSubstr: "execution_limits.transform_max_intermediate_rows must be greater than 0",
		},
		{
			name:          "transform intermediate bytes",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.TransformMaxIntermediateBytes = 0 },
			wantErrSubstr: "execution_limits.transform_max_intermediate_bytes must be greater than 0",
		},
		{
			name:          "transform output rows",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.TransformMaxOutputRows = 0 },
			wantErrSubstr: "execution_limits.transform_max_output_rows must be greater than 0",
		},
		{
			name:          "transform output bytes",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.TransformMaxOutputBytes = 0 },
			wantErrSubstr: "execution_limits.transform_max_output_bytes must be greater than 0",
		},
		{
			name:          "transform depth",
			mutate:        func(cfg *Config) { cfg.ExecutionLimits.TransformMaxDepth = 0 },
			wantErrSubstr: "execution_limits.transform_max_depth must be greater than 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validBackendConfig()
			tt.mutate(cfg)

			err := cfg.validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErrSubstr)
		})
	}
}

// TestBackendConfigValidateRejectsInvalidWorkerConcurrency verifies every River queue has positive per-process capacity.
func TestBackendConfigValidateRejectsInvalidWorkerConcurrency(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mutate        func(*Config)
		wantErrSubstr string
	}{
		{name: "PipelineGit", mutate: func(cfg *Config) { cfg.Worker.PipelineGitMaxWorkers = 0 }, wantErrSubstr: "worker.pipeline_git_max_workers"},
		{name: "Email", mutate: func(cfg *Config) { cfg.Worker.EmailMaxWorkers = 0 }, wantErrSubstr: "worker.email_max_workers"},
		{name: "NotificationDelivery", mutate: func(cfg *Config) { cfg.Worker.NotificationDeliveryMaxWorkers = 0 }, wantErrSubstr: "worker.notification_delivery_max_workers"},
		{name: "Events", mutate: func(cfg *Config) { cfg.Worker.EventsMaxWorkers = 0 }, wantErrSubstr: "worker.events_max_workers"},
		{name: "EventMaintenance", mutate: func(cfg *Config) { cfg.Worker.EventMaintenanceMaxWorkers = 0 }, wantErrSubstr: "worker.event_maintenance_max_workers"},
		{name: "PipelineSchedule", mutate: func(cfg *Config) { cfg.Worker.PipelineScheduleMaxWorkers = 0 }, wantErrSubstr: "worker.pipeline_schedule_max_workers"},
		{name: "PipelineRetry", mutate: func(cfg *Config) { cfg.Worker.PipelineRetryMaxWorkers = 0 }, wantErrSubstr: "worker.pipeline_retry_max_workers"},
		{name: "PipelineRun", mutate: func(cfg *Config) { cfg.Worker.PipelineRunMaxWorkers = 0 }, wantErrSubstr: "worker.pipeline_run_max_workers"},
		{name: "DataSourceSchemaImport", mutate: func(cfg *Config) { cfg.Worker.DataSourceSchemaImportMaxWorkers = 0 }, wantErrSubstr: "worker.data_source_schema_import_max_workers"},
		{name: "Execution", mutate: func(cfg *Config) { cfg.Worker.ExecutionMaxWorkers = 0 }, wantErrSubstr: "worker.execution_max_workers"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validBackendConfig()
			tt.mutate(cfg)

			err := cfg.validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErrSubstr)
		})
	}
}

func TestBackendConfigValidateRejectsHubSpotBaseURLAndEncryptionKeyProblems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mutate        func(*Config)
		wantErrSubstr string
	}{
		{
			name:          "hubspot base url missing scheme",
			mutate:        func(cfg *Config) { cfg.HubSpot.BaseURL = "api.hubspot.test" },
			wantErrSubstr: "must include http:// or https://",
		},
		{
			name:          "bigquery base url missing scheme",
			mutate:        func(cfg *Config) { cfg.BigQuery.BaseURL = "bigquery.test" },
			wantErrSubstr: "bigquery.base_url",
		},
		{
			name:          "bigquery token url missing scheme",
			mutate:        func(cfg *Config) { cfg.BigQuery.TokenURL = "oauth.test/token" },
			wantErrSubstr: "bigquery.token_url",
		},
		{
			name:          "bigquery base url missing host",
			mutate:        func(cfg *Config) { cfg.BigQuery.BaseURL = "https:///bigquery" },
			wantErrSubstr: "bigquery.base_url",
		},
		{
			name:          "empty encryption key",
			mutate:        func(cfg *Config) { cfg.Security.EncryptionKeyHex = "" },
			wantErrSubstr: "security.encryption_key_hex must be a 64-character hex string",
		},
		{
			name:          "short encryption key",
			mutate:        func(cfg *Config) { cfg.Security.EncryptionKeyHex = "abcd" },
			wantErrSubstr: "security.encryption_key_hex must be a 64-character hex string",
		},
		{
			name:          "non-hex encryption key",
			mutate:        func(cfg *Config) { cfg.Security.EncryptionKeyHex = strings.Repeat("z", 64) },
			wantErrSubstr: "security.encryption_key_hex must be a 64-character hex string",
		},
		{
			name:          "all-zero placeholder encryption key",
			mutate:        func(cfg *Config) { cfg.Security.EncryptionKeyHex = strings.Repeat("0", 64) },
			wantErrSubstr: "must not use an all-zero placeholder key",
		},
		{
			name:          "repository default encryption key",
			mutate:        func(cfg *Config) { cfg.Security.EncryptionKeyHex = defaultEncryptionKeyHex },
			wantErrSubstr: "must not use the repository default key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validBackendConfig()
			tt.mutate(cfg)

			err := cfg.validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErrSubstr)
		})
	}
}

// TestValidateHubSpotOAuthRequiresCompleteSafeConfiguration rejects incomplete or unsafe HubSpot OAuth configuration.
func TestValidateHubSpotOAuthRequiresCompleteSafeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  HubSpotConfig
		profile string
		wantErr string
	}{
		{name: "AllowsUnset", config: HubSpotConfig{}, profile: config.DeploymentProfileLocal},
		{name: "AllowsLocalBaseURLOverride", config: HubSpotConfig{OAuthBaseURL: "http://localhost:8080"}, profile: config.DeploymentProfileLocal},
		{name: "RejectsNonLocalBaseURLOverride", config: HubSpotConfig{OAuthBaseURL: "https://hubspot.example.test"}, profile: config.DeploymentProfileStaging, wantErr: "only supported in the local deployment profile"},
		{name: "RejectsProductionBaseURLOverride", config: HubSpotConfig{OAuthBaseURL: "https://hubspot.example.test"}, profile: config.DeploymentProfileProduction, wantErr: "only supported in the local deployment profile"},
		{name: "RejectsPartial", config: HubSpotConfig{OAuthClientID: "id"}, profile: config.DeploymentProfileLocal, wantErr: "configured together"},
		{name: "RejectsWrongCallback", config: HubSpotConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/callback"}, profile: config.DeploymentProfileLocal, wantErr: "must end with /provider-connectors/hubspot/callback"},
		{name: "RejectsNonLocalHTTP", config: HubSpotConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/provider-connectors/hubspot/callback"}, profile: config.DeploymentProfileProduction, wantErr: "must use https"},
		{name: "AllowsLocalCallback", config: HubSpotConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/provider-connectors/hubspot/callback"}, profile: config.DeploymentProfileLocal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateHubSpotOAuth(tt.config, tt.profile)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateGitLabOAuthRequiresCompleteSafeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  GitLabConfig
		profile string
		wantErr string
	}{
		{name: "AllowsUnset", config: GitLabConfig{}, profile: config.DeploymentProfileLocal},
		{name: "AllowsLocalBaseURLOverride", config: GitLabConfig{OAuthBaseURL: "http://localhost:8080"}, profile: config.DeploymentProfileLocal},
		{name: "RejectsPartial", config: GitLabConfig{OAuthClientID: "id"}, profile: config.DeploymentProfileLocal, wantErr: "configured together"},
		{name: "RejectsWrongCallback", config: GitLabConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/callback"}, profile: config.DeploymentProfileLocal, wantErr: "must end with /provider-connectors/gitlab/callback"},
		{name: "RejectsNonLocalHTTP", config: GitLabConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/provider-connectors/gitlab/callback"}, profile: config.DeploymentProfileProduction, wantErr: "must use https"},
		{name: "AllowsLocalCallback", config: GitLabConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/provider-connectors/gitlab/callback"}, profile: config.DeploymentProfileLocal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateGitLabOAuth(tt.config, tt.profile)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func validBackendConfig() *Config {
	cfg := defaults()
	cfg.Security.EncryptionKeyHex = testEncryptionKeyHex
	cfg.GitHubProof = validGitHubProofConfig()
	return cfg
}

func validProductionBackendConfig() *Config {
	cfg := validBackendConfig()
	cfg.Deployment.Profile = config.DeploymentProfileProduction
	cfg.Database.Host = "db.example.test"
	cfg.Database.User = "braidplane"
	cfg.Database.Password = "secret"
	cfg.Database.Database = "braidplane"
	cfg.Database.SSLMode = "require"
	cfg.Session.CookieSecure = true
	cfg.Server.DrainTimeout = config.NewDuration(5 * time.Second)
	cfg.FrontendURL = "https://app.example.test"
	cfg.Auth.GoogleRedirectURI = "https://app.example.test/api/auth/google/callback"
	cfg.CORS.AllowedOrigins = []string{"https://app.example.test"}
	cfg.SMTP.Host = "smtp.example.test:587"
	cfg.SMTP.Insecure = false
	cfg.SMTP.NoReplyAddress = "no-reply@notifications.example.test"
	cfg.Logging.Level = "info"
	cfg.Logging.LogHTTPRequests = false
	return cfg
}

func TestProofObservationExposureRequiresIndependentKey(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, key string
		valid     bool
	}{
		{"independent", strings.Repeat("04", 32), true},
		{"padded-independent", " " + strings.Repeat("04", 32) + "\n", true},
		{"missing", "", false},
		{"malformed", strings.Repeat("zz", 32), false},
		{"short", "0404", false},
		{"zero", strings.Repeat("00", 32), false},
		{"default", defaultEncryptionKeyHex, false},
		{"credential-key", testEncryptionKeyHex, false},
		{"delivery-key", testExecutionRequestKeyHex, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := validBackendConfig()
			cfg.Security.EncryptionKeyHex = " " + testEncryptionKeyHex + "\n"
			cfg.Security.ExecutionRequestKeyHex = " " + testExecutionRequestKeyHex + "\n"
			cfg.ProofObservations = ProofObservationsConfig{Enabled: true, EvidenceKeyHex: test.key}
			err := cfg.validate()
			if test.valid {
				require.NoError(t, err)
				require.Equal(t, strings.TrimSpace(test.key), cfg.ProofObservations.EvidenceKeyHex)
			} else {
				require.ErrorContains(t, err, "live Proof observations require")
			}
		})
	}
}

func TestProofObservationConfigPrecedenceAndDisabledDefault(t *testing.T) {
	setTestGitHubAppEnv(t)
	cfg := validBackendConfig()
	require.False(t, cfg.ProofObservations.Enabled)
	require.Empty(t, cfg.ProofObservations.EvidenceKeyHex)
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`[security]
encryption_key_hex = "`+testEncryptionKeyHex+`"
[proof_observations]
enabled = false
evidence_key_hex = "toml-key"
`), 0o600))
	t.Setenv("PROOF_OBSERVATIONS_ENABLED", "true")
	t.Setenv("PROOF_OBSERVATIONS_EVIDENCE_KEY_HEX", "env-key")
	loaded, err := LoadWithArgs(path, []string{"--proof-observations-enabled=false", "--proof-observations-evidence-key-hex=flag-key"})
	require.NoError(t, err)
	require.False(t, loaded.ProofObservations.Enabled)
	require.Equal(t, "flag-key", loaded.ProofObservations.EvidenceKeyHex)
	t.Setenv("PROOF_OBSERVATIONS_ENABLED", "false")
	loaded, err = LoadWithArgs(path, nil)
	require.NoError(t, err)
	require.Equal(t, "env-key", loaded.ProofObservations.EvidenceKeyHex)
}

func setTestGitHubAppEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HUBSPOT_BASE_URL", "HUBSPOT_OAUTH_BASE_URL", "GITLAB_OAUTH_BASE_URL", "BIGQUERY_BASE_URL", "BIGQUERY_TOKEN_URL", "ALLOW_PLAINTEXT_CONNECTORS"} {
		t.Setenv(key, "")
	}
	t.Setenv("GITHUB_PROOF_APP_ID", "1")
	t.Setenv("GITHUB_PROOF_CLIENT_ID", "test-client")
	t.Setenv("GITHUB_PROOF_CLIENT_SECRET", "test-secret")
	t.Setenv("GITHUB_PROOF_PRIVATE_KEY_FILE", "/test/github-app.pem")
	t.Setenv("GITHUB_PROOF_CALLBACK_URL", "https://app.example.test/github/callback")
	t.Setenv("GITHUB_PROOF_INSTALLATION_URL", "https://github.com/apps/braidplane-test/installations/new")
	t.Setenv("GITHUB_PROOF_WEBHOOK_SECRET", strings.Repeat("s", 32))
}

func TestValidateBigQueryOAuthRequiresCompleteSafeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  BigQueryConfig
		profile string
		wantErr string
	}{
		{name: "AllowsUnset", config: BigQueryConfig{}, profile: config.DeploymentProfileLocal},
		{name: "RejectsPartial", config: BigQueryConfig{OAuthClientID: "id"}, profile: config.DeploymentProfileLocal, wantErr: "configured together"},
		{name: "RejectsWrongCallback", config: BigQueryConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/callback"}, profile: config.DeploymentProfileLocal, wantErr: "must end with /provider-connectors/bigquery/callback"},
		{name: "RejectsNonLocalHTTP", config: BigQueryConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/provider-connectors/bigquery/callback"}, profile: config.DeploymentProfileProduction, wantErr: "must use https"},
		{name: "AllowsLocalCallback", config: BigQueryConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/provider-connectors/bigquery/callback"}, profile: config.DeploymentProfileLocal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateBigQueryOAuth(tt.config, tt.profile)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestBigQueryOAuthConfigPrecedence(t *testing.T) {
	setTestGitHubAppEnv(t)
	require.Empty(t, validBackendConfig().BigQuery.OAuthClientID)
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`[security]
encryption_key_hex = "`+testEncryptionKeyHex+`"
[bigquery]
oauth_client_id = "toml-client"
oauth_client_secret = "toml-secret"
oauth_redirect_uri = "http://localhost/provider-connectors/bigquery/callback"
`), 0o600))
	loaded, err := LoadWithArgs(path, nil)
	require.NoError(t, err)
	require.Equal(t, "toml-client", loaded.BigQuery.OAuthClientID)
	t.Setenv("BIGQUERY_OAUTH_CLIENT_ID", "env-client")
	t.Setenv("BIGQUERY_OAUTH_CLIENT_SECRET", "env-secret")
	loaded, err = LoadWithArgs(path, nil)
	require.NoError(t, err)
	require.Equal(t, "env-client", loaded.BigQuery.OAuthClientID)
	require.Equal(t, "env-secret", loaded.BigQuery.OAuthClientSecret)
	loaded, err = LoadWithArgs(path, []string{"--bigquery-oauth-client-id=flag-client", "--bigquery-oauth-client-secret=flag-secret"})
	require.NoError(t, err)
	require.Equal(t, "flag-client", loaded.BigQuery.OAuthClientID)
	require.Equal(t, "flag-secret", loaded.BigQuery.OAuthClientSecret)
}

func TestCustomerSnowflakeWIFModes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, issuer, socket string
		errorText            string
	}{
		{name: "customer", issuer: "https://issuer.test", socket: "/run/workload.sock"},
		{name: "issuer required", socket: "/run/workload.sock", errorText: "configured together"},
		{name: "absolute socket", issuer: "https://issuer.test", socket: "relative.sock", errorText: "absolute path"},
		{name: "socket required", issuer: "https://issuer.test", errorText: "configured together"},
		{name: "HTTPS required", issuer: "http://issuer.test", socket: "/run/workload.sock", errorText: "HTTPS issuer origin"},
		{name: "origin required", issuer: "https://issuer.test/path", socket: "/run/workload.sock", errorText: "HTTPS issuer origin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validBackendConfig()
			cfg.Snowflake = SnowflakeConfig{WIFIssuer: tc.issuer, WIFSocketPath: tc.socket}
			err := cfg.validate()
			if tc.errorText != "" {
				require.ErrorContains(t, err, tc.errorText)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCustomerSnowflakeWIFPrecedence(t *testing.T) {
	var cfg struct {
		Snowflake SnowflakeConfig `toml:"snowflake"`
	}
	path := filepath.Join(t.TempDir(), "customer.toml")
	require.NoError(t, os.WriteFile(path, []byte("[snowflake]\nwif_issuer = 'https://toml.test'\n"), 0o600))
	require.NoError(t, config.LoadIntoWithArgs(path, &cfg, nil))
	require.Equal(t, "https://toml.test", cfg.Snowflake.WIFIssuer)
	t.Setenv("SNOWFLAKE_WIF_ISSUER", "https://env.test")
	require.NoError(t, config.LoadIntoWithArgs(path, &cfg, nil))
	require.Equal(t, "https://env.test", cfg.Snowflake.WIFIssuer)
	require.NoError(t, config.LoadIntoWithArgs(path, &cfg, []string{"--snowflake-wif-issuer=https://flag.test"}))
	require.Equal(t, "https://flag.test", cfg.Snowflake.WIFIssuer)
}

func TestWorkspaceOAuthValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		value     GoogleWorkspaceConfig
		profile   string
		errorText string
	}{
		{name: "unconfigured default"},
		{name: "Picker key needs project", value: GoogleWorkspaceConfig{PickerAPIKey: "restricted-browser-key"}, errorText: "configured together"},
		{name: "project needs Picker key", value: GoogleWorkspaceConfig{ProjectNumber: "123"}, errorText: "configured together"},
		{name: "configured OAuth rejects whitespace Picker", value: GoogleWorkspaceConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/provider-connectors/google_workspace/callback", PickerAPIKey: "  "}, profile: config.DeploymentProfileLocal, errorText: "requires a restricted Picker"},
		{name: "invalid project number", value: GoogleWorkspaceConfig{PickerAPIKey: "restricted-browser-key", ProjectNumber: "abc"}, errorText: "only digits"},
		{name: "configured OAuth needs Picker", value: GoogleWorkspaceConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/provider-connectors/google_workspace/callback"}, profile: config.DeploymentProfileLocal, errorText: "requires a restricted Picker"},
		{name: "partial OAuth configuration", value: GoogleWorkspaceConfig{OAuthClientID: "id"}, errorText: "configured together"},
		{name: "wrong callback", value: GoogleWorkspaceConfig{OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/provider-connectors/bigquery/callback"}, errorText: "google_workspace/callback"},
		{name: "local configured", value: GoogleWorkspaceConfig{PickerAPIKey: "restricted-browser-key", ProjectNumber: "123", OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/provider-connectors/google_workspace/callback"}, profile: config.DeploymentProfileLocal},
		{name: "nonlocal insecure", value: GoogleWorkspaceConfig{PickerAPIKey: "restricted-browser-key", ProjectNumber: "123", OAuthClientID: "id", OAuthClientSecret: "secret", OAuthRedirectURI: "http://localhost/provider-connectors/google_workspace/callback"}, profile: config.DeploymentProfileProduction, errorText: "must use https"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGoogleWorkspaceOAuth(&tc.value, tc.profile)
			if tc.errorText == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.errorText)
			}
		})
	}
}

func TestWorkspacePickerSettingsAreNormalized(t *testing.T) {
	workspace := GoogleWorkspaceConfig{PickerAPIKey: " restricted-browser-key ", ProjectNumber: " 123 "}
	require.NoError(t, validateGoogleWorkspaceOAuth(&workspace, config.DeploymentProfileLocal))
	require.Equal(t, "restricted-browser-key", workspace.PickerAPIKey)
	require.Equal(t, "123", workspace.ProjectNumber)
}

func TestWorkspaceOAuthConfigPrecedence(t *testing.T) {
	setTestGitHubAppEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`[security]
encryption_key_hex = "`+testEncryptionKeyHex+`"
[google_workspace]
oauth_client_id = "local-id"
oauth_client_secret = "local-secret"
oauth_redirect_uri = "http://localhost/provider-connectors/google_workspace/callback"
picker_api_key = "restricted-browser-key"
project_number = "123"
`), 0o600))
	loaded, err := LoadWithArgs(path, nil)
	require.NoError(t, err)
	require.Equal(t, "local-id", loaded.GoogleWorkspace.OAuthClientID)
	t.Setenv("GOOGLE_WORKSPACE_OAUTH_CLIENT_ID", "env-id")
	loaded, err = LoadWithArgs(path, nil)
	require.NoError(t, err)
	require.Equal(t, "env-id", loaded.GoogleWorkspace.OAuthClientID)
	loaded, err = LoadWithArgs(path, []string{"--google-workspace-oauth-client-id=flag-id"})
	require.NoError(t, err)
	require.Equal(t, "flag-id", loaded.GoogleWorkspace.OAuthClientID)
}

func TestValidateOptionalHTTPURLPreservesOptionalAndInvalidValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ value, wantError string }{
		{"", ""},
		{"  ", ""},
		{" https://example.test/path ", ""},
		{"http://example.test/path", ""},
		{"https://%zz", "invalid connector.url"},
		{"ftp://example.test/path", "connector.url"},
		{"https:///path", "must include a host"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			err := validateOptionalHTTPURL(tc.value, "connector.url")
			if tc.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantError)
		})
	}
}

func TestHTTPOAuthCallbackConfiguration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, uri, profile string
		valid              bool
	}{
		{"unset", "", config.DeploymentProfileStaging, true},
		{"local TLS", "https://localhost/provider-connectors/http/callback", config.DeploymentProfileLocal, true},
		{"public", "https://api.example.com/provider-connectors/http/callback", config.DeploymentProfileStaging, true},
		{"HTTP", "http://localhost/provider-connectors/http/callback", config.DeploymentProfileLocal, false},
		{"wrong path", "https://api.example.com/callback", config.DeploymentProfileStaging, false},
		{"query", "https://api.example.com/provider-connectors/http/callback?next=x", config.DeploymentProfileStaging, false},
		{"userinfo", "https://user@api.example.com/provider-connectors/http/callback", config.DeploymentProfileLocal, false},
		{"private deployed", "https://127.0.0.1/provider-connectors/http/callback", config.DeploymentProfileStaging, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := (HTTPOAuthConfig{OAuthRedirectURI: tc.uri}).validate(tc.profile)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestHTTPOAuthCallbackPrecedence(t *testing.T) {
	setTestGitHubAppEnv(t)
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte(`[security]
encryption_key_hex = "0202020202020202020202020202020202020202020202020202020202020202"
[http_oauth]
oauth_redirect_uri = "https://toml.example.com/provider-connectors/http/callback"
`), 0o600))
	t.Setenv("HTTP_OAUTH_REDIRECT_URI", "https://env.example.com/provider-connectors/http/callback")
	cfg, err := LoadWithArgs(p, nil)
	require.NoError(t, err)
	require.Equal(t, "https://env.example.com/provider-connectors/http/callback", cfg.HTTPOAuth.OAuthRedirectURI)
	cfg, err = LoadWithArgs(p, []string{"--http-oauth-redirect-uri=https://flag.example.com/provider-connectors/http/callback"})
	require.NoError(t, err)
	require.Equal(t, "https://flag.example.com/provider-connectors/http/callback", cfg.HTTPOAuth.OAuthRedirectURI)
}

func TestRetiredRecencySettingDoesNotChangeConfiguration(t *testing.T) {
	for _, value := range []string{"false", "true"} {
		t.Run(value, func(t *testing.T) {
			cfg := defaults()
			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(path, []byte("[execution]\nsuccessful_run_recency_enabled = "+value+"\n"), 0o600))
			require.NoError(t, config.LoadIntoWithArgs(path, cfg, nil))
			require.Equal(t, defaults().Execution, cfg.Execution)
		})
	}
}

func TestCompletionTriggersConfigurationPrecedence(t *testing.T) {
	cfg := defaults()
	require.False(t, cfg.Execution.CompletionTriggersEnabled)
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[execution]\ncompletion_triggers_enabled = true\n"), 0o600))
	require.NoError(t, config.LoadIntoWithArgs(path, cfg, nil))
	require.True(t, cfg.Execution.CompletionTriggersEnabled)
	t.Setenv("EXECUTION_COMPLETION_TRIGGERS_ENABLED", "false")
	require.NoError(t, config.LoadIntoWithArgs(path, cfg, nil))
	require.False(t, cfg.Execution.CompletionTriggersEnabled)
	require.NoError(t, config.LoadIntoWithArgs(path, cfg, []string{"--execution-completion-triggers-enabled=true"}))
	require.True(t, cfg.Execution.CompletionTriggersEnabled)
}

func TestRetiredCompositionFlagsAreRejected(t *testing.T) {
	for _, name := range []string{"execution-contract-composition-enabled", "proof-operational-contract-evidence-enabled"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(path, nil, 0o600))
			err := config.LoadIntoWithArgs(path, defaults(), []string{"--" + name + "=false"})
			require.ErrorContains(t, err, "flag provided but not defined")
		})
	}
}

func TestConnectorTransportPolicyIsLocalOnlyForEveryBackendRole(t *testing.T) {
	for _, profile := range []string{"local", "staging", "production"} {
		t.Run(profile, func(t *testing.T) {
			cfg := defaults()
			cfg.Deployment.Profile = profile
			cfg.Security.AllowPlaintextConnectors = true
			err := cfg.validateConnectorTransport()
			if profile == "local" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "restricted to the local")
				require.ErrorContains(t, cfg.validate(), "restricted to the local")
				require.ErrorContains(t, cfg.validateExecutor(), "restricted to the local")
			}
		})
	}
	for _, field := range []string{"hubspot", "bigquery", "token"} {
		cfg := defaults()
		switch field {
		case "hubspot":
			cfg.HubSpot.BaseURL = "http://example.test"
		case "bigquery":
			cfg.BigQuery.BaseURL = "http://example.test"
		case "token":
			cfg.BigQuery.TokenURL = "http://example.test"
		}
		require.ErrorContains(t, cfg.validateConnectorTransport(), "requires HTTPS")
		cfg.Security.AllowPlaintextConnectors = true
		require.NoError(t, cfg.validateConnectorTransport())
	}
}

func TestConnectorTransportPolicyAcceptsTrimmedProviderEndpoints(t *testing.T) {
	t.Parallel()
	cfg := defaults()
	cfg.HubSpot.BaseURL = "  https://api.example.test  "
	cfg.BigQuery.BaseURL = "\thttps://query.example.test\n"
	cfg.BigQuery.TokenURL = " https://token.example.test "
	require.NoError(t, cfg.validateConnectorTransport())
}

func TestSelfHostedPrivateOriginsRetainSecurityChecks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{name: "valid", change: func(*Config) {}},
		{name: "broad private bypass", change: func(c *Config) { c.Security.AllowPrivateDataSourceHosts = true }, want: "explicit private destinations"},
		{name: "unbounded logs", change: func(c *Config) { c.Logging.MaxFileSizeMB = 0 }, want: "max_file_size_mb"},
		{name: "database identity", change: func(c *Config) { c.Database.SSLMode = "require" }, want: "verify-full"},
		{name: "cookie", change: func(c *Config) { c.Session.CookieSecure = false }, want: "cookie_secure"},
		{name: "SMTP", change: func(c *Config) { c.SMTP.Insecure = true }, want: "insecure_skip_verify"},
		{name: "CORS", change: func(c *Config) { c.CORS.AllowedOrigins = []string{"https://10.20.0.6"} }, want: "match the frontend_url"},
		{name: "logging", change: func(c *Config) { c.Logging.LogHTTPRequests = true }, want: "log_http_requests"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validProductionBackendConfig()
			cfg.Deployment.Profile = config.DeploymentProfileSelfHosted
			cfg.Database.SSLMode = "verify-full"
			cfg.FrontendURL = "https://10.20.0.5"
			cfg.Auth.GoogleRedirectURI = cfg.FrontendURL + "/api/auth/google/callback"
			cfg.CORS.AllowedOrigins = []string{cfg.FrontendURL}
			cfg.SMTP.Host = "10.20.0.7:587"
			tc.change(cfg)
			err := cfg.validate()
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPrivatePostgresConfiguration(t *testing.T) {
	valid := PrivatePostgresConfig{Organization: "customer", Source: "warehouse", Host: "warehouse.internal", Address: "10.20.0.8:5432"}
	require.NoError(t, valid.Validate(config.DeploymentProfileSelfHosted))
	require.Error(t, valid.Validate(config.DeploymentProfileProduction))
	require.NoError(t, (PrivatePostgresConfig{}).Validate(config.DeploymentProfileProduction))
	for _, tc := range []struct {
		name   string
		change func(*PrivatePostgresConfig)
	}{
		{"organization", func(c *PrivatePostgresConfig) { c.Organization = "" }},
		{"source", func(c *PrivatePostgresConfig) { c.Source = "" }},
		{"host", func(c *PrivatePostgresConfig) { c.Host = "localhost" }},
		{"port in host", func(c *PrivatePostgresConfig) { c.Host = "warehouse.internal:5432" }},
		{"public", func(c *PrivatePostgresConfig) { c.Address = "8.8.8.8:5432" }},
		{"loopback", func(c *PrivatePostgresConfig) { c.Address = "127.0.0.1:5432" }},
		{"metadata", func(c *PrivatePostgresConfig) { c.Address = "169.254.169.254:80" }},
		{"zero port", func(c *PrivatePostgresConfig) { c.Address = "10.20.0.8:0" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.change(&cfg)
			require.Error(t, cfg.Validate(config.DeploymentProfileSelfHosted))
		})
	}
}

func TestExecutorRejectsBroadPrivateDestinationBypass(t *testing.T) {
	cfg := validProductionBackendConfig()
	cfg.Deployment.Profile = config.DeploymentProfileSelfHosted
	cfg.Security.AllowPrivateDataSourceHosts = true
	require.ErrorContains(t, cfg.validateExecutor(), "explicit private destinations")
}
