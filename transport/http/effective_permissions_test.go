package http

import (
	"context"
	"net/http"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

func TestProjectAccessAndEffectivePermissionsUseCurrentBinding(t *testing.T) {
	grants := []model.AuthorizationBinding{{Scope: "project", ScopeID: "18", Permissions: []string{"pipelines:read"}}}
	auth, err := authorization.New(nil, func(_ context.Context, org, user, key slug.Slug, target model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		require.Equal(t, slug.Slug("alice"), user)
		require.Empty(t, key)
		if org != "acme" || target.Kind != "projects" {
			return model.AuthorizationFacts{}, nil
		}
		id := "17"
		if target.Slug == "inventory" {
			id = "18"
		}
		return model.AuthorizationFacts{Valid: true, OrganizationID: "9", ProjectID: id, TargetID: "projects:" + id, Bindings: grants}, nil
	})
	require.NoError(t, err)
	app, store := newTestApp(t, testServerConfig{
		authService: fakeAuthService{CanFn: auth.Can, EffectivePermissionsFn: auth.EffectivePermissions},
		pipelineCreationsService: fakeCreationService{list: func(context.Context, slug.Slug, slug.Slug, slug.Slug, int) (*pipelinesvc.CreationProposalPage, error) {
			return &pipelinesvc.CreationProposalPage{}, nil
		}},
	})
	check := func(org, project string, allowed bool) {
		t.Helper()
		path := "/organizations/" + org
		decisions := authenticatedRequest(t, app, store, "alice", jsonRequest(t, http.MethodGet, path+"/authorization/permissions?target_kind=projects&target_slug="+project, nil))
		require.Equal(t, http.StatusOK, decisions.StatusCode)
		got := parseResponse[struct {
			Permissions []api.Permission `json:"permissions"`
		}](t, decisions)
		require.NoError(t, decisions.Body.Close())
		if allowed {
			require.Equal(t, []api.Permission{{Resource: "pipelines", Action: "read"}}, got.Permissions)
		} else {
			require.Empty(t, got.Permissions)
		}
		response := authenticatedRequest(t, app, store, "alice", jsonRequest(t, http.MethodGet, path+"/projects/"+project+"/pipeline-creations", nil))
		require.NoError(t, response.Body.Close())
		if allowed {
			require.Equal(t, http.StatusOK, response.StatusCode)
		} else {
			require.Equal(t, http.StatusForbidden, response.StatusCode)
		}
	}
	check("acme", "inventory", true)
	check("acme", "other", false)
	check("other", "inventory", false)
	grants = nil
	check("acme", "inventory", false)
}
