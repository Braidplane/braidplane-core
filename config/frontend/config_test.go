package frontend

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/config"
)

func TestCustomSessionTable(t *testing.T) {
	t.Parallel()
	cfg := defaults()
	cfg.Session.TableName = "unmanaged_sessions"
	require.NoError(t, cfg.validate())
}

func TestFrontendURLConfigurationPrecedence(t *testing.T) {
	t.Setenv("FRONTEND_URL", "")
	require.NoError(t, os.Unsetenv("FRONTEND_URL"))

	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(`frontend_url = "https://toml.example.test"`), 0o600))

	tests := []struct {
		name string
		env  string
		args []string
		want string
	}{
		{name: "toml", want: "https://toml.example.test"},
		{name: "environment", env: "https://env.example.test", want: "https://env.example.test"},
		{
			name: "flag",
			env:  "https://env.example.test",
			args: []string{"--frontend-url=https://flag.example.test"},
			want: "https://flag.example.test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("FRONTEND_URL", tt.env)
			}

			cfg := defaults()
			require.NoError(t, config.LoadIntoWithArgs(configPath, cfg, tt.args))
			require.NoError(t, cfg.validate())
			require.Equal(t, tt.want, cfg.FrontendURL)
		})
	}
}

func TestDefaultsAreValidAndNormalizeURLs(t *testing.T) {
	t.Parallel()

	cfg := defaults()
	cfg.FrontendURL = "  https://www.example.test  "
	cfg.BackendAPIURL = "  https://api.example.test  "

	require.NoError(t, cfg.validate())
	require.Equal(t, "https://www.example.test", cfg.FrontendURL)
	require.Equal(t, "https://api.example.test", cfg.BackendAPIURL)
	require.False(t, cfg.Auth.SelfServiceRegistrationEnabled)
	require.Equal(t, "0.0.0.0:3002", cfg.Server.Address())
}

func TestValidateAcceptsNonLocalDeploymentProfile(t *testing.T) {
	t.Parallel()

	cfg := validProductionFrontendConfig()
	cfg.Deployment.Profile = " " + config.DeploymentProfileStaging + " "

	require.NoError(t, cfg.validate())
	require.Equal(t, config.DeploymentProfileStaging, cfg.Deployment.Profile)
}

func TestValidateRejectsPrivateStagingPostgresWithoutTLS(t *testing.T) {
	t.Parallel()

	cfg := validProductionFrontendConfig()
	cfg.Deployment.Profile = config.DeploymentProfileStaging
	cfg.Database.Host = "private-db"
	cfg.Database.SSLMode = "disable"

	require.ErrorContains(t, cfg.validate(), "database.ssl_mode")
}

func TestValidateRejectsStagingPostgresWithoutTLSOutsidePrivateRuntimeNetwork(t *testing.T) {
	t.Parallel()

	cfg := validProductionFrontendConfig()
	cfg.Deployment.Profile = config.DeploymentProfileStaging
	cfg.Database.Host = "db.example.test"
	cfg.Database.SSLMode = "disable"

	err := cfg.validate()
	require.ErrorContains(t, err, "database.ssl_mode")
}

func TestValidateRejectsUnsafeNonLocalDeployment(t *testing.T) {
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
			mutate:        func(cfg *Config) { cfg.Database.Host = "127.0.0.1" },
			wantErrSubstr: "database.host must not use a local",
		},
		{
			name:          "database tls disabled",
			mutate:        func(cfg *Config) { cfg.Database.SSLMode = "disable" },
			wantErrSubstr: "database.ssl_mode must be require, verify-ca, or verify-full",
		},
		{
			name:          "insecure session cookie",
			mutate:        func(cfg *Config) { cfg.Session.CookieSecure = false },
			wantErrSubstr: "frontend_session.cookie_secure must be true",
		},
		{
			name:          "MissingDrainTimeout",
			mutate:        func(cfg *Config) { cfg.Server.DrainTimeout = 0 },
			wantErrSubstr: "frontend_server.drain_timeout must be greater than 0 in non-local deployment profiles",
		},
		{
			name:          "local backend api url",
			mutate:        func(cfg *Config) { cfg.BackendAPIURL = "http://localhost:3001" },
			wantErrSubstr: "backend_api_url must not use a local",
		},
		{
			name:          "insecure documentation url",
			mutate:        func(cfg *Config) { cfg.DocumentationURL = "http://docs.example.test" },
			wantErrSubstr: "documentation_url must use https://",
		},
		{
			name:          "local documentation url",
			mutate:        func(cfg *Config) { cfg.DocumentationURL = "https://localhost" },
			wantErrSubstr: "documentation_url must not use a local",
		},
		{
			name:          "insecure frontend url",
			mutate:        func(cfg *Config) { cfg.FrontendURL = "http://www.example.test" },
			wantErrSubstr: "frontend_url must use https://",
		},
		{
			name:          "local frontend url",
			mutate:        func(cfg *Config) { cfg.FrontendURL = "https://localhost:3002" },
			wantErrSubstr: "frontend_url must not use a local",
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

			cfg := validProductionFrontendConfig()
			tt.mutate(cfg)

			err := cfg.validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErrSubstr)
		})
	}
}

func TestValidateRejectsInvalidFrontendOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		frontendURL   string
		wantErrSubstr string
	}{
		{name: "empty", frontendURL: "   ", wantErrSubstr: "frontend_url is required"},
		{name: "path", frontendURL: "https://www.example.test/app", wantErrSubstr: "must be an origin"},
		{name: "query", frontendURL: "https://www.example.test?tenant=acme", wantErrSubstr: "must be an origin"},
		{name: "fragment", frontendURL: "https://www.example.test#landing", wantErrSubstr: "must be an origin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := defaults()
			cfg.FrontendURL = tt.frontendURL

			err := cfg.validate()
			require.ErrorContains(t, err, tt.wantErrSubstr)
		})
	}
}

func TestValidateRejectsInvalidBackendAPIURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		backendURL    string
		wantErrSubstr string
	}{
		{
			name:          "empty",
			backendURL:    "   ",
			wantErrSubstr: "backend_api_url is required",
		},
		{
			name:          "missing scheme",
			backendURL:    "localhost:3001",
			wantErrSubstr: "must include http:// or https://",
		},
		{
			name:          "missing host",
			backendURL:    "https://",
			wantErrSubstr: "must include a host",
		},
		{
			name:          "empty host with port",
			backendURL:    "http://:3001",
			wantErrSubstr: "must include a host",
		},
		{
			name:          "unsupported scheme",
			backendURL:    "ftp://api.example.test",
			wantErrSubstr: "must include http:// or https://",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := defaults()
			cfg.BackendAPIURL = tt.backendURL

			err := cfg.validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErrSubstr)
		})
	}
}

func validProductionFrontendConfig() *Config {
	cfg := defaults()
	cfg.Deployment.Profile = "production"
	cfg.Database.Host = "db.example.test"
	cfg.Database.User = "braidplane"
	cfg.Database.Password = "secret"
	cfg.Database.Database = "braidplane"
	cfg.Database.SSLMode = "require"
	cfg.Session.CookieSecure = true
	cfg.Server.DrainTimeout = config.NewDuration(5 * time.Second)
	cfg.FrontendURL = "https://www.example.test"
	cfg.BackendAPIURL = "http://backend.internal:3001"
	cfg.DocumentationURL = "https://docs.example.test/"
	cfg.Logging.Level = "info"
	cfg.Logging.LogHTTPRequests = false
	return cfg
}

func TestDocumentationURLConfigurationPrecedence(t *testing.T) {
	t.Setenv("DOCUMENTATION_URL", "")
	require.NoError(t, os.Unsetenv("DOCUMENTATION_URL"))

	configPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(`documentation_url = "https://toml.example.test"`), 0o600))

	tests := []struct {
		name string
		env  string
		args []string
		want string
	}{
		{name: "toml", want: "https://toml.example.test"},
		{name: "environment", env: "https://env.example.test", want: "https://env.example.test"},
		{
			name: "flag",
			env:  "https://env.example.test",
			args: []string{"--documentation-url=https://flag.example.test"},
			want: "https://flag.example.test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("DOCUMENTATION_URL", tt.env)
			}

			cfg := defaults()
			require.NoError(t, config.LoadIntoWithArgs(configPath, cfg, tt.args))
			require.NoError(t, cfg.validate())
			require.Equal(t, tt.want, cfg.DocumentationURL)
		})
	}
}

func TestDocumentationURLValidation(t *testing.T) {
	for _, value := range []string{"javascript:alert(1)", "https://user:password@docs.example.test", "/docs"} {
		t.Run(value, func(t *testing.T) {
			cfg := defaults()
			cfg.DocumentationURL = value
			require.Error(t, cfg.validate())
		})
	}
}

func TestLoadWithArgsUsesIsolatedOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frontend.toml")
	require.NoError(t, os.WriteFile(path, []byte("[frontend_server]\nport = 3101\n"), 0o600))
	t.Setenv("FRONTEND_SERVER_PORT", "3102")
	cfg, err := LoadWithArgs(path, []string{"--frontend-server-port=3103"})
	require.NoError(t, err)
	require.Equal(t, 3103, cfg.Server.Port)
}

func TestSelfHostedPrivateApplicationAndDocumentation(t *testing.T) {
	cfg := validProductionFrontendConfig()
	cfg.Deployment.Profile = config.DeploymentProfileSelfHosted
	cfg.Database.SSLMode = "verify-full"
	cfg.FrontendURL = "https://10.20.0.5"
	cfg.DocumentationURL = "https://10.20.0.8/docs"
	require.NoError(t, cfg.validate())
	cfg.Session.CookieSecure = false
	require.ErrorContains(t, cfg.validate(), "cookie_secure")
}

func TestSelfHostedRejectsBroadPrivateSourceControls(t *testing.T) {
	cfg := validProductionFrontendConfig()
	cfg.Deployment.Profile = config.DeploymentProfileSelfHosted
	cfg.Database.SSLMode = "verify-full"
	cfg.Security.AllowPrivateDataSourceHosts = true
	require.ErrorContains(t, cfg.validate(), "explicit private destinations")
}
