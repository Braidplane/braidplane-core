package http

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
)

var testRole = model.OrgRole{
	Slug:      must.Slug("editor"),
	Name:      "Editor",
	IsDefault: false,
	Timestamps: model.Timestamps{
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	},
}

var testRolePermissions = []model.RolePermission{
	{Resource: "members", Action: "read"},
	{Resource: "members", Action: "update"},
}

var testRoleAPI = api.OrgRole{
	Slug:      "editor",
	Name:      "Editor",
	IsDefault: false,
	Permissions: []api.Permission{
		{Resource: "members", Action: "read"},
		{Resource: "members", Action: "update"},
	},
	CreatedAt: testRole.CreatedAt,
	UpdatedAt: testRole.UpdatedAt,
}

func TestRoleHandlersPassAuthenticatedActor(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("johndoe")

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		service    func(t *testing.T) *fakeRolesService
		wantStatus int
	}{
		{
			name:   "CreateRole",
			method: http.MethodPost,
			path:   "/organizations/acme/roles",
			body:   api.NewOrgRole{Name: "Editor", Permissions: testRoleAPI.Permissions},
			service: func(t *testing.T) *fakeRolesService {
				return &fakeRolesService{
					CreateRoleWithPermissionsFn: func(
						_ context.Context,
						_ slug.Slug,
						actor model.EventActor,
						_ model.NewRole,
						_ []model.RolePermission,
					) (*model.OrgRole, error) {
						assertUserEventActor(t, actor, userSlug)
						role := testRole
						return &role, nil
					},
				}
			},
			wantStatus: http.StatusCreated,
		},
		{
			name:   "UpdateRole",
			method: http.MethodPut,
			path:   "/organizations/acme/roles/editor",
			body:   api.UpdateOrgRole{Name: "Editor renamed", Permissions: testRoleAPI.Permissions},
			service: func(t *testing.T) *fakeRolesService {
				return &fakeRolesService{
					UpdateRoleWithPermissionsFn: func(
						_ context.Context,
						_, _ slug.Slug,
						actor model.EventActor,
						_ model.UpdateRole,
						_ []model.RolePermission,
					) error {
						assertUserEventActor(t, actor, userSlug)
						return nil
					},
				}
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:   "DeleteRole",
			method: http.MethodDelete,
			path:   "/organizations/acme/roles/editor",
			service: func(t *testing.T) *fakeRolesService {
				return &fakeRolesService{
					DeleteRoleFn: func(
						_ context.Context,
						_, _ slug.Slug,
						actor model.EventActor,
					) error {
						assertUserEventActor(t, actor, userSlug)
						return nil
					},
				}
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:   "RestoreRole",
			method: http.MethodPost,
			path:   "/organizations/acme/roles/editor/restore",
			service: func(t *testing.T) *fakeRolesService {
				return &fakeRolesService{
					RestoreRoleFn: func(
						_ context.Context,
						_, _ slug.Slug,
						actor model.EventActor,
					) error {
						assertUserEventActor(t, actor, userSlug)
						return nil
					},
				}
			},
			wantStatus: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				rolesService: tt.service(t),
			})

			req := jsonRequest(t, tt.method, tt.path, tt.body)
			resp := authenticatedRequest(t, app, store, userSlug, req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)
		})
	}
}

