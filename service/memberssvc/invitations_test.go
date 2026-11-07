package memberssvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
)

func invitationFixture() memberinvitations.Invitation {
	inviter := must.Slug("owner")
	return memberinvitations.Invitation{
		Slug:              must.Slug("jane-example-com-abc123"),
		OrganizationSlug:  must.Slug("acme"),
		Email:             "jane@example.com",
		RoleSlug:          must.Slug("viewer"),
		Status:            model.MemberInvitationStatusPending,
		InvitedByUserSlug: &inviter,
		ExpiresAt:         stubTime.Add(invitationExpiry),
		LastSentAt:        stubTime,
		CreatedAt:         stubTime,
		UpdatedAt:         stubTime,
	}
}

func newInvitationTestService(
	membersRepo *testutil.FakeMembersRepo,
	invitationsRepo *testutil.FakeMemberInvitationsRepo,
	emailProducer *testutil.FakeEmailProducer,
) Service {
	return newInvitationTestServiceWithEventRecorder(membersRepo, invitationsRepo, emailProducer, nil)
}

func newInvitationTestServiceWithEventRecorder(
	membersRepo *testutil.FakeMembersRepo,
	invitationsRepo *testutil.FakeMemberInvitationsRepo,
	emailProducer *testutil.FakeEmailProducer,
	events *fakeMembersEventRecorder,
) Service {
	if membersRepo.FindByEmailFn == nil {
		membersRepo.FindByEmailFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ string) (*members.Member, error) {
			return nil, members.ErrNotFound
		}
	}
	if invitationsRepo.FindPendingByEmailFn == nil {
		invitationsRepo.FindPendingByEmailFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ string) (*memberinvitations.Invitation, error) {
			return nil, memberinvitations.ErrNotFound
		}
	}
	if invitationsRepo.ExpirePendingByOrganizationFn == nil {
		invitationsRepo.ExpirePendingByOrganizationFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ time.Time) ([]*memberinvitations.Invitation, error) {
			return nil, nil
		}
	}
	orgsRepo := &testutil.FakeOrgsRepo{
		FindBySlugFn: func(_ context.Context, _ postgres.DBTX, orgSlug slug.Slug) (*organizations.Organization, error) {
			return &organizations.Organization{Name: "Acme", Slug: orgSlug}, nil
		},
	}
	rolesService := &fakes.RolesService{
		GetRoleFn: func(_ context.Context, _, roleSlug slug.Slug) (*model.OrgRole, error) {
			if roleSlug != "viewer" {
				return nil, rolessvc.ErrRoleNotFound
			}
			return &model.OrgRole{Slug: roleSlug}, nil
		},
	}
	usersService := &fakes.UsersService{
		GetUserBySlugFn: func(_ context.Context, userSlug slug.Slug) (*model.User, error) {
			if userSlug != "owner" {
				return &model.User{
					Slug:       userSlug,
					Email:      "jane@example.com",
					FullName:   "Jane Invitee",
					Locale:     "en",
					VerifiedAt: stubTime,
				}, nil
			}
			return &model.User{
				Slug:     userSlug,
				Email:    "owner@example.com",
				FullName: "Owner User",
				Locale:   "en",
			}, nil
		},
	}
	eventProducer := eventtest.NewProducer(nil)
	if events != nil {
		eventProducer = eventtest.NewProducer(events)
	}

	return newTestServiceWithDeps(
		membersRepo,
		invitationsRepo,
		orgsRepo,
		rolesService,
		usersService,
		emailProducer,
		eventProducer,
	)
}

func requireMemberInvitationEvent(
	t *testing.T,
	got eventssvc.CreateEventParams,
	orgSlug slug.Slug,
	actorSlug slug.Slug,
	invitation model.MemberInvitation,
	action model.EventAction,
) {
	t.Helper()

	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventClassSecurityAudit, got.EventClass)
	require.Equal(t, model.CurrentEventSchemaVersion, got.SchemaVersion)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeMemberInvitation, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, invitation.Slug, *got.EntitySlug)
	require.Equal(t, action, got.Action)
	require.NotContains(t, got.Metadata, "email")
	require.Equal(t, invitation.Role.String(), got.Metadata["role_slug"])
	require.Equal(t, invitation.Status.String(), got.Metadata["status"])
	if invitation.InvitedByUserSlug != nil {
		require.Equal(t, invitation.InvitedByUserSlug.String(), got.Metadata["invited_by_user_slug"])
	}
	if invitation.AcceptedUserSlug != nil {
		require.Equal(t, invitation.AcceptedUserSlug.String(), got.Metadata["accepted_user_slug"])
	}
	require.NotContains(t, got.Metadata, "token")
	require.NotContains(t, got.Metadata, "token_hash")
}

