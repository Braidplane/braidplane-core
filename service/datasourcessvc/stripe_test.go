package datasourcessvc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

func TestStripeStaticInputsRejectOAuthRuntimeCredentials(t *testing.T) {
	t.Parallel()
	s := &service{}
	for _, raw := range []string{
		`{"access_token":"fixture","oauth_mode":"test"}`,
		`{"api_key":"rk_test_fixture","access_token":"fixture"}`,
		`{"api_key":"rk_test_fixture","oauth_mode":""}`,
	} {
		require.Error(t, s.validateStaticConfigAndCredentials(model.DataSourceTypeStripe, []byte(`{"mode":"test"}`), []byte(raw)))
	}
	require.NoError(t, s.validateStaticConfigAndCredentials(model.DataSourceTypeStripe, []byte(`{"mode":"test"}`), []byte(`{"api_key":"rk_test_fixture"}`)))
}
