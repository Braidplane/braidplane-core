package http

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

var testInvitationTime = time.Date(2026, 5, 29, 20, 0, 0, 0, time.UTC)

func testMemberInvitation() model.MemberInvitation {
	inviter := must.Slug("owner")
	return model.MemberInvitation{
		Slug:              must.Slug("jane-example-com-abc123"),
		OrganizationSlug:  must.Slug("acme"),
		Email:             "jane@example.com",
		Role:              must.Slug("viewer"),
		Status:            model.MemberInvitationStatusPending,
		InvitedByUserSlug: &inviter,
		ExpiresAt:         testInvitationTime.Add(7 * 24 * time.Hour),
		LastSentAt:        testInvitationTime,
		Timestamps: model.Timestamps{
			CreatedAt: testInvitationTime,
			UpdatedAt: testInvitationTime,
		},
	}
}

func TestCreateMemberInvitation(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("owner")
	app, store := newTestApp(t, testServerConfig{
		membersService: &fakeMembersService{
			CreateMemberInvitationFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				inviterUserSlug slug.Slug,
				actor model.EventActor,
				newInvitation model.NewMemberInvitation,
			) (*model.MemberInvitation, error) {
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.Equal(t, userSlug, inviterUserSlug)
				assertUserEventActor(t, actor, userSlug)
				require.Equal(t, "Jane@Example.COM", newInvitation.Email)
				require.Equal(t, must.Slug("viewer"), newInvitation.Role)
				invitation := testMemberInvitation()
				return &invitation, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/member-invitations", api.NewMemberInvitation{
		Email: "Jane@Example.COM",
		Role:  "viewer",
	})
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	body := parseResponse[api.MemberInvitation](t, resp)
	require.Equal(t, api.Email("jane@example.com"), body.Email)
	require.Equal(t, api.MemberInvitationStatus(model.MemberInvitationStatusPending.String()), body.Status)
	require.Nil(t, body.AcceptedUserSlug)
}

func TestCreateMemberInvitationAllowsAuthorizedAPIKeyPrincipal(t *testing.T) {
	t.Parallel()

	apiKeySlug := must.Slug("deploy-key")
	app, _ := newTestApp(t, testServerConfig{
		membersService: &fakeMembersService{
			CreateMemberInvitationFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				inviterUserSlug slug.Slug,
				actor model.EventActor,
				newInvitation model.NewMemberInvitation,
			) (*model.MemberInvitation, error) {
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.Empty(t, inviterUserSlug)
				require.Equal(t, model.EventActorTypeAPIKey, actor.Type)
				require.NotNil(t, actor.Slug)
				require.Equal(t, apiKeySlug, *actor.Slug)
				require.Equal(t, "jane@example.com", newInvitation.Email)
				require.Equal(t, must.Slug("viewer"), newInvitation.Role)
				invitation := testMemberInvitation()
				invitation.InvitedByUserSlug = nil
				return &invitation, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, apiKeySlug, input.APIKeySlug)
				require.Equal(t, must.Slug("admin"), input.APIKeyRoleSlug)
				require.NotNil(t, input.OrgSlug)
				require.Equal(t, must.Slug("acme"), *input.OrgSlug)
				require.Equal(t, model.ActionCreate, input.Action)
				require.Equal(t, model.ResourceMembers, input.Resource)
				return true, nil
			},
		},
		middleware: []fiber.Handler{
			func(c fiber.Ctx) error {
				reqctx.SetAPIKeyPrincipal(c, reqctx.APIKeyPrincipal{
					Slug:    apiKeySlug,
					OrgSlug: must.Slug("acme"),
					Role:    must.Slug("admin"),
				})
				return c.Next()
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/member-invitations", api.NewMemberInvitation{
		Email: "jane@example.com",
		Role:  "viewer",
	})
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	body := parseResponse[api.MemberInvitation](t, resp)
	require.Nil(t, body.InvitedByUserSlug)
}

func TestMemberInvitationAdminRoutesRequireMemberAuthorization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		wantAction model.Action
	}{
		{
			name:       "Create",
			method:     http.MethodPost,
			path:       "/organizations/acme/member-invitations",
			body:       api.NewMemberInvitation{Email: "jane@example.com", Role: "viewer"},
			wantAction: model.ActionCreate,
		},
		{
			name:       "List",
			method:     http.MethodGet,
			path:       "/organizations/acme/member-invitations",
			wantAction: model.ActionRead,
		},
		{
			name:       "Resend",
			method:     http.MethodPost,
			path:       "/organizations/acme/member-invitations/jane-example-com-abc123/resend",
			wantAction: model.ActionUpdate,
		},
		{
			name:       "Revoke",
			method:     http.MethodPost,
			path:       "/organizations/acme/member-invitations/jane-example-com-abc123/revoke",
			wantAction: model.ActionDelete,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						require.NotNil(t, input.OrgSlug)
						require.Equal(t, must.Slug("acme"), *input.OrgSlug)
						require.Equal(t, must.Slug("owner"), input.UserSlug)
						require.Equal(t, tt.wantAction, input.Action)
						require.Equal(t, model.ResourceMembers, input.Resource)
						return false, nil
					},
				},
			})

			req := jsonRequest(t, tt.method, tt.path, tt.body)
			resp := authenticatedRequest(t, app, store, must.Slug("owner"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			require.Equal(t, http.StatusForbidden, resp.StatusCode)
		})
	}
}

