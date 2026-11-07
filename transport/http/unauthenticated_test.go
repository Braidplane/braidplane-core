package http

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
)

// TestUnauthenticatedEndpointsReturn401 verifies that all authenticated endpoints
// return 401 Unauthorized when no session cookie is present.
func TestUnauthenticatedEndpointsReturn401(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		// Organizations
		{name: "ListOrganizations", method: http.MethodGet, path: "/organizations"},
		{
			name:   "CreateOrganization",
			method: http.MethodPost,
			path:   "/organizations",
			body:   map[string]string{"name": "Test"},
		},
		{name: "GetOrganization", method: http.MethodGet, path: "/organizations/acme-corp"},
		{
			name:   "UpdateOrganization",
			method: http.MethodPut,
			path:   "/organizations/acme-corp",
			body:   map[string]string{"name": "Updated"},
		},

		// Members
		{name: "ListMembers", method: http.MethodGet, path: "/organizations/acme-corp/members"},
		{
			name:   "CreateMember",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/members",
			body:   map[string]string{"user_slug": "user", "role": "admin"},
		},
		{
			name:   "UpdateMember",
			method: http.MethodPut,
			path:   "/organizations/acme-corp/members/johndoe",
			body:   map[string]string{"role": "editor"},
		},
		{
			name:   "RemoveMember",
			method: http.MethodDelete,
			path:   "/organizations/acme-corp/members/johndoe",
		},

		// Roles
		{name: "ListRoles", method: http.MethodGet, path: "/organizations/acme-corp/roles"},
		{
			name:   "CreateRole",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/roles",
			body:   map[string]string{"name": "TestRole"},
		},
		{name: "GetRole", method: http.MethodGet, path: "/organizations/acme-corp/roles/editor"},
		{
			name:   "UpdateRole",
			method: http.MethodPut,
			path:   "/organizations/acme-corp/roles/editor",
			body:   map[string]string{"name": "Updated"},
		},
		{
			name:   "DeleteRole",
			method: http.MethodDelete,
			path:   "/organizations/acme-corp/roles/editor",
		},
		{
			name:   "RestoreRole",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/roles/editor/restore",
		},

		// API Keys
		{name: "ListAPIKeys", method: http.MethodGet, path: "/organizations/acme-corp/api-keys"},
		{
			name:   "CreateAPIKey",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/api-keys",
			body: map[string]string{
				"name": "Automation key",
				"role": "viewer",
			},
		},
		{name: "GetAPIKey", method: http.MethodGet, path: "/organizations/acme-corp/api-keys/automation-key"},
		{
			name:   "UpdateAPIKey",
			method: http.MethodPut,
			path:   "/organizations/acme-corp/api-keys/automation-key",
			body: map[string]string{
				"name": "Automation key v2",
			},
		},
		{
			name:   "DeleteAPIKey",
			method: http.MethodDelete,
			path:   "/organizations/acme-corp/api-keys/automation-key",
		},
		{
			name:   "DeleteRevokedAPIKey",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/api-keys/automation-key/delete",
		},
		{
			name:   "RotateAPIKey",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/api-keys/automation-key/rotate",
		},

		// Projects
		{name: "ListProjects", method: http.MethodGet, path: "/organizations/acme-corp/projects"},
		{
			name:   "CreateProject",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/projects",
			body:   map[string]string{"name": "Test"},
		},
		{name: "GetProject", method: http.MethodGet, path: "/organizations/acme-corp/projects/default"},
		{
			name:   "UpdateProject",
			method: http.MethodPut,
			path:   "/organizations/acme-corp/projects/default",
			body:   map[string]string{"name": "Updated"},
		},
		{
			name:   "DeleteProject",
			method: http.MethodDelete,
			path:   "/organizations/acme-corp/projects/default",
		},

		// Data Sources
		{
			name:   "ListDataSources",
			method: http.MethodGet,
			path:   "/organizations/acme-corp/data-sources",
		},
		{
			name:   "CreateDataSource",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/data-sources",
			body:   map[string]string{"name": "DS"},
		},
		{
			name:   "GetDataSource",
			method: http.MethodGet,
			path:   "/organizations/acme-corp/data-sources/prod-db",
		},
		{
			name:   "UpdateDataSource",
			method: http.MethodPatch,
			path:   "/organizations/acme-corp/data-sources/prod-db",
			body:   map[string]string{"name": "Updated"},
		},
		{
			name:   "UpdateDataSourceCredentials",
			method: http.MethodPatch,
			path:   "/organizations/acme-corp/data-sources/prod-db/credentials",
			body:   map[string]string{},
		},
		{
			name:   "DeleteDataSource",
			method: http.MethodDelete,
			path:   "/organizations/acme-corp/data-sources/prod-db",
		},
		{
			name:   "RestoreDataSource",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/data-sources/prod-db/restore",
		},
		{
			name:   "IntrospectDataSourceSchema",
			method: http.MethodGet,
			path:   "/organizations/acme-corp/data-sources/prod-db/schema",
		},
		{
			name:   "RefreshDataSourceSchema",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/data-sources/prod-db/schema",
		},

		// Notifications
		{
			name:   "GetNotification",
			method: http.MethodGet,
			path:   "/organizations/acme-corp/notifications/notif-001",
		},
		{
			name:   "AcknowledgeNotification",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/notifications/notif-001/acknowledge",
		},
		{
			name:   "ResolveNotification",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/notifications/notif-001/resolve",
		},

		// Queries
		{
			name:   "ListQueries",
			method: http.MethodGet,
			path:   "/organizations/acme-corp/data-sources/prod-db/queries",
		},
		{
			name:   "CreateQuery",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/data-sources/prod-db/queries",
			body:   map[string]string{"name": "Q"},
		},
		{
			name:   "GetQuery",
			method: http.MethodGet,
			path:   "/organizations/acme-corp/data-sources/prod-db/queries/my-query",
		},
		{
			name:   "UpdateQuery",
			method: http.MethodPut,
			path:   "/organizations/acme-corp/data-sources/prod-db/queries/my-query",
			body:   map[string]string{"name": "Q"},
		},
		{
			name:   "DeleteQuery",
			method: http.MethodDelete,
			path:   "/organizations/acme-corp/data-sources/prod-db/queries/my-query",
		},
		{
			name:   "ExecuteQuery",
			method: http.MethodPost,
			path:   "/organizations/acme-corp/data-sources/prod-db/queries/my-query/execute",
		},

		// User Profile
		{
			name:   "PatchUsersProfile",
			method: http.MethodPatch,
			path:   "/users/profile",
			body:   map[string]string{"full_name": "Test"},
		},
		{
			name:   "UpdateUserPassword",
			method: http.MethodPut,
			path:   "/users/profile/password",
			body:   map[string]string{"current_password": "currentpass", "new_password": "newsecurepass"},
		},
		{
			name:   "UpdatePersonalAccessToken",
			method: http.MethodPut,
			path:   "/users/profile/personal-access-tokens/automation-key",
			body:   map[string]string{"name": "Automation key v2"},
		},
		{
			name:   "DeleteRevokedPersonalAccessToken",
			method: http.MethodPost,
			path:   "/users/profile/personal-access-tokens/automation-key/delete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, _ := newTestApp(t, testServerConfig{})

			// Send request without session cookie (unauthenticated)
			req := jsonRequest(t, tt.method, tt.path, tt.body)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			assertErrorResponse(t, resp, apierror.Unauthorized())
		})
	}
}