func TestListMemberInvitationsExpiresPendingRowsBeforePaginating(t *testing.T) {
	t.Parallel()

	expiredPending := invitationFixture()
	expiredPending.ExpiresAt = stubTime.Add(-time.Second)
	expiredPending.Status = model.MemberInvitationStatusExpired
	expiredPending.ExpiredAt = &stubTime
	currentPending := invitationFixture()
	currentPending.Slug = must.Slug("sam-example-com-abc123")
	currentPending.Email = "sam@example.com"

	var calls []string
	events := &fakeMembersEventRecorder{}
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		ExpirePendingByOrganizationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			orgSlug slug.Slug,
			expiredAt time.Time,
		) ([]*memberinvitations.Invitation, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, stubTime, expiredAt)
			calls = append(calls, "expire")
			return []*memberinvitations.Invitation{&expiredPending}, nil
		},
		ListByOrganizationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			orgSlug slug.Slug,
			_ model.ListRequest,
		) ([]*memberinvitations.Invitation, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, []string{"expire"}, calls)
			calls = append(calls, "list")
			return []*memberinvitations.Invitation{&expiredPending, &currentPending}, nil
		},
	}
	svc := newInvitationTestServiceWithEventRecorder(
		&testutil.FakeMembersRepo{},
		invitationsRepo,
		&testutil.FakeEmailProducer{},
		events,
	)

	got, err := svc.ListMemberInvitations(context.Background(), must.Slug("acme"), model.ListRequest{Limit: 20})

	require.NoError(t, err)
	require.Equal(t, []string{"expire", "list"}, calls)
	require.Len(t, got.Items, 2)
	require.Equal(t, model.MemberInvitationStatusExpired, got.Items[0].Status)
	require.NotNil(t, got.Items[0].ExpiredAt)
	require.Equal(t, model.MemberInvitationStatusPending, got.Items[1].Status)
	require.Len(t, events.calls, 1)
	require.Equal(t, model.EventActorTypeSystem, events.calls[0].ActorType)
	require.Nil(t, events.calls[0].ActorSlug)
	require.Equal(t, model.EventActionExpire, events.calls[0].Action)
	require.NotNil(t, events.calls[0].EntitySlug)
	require.Equal(t, expiredPending.Slug, *events.calls[0].EntitySlug)
}

func TestListMemberInvitationsPreservesNanosecondTimestampCursors(t *testing.T) {
	t.Parallel()

	first := invitationFixture()
	first.CreatedAt = stubTime.Add(123456789 * time.Nanosecond)
	first.UpdatedAt = first.CreatedAt
	second := invitationFixture()
	second.Slug = must.Slug("sam-example-com-abc123")
	second.Email = "sam@example.com"
	second.CreatedAt = stubTime.Add(987654321 * time.Nanosecond)
	second.UpdatedAt = second.CreatedAt

	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		ListByOrganizationFn: func(
			_ context.Context,
			_ postgres.DBTX,
			_ slug.Slug,
			_ model.ListRequest,
		) ([]*memberinvitations.Invitation, error) {
			return []*memberinvitations.Invitation{&first, &second}, nil
		},
	}
	svc := newInvitationTestService(&testutil.FakeMembersRepo{}, invitationsRepo, &testutil.FakeEmailProducer{})

	got, err := svc.ListMemberInvitations(context.Background(), must.Slug("acme"), model.ListRequest{Limit: 1})

	require.NoError(t, err)
	require.NotNil(t, got.NextCursor)
	require.Contains(t, *got.NextCursor, first.CreatedAt.Format(time.RFC3339Nano))
	require.Contains(t, *got.NextCursor, first.Slug.String())
}

func TestGetMemberInvitationRecordsExpirationEvent(t *testing.T) {
	t.Parallel()

	invitation := invitationFixture()
	invitation.ExpiresAt = stubTime.Add(-time.Second)
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindFn: func(_ context.Context, _ postgres.DBTX, orgSlug, invitationSlug slug.Slug) (*memberinvitations.Invitation, error) {
			require.Equal(t, invitation.OrganizationSlug, orgSlug)
			require.Equal(t, invitation.Slug, invitationSlug)
			return &invitation, nil
		},
		MarkExpiredFn: func(_ context.Context, _ postgres.DBTX, params memberinvitations.MarkExpiredParams) (*memberinvitations.Invitation, error) {
			require.Equal(t, invitation.OrganizationSlug, params.OrganizationSlug)
			require.Equal(t, invitation.Slug, params.InvitationSlug)
			expired := invitation
			expired.Status = model.MemberInvitationStatusExpired
			expired.ExpiredAt = &params.ExpiredAt
			return &expired, nil
		},
	}
	events := &fakeMembersEventRecorder{}
	svc := newInvitationTestServiceWithEventRecorder(
		&testutil.FakeMembersRepo{},
		invitationsRepo,
		&testutil.FakeEmailProducer{},
		events,
	)

	got, err := svc.GetMemberInvitation(context.Background(), must.Slug("acme"), invitation.Slug)

	require.NoError(t, err)
	require.Equal(t, model.MemberInvitationStatusExpired, got.Status)
	require.NotNil(t, got.ExpiredAt)
	require.Len(t, events.calls, 1)
	require.Equal(t, model.EventActorTypeSystem, events.calls[0].ActorType)
	require.Nil(t, events.calls[0].ActorSlug)
	require.Equal(t, model.EventActionExpire, events.calls[0].Action)
	require.NotNil(t, events.calls[0].EntitySlug)
	require.Equal(t, invitation.Slug, *events.calls[0].EntitySlug)
	require.Equal(t, model.EventEntityTypeMemberInvitation, events.calls[0].EntityType)
	require.Equal(t, model.MemberInvitationStatusExpired.String(), events.calls[0].Metadata["status"])
}

