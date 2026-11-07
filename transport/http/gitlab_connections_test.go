package http

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/stretchr/testify/require"
)

func TestGitLabConnectionsRequireOwnerPermissions(t *testing.T) {
	t.Parallel()
	for _, route := range []struct {
		method, path string
		action       model.Action
		resource     model.Resource
	}{
		{http.MethodGet, "/organizations/acme/gitlab-connection", model.Read, model.ResourceOrganizations},
		{http.MethodPost, "/organizations/acme/gitlab-connection/authorization", model.Update, model.ResourceOrganizations},
		{http.MethodDelete, "/organizations/acme/gitlab-connection", model.Update, model.ResourceOrganizations},
		{http.MethodGet, "/organizations/acme/gitlab-connection/repositories", model.Read, model.ResourceDataSources},
		{http.MethodPost, "/organizations/acme/data-sources/repository/gitlab-connection", model.Update, model.ResourceDataSources},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			checked := false
			app, store := newTestApp(t, testServerConfig{authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				checked = true
				require.Equal(t, route.action, input.Action)
				require.Equal(t, route.resource, input.Resource)
				return false, nil
			}}})
			req, err := http.NewRequestWithContext(t.Context(), route.method, route.path, strings.NewReader(`{"include_write_scopes":false}`))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			resp := authenticatedRequest(t, app, store, "test-user", req)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
			require.True(t, checked)
		})
	}
}

func TestOrganizationGitLabCallbackAlwaysRequiresOrganizationUpdate(t *testing.T) {
	t.Parallel()
	for _, intent := range []credentialssvc.OAuthInstallIntent{credentialssvc.OAuthInstallIntentCreate, credentialssvc.OAuthInstallIntentRotate} {
		action, resource := oauthCallbackPermission("gitlab", credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindOrganizationGitLab}, intent)
		require.Equal(t, model.Update, action)
		require.Equal(t, model.ResourceOrganizations, resource)
	}
}
