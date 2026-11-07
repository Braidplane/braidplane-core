package config

import (
	"encoding"
	"flag"
	"io"
	"net"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	// DeploymentProfileLocal keeps developer and worktree defaults usable.
	DeploymentProfileLocal = "local"
	// DeploymentProfileStaging enables non-local startup safety checks for staging.
	DeploymentProfileStaging = "staging"
	// DeploymentProfileProduction enables non-local startup safety checks for production.
	DeploymentProfileProduction = "production"
	// DeploymentProfileSelfHosted retains production protections with private operator origins.
	DeploymentProfileSelfHosted = "self-hosted"

	httpScheme  = "http"
	httpsScheme = "https"
)

// DeploymentConfig contains process deployment safety profile configuration.
type DeploymentConfig struct {
	Profile string `toml:"profile" env:"DEPLOYMENT_PROFILE" flag:"deployment-profile" desc:"Deployment safety profile (local, staging, production, self-hosted)"`
}

// ProofConfig controls publication of Braidplane Proof results.
type ProofConfig struct {
	GitChecksEnabled bool `toml:"git_checks_enabled" env:"PROOF_GIT_CHECKS_ENABLED" flag:"proof-git-checks-enabled" desc:"Publish informational Braidplane Proof checks to Git providers"`
}

// SMTP contains SMTP server configuration.
type SMTP struct {
	Host           string `toml:"host"                 env:"SMTP_HOST"                 flag:"smtp-host"                 desc:"SMTP server host with port"`
	User           string `toml:"user"                 env:"SMTP_USER"                 flag:"smtp-user"                 desc:"SMTP username"`
	Password       string `toml:"password"             env:"SMTP_PASSWORD"             flag:"smtp-password"             desc:"SMTP password"`
	CACert         string `toml:"ca_cert"              env:"SMTP_CA_CERT"              flag:"smtp-ca-cert"              desc:"SMTP CA certificate in PEM format"`
	CACertFile     string `toml:"ca_cert_file"         env:"SMTP_CA_CERT_FILE"         flag:"smtp-ca-cert-file"         desc:"Path to SMTP CA certificate file in PEM format"`
	Insecure       bool   `toml:"insecure_skip_verify" env:"SMTP_INSECURE_SKIP_VERIFY" flag:"smtp-insecure-skip-verify" desc:"Skip SMTP TLS certificate verification"`
	NoReplyAddress string `toml:"no_reply_address"     env:"SMTP_NO_REPLY_ADDRESS"     flag:"smtp-no-reply-address"     desc:"No-reply email address"`
}

// ValidateCredentials rejects partial SMTP authentication rather than silently using anonymous delivery.
func (s SMTP) ValidateCredentials() error {
	if (s.User == "") != (s.Password == "") {
		return xerror.ValidationFailed().Detail("smtp.user and smtp.password must be configured together")
	}
	return nil
}

// TracingConfig contains OpenTelemetry trace export and sampling configuration.
type TracingConfig struct {
	OTLPTracesEndpoint string  `toml:"otlp_traces_endpoint" env:"TRACING_OTLP_TRACES_ENDPOINT" flag:"tracing-otlp-traces-endpoint" desc:"Full OTLP/HTTP traces endpoint (empty disables trace export)"`
	SamplingRatio      float64 `toml:"sampling_ratio"       env:"TRACING_SAMPLING_RATIO"      flag:"tracing-sampling-ratio"      desc:"Root trace sampling ratio from 0.0 to 1.0"`
}

// MetricsConfig contains Prometheus-compatible metrics exposure configuration.
type MetricsConfig struct {
	Enabled bool   `toml:"enabled" env:"METRICS_ENABLED" flag:"metrics-enabled" desc:"Expose Prometheus-compatible metrics endpoint"`
	Path    string `toml:"path"    env:"METRICS_PATH"    flag:"metrics-path"    desc:"Prometheus-compatible metrics endpoint path"`
}