func TestListMyMemberInvitationsUsesVerifiedUserEmail(t *testing.T) {
	t.Parallel()

	later := invitationFixture()
	later.Slug = must.Slug("later")
	later.ExpiresAt = stubTime.Add(3 * time.Hour)
	sooner := invitationFixture()
	sooner.Slug = must.Slug("sooner")
	sooner.ExpiresAt = stubTime.Add(time.Hour)

	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		ListPendingByEmailFn: func(_ context.Context, _ postgres.DBTX, email string, asOf time.Time) ([]*memberinvitations.Invitation, error) {
			require.Equal(t, "jane@example.com", email)
			require.Equal(t, stubTime, asOf)
			return []*memberinvitations.Invitation{&later, &sooner}, nil
		},
	}
	svc := newInvitationTestService(&testutil.FakeMembersRepo{}, invitationsRepo, &testutil.FakeEmailProducer{})

	got, err := svc.ListMyMemberInvitations(
		context.Background(),
		must.Slug("invitee"),
		model.ListRequest{Limit: 1, SortBy: model.ListSortExpiresAt, Order: model.ListOrderAsc},
	)

	require.NoError(t, err)
	require.True(t, got.HasMore)
	require.NotNil(t, got.NextCursor)
	require.Len(t, got.Items, 1)
	require.Equal(t, must.Slug("sooner"), got.Items[0].Slug)
}

func TestListMyMemberInvitationsComparesTimestampCursorChronologically(t *testing.T) {
	t.Parallel()

	wholeSecond := invitationFixture()
	wholeSecond.Slug = must.Slug("whole-second")
	wholeSecond.CreatedAt = stubTime
	fractional := invitationFixture()
	fractional.Slug = must.Slug("fractional")
	fractional.CreatedAt = stubTime.Add(100 * time.Millisecond)

	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		ListPendingByEmailFn: func(_ context.Context, _ postgres.DBTX, _ string, _ time.Time) ([]*memberinvitations.Invitation, error) {
			return []*memberinvitations.Invitation{&fractional, &wholeSecond}, nil
		},
	}
	svc := newInvitationTestService(&testutil.FakeMembersRepo{}, invitationsRepo, &testutil.FakeEmailProducer{})

	got, err := svc.ListMyMemberInvitations(
		context.Background(),
		must.Slug("invitee"),
		model.ListRequest{
			Limit:  10,
			SortBy: model.ListSortCreatedAt,
			Order:  model.ListOrderAsc,
			Cursor: wholeSecond.CreatedAt.Format(time.RFC3339Nano) + "_" + wholeSecond.Slug.String(),
		},
	)

	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	require.Equal(t, fractional.Slug, got.Items[0].Slug)
}

func TestListMyMemberInvitationsRejectsMalformedCursor(t *testing.T) {
	t.Parallel()

	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		ListPendingByEmailFn: func(_ context.Context, _ postgres.DBTX, _ string, _ time.Time) ([]*memberinvitations.Invitation, error) {
			invitation := invitationFixture()
			return []*memberinvitations.Invitation{&invitation}, nil
		},
	}
	svc := newInvitationTestService(&testutil.FakeMembersRepo{}, invitationsRepo, &testutil.FakeEmailProducer{})

	_, err := svc.ListMyMemberInvitations(
		context.Background(),
		must.Slug("invitee"),
		model.ListRequest{
			Limit:  10,
			SortBy: model.ListSortCreatedAt,
			Order:  model.ListOrderAsc,
			Cursor: "not-a-valid-cursor",
		},
	)

	require.ErrorIs(t, err, xerror.InvalidInput(xerror.Cursor))
}

func TestListMyMemberInvitationsRequiresVerifiedEmail(t *testing.T) {
	t.Parallel()

	usersService := &fakes.UsersService{
		GetUserBySlugFn: func(_ context.Context, userSlug slug.Slug) (*model.User, error) {
			return &model.User{
				Slug:  userSlug,
				Email: "jane@example.com",
			}, nil
		},
	}
	svc := newTestServiceWithDeps(
		&testutil.FakeMembersRepo{},
		&testutil.FakeMemberInvitationsRepo{},
		&testutil.FakeOrgsRepo{},
		&fakes.RolesService{},
		usersService,
		&testutil.FakeEmailProducer{},
		nil,
	)

	_, err := svc.ListMyMemberInvitations(
		context.Background(),
		must.Slug("invitee"),
		model.ListRequest{},
	)

	require.ErrorIs(t, err, xerror.EmailNotVerified())
}

