package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommunityDoesNotRegisterBillingProfileRoutes(t *testing.T) {
	app, _ := newTestApp(t, testServerConfig{})
	for _, path := range []string{"/users/profile/billing-profile", "/organizations/acme/billing-profile"} {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			response, err := app.Test(httptest.NewRequestWithContext(t.Context(), method, path, nil))
			require.NoError(t, err)
			require.Equal(t, http.StatusNotFound, response.StatusCode)
			require.NoError(t, response.Body.Close())
		}
	}
}