func TestListMemberInvitations(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		membersService: &fakeMembersService{
			ListMemberInvitationsFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				req model.ListRequest,
			) (model.ListResponse[model.MemberInvitation], error) {
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.Equal(t, 2, req.Limit)
				require.Equal(t, model.ListSortEmail, req.SortBy)
				require.Equal(t, model.MemberInvitationStatusPending.String(), req.Status)
				return model.ListResponse[model.MemberInvitation]{
					Items:   []model.MemberInvitation{testMemberInvitation()},
					HasMore: false,
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/organizations/acme/member-invitations?limit=2&sort_by=invitation_email&status=pending", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("owner"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := parseResponse[api.MemberInvitationListResponse](t, resp)
	require.Len(t, body.Invitations, 1)
	require.Equal(t, api.Email("jane@example.com"), body.Invitations[0].Email)
}

func TestListMyMemberInvitations(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("invitee")
	app, store := newTestApp(t, testServerConfig{
		membersService: &fakeMembersService{
			ListMyMemberInvitationsFn: func(
				_ context.Context,
				gotUserSlug slug.Slug,
				req model.ListRequest,
			) (model.ListResponse[model.MemberInvitation], error) {
				require.Equal(t, userSlug, gotUserSlug)
				require.Equal(t, 2, req.Limit)
				require.Equal(t, model.ListSortEmail, req.SortBy)
				return model.ListResponse[model.MemberInvitation]{
					Items:   []model.MemberInvitation{testMemberInvitation()},
					HasMore: false,
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/member-invitations?limit=2&sort_by=invitation_email", nil)
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := parseResponse[api.MemberInvitationListResponse](t, resp)
	require.Len(t, body.Invitations, 1)
	require.Equal(t, api.Email("jane@example.com"), body.Invitations[0].Email)
}

func TestResendAndRevokeMemberInvitation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		setup  func(t *testing.T) *fakeMembersService
		status int
	}{
		{
			name: "Resend",
			path: "/organizations/acme/member-invitations/jane-example-com-abc123/resend",
			setup: func(t *testing.T) *fakeMembersService {
				return &fakeMembersService{
					ResendMemberInvitationFn: func(
						_ context.Context,
						orgSlug, invitationSlug slug.Slug,
						actor model.EventActor,
					) (*model.MemberInvitation, error) {
						require.Equal(t, must.Slug("acme"), orgSlug)
						require.Equal(t, must.Slug("jane-example-com-abc123"), invitationSlug)
						assertUserEventActor(t, actor, must.Slug("owner"))
						invitation := testMemberInvitation()
						return &invitation, nil
					},
				}
			},
			status: http.StatusOK,
		},
		{
			name: "Revoke",
			path: "/organizations/acme/member-invitations/jane-example-com-abc123/revoke",
			setup: func(t *testing.T) *fakeMembersService {
				return &fakeMembersService{
					RevokeMemberInvitationFn: func(
						_ context.Context,
						orgSlug, invitationSlug slug.Slug,
						actor model.EventActor,
					) (*model.MemberInvitation, error) {
						require.Equal(t, must.Slug("acme"), orgSlug)
						require.Equal(t, must.Slug("jane-example-com-abc123"), invitationSlug)
						assertUserEventActor(t, actor, must.Slug("owner"))
						invitation := testMemberInvitation()
						invitation.Status = model.MemberInvitationStatusRevoked
						return &invitation, nil
					},
				}
			},
			status: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{membersService: tt.setup(t)})
			req := jsonRequest(t, http.MethodPost, tt.path, nil)
			resp := authenticatedRequest(t, app, store, must.Slug("owner"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			require.Equal(t, tt.status, resp.StatusCode)
		})
	}
}

func TestAcceptMemberInvitation(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("invitee")
	acceptedInvitation := testMemberInvitation()
	acceptedInvitation.Status = model.MemberInvitationStatusAccepted
	acceptedInvitation.AcceptedUserSlug = &userSlug
	app, store := newTestApp(t, testServerConfig{
		membersService: &fakeMembersService{
			AcceptMemberInvitationFn: func(
				_ context.Context,
				gotUserSlug slug.Slug,
				token string,
				actor model.EventActor,
			) (*model.MemberInvitationAcceptance, error) {
				require.Equal(t, userSlug, gotUserSlug)
				require.Equal(t, "raw-token", token)
				assertUserEventActor(t, actor, userSlug)
				return &model.MemberInvitationAcceptance{
					Invitation: acceptedInvitation,
					Member: model.Member{
						UserSlug: userSlug,
						Email:    "jane@example.com",
						Role:     must.Slug("viewer"),
						Timestamps: model.Timestamps{
							CreatedAt: testInvitationTime,
							UpdatedAt: testInvitationTime,
						},
					},
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/member-invitations/accept", api.AcceptMemberInvitationJSONRequestBody{
		Token: "raw-token",
	})
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := parseResponse[api.MemberInvitationAcceptance](t, resp)
	require.Equal(t, api.MemberInvitationStatus(model.MemberInvitationStatusAccepted.String()), body.Invitation.Status)
	require.Equal(t, api.Email("jane@example.com"), body.Member.Email)
	require.Equal(t, api.MemberRole("viewer"), body.Member.Role)
}

func TestAcceptMyMemberInvitation(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("invitee")
	acceptedInvitation := testMemberInvitation()
	acceptedInvitation.Status = model.MemberInvitationStatusAccepted
	acceptedInvitation.AcceptedUserSlug = &userSlug
	app, store := newTestApp(t, testServerConfig{
		membersService: &fakeMembersService{
			AcceptMyMemberInvitationFn: func(
				_ context.Context,
				gotUserSlug, orgSlug, invitationSlug slug.Slug,
				actor model.EventActor,
			) (*model.MemberInvitationAcceptance, error) {
				require.Equal(t, userSlug, gotUserSlug)
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.Equal(t, must.Slug("jane-example-com-abc123"), invitationSlug)
				assertUserEventActor(t, actor, userSlug)
				return &model.MemberInvitationAcceptance{
					Invitation: acceptedInvitation,
					Member: model.Member{
						UserSlug: userSlug,
						Email:    "jane@example.com",
						Role:     must.Slug("viewer"),
						Timestamps: model.Timestamps{
							CreatedAt: testInvitationTime,
							UpdatedAt: testInvitationTime,
						},
					},
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/member-invitations/acme/jane-example-com-abc123/accept", map[string]any{})
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := parseResponse[api.MemberInvitationAcceptance](t, resp)
	require.Equal(t, api.MemberInvitationStatus(model.MemberInvitationStatusAccepted.String()), body.Invitation.Status)
	require.Equal(t, api.Email("jane@example.com"), body.Member.Email)
	require.Equal(t, api.MemberRole("viewer"), body.Member.Role)
}

func TestDeclineMemberInvitation(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("invitee")
	declinedInvitation := testMemberInvitation()
	declinedInvitation.Status = model.MemberInvitationStatusDeclined
	app, store := newTestApp(t, testServerConfig{
		membersService: &fakeMembersService{
			DeclineMemberInvitationFn: func(
				_ context.Context,
				gotUserSlug slug.Slug,
				token string,
				actor model.EventActor,
			) (*model.MemberInvitation, error) {
				require.Equal(t, userSlug, gotUserSlug)
				require.Equal(t, "raw-token", token)
				assertUserEventActor(t, actor, userSlug)
				return &declinedInvitation, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/member-invitations/decline", api.MemberInvitationTokenRequest{
		Token: "raw-token",
	})
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := parseResponse[api.MemberInvitation](t, resp)
	require.Equal(t, api.MemberInvitationStatus(model.MemberInvitationStatusDeclined.String()), body.Status)
}

func TestDeclineMyMemberInvitation(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("invitee")
	declinedInvitation := testMemberInvitation()
	declinedInvitation.Status = model.MemberInvitationStatusDeclined
	app, store := newTestApp(t, testServerConfig{
		membersService: &fakeMembersService{
			DeclineMyMemberInvitationFn: func(
				_ context.Context,
				gotUserSlug, orgSlug, invitationSlug slug.Slug,
				actor model.EventActor,
			) (*model.MemberInvitation, error) {
				require.Equal(t, userSlug, gotUserSlug)
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.Equal(t, must.Slug("jane-example-com-abc123"), invitationSlug)
				assertUserEventActor(t, actor, userSlug)
				return &declinedInvitation, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/member-invitations/acme/jane-example-com-abc123/decline", nil)
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := parseResponse[api.MemberInvitation](t, resp)
	require.Equal(t, api.MemberInvitationStatus(model.MemberInvitationStatusDeclined.String()), body.Status)
}

func TestMemberAdmissionDenialPreventsInvitations(t *testing.T) {
	for _, path := range []string{"/organizations/acme/member-invitations", "/organizations/acme/member-invitations/invite-one/resend"} {
		t.Run(path, func(t *testing.T) {
			app, store := newTestApp(t, testServerConfig{memberAdmission: fakeMemberAdmission{err: xerror.New(xerror.KindUnavailable, "TEST_MEMBER_ADMISSION_DENIED", "Membership is restricted")}})
			req := jsonRequest(t, http.MethodPost, path, api.NewMemberInvitation{Email: "person@example.com", Role: "viewer"})
			resp := authenticatedRequest(t, app, store, must.Slug("owner"), req)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
		})
	}
}

func TestMemberAdmissionDenialPreventsAddingMembers(t *testing.T) {
	app, store := newTestApp(t, testServerConfig{memberAdmission: fakeMemberAdmission{err: xerror.New(xerror.KindUnavailable, "TEST_MEMBER_ADMISSION_DENIED", "Membership is restricted")}})
	req := jsonRequest(t, http.MethodPost, "/organizations/acme/members", api.NewMember{UserSlug: "person", Role: "viewer"})
	resp := authenticatedRequest(t, app, store, must.Slug("owner"), req)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}