func TestCreateMemberInvitationCreatesRecordAndQueuesEmail(t *testing.T) {
	t.Parallel()

	membersRepo := &testutil.FakeMembersRepo{}
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{}
	emailProducer := &testutil.FakeEmailProducer{}
	events := &fakeMembersEventRecorder{}
	var created memberinvitations.CreateParams
	invitationsRepo.CreateFn = func(
		_ context.Context,
		_ postgres.DBTX,
		params memberinvitations.CreateParams,
	) (*memberinvitations.Invitation, error) {
		created = params
		invitation := invitationFixture()
		invitation.Slug = params.InvitationSlug
		invitation.Email = params.Email
		invitation.ExpiresAt = params.ExpiresAt
		return &invitation, nil
	}
	svc := newInvitationTestServiceWithEventRecorder(membersRepo, invitationsRepo, emailProducer, events)

	got, err := svc.CreateMemberInvitation(
		context.Background(),
		must.Slug("acme"),
		must.Slug("owner"),
		model.UserEventActor(must.Slug("owner")),
		model.NewMemberInvitation{Email: " Jane@Example.COM ", Role: "viewer"},
	)

	require.NoError(t, err)
	require.Equal(t, "jane@example.com", got.Email)
	require.Equal(t, must.Slug("viewer"), got.Role)
	require.Equal(t, "jane@example.com", created.Email)
	require.NotNil(t, created.InvitedByUserSlug)
	require.Equal(t, must.Slug("owner"), *created.InvitedByUserSlug)
	require.NotEmpty(t, created.TokenHash)
	require.Equal(t, stubTime.Add(invitationExpiry), created.ExpiresAt)
	require.Equal(t, 1, emailProducer.EnqueueMemberInvitationCalls)
	require.Equal(t, "jane@example.com", emailProducer.LastMemberInvitation.Email)
	require.Equal(t, "Acme", emailProducer.LastMemberInvitation.OrganizationName)
	require.Equal(t, "Owner User", emailProducer.LastMemberInvitation.InviterName)
	require.Equal(t, hashInvitationToken(emailProducer.LastMemberInvitationToken), created.TokenHash)
	require.Equal(t, "https://app.example.com/member-invitations/accept", emailProducer.LastMemberInvitationURL)
	require.Len(t, events.calls, 1)
	requireMemberInvitationEvent(t, events.calls[0], must.Slug("acme"), must.Slug("owner"), *got, model.EventActionCreate)
}

func TestCreateMemberInvitationSupportsAPIKeyActorWithoutInviterUser(t *testing.T) {
	t.Parallel()

	membersRepo := &testutil.FakeMembersRepo{}
	emailProducer := &testutil.FakeEmailProducer{}
	var created memberinvitations.CreateParams
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{}
	invitationsRepo.CreateFn = func(_ context.Context, _ postgres.DBTX, params memberinvitations.CreateParams) (*memberinvitations.Invitation, error) {
		created = params
		invitation := invitationFixture()
		invitation.InvitedByUserSlug = nil
		invitation.Slug = params.InvitationSlug
		invitation.Email = params.Email
		invitation.ExpiresAt = params.ExpiresAt
		return &invitation, nil
	}
	svc := newInvitationTestService(membersRepo, invitationsRepo, emailProducer)

	got, err := svc.CreateMemberInvitation(
		context.Background(),
		must.Slug("acme"),
		"",
		model.APIKeyEventActor(must.Slug("deploy-key")),
		model.NewMemberInvitation{Email: "jane@example.com", Role: "viewer"},
	)

	require.NoError(t, err)
	require.Nil(t, created.InvitedByUserSlug)
	require.Nil(t, got.InvitedByUserSlug)
	require.Equal(t, "API key deploy-key", emailProducer.LastMemberInvitation.InviterName)
	require.Equal(t, "no-reply@braidplane.local", emailProducer.LastMemberInvitation.InviterEmail)
}

func TestCreateMemberInvitationRejectsExistingMemberEmail(t *testing.T) {
	t.Parallel()

	membersRepo := &testutil.FakeMembersRepo{
		FindByEmailFn: func(_ context.Context, _ postgres.DBTX, orgSlug slug.Slug, email string) (*members.Member, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, "jane@example.com", email)
			return stubMember("jane", "jane@example.com", "viewer"), nil
		},
	}
	svc := newInvitationTestService(membersRepo, &testutil.FakeMemberInvitationsRepo{}, &testutil.FakeEmailProducer{})

	_, err := svc.CreateMemberInvitation(
		context.Background(),
		must.Slug("acme"),
		must.Slug("owner"),
		model.UserEventActor(must.Slug("owner")),
		model.NewMemberInvitation{Email: "jane@example.com", Role: "viewer"},
	)

	require.ErrorIs(t, err, ErrMemberAlreadyExists)
}

func TestCreateMemberInvitationRejectsDuplicatePendingInvitation(t *testing.T) {
	t.Parallel()

	existing := invitationFixture()
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindPendingByEmailFn: func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ string) (*memberinvitations.Invitation, error) {
			return &existing, nil
		},
	}
	svc := newInvitationTestService(&testutil.FakeMembersRepo{}, invitationsRepo, &testutil.FakeEmailProducer{})

	_, err := svc.CreateMemberInvitation(
		context.Background(),
		must.Slug("acme"),
		must.Slug("owner"),
		model.UserEventActor(must.Slug("owner")),
		model.NewMemberInvitation{Email: "jane@example.com", Role: "viewer"},
	)

	require.ErrorIs(t, err, ErrInvitationPending)
}

