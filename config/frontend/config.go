package frontend

import (
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/config"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	defaultFrontendServerPort = 3002
	defaultFrontendURL        = "http://localhost:3002"
	defaultBackendAPIURL      = "http://localhost:3001"
)

// Config represents the standalone frontend runtime configuration.
type Config struct {
	Deployment config.DeploymentConfig `toml:"deployment"`
	Server     ServerConfig            `toml:"frontend_server"`
	Database   DatabaseConfig          `toml:"database"`
	Session    SessionConfig           `toml:"frontend_session"`
	Security   SecurityConfig          `toml:"security"`
	Auth       AuthConfig              `toml:"auth"`

	Logging          LoggingConfig        `toml:"logging"`
	Tracing          config.TracingConfig `toml:"tracing"`
	Metrics          config.MetricsConfig `toml:"metrics"`
	DocumentationURL string               `toml:"documentation_url" env:"DOCUMENTATION_URL" flag:"documentation-url" desc:"Public documentation URL (empty hides navigation links)"`
	FrontendURL      string               `toml:"frontend_url"    env:"FRONTEND_URL"    flag:"frontend-url"    desc:"Canonical browser application origin"`

	BackendAPIURL string `toml:"backend_api_url" env:"BACKEND_API_URL" flag:"backend-api-url" desc:"Backend API base URL used by the standalone frontend server"`
}

// ServerConfig contains standalone frontend HTTP server configuration.
type ServerConfig struct {
	Host              string          `toml:"host"                 env:"FRONTEND_SERVER_HOST"                 flag:"frontend-server-host"                 desc:"Frontend server host"`
	Port              int             `toml:"port"                 env:"FRONTEND_SERVER_PORT"                 flag:"frontend-server-port"                 desc:"Frontend server port"`
	ShutdownTimeout   config.Duration `toml:"shutdown_timeout"     env:"FRONTEND_SERVER_SHUTDOWN_TIMEOUT"     flag:"frontend-server-shutdown-timeout"     desc:"Frontend graceful shutdown timeout (e.g., 30s, 1m)"`
	DrainTimeout      config.Duration `toml:"drain_timeout"        env:"FRONTEND_SERVER_DRAIN_TIMEOUT"        flag:"frontend-server-drain-timeout"        desc:"Frontend readiness drain window before graceful shutdown starts (e.g., 5s)"`
	StaticCacheMaxAge uint            `toml:"static_cache_max_age" env:"FRONTEND_SERVER_STATIC_CACHE_MAX_AGE" flag:"frontend-server-static-cache-max-age" desc:"Max-Age for successful frontend assets whose version matches the file digest (0 = no-store)"`
	ProxyHeader       string          `toml:"proxy_header"         env:"FRONTEND_SERVER_PROXY_HEADER"         flag:"frontend-server-proxy-header"         desc:"Frontend trusted reverse proxy client IP header"`
	TrustedProxies    []string        `toml:"trusted_proxies"      env:"FRONTEND_SERVER_TRUSTED_PROXIES"      flag:"frontend-server-trusted-proxies"      desc:"Comma-separated frontend trusted reverse proxy IPs or CIDRs"`
}

// DatabaseConfig contains standalone frontend database connection configuration.
type DatabaseConfig = config.DatabaseConfig

// SessionConfig contains standalone frontend session storage configuration.
type SessionConfig struct {
	TableName    string `toml:"table_name"    env:"FRONTEND_SESSION_TABLE_NAME"    flag:"frontend-session-table-name"    desc:"Frontend session table name"`
	CookieSecure bool   `toml:"cookie_secure" env:"FRONTEND_SESSION_COOKIE_SECURE" flag:"frontend-session-cookie-secure" desc:"Set Secure flag on frontend session cookies"`
}

// SecurityConfig contains standalone frontend security display configuration.
type SecurityConfig struct {
	AllowPrivateDataSourceHosts bool `toml:"allow_private_data_source_hosts" env:"ALLOW_PRIVATE_DATA_SOURCE_HOSTS" flag:"allow-private-data-source-hosts" desc:"Show customer data source controls that opt into private or loopback hosts"`
}

// AuthConfig contains standalone frontend authentication display configuration.
type AuthConfig struct {
	SelfServiceRegistrationEnabled bool `toml:"self_service_registration_enabled" env:"SELF_SERVICE_REGISTRATION_ENABLED" flag:"self-service-registration-enabled" desc:"Show public signup affordances for email/password and first-time OIDC registration"`
}

// LoggingConfig contains standalone frontend logging configuration.
type LoggingConfig struct {
	Level            string          `toml:"level"             env:"LOG_LEVEL"             flag:"log-level"             desc:"Log level (none, debug, info, warn, error)"`
	File             string          `toml:"frontend_file"     env:"LOG_FRONTEND_FILE"     flag:"log-frontend-file"     desc:"Frontend log file path (empty disables file logging)"`
	RotationInterval config.Duration `toml:"rotation_interval" env:"LOG_ROTATION_INTERVAL" flag:"log-rotation-interval" desc:"Log file rotation interval (e.g., 1h, 24h)"`
	LogHTTPRequests  bool            `toml:"log_http_requests" env:"LOG_HTTP_REQUESTS"     flag:"log-http-requests"     desc:"Log full HTTP request/response bodies"`
	MaxFileSizeMB    int             `toml:"max_file_size_mb"  env:"LOG_MAX_FILE_SIZE_MB"  flag:"log-max-file-size-mb"  desc:"Maximum log file size in MB before rotation"`
	Environment      string          `toml:"environment"       env:"LOG_ENVIRONMENT"       flag:"log-environment"       desc:"Deployment environment for structured logs"`
	OTLPLogsEndpoint string          `toml:"otlp_logs_endpoint" env:"LOG_OTLP_LOGS_ENDPOINT" flag:"log-otlp-logs-endpoint" desc:"Full OTLP/HTTP logs endpoint (empty disables direct log export)"`
}