// DatabaseConfig contains shared PostgreSQL connection configuration.
type DatabaseConfig struct {
	Host              string `toml:"host"                env:"DB_HOST"                flag:"db-host"                desc:"Database host"`
	Port              int    `toml:"port"                env:"DB_PORT"                flag:"db-port"                desc:"Database port"`
	User              string `toml:"user"                env:"DB_USER"                flag:"db-user"                desc:"Database user"`
	Password          string `toml:"password"            env:"DB_PASSWORD"            flag:"db-password"            desc:"Database password"`
	Database          string `toml:"database"            env:"DB_DATABASE"            flag:"db-database"            desc:"Database name"`
	SSLMode           string `toml:"ssl_mode"            env:"DB_SSL_MODE"            flag:"db-ssl-mode"            desc:"Database SSL mode"`
	SSLRootCert       string `toml:"ssl_root_cert"       env:"DB_SSL_ROOT_CERT"       flag:"db-ssl-root-cert"       desc:"Database TLS root certificate path"`
	MaxConns          int    `toml:"max_conns"           env:"DB_MAX_CONNS"           flag:"db-max-conns"           desc:"Database max connections"`
	MinConns          int    `toml:"min_conns"           env:"DB_MIN_CONNS"           flag:"db-min-conns"           desc:"Database min connections"`
	MaxConnLifetime   int    `toml:"max_conn_lifetime"   env:"DB_MAX_CONN_LIFETIME"   flag:"db-max-conn-lifetime"   desc:"Database max connection lifetime (seconds)"`
	MaxConnIdleTime   int    `toml:"max_conn_idle_time"  env:"DB_MAX_CONN_IDLE_TIME"  flag:"db-max-conn-idle-time"  desc:"Database max connection idle time (seconds)"`
	HealthCheckPeriod int    `toml:"health_check_period" env:"DB_HEALTH_CHECK_PERIOD" flag:"db-health-check-period" desc:"Database health check period (seconds)"`
}

// DefaultDatabaseConfig returns the local PostgreSQL defaults shared by Braidplane processes.
func DefaultDatabaseConfig() DatabaseConfig {
	return DatabaseConfig{
		Host:              "localhost",
		Port:              35432,
		User:              "postgres",
		Password:          "postgres",
		Database:          "braidplane",
		SSLMode:           "disable",
		MaxConns:          25,
		MinConns:          5,
		MaxConnLifetime:   3600,
		MaxConnIdleTime:   300,
		HealthCheckPeriod: 60,
	}
}

// ConnectionURL returns the PostgreSQL connection URL without pool-specific parameters.
func (d *DatabaseConfig) ConnectionURL() string {
	return d.connectionURL(nil)
}

// ConnectionString returns the PostgreSQL connection string in URL format with all pool settings.
func (d *DatabaseConfig) ConnectionString() string {
	return d.connectionURL([]string{
		"pool_max_conns=" + strconv.Itoa(d.MaxConns),
		"pool_min_conns=" + strconv.Itoa(d.MinConns),
		"pool_max_conn_lifetime=" + strconv.Itoa(d.MaxConnLifetime) + "s",
		"pool_max_conn_idle_time=" + strconv.Itoa(d.MaxConnIdleTime) + "s",
		"pool_health_check_period=" + strconv.Itoa(d.HealthCheckPeriod) + "s",
	})
}

// connectionURL renders the shared PostgreSQL fields with caller-specific query settings.
func (d *DatabaseConfig) connectionURL(extraQuery []string) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(d.User, d.Password),
		Host:   net.JoinHostPort(d.Host, strconv.Itoa(d.Port)),
		Path:   d.Database,
	}
	query := []string{"sslmode=" + url.QueryEscape(d.SSLMode)}
	if strings.TrimSpace(d.SSLRootCert) != "" {
		query = append(query, "sslrootcert="+url.QueryEscape(d.SSLRootCert))
	}
	query = append(query, extraQuery...)
	u.RawQuery = strings.Join(query, "&")

	return u.String()
}