func TestCreateMemberInvitationExpiresOldPendingInvitationBeforeCreatingNewOne(t *testing.T) {
	t.Parallel()

	existing := invitationFixture()
	existing.ExpiresAt = stubTime.Add(-time.Second)
	var expired bool
	var created bool
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindPendingByEmailFn: func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ string) (*memberinvitations.Invitation, error) {
			return &existing, nil
		},
		MarkExpiredFn: func(_ context.Context, _ postgres.DBTX, params memberinvitations.MarkExpiredParams) (*memberinvitations.Invitation, error) {
			require.Equal(t, existing.Slug, params.InvitationSlug)
			expired = true
			expiredInvitation := existing
			expiredInvitation.Status = model.MemberInvitationStatusExpired
			expiredInvitation.ExpiredAt = &params.ExpiredAt
			return &expiredInvitation, nil
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, params memberinvitations.CreateParams) (*memberinvitations.Invitation, error) {
			created = true
			invitation := invitationFixture()
			invitation.Slug = params.InvitationSlug
			return &invitation, nil
		},
	}
	events := &fakeMembersEventRecorder{}
	svc := newInvitationTestServiceWithEventRecorder(
		&testutil.FakeMembersRepo{},
		invitationsRepo,
		&testutil.FakeEmailProducer{},
		events,
	)

	_, err := svc.CreateMemberInvitation(
		context.Background(),
		must.Slug("acme"),
		must.Slug("owner"),
		model.UserEventActor(must.Slug("owner")),
		model.NewMemberInvitation{Email: "jane@example.com", Role: "viewer"},
	)

	require.NoError(t, err)
	require.True(t, expired)
	require.True(t, created)
	require.Len(t, events.calls, 2)
	expiredModel := *toMemberInvitation(&existing)
	expiredModel.Status = model.MemberInvitationStatusExpired
	expiredModel.ExpiredAt = &stubTime
	requireMemberInvitationEvent(t, events.calls[0], must.Slug("acme"), must.Slug("owner"), expiredModel, model.EventActionExpire)
}

func TestResendMemberInvitationRotatesTokenAndQueuesEmail(t *testing.T) {
	t.Parallel()

	invitation := invitationFixture()
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindFn: func(_ context.Context, _ postgres.DBTX, orgSlug, invitationSlug slug.Slug) (*memberinvitations.Invitation, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, invitation.Slug, invitationSlug)
			return &invitation, nil
		},
		RefreshTokenFn: func(_ context.Context, _ postgres.DBTX, params memberinvitations.RefreshTokenParams) (*memberinvitations.Invitation, error) {
			require.Equal(t, invitation.Slug, params.InvitationSlug)
			require.NotEmpty(t, params.TokenHash)
			refreshed := invitation
			refreshed.ExpiresAt = params.ExpiresAt
			refreshed.LastSentAt = params.LastSentAt
			return &refreshed, nil
		},
	}
	emailProducer := &testutil.FakeEmailProducer{}
	events := &fakeMembersEventRecorder{}
	svc := newInvitationTestServiceWithEventRecorder(&testutil.FakeMembersRepo{}, invitationsRepo, emailProducer, events)

	got, err := svc.ResendMemberInvitation(
		context.Background(),
		must.Slug("acme"),
		invitation.Slug,
		model.UserEventActor(must.Slug("owner")),
	)

	require.NoError(t, err)
	require.Equal(t, stubTime, got.LastSentAt)
	require.Equal(t, 1, emailProducer.EnqueueMemberInvitationCalls)
	require.NotEmpty(t, emailProducer.LastMemberInvitationToken)
	require.Len(t, events.calls, 1)
	requireMemberInvitationEvent(t, events.calls[0], must.Slug("acme"), must.Slug("owner"), *got, model.EventActionRefresh)
}

func TestResendMemberInvitationExpiresAndRejectsExpiredInvitation(t *testing.T) {
	t.Parallel()

	invitation := invitationFixture()
	invitation.ExpiresAt = stubTime.Add(-time.Second)
	var expired bool
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindFn: func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*memberinvitations.Invitation, error) {
			return &invitation, nil
		},
		MarkExpiredFn: func(_ context.Context, _ postgres.DBTX, params memberinvitations.MarkExpiredParams) (*memberinvitations.Invitation, error) {
			expired = true
			expiredInvitation := invitation
			expiredInvitation.Status = model.MemberInvitationStatusExpired
			expiredInvitation.ExpiredAt = &params.ExpiredAt
			return &expiredInvitation, nil
		},
	}
	events := &fakeMembersEventRecorder{}
	svc := newInvitationTestServiceWithEventRecorder(&testutil.FakeMembersRepo{}, invitationsRepo, &testutil.FakeEmailProducer{}, events)

	got, err := svc.ResendMemberInvitation(
		context.Background(),
		must.Slug("acme"),
		invitation.Slug,
		model.UserEventActor(must.Slug("owner")),
	)

	require.ErrorIs(t, err, ErrInvitationExpired)
	require.True(t, expired)
	require.NotNil(t, got)
	require.Equal(t, model.MemberInvitationStatusExpired, got.Status)
	require.Len(t, events.calls, 1)
	requireMemberInvitationEvent(t, events.calls[0], must.Slug("acme"), must.Slug("owner"), *got, model.EventActionExpire)
}

func TestRevokeMemberInvitationMarksPendingInvitationRevoked(t *testing.T) {
	t.Parallel()

	invitation := invitationFixture()
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindFn: func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*memberinvitations.Invitation, error) {
			return &invitation, nil
		},
		MarkRevokedFn: func(_ context.Context, _ postgres.DBTX, params memberinvitations.MarkRevokedParams) (*memberinvitations.Invitation, error) {
			require.Equal(t, invitation.Slug, params.InvitationSlug)
			revoked := invitation
			revoked.Status = model.MemberInvitationStatusRevoked
			revoked.RevokedAt = &params.RevokedAt
			return &revoked, nil
		},
	}
	events := &fakeMembersEventRecorder{}
	svc := newInvitationTestServiceWithEventRecorder(
		&testutil.FakeMembersRepo{},
		invitationsRepo,
		&testutil.FakeEmailProducer{},
		events,
	)

	got, err := svc.RevokeMemberInvitation(
		context.Background(),
		must.Slug("acme"),
		invitation.Slug,
		model.UserEventActor(must.Slug("owner")),
	)

	require.NoError(t, err)
	require.Equal(t, model.MemberInvitationStatusRevoked, got.Status)
	require.NotNil(t, got.RevokedAt)
	require.Len(t, events.calls, 1)
	requireMemberInvitationEvent(t, events.calls[0], must.Slug("acme"), must.Slug("owner"), *got, model.EventActionRevoke)
}

