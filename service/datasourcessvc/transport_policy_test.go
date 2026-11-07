package datasourcessvc

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/stretchr/testify/require"
)

func TestCustomerTransportValidationRequiresEncryptedEndpoints(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		kind                model.DataSourceType
		config, credentials string
	}{
		{model.DataSourceTypeHTTP, `{"base_url":"http://example.test"}`, `{"bearer_token":"synthetic"}`},
		{model.DataSourceTypeHTTP, `{"base_url":"https://example.test","openapi_url":"http://example.test/schema"}`, `{}`},
		{model.DataSourceTypeS3, `{"region":"test","bucket":"test","endpoint":"http://example.test"}`, `{"access_key_id":"synthetic","secret_access_key":"synthetic"}`},
		{model.DataSourceTypePostgres, `{"host":"example.test","port":5432,"database":"test","ssl_mode":"disable"}`, `{"username":"synthetic","password":"synthetic"}`},
	} {
		s := &service{}
		require.Error(t, s.validateConfigAndCredentials(tt.kind, []byte(tt.config), []byte(tt.credentials)))
		s.customerConfigPolicy.AllowPlaintextConnectors = true
		require.NoError(t, s.validateConfigAndCredentials(tt.kind, []byte(tt.config), []byte(tt.credentials)))
	}
}