func TestListRoles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		orgSlug           slug.Slug
		query             string
		authAllowed       bool
		listRolesFn       func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest, includeDeleted, onlyDeleted bool) (model.ListResponse[model.OrgRole], error)
		listPermissionsFn func(ctx context.Context, orgSlug, roleSlug slug.Slug) ([]model.RolePermission, error)
		wantStatus        int
		wantBody          *api.RoleListResponse
		wantErr           *apierror.Error
	}{
		{
			name:        "ReturnsRolesList",
			orgSlug:     must.Slug("acme"),
			authAllowed: true,
			listRolesFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest, includeDeleted, onlyDeleted bool) (model.ListResponse[model.OrgRole], error) {
				require.False(t, includeDeleted)
				require.False(t, onlyDeleted)
				return model.ListResponse[model.OrgRole]{Items: []model.OrgRole{testRole}}, nil
			},
			listPermissionsFn: func(_ context.Context, _, _ slug.Slug) ([]model.RolePermission, error) {
				return testRolePermissions, nil
			},
			wantStatus: http.StatusOK,
			wantBody: &api.RoleListResponse{
				Roles:   []api.OrgRole{testRoleAPI},
				HasMore: false,
			},
		},
		{
			name:        "IncludesDeletedRolesWhenRequested",
			orgSlug:     must.Slug("acme"),
			query:       "?include_deleted=true",
			authAllowed: true,
			listRolesFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest, includeDeleted, onlyDeleted bool) (model.ListResponse[model.OrgRole], error) {
				require.True(t, includeDeleted)
				require.False(t, onlyDeleted)
				archived := testRole
				deletedAt := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
				archived.DeletedAt = &deletedAt
				return model.ListResponse[model.OrgRole]{Items: []model.OrgRole{archived}}, nil
			},
			listPermissionsFn: func(_ context.Context, _, _ slug.Slug) ([]model.RolePermission, error) {
				return testRolePermissions, nil
			},
			wantStatus: http.StatusOK,
			wantBody: &api.RoleListResponse{
				Roles: []api.OrgRole{func() api.OrgRole {
					archived := testRoleAPI
					deletedAt := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
					archived.DeletedAt = &deletedAt
					return archived
				}()},
				HasMore: false,
			},
		},
		{
			name:        "OnlyReturnsDeletedRolesWhenRequested",
			orgSlug:     must.Slug("acme"),
			query:       "?only_deleted=true",
			authAllowed: true,
			listRolesFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest, includeDeleted, onlyDeleted bool) (model.ListResponse[model.OrgRole], error) {
				require.True(t, includeDeleted)
				require.True(t, onlyDeleted)
				archived := testRole
				deletedAt := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
				archived.DeletedAt = &deletedAt
				return model.ListResponse[model.OrgRole]{Items: []model.OrgRole{archived}}, nil
			},
			listPermissionsFn: func(_ context.Context, _, _ slug.Slug) ([]model.RolePermission, error) {
				return testRolePermissions, nil
			},
			wantStatus: http.StatusOK,
			wantBody: &api.RoleListResponse{
				Roles: []api.OrgRole{func() api.OrgRole {
					archived := testRoleAPI
					deletedAt := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
					archived.DeletedAt = &deletedAt
					return archived
				}()},
				HasMore: false,
			},
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			orgSlug:     must.Slug("acme"),
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("roles", "read"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				rolesService: &fakeRolesService{
					ListRolesFn:           tt.listRolesFn,
					ListRolePermissionsFn: tt.listPermissionsFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				fmt.Sprintf("/organizations/%s/roles%s", tt.orgSlug, tt.query),
				nil,
			)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody != nil {
				assertResponse(t, resp, *tt.wantBody)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestListRolesRequiresRestorePermissionWhenIncludingDeleted(t *testing.T) {
	t.Parallel()

	var actions []model.Action
	app, store := newTestApp(t, testServerConfig{
		rolesService: &fakeRolesService{
			ListRolesFn: func(context.Context, slug.Slug, model.ListRequest, bool, bool) (model.ListResponse[model.OrgRole], error) {
				t.Fatalf("roles should not be listed without restore permission")
				return model.ListResponse[model.OrgRole]{}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				actions = append(actions, input.Action)
				return input.Action == model.Read, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/organizations/acme/roles?only_deleted=true", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, []model.Action{model.Read, model.Restore}, actions)
	assertErrorResponse(t, resp, apierror.Forbidden("roles", "restore"))
}

func TestCreateRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		orgSlug            slug.Slug
		body               any
		authAllowed        bool
		createRoleFn       func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, newRole model.NewRole, perms []model.RolePermission) (*model.OrgRole, error)
		wantStatus         int
		wantBody           *api.OrgRole
		wantErr            *apierror.Error
		wantCreateRoleSlug slug.Slug
	}{
		{
			name:        "CreatesRoleAndReturns201",
			orgSlug:     must.Slug("acme"),
			body:        api.NewOrgRole{Name: "Editor", Permissions: testRoleAPI.Permissions},
			authAllowed: true,
			createRoleFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ model.NewRole, _ []model.RolePermission) (*model.OrgRole, error) {
				role := testRole
				return &role, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   &testRoleAPI,
		},
		{
			name:        "Returns409WhenSlugExists",
			orgSlug:     must.Slug("acme"),
			body:        api.NewOrgRole{Name: "Editor", Permissions: testRoleAPI.Permissions},
			authAllowed: true,
			createRoleFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ model.NewRole, _ []model.RolePermission) (*model.OrgRole, error) {
				return nil, rolessvc.ErrRoleSlugAlreadyExists
			},
			wantStatus: http.StatusConflict,
			wantErr:    apierror.New(409, "ROLE_ALREADY_EXISTS", "role already exists"),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			orgSlug:     must.Slug("acme"),
			body:        api.NewOrgRole{Name: "Editor", Permissions: testRoleAPI.Permissions},
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("roles", "create"),
		},
		{
			name:        "Returns400WhenInvalidRequestBody",
			orgSlug:     must.Slug("acme"),
			body:        "not-json",
			authAllowed: true,
			wantStatus:  http.StatusBadRequest,
			wantErr:     apierror.InvalidRequestBody(),
		},
		{
			name:        "Returns400WhenServiceReturnsInvalidInput",
			orgSlug:     must.Slug("acme"),
			body:        api.NewOrgRole{Name: "Editor", Permissions: testRoleAPI.Permissions},
			authAllowed: true,
			createRoleFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ model.NewRole, _ []model.RolePermission) (*model.OrgRole, error) {
				return nil, rolessvc.ErrInvalidInput
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "VALIDATION_FAILED", "validation failed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				rolesService: &fakeRolesService{
					CreateRoleWithPermissionsFn: tt.createRoleFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(
				t,
				http.MethodPost,
				fmt.Sprintf("/organizations/%s/roles", tt.orgSlug),
				tt.body,
			)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody != nil {
				assertResponse(t, resp, tt.wantBody)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestGetRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		orgSlug           slug.Slug
		roleSlug          slug.Slug
		authAllowed       bool
		getRoleFn         func(ctx context.Context, orgSlug, roleSlug slug.Slug) (*model.OrgRole, error)
		listPermissionsFn func(ctx context.Context, orgSlug, roleSlug slug.Slug) ([]model.RolePermission, error)
		wantStatus        int
		wantBody          *api.OrgRole
		wantErr           *apierror.Error
	}{
		{
			name:        "ReturnsRole",
			orgSlug:     must.Slug("acme"),
			roleSlug:    must.Slug("editor"),
			authAllowed: true,
			getRoleFn: func(_ context.Context, _, _ slug.Slug) (*model.OrgRole, error) {
				role := testRole
				return &role, nil
			},
			listPermissionsFn: func(_ context.Context, _, _ slug.Slug) ([]model.RolePermission, error) {
				return testRolePermissions, nil
			},
			wantStatus: http.StatusOK,
			wantBody:   &testRoleAPI,
		},
		{
			name:        "Returns404WhenRoleNotFound",
			orgSlug:     must.Slug("acme"),
			roleSlug:    must.Slug("missing"),
			authAllowed: true,
			getRoleFn: func(_ context.Context, _, _ slug.Slug) (*model.OrgRole, error) {
				return nil, rolessvc.ErrRoleNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "ROLE_NOT_FOUND", "role not found"),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			orgSlug:     must.Slug("acme"),
			roleSlug:    must.Slug("editor"),
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("roles", "read"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				rolesService: &fakeRolesService{
					GetRoleFn:             tt.getRoleFn,
					ListRolePermissionsFn: tt.listPermissionsFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				fmt.Sprintf("/organizations/%s/roles/%s", tt.orgSlug, tt.roleSlug),
				nil,
			)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody != nil {
				assertResponse(t, resp, tt.wantBody)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestUpdateRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		orgSlug     slug.Slug
		roleSlug    slug.Slug
		body        any
		authAllowed bool
		updateFn    func(ctx context.Context, orgSlug, roleSlug slug.Slug, actor model.EventActor, update model.UpdateRole, perms []model.RolePermission) error
		wantStatus  int
		wantErr     *apierror.Error
	}{
		{
			name:     "UpdatesRoleAndReturns204",
			orgSlug:  must.Slug("acme"),
			roleSlug: must.Slug("editor"),
			body: api.UpdateOrgRole{
				Name:        "Senior Editor",
				Permissions: testRoleAPI.Permissions,
			},
			authAllowed: true,
			updateFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ model.UpdateRole, _ []model.RolePermission) error {
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:     "Returns404WhenRoleNotFound",
			orgSlug:  must.Slug("acme"),
			roleSlug: must.Slug("missing"),
			body: api.UpdateOrgRole{
				Name:        "Senior Editor",
				Permissions: testRoleAPI.Permissions,
			},
			authAllowed: true,
			updateFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ model.UpdateRole, _ []model.RolePermission) error {
				return rolessvc.ErrRoleNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "ROLE_NOT_FOUND", "role not found"),
		},
		{
			name:     "Returns400WhenDefaultRole",
			orgSlug:  must.Slug("acme"),
			roleSlug: must.Slug("admin"),
			body: api.UpdateOrgRole{
				Name:        "Admin renamed",
				Permissions: testRoleAPI.Permissions,
			},
			authAllowed: true,
			updateFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ model.UpdateRole, _ []model.RolePermission) error {
				return rolessvc.ErrCannotUpdateDefaultRole
			},
			wantStatus: http.StatusBadRequest,
			wantErr: apierror.New(
				400,
				"ROLE_CANNOT_UPDATE_DEFAULT",
				"cannot update default role",
			),
		},
		{
			name:     "Returns403WhenAuthorizationDenied",
			orgSlug:  must.Slug("acme"),
			roleSlug: must.Slug("editor"),
			body: api.UpdateOrgRole{
				Name:        "Senior Editor",
				Permissions: testRoleAPI.Permissions,
			},
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("roles", "update"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				rolesService: &fakeRolesService{
					UpdateRoleWithPermissionsFn: tt.updateFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(
				t,
				http.MethodPut,
				fmt.Sprintf("/organizations/%s/roles/%s", tt.orgSlug, tt.roleSlug),
				tt.body,
			)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestDeleteRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		orgSlug     slug.Slug
		roleSlug    slug.Slug
		authAllowed bool
		deleteFn    func(ctx context.Context, orgSlug, roleSlug slug.Slug, actor model.EventActor) error
		wantStatus  int
		wantErr     *apierror.Error
	}{
		{
			name:        "DeletesRoleAndReturns204",
			orgSlug:     must.Slug("acme"),
			roleSlug:    must.Slug("editor"),
			authAllowed: true,
			deleteFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor) error {
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:        "Returns400WhenDefaultRole",
			orgSlug:     must.Slug("acme"),
			roleSlug:    must.Slug("admin"),
			authAllowed: true,
			deleteFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor) error {
				return rolessvc.ErrCannotDeleteDefaultRole
			},
			wantStatus: http.StatusBadRequest,
			wantErr: apierror.New(
				400,
				"ROLE_CANNOT_DELETE_DEFAULT",
				"cannot delete default role",
			),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			orgSlug:     must.Slug("acme"),
			roleSlug:    must.Slug("editor"),
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("roles", "delete"),
		},
		{
			name:        "Returns404WhenRoleNotFound",
			orgSlug:     must.Slug("acme"),
			roleSlug:    must.Slug("nonexistent"),
			authAllowed: true,
			deleteFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor) error {
				return rolessvc.ErrRoleNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "ROLE_NOT_FOUND", "role not found"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				rolesService: &fakeRolesService{
					DeleteRoleFn: tt.deleteFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(
				t,
				http.MethodDelete,
				fmt.Sprintf("/organizations/%s/roles/%s", tt.orgSlug, tt.roleSlug),
				nil,
			)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestRestoreRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		authAllowed bool
		restoreFn   func(ctx context.Context, orgSlug, roleSlug slug.Slug, actor model.EventActor) error
		wantStatus  int
		wantErr     *apierror.Error
	}{
		{
			name:        "RestoresRoleAndReturns204",
			authAllowed: true,
			restoreFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor) error {
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("roles", "restore"),
		},
		{
			name:        "Returns404WhenRoleNotFound",
			authAllowed: true,
			restoreFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor) error {
				return rolessvc.ErrRoleNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "ROLE_NOT_FOUND", "role not found"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				rolesService: &fakeRolesService{
					RestoreRoleFn: tt.restoreFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodPost, "/organizations/acme/roles/editor/restore", nil)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestPermissionsFromAPI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		perms   []api.Permission
		wantLen int
		wantErr bool
	}{
		{
			name: "ConvertsBasicPermissions",
			perms: []api.Permission{
				{Resource: "members", Action: "read"},
			},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := permissionsFromAPI(tt.perms)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, result, tt.wantLen)
		})
	}
}