func TestAcceptMemberInvitationCreatesMemberAndMarksInvitationAccepted(t *testing.T) {
	t.Parallel()

	invitation := invitationFixture()
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindByTokenHashFn: func(_ context.Context, _ postgres.DBTX, tokenHash string) (*memberinvitations.Invitation, error) {
			require.Equal(t, hashInvitationToken("raw-token"), tokenHash)
			return &invitation, nil
		},
		MarkAcceptedFn: func(_ context.Context, _ postgres.DBTX, params memberinvitations.MarkAcceptedParams) (*memberinvitations.Invitation, error) {
			require.Equal(t, invitation.OrganizationSlug, params.OrganizationSlug)
			require.Equal(t, invitation.Slug, params.InvitationSlug)
			require.Equal(t, must.Slug("invitee"), params.AcceptedUserSlug)
			accepted := invitation
			accepted.Status = model.MemberInvitationStatusAccepted
			accepted.AcceptedUserSlug = &params.AcceptedUserSlug
			accepted.AcceptedAt = &params.AcceptedAt
			return &accepted, nil
		},
	}
	membersRepo := &testutil.FakeMembersRepo{
		FindFn: func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
			return nil, members.ErrNotFound
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, params *members.CreateParams) (*members.Member, error) {
			require.Equal(t, invitation.OrganizationSlug, params.OrganizationSlug)
			require.Equal(t, must.Slug("invitee"), params.UserSlug)
			require.Equal(t, invitation.RoleSlug, params.RoleSlug)
			return stubMember(params.UserSlug, invitation.Email, params.RoleSlug.String()), nil
		},
	}
	events := &fakeMembersEventRecorder{}
	svc := newInvitationTestServiceWithEventRecorder(membersRepo, invitationsRepo, &testutil.FakeEmailProducer{}, events)

	got, err := svc.AcceptMemberInvitation(
		context.Background(),
		must.Slug("invitee"),
		"raw-token",
		model.UserEventActor(must.Slug("invitee")),
	)

	require.NoError(t, err)
	require.Equal(t, model.MemberInvitationStatusAccepted, got.Invitation.Status)
	require.Equal(t, must.Slug("invitee"), got.Member.UserSlug)
	require.Equal(t, must.Slug("viewer"), got.Member.Role)
	require.Len(t, events.calls, 2)
	require.Equal(t, model.EventEntityTypeMember, events.calls[0].EntityType)
	require.Equal(t, model.EventActionCreate, events.calls[0].Action)
	requireMemberInvitationEvent(t, events.calls[1], must.Slug("acme"), must.Slug("invitee"), got.Invitation, model.EventActionAccept)
}

func TestAcceptMemberInvitationReturnsCallbackFailure(t *testing.T) {
	t.Parallel()
	invitation := invitationFixture()
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindByTokenHashFn: func(context.Context, postgres.DBTX, string) (*memberinvitations.Invitation, error) {
			return &invitation, nil
		},
		MarkAcceptedFn: func(_ context.Context, _ postgres.DBTX, params memberinvitations.MarkAcceptedParams) (*memberinvitations.Invitation, error) {
			accepted := invitation
			accepted.Status = model.MemberInvitationStatusAccepted
			accepted.AcceptedUserSlug = &params.AcceptedUserSlug
			return &accepted, nil
		},
	}
	membersRepo := &testutil.FakeMembersRepo{
		FindFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*members.Member, error) {
			return nil, members.ErrNotFound
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, params *members.CreateParams) (*members.Member, error) {
			return stubMember(params.UserSlug, invitation.Email, params.RoleSlug.String()), nil
		},
	}
	events := &fakeMembersEventRecorder{}
	svc := newInvitationTestServiceWithEventRecorder(membersRepo, invitationsRepo, &testutil.FakeEmailProducer{}, events)
	svc.(*service).accepted = func(_ context.Context, _ postgres.DBTX, _ *memberinvitations.Invitation, _ model.User, retry bool) error {
		require.False(t, retry)
		return xerror.Unexpected().Detail("callback unavailable")
	}

	got, err := svc.AcceptMemberInvitation(context.Background(), must.Slug("invitee"), "raw-token", model.UserEventActor(must.Slug("invitee")))

	require.Nil(t, got)
	require.ErrorContains(t, err, "callback unavailable")
	require.Empty(t, events.calls)
}

