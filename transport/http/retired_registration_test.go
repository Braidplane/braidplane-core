package http

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetiredPublicRegistrationPathReturnsNotFound(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t, testServerConfig{})
	resp, err := app.Test(jsonRequest(t, http.MethodPost, "/wishlist-registrations", map[string]string{
		"work_email": "retired@example.test",
	}))
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
