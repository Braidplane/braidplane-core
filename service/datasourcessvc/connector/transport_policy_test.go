package connector

import (
	"testing"

	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	s3client "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3/client"
	postgresconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
	"github.com/stretchr/testify/require"
)

func TestTransportPolicyRejectsUnsafeSavedConfiguration(t *testing.T) {
	t.Parallel()
	for _, cfg := range []any{
		httpconnector.Config{BaseURL: "http://example.test"},
		httpconnector.Config{BaseURL: "https://example.test", OpenAPIURL: "http://example.test/schema"},
		s3client.Config{Endpoint: "http://example.test"},
		postgresconnector.Config{SSLMode: "disable"},
		postgresconnector.Config{SSLMode: "allow"},
		postgresconnector.Config{SSLMode: "prefer"},
		postgresconnector.Config{SSLMode: "require"},
		postgresconnector.Config{SSLMode: "verify-ca"},
	} {
		require.Error(t, ValidateTransportConfig(cfg, false))
		require.NoError(t, ValidateTransportConfig(cfg, true))
	}
	for _, cfg := range []any{
		httpconnector.Config{BaseURL: "https://example.test", OpenAPIURL: "https://example.test/schema"},
		s3client.Config{Endpoint: "https://example.test"},
		s3client.Config{},
		postgresconnector.Config{},
		postgresconnector.Config{SSLMode: "verify-full"},
	} {
		require.NoError(t, ValidateTransportConfig(cfg, false))
	}
}