func TestAcceptMemberInvitationIsIdempotentForAcceptedInvitation(t *testing.T) {
	t.Parallel()

	acceptedUserSlug := must.Slug("invitee")
	invitation := invitationFixture()
	invitation.Status = model.MemberInvitationStatusAccepted
	invitation.AcceptedUserSlug = &acceptedUserSlug
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindByTokenHashFn: func(_ context.Context, _ postgres.DBTX, _ string) (*memberinvitations.Invitation, error) {
			return &invitation, nil
		},
		MarkAcceptedFn: func(context.Context, postgres.DBTX, memberinvitations.MarkAcceptedParams) (*memberinvitations.Invitation, error) {
			t.Fatal("accepted invitation should not be marked accepted again")
			return nil, xerror.Unexpected().Detail("unexpected accepted invitation mutation")
		},
	}
	membersRepo := &testutil.FakeMembersRepo{
		FindFn: func(_ context.Context, _ postgres.DBTX, orgSlug, userSlug slug.Slug) (*members.Member, error) {
			require.Equal(t, invitation.OrganizationSlug, orgSlug)
			require.Equal(t, acceptedUserSlug, userSlug)
			return stubMember(userSlug, invitation.Email, invitation.RoleSlug.String()), nil
		},
	}
	svc := newInvitationTestService(membersRepo, invitationsRepo, &testutil.FakeEmailProducer{})

	callbackCalls := 0
	svc.(*service).accepted = func(_ context.Context, _ postgres.DBTX, _ *memberinvitations.Invitation, _ model.User, retry bool) error {
		require.True(t, retry)
		callbackCalls++
		return nil
	}

	got, err := svc.AcceptMemberInvitation(
		context.Background(),
		acceptedUserSlug,
		"raw-token",
		model.UserEventActor(acceptedUserSlug),
	)

	require.Equal(t, 1, callbackCalls)
	require.NoError(t, err)
	require.Equal(t, model.MemberInvitationStatusAccepted, got.Invitation.Status)
	require.Equal(t, acceptedUserSlug, got.Member.UserSlug)
}

func TestAcceptMemberInvitationRejectsExistingMemberWithDifferentRole(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("invitee")
	invitation := invitationFixture()
	invitation.RoleSlug = must.Slug("admin")
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindByTokenHashFn: func(_ context.Context, _ postgres.DBTX, _ string) (*memberinvitations.Invitation, error) {
			return &invitation, nil
		},
		MarkAcceptedFn: func(context.Context, postgres.DBTX, memberinvitations.MarkAcceptedParams) (*memberinvitations.Invitation, error) {
			t.Fatal("role-conflicting invitation should not be marked accepted")
			return nil, xerror.Unexpected().Detail("unexpected invitation acceptance")
		},
	}
	membersRepo := &testutil.FakeMembersRepo{
		FindFn: func(_ context.Context, _ postgres.DBTX, orgSlug, gotUserSlug slug.Slug) (*members.Member, error) {
			require.Equal(t, invitation.OrganizationSlug, orgSlug)
			require.Equal(t, userSlug, gotUserSlug)
			return stubMember(userSlug, invitation.Email, "viewer"), nil
		},
		CreateFn: func(context.Context, postgres.DBTX, *members.CreateParams) (*members.Member, error) {
			t.Fatal("existing member should not be created again")
			return nil, xerror.Unexpected().Detail("unexpected member create")
		},
	}
	svc := newInvitationTestService(membersRepo, invitationsRepo, &testutil.FakeEmailProducer{})

	_, err := svc.AcceptMemberInvitation(
		context.Background(),
		userSlug,
		"raw-token",
		model.UserEventActor(userSlug),
	)

	require.ErrorIs(t, err, ErrInvitationNotFound)
}

func TestAcceptMemberInvitationRejectsInvalidStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(*memberinvitations.Invitation, *testutil.FakeMemberInvitationsRepo)
		userSlug  slug.Slug
		wantError error
	}{
		{
			name: "TokenMismatch",
			setup: func(_ *memberinvitations.Invitation, repo *testutil.FakeMemberInvitationsRepo) {
				repo.FindByTokenHashFn = func(context.Context, postgres.DBTX, string) (*memberinvitations.Invitation, error) {
					return nil, memberinvitations.ErrNotFound
				}
			},
			userSlug:  must.Slug("invitee"),
			wantError: ErrInvitationNotFound,
		},
		{
			name: "EmailMismatch",
			setup: func(invitation *memberinvitations.Invitation, repo *testutil.FakeMemberInvitationsRepo) {
				invitation.Email = "other@example.com"
				repo.FindByTokenHashFn = func(context.Context, postgres.DBTX, string) (*memberinvitations.Invitation, error) {
					return invitation, nil
				}
			},
			userSlug:  must.Slug("invitee"),
			wantError: ErrInvitationNotFound,
		},
		{
			name: "Expired",
			setup: func(invitation *memberinvitations.Invitation, repo *testutil.FakeMemberInvitationsRepo) {
				invitation.ExpiresAt = stubTime.Add(-time.Second)
				repo.FindByTokenHashFn = func(context.Context, postgres.DBTX, string) (*memberinvitations.Invitation, error) {
					return invitation, nil
				}
				repo.MarkExpiredFn = func(context.Context, postgres.DBTX, memberinvitations.MarkExpiredParams) (*memberinvitations.Invitation, error) {
					expired := *invitation
					expired.Status = model.MemberInvitationStatusExpired
					return &expired, nil
				}
			},
			userSlug:  must.Slug("invitee"),
			wantError: ErrInvitationNotFound,
		},
		{
			name: "Revoked",
			setup: func(invitation *memberinvitations.Invitation, repo *testutil.FakeMemberInvitationsRepo) {
				invitation.Status = model.MemberInvitationStatusRevoked
				repo.FindByTokenHashFn = func(context.Context, postgres.DBTX, string) (*memberinvitations.Invitation, error) {
					return invitation, nil
				}
			},
			userSlug:  must.Slug("invitee"),
			wantError: ErrInvitationNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			invitation := invitationFixture()
			invitationsRepo := &testutil.FakeMemberInvitationsRepo{}
			tt.setup(&invitation, invitationsRepo)
			svc := newInvitationTestService(&testutil.FakeMembersRepo{}, invitationsRepo, &testutil.FakeEmailProducer{})

			_, err := svc.AcceptMemberInvitation(
				context.Background(),
				tt.userSlug,
				"raw-token",
				model.UserEventActor(tt.userSlug),
			)

			require.ErrorIs(t, err, tt.wantError)
		})
	}
}

