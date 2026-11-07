package command

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/config"
	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
)

func TestRunFakeRejectsGitFixtureStateWithoutGitOnly(t *testing.T) {
	t.Parallel()

	err := Execute(context.Background(), Options{GitFixtureState: "deleted"}, nil, &bytes.Buffer{})
	require.ErrorContains(t, err, "--git-fixture-state requires --git-only")
}

// TestSplitFakeArgsKeepsCommandFlags verifies backend config flags do not leak into Kong parsing.
func TestSplitFakeArgsKeepsCommandFlags(t *testing.T) {
	t.Parallel()

	configArgs, commandArgs, err := config.ExtractFlagArgs(&backendconfig.Config{}, []string{
		"--db-host",
		"flag-db.example.test",
		"--config",
		"seed.toml",
		"--git-only",
	})

	require.NoError(t, err)
	require.Equal(t, []string{"--db-host", "flag-db.example.test"}, configArgs)
	require.Equal(t, []string{"--config", "seed.toml", "--git-only"}, commandArgs)
}

// TestLoadBackendConfigUsesSharedPrecedence proves fake uses backend config precedence.
func TestLoadBackendConfigUsesSharedPrecedence(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	writeFakeConfig(t, configPath, `[database]
host = "toml-db.example.test"
port = 15430
user = "toml_user"
password = "toml-password"
database = "toml_db"
ssl_mode = "disable"

[security]
encryption_key_hex = "0202020202020202020202020202020202020202020202020202020202020202"

[github_proof]
app_id = 1
client_id = "test-client"
client_secret = "test-secret"
private_key_file = "/test/github-app.pem"
callback_url = "https://app.example.test/github/callback"
installation_url = "https://github.com/apps/braidplane-test/installations/new"
webhook_secret = "test-webhook-secret-not-for-production"
`)
	clearFakeConfigEnv(t)
	t.Setenv("DB_HOST", "env-db.example.test")
	t.Setenv("DB_USER", "env_user")
	t.Setenv("DB_PASSWORD", "env-password")

	cfg, err := backendconfig.LoadWithArgs(configPath, []string{
		"--db-host=flag-db.example.test",
		"--db-port",
		"15432",
	})
	require.NoError(t, err)

	require.Equal(t, "flag-db.example.test", cfg.Database.Host)
	require.Equal(t, 15432, cfg.Database.Port)
	require.Equal(t, "env_user", cfg.Database.User)
	require.Equal(t, "env-password", cfg.Database.Password)
	require.Equal(t, "toml_db", cfg.Database.Database)
	require.Equal(t, "disable", cfg.Database.SSLMode)
}

// TestRunVariants verifies the Kong-owned parser handles early exits before
// the fake seeding path touches database or Git fixtures.
func TestRunVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		args   []string
		assert func(t *testing.T, err error, stdout, stderr string)
	}{
		{
			name: "RejectsUnexpectedArgument",
			args: []string{"unexpected"},
			assert: func(t *testing.T, err error, stdout, stderr string) {
				t.Helper()

				require.Error(t, err)
				require.ErrorContains(t, err, "parse fake seed command")
				require.ErrorContains(t, err, "unexpected")
			},
		},
		{
			name: "HelpWritesKongUsage",
			args: []string{"--help"},
			assert: func(t *testing.T, err error, stdout, stderr string) {
				t.Helper()

				require.NoError(t, err)
				require.Contains(t, stdout, "Usage: fake")
				require.Contains(t, stdout, "--config")
				require.Contains(t, stdout, "--git-only")
				require.NotContains(t, stdout, "--profile")
				require.Empty(t, stderr)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			err := Run(tt.args, &stdout, &stderr)

			tt.assert(t, err, stdout.String(), stderr.String())
		})
	}
}

// clearFakeConfigEnv removes shared backend config environment overrides from command tests.
func clearFakeConfigEnv(t *testing.T) {
	t.Helper()

	for _, envVar := range []string{
		"DEPLOYMENT_PROFILE",
		"SERVER_HOST",
		"SERVER_PORT",
		"SERVER_SHUTDOWN_TIMEOUT",
		"SERVER_DRAIN_TIMEOUT",
		"SERVER_STATIC_CACHE_MAX_AGE",
		"SERVER_PROXY_HEADER",
		"SERVER_TRUSTED_PROXIES",
		"DB_HOST",
		"DB_PORT",
		"DB_USER",
		"DB_PASSWORD",
		"DB_DATABASE",
		"DB_SSL_MODE",
		"DB_MAX_CONNS",
		"DB_MIN_CONNS",
		"DB_MAX_CONN_LIFETIME",
		"DB_MAX_CONN_IDLE_TIME",
		"DB_HEALTH_CHECK_PERIOD",
		"SESSION_TABLE_NAME",
		"SESSION_COOKIE_SECURE",
		"ARGON2_MEMORY",
		"ARGON2_ITERATIONS",
		"ARGON2_PARALLELISM",
		"ARGON2_SALT_LENGTH",
		"ARGON2_KEY_LENGTH",
		"ENCRYPTION_KEY_HEX",
		"GOOGLE_CLIENT_ID",
		"GOOGLE_CLIENT_SECRET",
		"GOOGLE_REDIRECT_URI",
		"SELF_SERVICE_REGISTRATION_ENABLED",
		"CORS_ALLOWED_ORIGINS",
		"HUBSPOT_BASE_URL",
		"HUBSPOT_OAUTH_BASE_URL",
		"GITLAB_OAUTH_BASE_URL",
		"ALLOW_PLAINTEXT_CONNECTORS",
		"HUBSPOT_ALLOW_PRIVATE_HOSTS",
		"BIGQUERY_BASE_URL",
		"BIGQUERY_TOKEN_URL",
		"BIGQUERY_ALLOW_PRIVATE_HOSTS",
		"EXECUTION_LIMITS_QUERY_TIMEOUT",
		"EXECUTION_LIMITS_CONNECTOR_TIMEOUT",
		"EXECUTION_LIMITS_MAX_RESULT_ROWS",
		"EXECUTION_LIMITS_MAX_RESPONSE_BYTES",
		"EXECUTION_LIMITS_CONNECTOR_CONCURRENCY_PER_DATA_SOURCE",
		"EXECUTION_LIMITS_HTTP_TIMEOUT",
		"EXECUTION_LIMITS_HTTP_MAX_REDIRECTS",
		"EXECUTION_LIMITS_HTTP_MAX_REQUEST_BYTES",
		"EXECUTION_LIMITS_HTTP_MAX_RESPONSE_BYTES",
		"EXECUTION_LIMITS_HTTP_RETRY_ATTEMPTS",
		"EXECUTION_LIMITS_HTTP_RETRY_BACKOFF",
		"SMTP_HOST",
		"SMTP_USER",
		"SMTP_PASSWORD",
		"SMTP_CA_CERT",
		"SMTP_CA_CERT_FILE",
		"SMTP_INSECURE_SKIP_VERIFY",
		"SMTP_NO_REPLY_ADDRESS",
		"LOG_LEVEL",
		"LOG_BACKEND_FILE",
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
		"DEFAULT_LANGUAGE",
	} {
		t.Setenv(envVar, "")
	}
}

// writeFakeConfig writes a TOML fixture for fake command config tests.
func writeFakeConfig(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