// LoadInto reads and parses a configuration file into cfg with this precedence:
// 1. Command-line flags (highest priority)
// 2. Environment variables
// 3. TOML config file
// 4. Default values (lowest priority)
func LoadInto(path string, cfg any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return xerror.Unexpected().Detailf("failed to read config file: %w", err)
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return xerror.Unexpected().Detailf("failed to parse config file: %w", err)
	}

	if err := applyEnvOverrides(cfg); err != nil {
		return xerror.Wrapf("failed to apply env overrides: %w", err)
	}

	if err := applyFlagOverrides(cfg); err != nil {
		return xerror.Wrapf("failed to apply flag overrides: %w", err)
	}

	return nil
}

// LoadIntoWithArgs reads and parses a configuration file using an isolated flag set.
func LoadIntoWithArgs(path string, cfg any, args []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return xerror.Unexpected().Detailf("failed to read config file: %w", err)
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return xerror.Unexpected().Detailf("failed to parse config file: %w", err)
	}

	if err := applyEnvOverrides(cfg); err != nil {
		return xerror.Wrapf("failed to apply env overrides: %w", err)
	}

	if err := applyFlagOverridesFromArgs(cfg, args); err != nil {
		return xerror.Wrapf("failed to apply flag overrides: %w", err)
	}

	return nil
}

// ExtractFlagArgs separates shared config flags from command-specific arguments.
func ExtractFlagArgs(cfg any, args []string) ([]string, []string, error) {
	flags, err := collectFlagMetadata(cfg)
	if err != nil {
		return nil, nil, err
	}

	configArgs := make([]string, 0)
	commandArgs := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			commandArgs = append(commandArgs, args[i:]...)
			break
		}

		name, hasValue, ok := splitFlagArgument(arg)
		if !ok {
			commandArgs = append(commandArgs, arg)
			continue
		}

		meta, exists := flags[name]
		if !exists {
			commandArgs = append(commandArgs, arg)
			continue
		}

		configArgs = append(configArgs, arg)
		if !hasValue && !meta.Bool && i+1 < len(args) {
			i++
			configArgs = append(configArgs, args[i])
		}
	}

	return configArgs, commandArgs, nil
}

// ValidateRuntime validates process-shared logging, tracing, and metrics settings.
func ValidateRuntime(
	logLevel string,
	logEnvironment string,
	tracing *TracingConfig,
	metrics *MetricsConfig,
) error {
	validLevels := map[string]struct{}{
		"":      {},
		"none":  {},
		"debug": {},
		"info":  {},
		"warn":  {},
		"error": {},
	}

	if _, ok := validLevels[strings.ToLower(strings.TrimSpace(logLevel))]; !ok {
		return xerror.Unexpected().Detailf(
			"invalid logging.level %q: must be one of none, debug, info, warn, error",
			logLevel,
		)
	}
	if strings.TrimSpace(logEnvironment) == "" {
		return xerror.Unexpected().Detail("logging.environment is required")
	}
	if tracing.SamplingRatio < 0 || tracing.SamplingRatio > 1 {
		return xerror.Unexpected().Detailf(
			"tracing.sampling_ratio must be between 0.0 and 1.0, got %v",
			tracing.SamplingRatio,
		)
	}
	metricsPath := strings.TrimSpace(metrics.Path)
	if metricsPath == "" {
		return xerror.Unexpected().Detail("metrics.path is required")
	}
	parsedMetricsPath, err := url.Parse(metricsPath)
	if err != nil {
		return xerror.Unexpected().Detailf("invalid metrics.path %q: %w", metricsPath, err)
	}
	if parsedMetricsPath.Path != metricsPath ||
		!strings.HasPrefix(metricsPath, "/") ||
		metricsPath == "/" ||
		parsedMetricsPath.RawQuery != "" ||
		parsedMetricsPath.Fragment != "" {
		return xerror.Unexpected().Detail(
			"metrics.path must be an absolute path without query or fragment",
		)
	}
	if metricsPath == "/api" || strings.HasPrefix(metricsPath, "/api/") {
		return xerror.Unexpected().Detail(
			"metrics.path must not use the reserved /api namespace",
		)
	}
	metrics.Path = metricsPath

	return nil
}