func TestValidateInvitationUserChecksEmailBeforeVerification(t *testing.T) {
	t.Parallel()

	invitation := invitationFixture()
	err := validateInvitationUser(invitation, model.User{
		Slug:  must.Slug("other-user"),
		Email: "other@example.com",
	})

	require.ErrorIs(t, err, ErrInvitationNotFound)
}

func TestAcceptMemberInvitationRecordsExpirationEvent(t *testing.T) {
	t.Parallel()

	invitation := invitationFixture()
	invitation.ExpiresAt = stubTime.Add(-time.Second)
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindByTokenHashFn: func(_ context.Context, _ postgres.DBTX, _ string) (*memberinvitations.Invitation, error) {
			return &invitation, nil
		},
		MarkExpiredFn: func(_ context.Context, _ postgres.DBTX, params memberinvitations.MarkExpiredParams) (*memberinvitations.Invitation, error) {
			require.Equal(t, invitation.OrganizationSlug, params.OrganizationSlug)
			require.Equal(t, invitation.Slug, params.InvitationSlug)
			expired := invitation
			expired.Status = model.MemberInvitationStatusExpired
			expired.ExpiredAt = &params.ExpiredAt
			return &expired, nil
		},
	}
	events := &fakeMembersEventRecorder{}
	svc := newInvitationTestServiceWithEventRecorder(
		&testutil.FakeMembersRepo{},
		invitationsRepo,
		&testutil.FakeEmailProducer{},
		events,
	)

	_, err := svc.AcceptMemberInvitation(
		context.Background(),
		must.Slug("invitee"),
		"raw-token",
		model.UserEventActor(must.Slug("invitee")),
	)

	require.ErrorIs(t, err, ErrInvitationNotFound)
	require.Len(t, events.calls, 1)
	expiredModel := *toMemberInvitation(&invitation)
	expiredModel.Status = model.MemberInvitationStatusExpired
	expiredModel.ExpiredAt = &stubTime
	requireMemberInvitationEvent(t, events.calls[0], must.Slug("acme"), must.Slug("invitee"), expiredModel, model.EventActionExpire)
}

func TestDeclineMemberInvitationMarksInvitationDeclined(t *testing.T) {
	t.Parallel()

	invitation := invitationFixture()
	invitationsRepo := &testutil.FakeMemberInvitationsRepo{
		FindByTokenHashFn: func(_ context.Context, _ postgres.DBTX, tokenHash string) (*memberinvitations.Invitation, error) {
			require.Equal(t, hashInvitationToken("raw-token"), tokenHash)
			return &invitation, nil
		},
		MarkDeclinedFn: func(_ context.Context, _ postgres.DBTX, params memberinvitations.MarkDeclinedParams) (*memberinvitations.Invitation, error) {
			require.Equal(t, invitation.OrganizationSlug, params.OrganizationSlug)
			require.Equal(t, invitation.Slug, params.InvitationSlug)
			declined := invitation
			declined.Status = model.MemberInvitationStatusDeclined
			declined.DeclinedAt = &params.DeclinedAt
			return &declined, nil
		},
	}
	events := &fakeMembersEventRecorder{}
	svc := newInvitationTestServiceWithEventRecorder(&testutil.FakeMembersRepo{}, invitationsRepo, &testutil.FakeEmailProducer{}, events)

	got, err := svc.DeclineMemberInvitation(
		context.Background(),
		must.Slug("invitee"),
		"raw-token",
		model.UserEventActor(must.Slug("invitee")),
	)

	require.NoError(t, err)
	require.Equal(t, model.MemberInvitationStatusDeclined, got.Status)
	require.NotNil(t, got.DeclinedAt)
	require.Len(t, events.calls, 1)
	requireMemberInvitationEvent(t, events.calls[0], must.Slug("acme"), must.Slug("invitee"), *got, model.EventActionDecline)
}

func TestCreateMemberInvitationRejectsInvalidEmailAndRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		newInvitation model.NewMemberInvitation
		wantErr       error
	}{
		{
			name:          "InvalidEmail",
			newInvitation: model.NewMemberInvitation{Email: "not-an-email", Role: "viewer"},
			wantErr:       xerror.ValidationFailed(),
		},
		{
			name:          "InvalidRole",
			newInvitation: model.NewMemberInvitation{Email: "jane@example.com", Role: "unknown"},
			wantErr:       ErrInvalidRole,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newInvitationTestService(&testutil.FakeMembersRepo{}, &testutil.FakeMemberInvitationsRepo{}, &testutil.FakeEmailProducer{})

			_, err := svc.CreateMemberInvitation(
				context.Background(),
				must.Slug("acme"),
				must.Slug("owner"),
				model.UserEventActor(must.Slug("owner")),
				tt.newInvitation,
			)

			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
