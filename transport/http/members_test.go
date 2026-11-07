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
	"github.com/Braidplane/braidplane-core/service/memberssvc"
)

var testMember = model.Member{
	UserSlug: "janedoe",
	Email:    "jane@example.com",
	Role:     "viewer",
	Timestamps: model.Timestamps{
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	},
}

var testMemberAPI = memberToHTTP(testMember)

func TestMemberHandlersPassAuthenticatedActor(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("johndoe")

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		service    func(t *testing.T) *fakeMembersService
		wantStatus int
	}{
		{
			name:   "CreateMember",
			method: http.MethodPost,
			path:   "/organizations/acme/members",
			body:   api.NewMember{UserSlug: "janedoe", Role: "viewer"},
			service: func(t *testing.T) *fakeMembersService {
				return &fakeMembersService{
					CreateMemberFn: func(
						_ context.Context,
						_ slug.Slug,
						actor model.EventActor,
						_ model.NewMember,
					) (*model.Member, error) {
						assertUserEventActor(t, actor, userSlug)
						m := testMember
						return &m, nil
					},
				}
			},
			wantStatus: http.StatusCreated,
		},
		{
			name:   "UpdateMember",
			method: http.MethodPut,
			path:   "/organizations/acme/members/janedoe",
			body:   api.UpdateMember{Role: "admin"},
			service: func(t *testing.T) *fakeMembersService {
				return &fakeMembersService{
					UpdateMemberFn: func(
						_ context.Context,
						_, _ slug.Slug,
						actor model.EventActor,
						_ model.UpdateMember,
					) (*model.Member, error) {
						assertUserEventActor(t, actor, userSlug)
						return &model.Member{}, nil
					},
				}
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:   "RemoveMember",
			method: http.MethodDelete,
			path:   "/organizations/acme/members/janedoe",
			service: func(t *testing.T) *fakeMembersService {
				return &fakeMembersService{
					RemoveMemberFn: func(
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
				membersService: tt.service(t),
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

func TestListMembers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		orgSlug       slug.Slug
		authAllowed   bool
		listMembersFn func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.Member], error)
		wantStatus    int
		wantBody      *api.MemberListResponse
		wantErr       *apierror.Error
	}{
		{
			name:        "ReturnsMembersList",
			orgSlug:     must.Slug("acme"),
			authAllowed: true,
			listMembersFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) (model.ListResponse[model.Member], error) {
				return model.ListResponse[model.Member]{Items: []model.Member{testMember}}, nil
			},
			wantStatus: http.StatusOK,
			wantBody: &api.MemberListResponse{
				Members: []api.Member{testMemberAPI},
				HasMore: false,
			},
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			orgSlug:     "acme",
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("members", "read"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				membersService: &fakeMembersService{
					ListMembersFn: tt.listMembersFn,
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
				fmt.Sprintf("/organizations/%s/members", tt.orgSlug),
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

func TestCreateMember(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		orgSlug        slug.Slug
		body           any
		authAllowed    bool
		createMemberFn func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, newMember model.NewMember) (*model.Member, error)
		wantStatus     int
		wantBody       *api.Member
		wantErr        *apierror.Error
	}{
		{
			name:        "CreatesMemberAndReturns201",
			orgSlug:     must.Slug("acme"),
			body:        api.NewMember{UserSlug: "janedoe", Role: "viewer"},
			authAllowed: true,
			createMemberFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ model.NewMember) (*model.Member, error) {
				m := testMember
				return &m, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   &testMemberAPI,
		},
		{
			name:        "Returns409WhenMemberAlreadyExists",
			orgSlug:     must.Slug("acme"),
			body:        api.NewMember{UserSlug: "janedoe", Role: "viewer"},
			authAllowed: true,
			createMemberFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ model.NewMember) (*model.Member, error) {
				return nil, memberssvc.ErrMemberAlreadyExists
			},
			wantStatus: http.StatusConflict,
			wantErr:    apierror.New(409, "MEMBER_ALREADY_EXISTS", "member already exists"),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			orgSlug:     "acme",
			body:        api.NewMember{UserSlug: "janedoe", Role: "viewer"},
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("members", "create"),
		},
		{
			name:        "Returns400WhenInvalidRole",
			orgSlug:     "acme",
			body:        api.NewMember{UserSlug: "janedoe", Role: "nonexistent"},
			authAllowed: true,
			createMemberFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ model.NewMember) (*model.Member, error) {
				return nil, memberssvc.ErrInvalidRole
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "MEMBER_INVALID_INPUT", "member invalid input"),
		},
		{
			name:        "Returns400WhenInvalidRoleSlug",
			orgSlug:     "acme",
			body:        api.NewMember{UserSlug: "janedoe", Role: "invalid role"},
			authAllowed: true,
			wantStatus:  http.StatusBadRequest,
			wantErr: apierror.ValidationFailed(
				"Invalid role slug: invalid slug format: must be 1-100 lowercase alphanumeric characters with hyphens",
			),
		},
		{
			name:        "Returns404WhenMemberNotFound",
			orgSlug:     "acme",
			body:        api.NewMember{UserSlug: "ghost", Role: "viewer"},
			authAllowed: true,
			createMemberFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ model.NewMember) (*model.Member, error) {
				return nil, memberssvc.ErrMemberNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "MEMBER_NOT_FOUND", "member not found"),
		},
		{
			name:        "Returns400WhenInvalidRequestBody",
			orgSlug:     "acme",
			body:        "not-json",
			authAllowed: true,
			wantStatus:  http.StatusBadRequest,
			wantErr:     apierror.InvalidRequestBody(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				membersService: &fakeMembersService{
					CreateMemberFn: tt.createMemberFn,
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
				fmt.Sprintf("/organizations/%s/members", tt.orgSlug),
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

func TestUpdateMember(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		orgSlug        slug.Slug
		userSlug       slug.Slug
		body           any
		authAllowed    bool
		updateMemberFn func(ctx context.Context, orgSlug, userSlug slug.Slug, actor model.EventActor, update model.UpdateMember) (*model.Member, error)
		wantStatus     int
		wantErr        *apierror.Error
	}{
		{
			name:        "UpdatesMemberRoleAndReturns204",
			orgSlug:     must.Slug("acme"),
			userSlug:    must.Slug("janedoe"),
			body:        api.UpdateMember{Role: "admin"},
			authAllowed: true,
			updateMemberFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ model.UpdateMember) (*model.Member, error) {
				return &model.Member{}, nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:        "Returns404WhenMemberNotFound",
			orgSlug:     must.Slug("acme"),
			userSlug:    must.Slug("ghost"),
			body:        api.UpdateMember{Role: "admin"},
			authAllowed: true,
			updateMemberFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ model.UpdateMember) (*model.Member, error) {
				return nil, memberssvc.ErrMemberNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "MEMBER_NOT_FOUND", "member not found"),
		},
		{
			name:        "Returns400WhenDemotingLastAdmin",
			orgSlug:     must.Slug("acme"),
			userSlug:    must.Slug("admin-user"),
			body:        api.UpdateMember{Role: "viewer"},
			authAllowed: true,
			updateMemberFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ model.UpdateMember) (*model.Member, error) {
				return nil, memberssvc.ErrCannotDemoteLastAdmin
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "CANNOT_DEMOTE_OWNER", "cannot demote owner"),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			orgSlug:     must.Slug("acme"),
			userSlug:    must.Slug("janedoe"),
			body:        api.UpdateMember{Role: "admin"},
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("members", "update"),
		},
		{
			name:        "RejectsForeignOrganizationRoleChange",
			orgSlug:     must.Slug("foreign-org"),
			userSlug:    must.Slug("janedoe"),
			body:        api.UpdateMember{Role: "admin"},
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("members", "update"),
		},
		{
			name:        "Returns400WhenInvalidRole",
			orgSlug:     must.Slug("acme"),
			userSlug:    must.Slug("janedoe"),
			body:        api.UpdateMember{Role: "nonexistent"},
			authAllowed: true,
			updateMemberFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor, _ model.UpdateMember) (*model.Member, error) {
				return nil, memberssvc.ErrInvalidRole
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "MEMBER_INVALID_INPUT", "member invalid input"),
		},
		{
			name:        "Returns400WhenInvalidRoleSlug",
			orgSlug:     must.Slug("acme"),
			userSlug:    must.Slug("janedoe"),
			body:        api.UpdateMember{Role: "invalid role"},
			authAllowed: true,
			wantStatus:  http.StatusBadRequest,
			wantErr: apierror.ValidationFailed(
				"Invalid role slug: invalid slug format: must be 1-100 lowercase alphanumeric characters with hyphens",
			),
		},
		{
			name:        "Returns400WhenInvalidRequestBody",
			orgSlug:     must.Slug("acme"),
			userSlug:    must.Slug("janedoe"),
			body:        "not-json",
			authAllowed: true,
			wantStatus:  http.StatusBadRequest,
			wantErr:     apierror.InvalidRequestBody(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				membersService: &fakeMembersService{
					UpdateMemberFn: tt.updateMemberFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						require.NotNil(t, input.OrgSlug)
						assert.Equal(t, tt.orgSlug, *input.OrgSlug)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(
				t,
				http.MethodPut,
				fmt.Sprintf("/organizations/%s/members/%s", tt.orgSlug, tt.userSlug),
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

func TestRemoveMember(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		orgSlug        slug.Slug
		userSlug       slug.Slug
		authAllowed    bool
		removeMemberFn func(ctx context.Context, orgSlug, userSlug slug.Slug, actor model.EventActor) error
		wantStatus     int
		wantErr        *apierror.Error
	}{
		{
			name:        "RemovesMemberAndReturns204",
			orgSlug:     must.Slug("acme"),
			userSlug:    must.Slug("janedoe"),
			authAllowed: true,
			removeMemberFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor) error {
				return nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:        "Returns400WhenRemovingLastAdmin",
			orgSlug:     must.Slug("acme"),
			userSlug:    must.Slug("admin-user"),
			authAllowed: true,
			removeMemberFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor) error {
				return memberssvc.ErrCannotRemoveLastAdmin
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.New(400, "CANNOT_REMOVE_OWNER", "cannot remove owner"),
		},
		{
			name:        "Returns404WhenMemberNotFound",
			orgSlug:     must.Slug("acme"),
			userSlug:    must.Slug("ghost"),
			authAllowed: true,
			removeMemberFn: func(_ context.Context, _, _ slug.Slug, _ model.EventActor) error {
				return memberssvc.ErrMemberNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "MEMBER_NOT_FOUND", "member not found"),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			orgSlug:     must.Slug("acme"),
			userSlug:    must.Slug("janedoe"),
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("members", "delete"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				membersService: &fakeMembersService{
					RemoveMemberFn: tt.removeMemberFn,
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
				fmt.Sprintf("/organizations/%s/members/%s", tt.orgSlug, tt.userSlug),
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