// ValidateDeploymentProfile normalizes and validates the deployment safety profile.
func ValidateDeploymentProfile(profile *string) error {
	normalized := strings.ToLower(strings.TrimSpace(*profile))
	switch normalized {
	case DeploymentProfileLocal, DeploymentProfileStaging, DeploymentProfileProduction, DeploymentProfileSelfHosted:
		*profile = normalized
		return nil
	default:
		return xerror.Unexpected().Detailf(
			"deployment.profile must be one of local, staging, production, self-hosted, got %q",
			*profile,
		)
	}
}

// IsNonLocalDeploymentProfile reports whether startup must enforce deployment safety checks.
func IsNonLocalDeploymentProfile(profile string) bool {
	return strings.ToLower(strings.TrimSpace(profile)) != DeploymentProfileLocal
}

// ValidateHTTPURL trims and validates an absolute HTTP or HTTPS URL.
func ValidateHTTPURL(value, field string) (*url.URL, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, xerror.Unexpected().Detailf("%s is required", field)
	}
	parsedURL, err := url.Parse(trimmed)
	if err != nil {
		return nil, xerror.Unexpected().Detailf("invalid %s %q: %w", field, trimmed, err)
	}
	if parsedURL.Scheme != httpScheme && parsedURL.Scheme != httpsScheme {
		return nil, xerror.Unexpected().Detailf(
			"%s %q must include http:// or https://",
			field,
			trimmed,
		)
	}
	if strings.TrimSpace(parsedURL.Hostname()) == "" {
		return nil, xerror.Unexpected().Detailf("%s %q must include a host", field, trimmed)
	}
	return parsedURL, nil
}

// ValidateHTTPOrigin trims and validates an HTTP or HTTPS origin value.
func ValidateHTTPOrigin(value, field string) (*url.URL, error) {
	parsedURL, err := ValidateHTTPURL(value, field)
	if err != nil {
		return nil, err
	}
	if parsedURL.Path != "" ||
		parsedURL.RawQuery != "" ||
		parsedURL.Fragment != "" {
		return nil, xerror.Unexpected().Detailf(
			"%s %q must be an origin without path, query, or fragment",
			field,
			strings.TrimSpace(value),
		)
	}
	return parsedURL, nil
}

// ValidatePublicHTTPSURL enforces an HTTPS URL with a non-local host for non-local profiles.
func ValidatePublicHTTPSURL(value, field string) (*url.URL, error) {
	parsedURL, err := ValidateHTTPURL(value, field)
	if err != nil {
		return nil, err
	}
	if parsedURL.Scheme != "https" {
		return nil, xerror.Unexpected().Detailf(
			"%s must use https:// in non-local deployment profiles",
			field,
		)
	}
	if IsLocalDevelopmentHost(parsedURL.Hostname()) || isPrivateNetworkIP(parsedURL.Hostname()) {
		return nil, xerror.Unexpected().Detailf(
			"%s must not use a local, private, loopback, or wildcard host in non-local deployment profiles",
			field,
		)
	}
	return parsedURL, nil
}

// ValidateDeploymentHTTPSURL permits private operator origins only in self-hosted deployments.
func ValidateDeploymentHTTPSURL(profile, value, field string) (*url.URL, error) {
	if profile != DeploymentProfileSelfHosted {
		return ValidatePublicHTTPSURL(value, field)
	}
	parsed, err := ValidateHTTPURL(value, field)
	if err != nil {
		return nil, err
	}
	if parsed.User != nil {
		return nil, xerror.ValidationFailed().Detailf("%s must not contain URL credentials", field)
	}
	if parsed.Scheme != httpsScheme {
		return nil, xerror.Unexpected().Detailf("%s must use https:// in self-hosted deployments", field)
	}
	if err := ValidateNonLocalHost(parsed.Hostname(), field); err != nil {
		return nil, err
	}
	return parsed, nil
}

