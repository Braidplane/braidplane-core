package http

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/stretchr/testify/require"
)

func TestProofConfigurationRequiresBothPermissions(t *testing.T) {
	t.Parallel()
	for _, route := range []struct {
		method, path string
		action       model.Action
	}{
		{http.MethodGet, "/organizations/acme/proof-configurations", model.Read},
		{http.MethodGet, "/organizations/acme/data-sources/repository/proof", model.Read},
		{http.MethodPut, "/organizations/acme/data-sources/repository/proof", model.Update},
	} {
		for _, denied := range []model.Resource{model.ResourceDataSources, model.ResourcePipelines} {
			t.Run(route.method+route.path+denied.String(), func(t *testing.T) {
				var checked []model.Resource
				app, store := newTestApp(t, testServerConfig{authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, route.action, input.Action)
					checked = append(checked, input.Resource.(model.Resource))
					return input.Resource != denied, nil
				}}})
				req, err := http.NewRequestWithContext(t.Context(), route.method, route.path, strings.NewReader(`{"enabled":false,"publish_checks":false,"expected_repository_id":"42","targets":[]}`))
				require.NoError(t, err)
				req.Header.Set("Content-Type", "application/json")
				resp := authenticatedRequest(t, app, store, "test-user", req)
				defer func() { _ = resp.Body.Close() }()
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
				require.Contains(t, checked, denied)
			})
		}
	}
}