// Load reads and parses standalone frontend configuration.
func Load(path string) (*Config, error) {
	return load(path, func(path string, cfg *Config) error { return config.LoadInto(path, cfg) })
}

// LoadWithArgs reads frontend settings through an isolated command-line flag set.
func LoadWithArgs(path string, args []string) (*Config, error) {
	return load(path, func(path string, cfg *Config) error { return config.LoadIntoWithArgs(path, cfg, args) })
}

func load(path string, loadInto func(string, *Config) error) (*Config, error) {
	cfg := defaults()
	if err := loadInto(path, cfg); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, xerror.Wrapf("invalid configuration: %w", err)
	}

	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Deployment: config.DeploymentConfig{
			Profile: config.DeploymentProfileLocal,
		},
		Server: ServerConfig{
			Host:            "0.0.0.0",
			Port:            defaultFrontendServerPort,
			ShutdownTimeout: config.NewDuration(30 * time.Second),
		},
		Database: config.DefaultDatabaseConfig(),
		Session: SessionConfig{
			TableName:    "user_sessions",
			CookieSecure: true,
		},
		Security: SecurityConfig{
			AllowPrivateDataSourceHosts: false,
		},
		Auth: AuthConfig{
			SelfServiceRegistrationEnabled: false,
		},
		Logging: LoggingConfig{
			Level:            "debug",
			File:             "logs/frontend.log",
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
		FrontendURL:   defaultFrontendURL,
		BackendAPIURL: defaultBackendAPIURL,
	}
}

// Address returns the frontend server address string.
func (s *ServerConfig) Address() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

func (c *Config) validate() error {
	if err := config.ValidateDeploymentProfile(&c.Deployment.Profile); err != nil {
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
		"frontend_server",
		&c.Server.ProxyHeader,
		c.Server.TrustedProxies,
		func(i int, value string) {
			c.Server.TrustedProxies[i] = value
		},
	); err != nil {
		return err
	}
	if c.Server.ShutdownTimeout <= 0 {
		return xerror.Unexpected().Detail("frontend_server.shutdown_timeout must be greater than 0")
	}
	if c.Server.DrainTimeout < 0 {
		return xerror.Unexpected().Detail(
			"frontend_server.drain_timeout must be greater than or equal to 0",
		)
	}
	frontendURL, err := config.ValidateHTTPOrigin(c.FrontendURL, "frontend_url")
	if err != nil {
		return err
	}
	c.FrontendURL = frontendURL.String()
	c.DocumentationURL = strings.TrimSpace(c.DocumentationURL)
	if c.DocumentationURL != "" {
		documentationURL, err := config.ValidateHTTPURL(c.DocumentationURL, "documentation_url")
		if err != nil {
			return err
		}
		if documentationURL.User != nil {
			return xerror.Unexpected().Detail("documentation_url must not contain credentials")
		}
		if config.IsNonLocalDeploymentProfile(c.Deployment.Profile) {
			if _, err := config.ValidateDeploymentHTTPSURL(c.Deployment.Profile, c.DocumentationURL, "documentation_url"); err != nil {
				return err
			}
		}
	}

	backendAPIURL := strings.TrimSpace(c.BackendAPIURL)
	if _, err := config.ValidateHTTPURL(backendAPIURL, "backend_api_url"); err != nil {
		return err
	}
	c.BackendAPIURL = backendAPIURL

	if config.IsNonLocalDeploymentProfile(c.Deployment.Profile) {
		if err := c.validateNonLocalDeployment(); err != nil {
			return err
		}
	}

	return nil
}

// validateNonLocalDeployment rejects local development defaults for frontend deployments.
func (c *Config) validateNonLocalDeployment() error {
	if c.Deployment.Profile == config.DeploymentProfileSelfHosted && c.Security.AllowPrivateDataSourceHosts {
		return xerror.ValidationFailed().Detail("self-hosted deployments require explicit private destinations; security.allow_private_data_source_hosts must be false")
	}
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
			"frontend_session.cookie_secure must be true in non-local deployment profiles",
		)
	}
	if c.Server.DrainTimeout <= 0 {
		return xerror.Unexpected().Detail(
			"frontend_server.drain_timeout must be greater than 0 in non-local deployment profiles",
		)
	}
	if _, err := config.ValidateNonLocalHTTPURL(c.BackendAPIURL, "backend_api_url"); err != nil {
		return err
	}
	if _, err := config.ValidateDeploymentHTTPSURL(c.Deployment.Profile, c.FrontendURL, "frontend_url"); err != nil {
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
