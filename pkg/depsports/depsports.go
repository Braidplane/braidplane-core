package depsports

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultHTTPConnectorPort     = 38080
	DefaultOpenAPIServerPort     = 38081
	DefaultHubSpotMockPort       = 38082
	DefaultGitProviderMockPort   = 38083
	DefaultBigQueryMockPort      = 38084
	DefaultS3ConnectorPort       = 39000
	DefaultPostgresConnectorPort = 35433
	DefaultMySQLConnectorPort    = 33307
	DefaultMongoConnectorPort    = 37017
	DefaultFTPConnectorPort      = 32121
	DefaultGitSSHPort            = 32223
	DefaultGitHTTPPort           = 32080

	localhost = "localhost"
)

func readPort(envKey string, fallback int) int {
	port := fallback
	if raw := strings.TrimSpace(os.Getenv(envKey)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			port = parsed
		}
	}

	return port
}

func HTTPConnectorPort() int {
	return readPort("HTTP_CONNECTOR_PORT", DefaultHTTPConnectorPort)
}

func OpenAPIServerPort() int {
	return readPort("OPENAPI_SERVER_PORT", DefaultOpenAPIServerPort)
}

func HubSpotMockPort() int {
	return readPort("HUBSPOT_MOCK_PORT", DefaultHubSpotMockPort)
}

func GitProviderMockPort() int {
	return readPort("GIT_PROVIDER_MOCK_PORT", DefaultGitProviderMockPort)
}

// BigQueryMockPort returns the configured host port for the BigQuery-compatible mock.
func BigQueryMockPort() int {
	return readPort("BIGQUERY_MOCK_PORT", DefaultBigQueryMockPort)
}

// S3ConnectorPort returns the configured host port for the S3-compatible connector fixture.
func S3ConnectorPort() int {
	return readPort("S3_CONNECTOR_PORT", DefaultS3ConnectorPort)
}

func PostgresConnectorPort() int {
	return readPort("POSTGRES_CONNECTOR_PORT", DefaultPostgresConnectorPort)
}

func MySQLConnectorPort() int {
	return readPort("MYSQL_CONNECTOR_PORT", DefaultMySQLConnectorPort)
}

func MongoConnectorPort() int {
	return readPort("MONGO_CONNECTOR_PORT", DefaultMongoConnectorPort)
}

func FTPConnectorPort() int {
	return readPort("FTP_CONNECTOR_PORT", DefaultFTPConnectorPort)
}

func GitSSHPort() int {
	return readPort("GIT_SSH_PORT", DefaultGitSSHPort)
}

func GitHTTPPort() int {
	return readPort("GIT_HTTP_PORT", DefaultGitHTTPPort)
}

func HTTPConnectorBaseURL() string {
	return "http://" + net.JoinHostPort(localhost, strconv.Itoa(HTTPConnectorPort()))
}

func OpenAPIServerSchemaURL() string {
	return "http://" + net.JoinHostPort(localhost, strconv.Itoa(OpenAPIServerPort())) + "/schema.yml"
}

func HubSpotMockBaseURL() string {
	return "http://" + net.JoinHostPort(localhost, strconv.Itoa(HubSpotMockPort()))
}

func GitProviderMockBaseURL() string {
	return "http://" + net.JoinHostPort(localhost, strconv.Itoa(GitProviderMockPort()))
}

// BigQueryMockBaseURL returns the base URL for the BigQuery-compatible mock.
func BigQueryMockBaseURL() string {
	return "http://" + net.JoinHostPort(localhost, strconv.Itoa(BigQueryMockPort()))
}

// S3ConnectorEndpoint returns the endpoint URL for the S3-compatible connector fixture.
func S3ConnectorEndpoint() string {
	return "http://" + net.JoinHostPort(localhost, strconv.Itoa(S3ConnectorPort()))
}

func GitTestPrivateKeyPath() string {
	path := strings.TrimSpace(os.Getenv("GIT_TEST_SSH_KEY_PATH"))
	if path == "" {
		path = strings.TrimSpace(os.Getenv("GIT_TEST_SSH_PRIVATE_KEY_PATH"))
	}
	if path == "" {
		path = filepath.Join(".worktree", "git-keys", "id_ed25519")
	}

	if filepath.IsAbs(path) {
		return path
	}

	if _, err := os.Stat(path); err == nil {
		return path
	}

	if repoRoot := findRepoRoot(); repoRoot != "" {
		return filepath.Join(repoRoot, path)
	}

	return path
}

func findRepoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}

	current := wd
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current
		}

		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}

		current = parent
	}
}