// ValidateNonLocalHTTPURL rejects local hosts while allowing HTTP service URLs.
func ValidateNonLocalHTTPURL(value, field string) (*url.URL, error) {
	parsedURL, err := ValidateHTTPURL(value, field)
	if err != nil {
		return nil, err
	}
	if IsLocalDevelopmentHost(parsedURL.Hostname()) {
		return nil, xerror.Unexpected().Detailf(
			"%s must not use a local, loopback, or wildcard host in non-local deployment profiles",
			field,
		)
	}
	return parsedURL, nil
}

// ValidateNonLocalHost rejects localhost, loopback, and wildcard host values.
func ValidateNonLocalHost(host, field string) error {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return xerror.Unexpected().Detailf("%s is required", field)
	}
	if IsLocalDevelopmentHost(trimmed) {
		return xerror.Unexpected().Detailf(
			"%s must not use a local, loopback, or wildcard host in non-local deployment profiles",
			field,
		)
	}
	return nil
}

// ValidateNonLocalPostgres enforces database settings required outside local development.
func ValidateNonLocalPostgres(host, user, password, sslMode string) error {
	return ValidateDeploymentPostgres(
		DeploymentProfileProduction,
		host,
		user,
		password,
		sslMode,
	)
}

// ValidateDeploymentPostgres enforces database settings for a deployment profile.
func ValidateDeploymentPostgres(profile, host, user, password, sslMode string) error {
	if err := ValidateNonLocalHost(host, "database.host"); err != nil {
		return err
	}
	if strings.TrimSpace(user) == "" {
		return xerror.Unexpected().Detail("database.user is required in non-local deployment profiles")
	}
	if strings.TrimSpace(password) == "" {
		return xerror.Unexpected().Detail(
			"database.password is required in non-local deployment profiles",
		)
	}
	if strings.EqualFold(strings.TrimSpace(user), "postgres") && password == "postgres" {
		return xerror.Unexpected().Detail(
			"database user and password must not use postgres/postgres defaults in non-local deployment profiles",
		)
	}
	normalizedSSLMode := strings.ToLower(strings.TrimSpace(sslMode))
	if profile == DeploymentProfileSelfHosted && normalizedSSLMode != "verify-full" {
		return xerror.ValidationFailed().Detail("database.ssl_mode must be verify-full in self-hosted deployments")
	}
	if isSecurePostgresSSLMode(normalizedSSLMode) {
		return nil
	}
	return xerror.Unexpected().Detail(
		"database.ssl_mode must be require, verify-ca, or verify-full in non-local deployment profiles",
	)
}

// isSecurePostgresSSLMode reports whether sslMode requires PostgreSQL TLS.
func isSecurePostgresSSLMode(sslMode string) bool {
	switch sslMode {
	case "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}

// IsLocalDevelopmentHost reports whether a host is only suitable for local development.
func IsLocalDevelopmentHost(host string) bool {
	normalized := strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	switch normalized {
	case "", "localhost", "0.0.0.0", "::", "::1":
		return true
	}
	if strings.HasSuffix(normalized, ".localhost") {
		return true
	}
	ip := net.ParseIP(normalized)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified()
}

// isPrivateNetworkIP reports whether a host is an RFC1918 or link-local IP literal.
func isPrivateNetworkIP(host string) bool {
	normalized := strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	ip := net.ParseIP(normalized)
	if ip == nil {
		return false
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

// ValidateProxyConfig trims and validates a process-specific proxy header/trusted proxy pair.
func ValidateProxyConfig(
	tomlName string,
	proxyHeader *string,
	trustedProxies []string,
	setTrustedProxy func(i int, value string),
) error {
	*proxyHeader = strings.TrimSpace(*proxyHeader)
	for i, trustedProxy := range trustedProxies {
		setTrustedProxy(i, strings.TrimSpace(trustedProxy))
	}

	if *proxyHeader != "" && len(trustedProxies) == 0 {
		return xerror.Unexpected().Detail(
			tomlName + ".proxy_header requires at least one " + tomlName + ".trusted_proxies entry",
		)
	}
	for _, trustedProxy := range trustedProxies {
		if trustedProxy == "" {
			return xerror.Unexpected().Detail(tomlName + ".trusted_proxies cannot contain empty values")
		}
		if ip := net.ParseIP(trustedProxy); ip != nil {
			continue
		}
		if _, _, err := net.ParseCIDR(trustedProxy); err == nil {
			continue
		}
		return xerror.Unexpected().Detailf(
			"%s.trusted_proxies contains invalid IP or CIDR %q",
			tomlName,
			trustedProxy,
		)
	}

	return nil
}

// TLSConfig represents TLS configuration.
type TLSConfig struct {
	Insecure   bool
	CACert     string
	CACertFile string
}

// ToTLSConfig converts SMTP config to TLSConfig.
func (w SMTP) ToTLSConfig() TLSConfig {
	return TLSConfig{
		Insecure:   w.Insecure,
		CACert:     w.CACert,
		CACertFile: w.CACertFile,
	}
}

// applyEnvOverrides applies non-empty environment variables tagged on cfg fields.
func applyEnvOverrides(cfg any) error {
	return processStruct(
		reflect.ValueOf(cfg).Elem(),
		func(field reflect.Value, tag reflect.StructTag) error {
			envVar := tag.Get("env")
			if envVar == "" {
				return nil
			}

			envValue := os.Getenv(envVar)
			if envValue == "" {
				return nil
			}

			return setFieldValue(field, envValue)
		},
	)
}

// applyFlagOverrides applies process command-line flags tagged on cfg fields.
func applyFlagOverrides(cfg any) error {
	if err := registerFlagOverrides(cfg, flag.CommandLine); err != nil {
		return err
	}

	if !flag.Parsed() {
		flag.Parse()
	}

	return applyVisitedFlagOverrides(cfg, flag.CommandLine)
}

// applyFlagOverridesFromArgs applies isolated command-line flags tagged on cfg fields.
func applyFlagOverridesFromArgs(cfg any, args []string) error {
	flagSet := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	if err := registerFlagOverrides(cfg, flagSet); err != nil {
		return err
	}
	if err := flagSet.Parse(args); err != nil {
		return xerror.ValidationFailed().With(err)
	}

	return applyVisitedFlagOverrides(cfg, flagSet)
}

// registerFlagOverrides registers cfg field tags onto the provided flag set.
func registerFlagOverrides(cfg any, flagSet *flag.FlagSet) error {
	return processStruct(
		reflect.ValueOf(cfg).Elem(),
		func(field reflect.Value, tag reflect.StructTag) error {
			flagName := tag.Get("flag")
			if flagName == "" {
				return nil
			}

			desc := tag.Get("desc")
			if desc == "" {
				desc = flagName
			}

			switch {
			case field.CanAddr() && field.Addr().Type().Implements(reflect.TypeOf((*flag.Value)(nil)).Elem()):
				flagSet.Var(field.Addr().Interface().(flag.Value), flagName, desc)
			case field.Kind() == reflect.String:
				flagSet.String(flagName, "", desc)
			case field.Kind() == reflect.Int ||
				field.Kind() == reflect.Int8 ||
				field.Kind() == reflect.Int16 ||
				field.Kind() == reflect.Int32 ||
				field.Kind() == reflect.Int64:
				flagSet.Int(flagName, 0, desc)
			case field.Kind() == reflect.Uint ||
				field.Kind() == reflect.Uint8 ||
				field.Kind() == reflect.Uint16 ||
				field.Kind() == reflect.Uint32 ||
				field.Kind() == reflect.Uint64:
				flagSet.Uint(flagName, 0, desc)
			case field.Kind() == reflect.Bool:
				flagSet.Bool(flagName, false, desc)
			case field.Kind() == reflect.Float32 || field.Kind() == reflect.Float64:
				flagSet.Float64(flagName, 0, desc)
			case field.Kind() == reflect.Slice:
				if field.Type().Elem().Kind() != reflect.String {
					return xerror.Unexpected().Detailf("unsupported flag type: %s", field.Kind())
				}
				flagSet.String(flagName, "", desc)
			default:
				return xerror.Unexpected().Detailf("unsupported flag type: %s", field.Kind())
			}

			return nil
		},
	)
}

// applyVisitedFlagOverrides copies explicitly provided flag values into cfg.
func applyVisitedFlagOverrides(cfg any, flagSet *flag.FlagSet) error {
	flags := make(map[string]*flag.Flag)
	flagSet.Visit(func(f *flag.Flag) {
		flags[f.Name] = f
	})

	return processStruct(
		reflect.ValueOf(cfg).Elem(),
		func(field reflect.Value, tag reflect.StructTag) error {
			flagName := tag.Get("flag")
			if flagName == "" {
				return nil
			}

			f, exists := flags[flagName]
			if !exists {
				return nil
			}

			return setFieldValue(field, f.Value.String())
		},
	)
}

type flagMetadata struct {
	Bool bool
}

// collectFlagMetadata records known tagged flag names for config argument extraction.
func collectFlagMetadata(cfg any) (map[string]flagMetadata, error) {
	flags := make(map[string]flagMetadata)
	if err := processStruct(
		reflect.ValueOf(cfg).Elem(),
		func(field reflect.Value, tag reflect.StructTag) error {
			flagName := tag.Get("flag")
			if flagName == "" {
				return nil
			}

			flags[flagName] = flagMetadata{Bool: field.Kind() == reflect.Bool}
			return nil
		},
	); err != nil {
		return nil, err
	}

	return flags, nil
}

// splitFlagArgument returns a normalized flag name and whether it used an inline value.
func splitFlagArgument(arg string) (string, bool, bool) {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "-" || trimmed == "--" {
		return "", false, false
	}
	switch {
	case strings.HasPrefix(trimmed, "--"):
		trimmed = strings.TrimPrefix(trimmed, "--")
	case strings.HasPrefix(trimmed, "-"):
		trimmed = strings.TrimPrefix(trimmed, "-")
	default:
		return "", false, false
	}
	if trimmed == "" {
		return "", false, false
	}

	name, _, hasValue := strings.Cut(trimmed, "=")
	if name == "" {
		return "", false, false
	}
	return name, hasValue, true
}

// processStruct visits every settable non-struct field in cfg recursively.
func processStruct(
	v reflect.Value,
	fn func(field reflect.Value, tag reflect.StructTag) error,
) error {
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		structField := t.Field(i)

		if !field.CanSet() {
			continue
		}

		if field.Kind() == reflect.Struct {
			if err := processStruct(field, fn); err != nil {
				return xerror.Wrapf("process nested struct: %w", err)
			}
			continue
		}

		if err := fn(field, structField.Tag); err != nil {
			return xerror.Wrapf("field %s: %w", structField.Name, err)
		}
	}

	return nil
}

// setFieldValue parses a string source value into a supported config field.
func setFieldValue(field reflect.Value, value string) error {
	if field.CanAddr() &&
		field.Addr().Type().Implements(reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()) {
		if err := field.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText(
			[]byte(value),
		); err != nil {
			return xerror.Unexpected().With(err)
		}
		return nil
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(value)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		field.SetInt(i)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		field.SetUint(u)

	case reflect.Bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		field.SetBool(b)

	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		field.SetFloat(f)

	case reflect.Slice:
		if field.Type().Elem().Kind() != reflect.String {
			return xerror.Unexpected().Detailf("unsupported field type: %s", field.Kind())
		}
		parts := strings.Split(value, ",")
		values := make([]string, 0, len(parts))
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				values = append(values, trimmed)
			}
		}
		field.Set(reflect.ValueOf(values))

	default:
		return xerror.Unexpected().Detailf("unsupported field type: %s", field.Kind())
	}

	return nil
}
