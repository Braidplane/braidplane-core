package memberssvc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

var stubTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func stubMember(userSlug slug.Slug, email, role string) *members.Member {
	return &members.Member{
		UserSlug:  userSlug,
		UserEmail: email,
		RoleSlug:  slug.Slug(role),
		CreatedAt: stubTime,
		UpdatedAt: stubTime,
	}
}

type fakeMembersEventRecorder struct {
	err   error
	calls []eventssvc.CreateEventParams
}

func (f *fakeMembersEventRecorder) CreateEvent(
	_ context.Context,
	params eventssvc.CreateEventParams,
) (*model.Event, error) {
	f.calls = append(f.calls, params)
	if f.err != nil {
		return nil, f.err
	}
	return &model.Event{
		Slug:       params.Slug,
		ActorType:  params.ActorType,
		ActorSlug:  params.ActorSlug,
		EntityType: params.EntityType,
		EntitySlug: params.EntitySlug,
		Action:     params.Action,
		Metadata:   params.Metadata,
	}, nil
}

func newTestService(repo *testutil.FakeMembersRepo) Service {
	if repo.FindFn == nil {
		repo.FindFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
			return nil, members.ErrNotFound
		}
	}

	// Fake roles service that accepts "admin" and "viewer" roles
	fakeRolesService := &fakes.RolesService{
		GetRoleFn: func(ctx context.Context, orgSlug, roleSlug slug.Slug) (*model.OrgRole, error) {
			if roleSlug == "admin" || roleSlug == "viewer" {
				return &model.OrgRole{Slug: roleSlug}, nil
			}
			return nil, rolessvc.ErrRoleNotFound
		},
	}
	fakeUsersService := &fakes.UsersService{
		GetUserBySlugFn: func(ctx context.Context, userSlug slug.Slug) (*model.User, error) {
			return &model.User{Slug: userSlug}, nil
		},
	}

	return newTestServiceWithDeps(
		repo,
		&testutil.FakeMemberInvitationsRepo{},
		&testutil.FakeOrgsRepo{},
		fakeRolesService,
		fakeUsersService,
		&testutil.FakeEmailProducer{},
		eventtest.NewProducer(&fakeMembersEventRecorder{}),
	)
}

func newTestServiceWithDeps(
	repo *testutil.FakeMembersRepo,
	invitationsRepo *testutil.FakeMemberInvitationsRepo,
	orgsRepo *testutil.FakeOrgsRepo,
	rolesService *fakes.RolesService,
	usersService *fakes.UsersService,
	emailProducer *testutil.FakeEmailProducer,
	eventProducer workerproducer.EventProducer,
) Service {
	if orgsRepo.FindBySlugForUpdateFn == nil {
		orgsRepo.FindBySlugForUpdateFn = func(_ context.Context, _ postgres.DBTX, orgSlug slug.Slug) (*organizations.Organization, error) {
			return &organizations.Organization{Slug: orgSlug}, nil
		}
	}
	return New(
		testutil.FakeTransactionManager{},
		repo,
		invitationsRepo,
		orgsRepo,
		rolesService,
		usersService,
		emailProducer,
		eventProducer,
		"https://app.example.com",
		testutil.FakeClock{NowFn: func() time.Time { return stubTime }},
	)
}

func TestListMembers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		setup           func(repo *testutil.FakeMembersRepo)
		want            []model.Member
		wantErrContains string
	}{
		{
			name:    "ReturnsMembersForAnOrganization",
			orgSlug: must.Slug("my-org"),
			setup: func(repo *testutil.FakeMembersRepo) {
				repo.ListByOrganizationFn = func(_ context.Context, _ postgres.DBTX, orgSlugParam slug.Slug, _ model.ListRequest) ([]*members.Member, error) {
					assert.Equal(t, must.Slug("my-org"), orgSlugParam)
					return []*members.Member{
						stubMember("alice", "alice@example.com", "admin"),
						stubMember("bob", "bob@example.com", "viewer"),
					}, nil
				}
			},
			want: []model.Member{
				{
					UserSlug:   "alice",
					Email:      "alice@example.com",
					Role:       "admin",
					Timestamps: model.Timestamps{CreatedAt: stubTime, UpdatedAt: stubTime},
				},
				{
					UserSlug:   "bob",
					Email:      "bob@example.com",
					Role:       "viewer",
					Timestamps: model.Timestamps{CreatedAt: stubTime, UpdatedAt: stubTime},
				},
			},
		},
		{
			name:    "ReturnsEmptySliceWhenNoMembersExist",
			orgSlug: must.Slug("empty-org"),
			setup: func(repo *testutil.FakeMembersRepo) {
				repo.ListByOrganizationFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ model.ListRequest) ([]*members.Member, error) {
					return []*members.Member{}, nil
				}
			},
			want: []model.Member{},
		},
		{
			name:    "PropagatesRepositoryError",
			orgSlug: must.Slug("my-org"),
			setup: func(repo *testutil.FakeMembersRepo) {
				repo.ListByOrganizationFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ model.ListRequest) ([]*members.Member, error) {
					return nil, fmt.Errorf("db down")
				}
			},
			wantErrContains: "db down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeMembersRepo{}
			tt.setup(repo)
			svc := newTestService(repo)

			got, err := svc.ListMembers(context.Background(), tt.orgSlug, model.ListRequest{})
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Items)
		})
	}
}

func TestListMembers_BackwardPagination(t *testing.T) {
	t.Parallel()

	repo := &testutil.FakeMembersRepo{}
	repo.ListByOrganizationFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_ slug.Slug,
		req model.ListRequest,
	) ([]*members.Member, error) {
		assert.Equal(t, model.ListOrderAsc, req.Order)
		assert.Equal(t, 3, req.Limit)

		return []*members.Member{
			stubMember("alice", "alice@example.com", "admin"),
			stubMember("bob", "bob@example.com", "viewer"),
			stubMember("carol", "carol@example.com", "viewer"),
		}, nil
	}

	svc := newTestService(repo)
	result, err := svc.ListMembers(context.Background(), must.Slug("my-org"), model.ListRequest{
		Limit:     2,
		Cursor:    "2026-01-01T00:00:00Z_user-z",
		SortBy:    model.ListSortCreatedAt,
		Order:     model.ListOrderDesc,
		Direction: model.ListDirectionPrev,
	})

	require.NoError(t, err)
	require.Len(t, result.Items, 2)
	assert.Equal(t, must.Slug("bob"), result.Items[0].UserSlug)
	assert.Equal(t, must.Slug("alice"), result.Items[1].UserSlug)
	assert.True(t, result.HasMore)
	assert.NotNil(t, result.NextCursor)
	assert.NotNil(t, result.PrevCursor)
}

func TestCreateMember(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		newMember       model.NewMember
		setup           func(repo *testutil.FakeMembersRepo, roles *fakes.RolesService, users *fakes.UsersService)
		wantRole        slug.Slug
		wantErr         error
		wantErrContains string
	}{
		{
			name:    "CreatesAMemberSuccessfully",
			orgSlug: must.Slug("my-org"),
			newMember: model.NewMember{
				UserSlug: "alice",
				Role:     "viewer",
			},
			setup: func(repo *testutil.FakeMembersRepo, _ *fakes.RolesService, _ *fakes.UsersService) {
				repo.CreateFn = func(_ context.Context, _ postgres.DBTX, p *members.CreateParams) (*members.Member, error) {
					assert.Equal(t, must.Slug("my-org"), p.OrganizationSlug)
					assert.Equal(t, must.Slug("alice"), p.UserSlug)
					assert.Equal(t, slug.Slug("viewer"), p.RoleSlug)
					return stubMember("alice", "alice@example.com", "viewer"), nil
				}
			},
			wantRole: slug.Slug("viewer"),
		},
		{
			name:    "ReturnsErrorWhenMemberAlreadyExists",
			orgSlug: must.Slug("my-org"),
			newMember: model.NewMember{
				UserSlug: "alice",
				Role:     "viewer",
			},
			setup: func(repo *testutil.FakeMembersRepo, _ *fakes.RolesService, _ *fakes.UsersService) {
				repo.CreateFn = func(_ context.Context, _ postgres.DBTX, _ *members.CreateParams) (*members.Member, error) {
					return nil, members.ErrAlreadyMember
				}
			},
			wantErr: ErrMemberAlreadyExists,
		},
		{
			name:    "ReturnsErrorWhenUserDoesNotExist",
			orgSlug: must.Slug("my-org"),
			newMember: model.NewMember{
				UserSlug: "ghost-user",
				Role:     "viewer",
			},
			setup: func(repo *testutil.FakeMembersRepo, _ *fakes.RolesService, users *fakes.UsersService) {
				repo.FindFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
					return nil, members.ErrNotFound
				}
				users.GetUserBySlugFn = func(ctx context.Context, userSlug slug.Slug) (*model.User, error) {
					return nil, userssvc.ErrUserNotFound
				}
			},
			wantErr: ErrInvalidUserSlug,
		},
		{
			name:    "ReturnsErrorForInvalidRole",
			orgSlug: must.Slug("my-org"),
			newMember: model.NewMember{
				UserSlug: "alice",
				Role:     "superadmin",
			},
			setup:   func(_ *testutil.FakeMembersRepo, _ *fakes.RolesService, _ *fakes.UsersService) {},
			wantErr: ErrInvalidRole,
		},
		{
			name:    "ReturnsValidationErrorForMissingUserSlug",
			orgSlug: must.Slug("my-org"),
			newMember: model.NewMember{
				Role: "viewer",
			},
			setup:           func(_ *testutil.FakeMembersRepo, _ *fakes.RolesService, _ *fakes.UsersService) {},
			wantErrContains: "validation failed",
		},
		{
			name:    "ReturnsErrorWhenMembershipLookupFails",
			orgSlug: must.Slug("my-org"),
			newMember: model.NewMember{
				UserSlug: "alice",
				Role:     "viewer",
			},
			setup: func(repo *testutil.FakeMembersRepo, _ *fakes.RolesService, _ *fakes.UsersService) {
				repo.FindFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
					return nil, fmt.Errorf("db read failed")
				}
			},
			wantErrContains: "find member: db read failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeMembersRepo{}
			fakeRolesService := &fakes.RolesService{
				GetRoleFn: func(ctx context.Context, orgSlug, roleSlug slug.Slug) (*model.OrgRole, error) {
					if roleSlug == "admin" || roleSlug == "viewer" {
						return &model.OrgRole{Slug: roleSlug}, nil
					}
					return nil, rolessvc.ErrRoleNotFound
				},
			}
			fakeUsersService := &fakes.UsersService{
				GetUserBySlugFn: func(ctx context.Context, userSlug slug.Slug) (*model.User, error) {
					return &model.User{Slug: userSlug}, nil
				},
			}

			tt.setup(repo, fakeRolesService, fakeUsersService)
			if repo.FindFn == nil {
				repo.FindFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
					return nil, members.ErrNotFound
				}
			}
			svc := newTestServiceWithDeps(
				repo,
				&testutil.FakeMemberInvitationsRepo{},
				&testutil.FakeOrgsRepo{},
				fakeRolesService,
				fakeUsersService,
				&testutil.FakeEmailProducer{},
				eventtest.NewProducer(&fakeMembersEventRecorder{}),
			)

			got, err := svc.CreateMember(
				context.Background(),
				tt.orgSlug,
				model.UserEventActor(must.Slug("admin-user")),
				tt.newMember,
			)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.newMember.UserSlug, got.UserSlug)
			assert.Equal(t, tt.wantRole, got.Role)
		})
	}
}

func TestCreateMemberEmitsCreateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	userSlug := must.Slug("alice")
	actorSlug := must.Slug("admin-user")
	events := &fakeMembersEventRecorder{}
	repo := &testutil.FakeMembersRepo{
		FindFn: func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
			return nil, members.ErrNotFound
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, p *members.CreateParams) (*members.Member, error) {
			require.Equal(t, orgSlug, p.OrganizationSlug)
			require.Equal(t, userSlug, p.UserSlug)
			require.Equal(t, must.Slug("viewer"), p.RoleSlug)
			return stubMember(userSlug, "alice@example.com", "viewer"), nil
		},
	}
	svc := newTestServiceWithDeps(
		repo,
		&testutil.FakeMemberInvitationsRepo{},
		&testutil.FakeOrgsRepo{},
		&fakes.RolesService{
			GetRoleFn: func(context.Context, slug.Slug, slug.Slug) (*model.OrgRole, error) {
				return &model.OrgRole{Slug: "viewer"}, nil
			},
		},
		&fakes.UsersService{
			GetUserBySlugFn: func(context.Context, slug.Slug) (*model.User, error) {
				return &model.User{Slug: userSlug}, nil
			},
		},
		&testutil.FakeEmailProducer{},
		eventtest.NewProducer(events),
	)

	_, err := svc.CreateMember(
		context.Background(),
		orgSlug,
		model.UserEventActor(actorSlug),
		model.NewMember{UserSlug: userSlug, Role: "viewer"},
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeMember, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, userSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionCreate, got.Action)
	require.Equal(t, "viewer", got.Metadata["role_slug"])
}

func TestCreateBootstrapMemberDoesNotEmitCreateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	userSlug := must.Slug("alice")
	events := &fakeMembersEventRecorder{}
	repo := &testutil.FakeMembersRepo{
		FindFn: func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
			return nil, members.ErrNotFound
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, p *members.CreateParams) (*members.Member, error) {
			require.Equal(t, orgSlug, p.OrganizationSlug)
			require.Equal(t, userSlug, p.UserSlug)
			require.Equal(t, must.Slug("admin"), p.RoleSlug)
			return stubMember(userSlug, "alice@example.com", "admin"), nil
		},
	}
	svc := newTestServiceWithDeps(
		repo,
		&testutil.FakeMemberInvitationsRepo{},
		&testutil.FakeOrgsRepo{},
		&fakes.RolesService{
			GetRoleFn: func(context.Context, slug.Slug, slug.Slug) (*model.OrgRole, error) {
				return &model.OrgRole{Slug: "admin"}, nil
			},
		},
		&fakes.UsersService{
			GetUserBySlugFn: func(context.Context, slug.Slug) (*model.User, error) {
				return &model.User{Slug: userSlug}, nil
			},
		},
		&testutil.FakeEmailProducer{},
		eventtest.NewProducer(events),
	)

	got, err := svc.CreateBootstrapMember(
		context.Background(),
		orgSlug,
		model.NewMember{UserSlug: userSlug, Role: "admin"},
	)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, events.calls)
}

func TestCreateMemberFailsClosedWhenAuditEventCannotBeRecorded(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	userSlug := must.Slug("alice")
	events := &fakeMembersEventRecorder{err: xerror.Unexpected().Detail("event store down")}
	repo := &testutil.FakeMembersRepo{
		FindFn: func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
			return nil, members.ErrNotFound
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, _ *members.CreateParams) (*members.Member, error) {
			return stubMember(userSlug, "alice@example.com", "viewer"), nil
		},
	}
	svc := newTestServiceWithDeps(
		repo,
		&testutil.FakeMemberInvitationsRepo{},
		&testutil.FakeOrgsRepo{},
		&fakes.RolesService{
			GetRoleFn: func(context.Context, slug.Slug, slug.Slug) (*model.OrgRole, error) {
				return &model.OrgRole{Slug: "viewer"}, nil
			},
		},
		&fakes.UsersService{
			GetUserBySlugFn: func(context.Context, slug.Slug) (*model.User, error) {
				return &model.User{Slug: userSlug}, nil
			},
		},
		&testutil.FakeEmailProducer{},
		eventtest.NewProducer(events),
	)

	got, err := svc.CreateMember(
		context.Background(),
		orgSlug,
		model.UserEventActor(must.Slug("admin-user")),
		model.NewMember{UserSlug: userSlug, Role: "viewer"},
	)
	require.ErrorContains(t, err, "enqueue required membership audit event")
	require.ErrorContains(t, err, "event store down")
	require.Nil(t, got)
	require.Len(t, events.calls, 1)
}

func TestUpdateMemberEmitsUpdateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	userSlug := must.Slug("alice")
	actorSlug := must.Slug("admin-user")
	events := &fakeMembersEventRecorder{}
	repo := &testutil.FakeMembersRepo{
		UpdateFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotUserSlug, roleSlug slug.Slug) error {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, userSlug, gotUserSlug)
			require.Equal(t, must.Slug("admin"), roleSlug)
			return nil
		},
	}
	svc := newTestServiceWithDeps(
		repo,
		&testutil.FakeMemberInvitationsRepo{},
		&testutil.FakeOrgsRepo{},
		&fakes.RolesService{
			GetRoleFn: func(context.Context, slug.Slug, slug.Slug) (*model.OrgRole, error) {
				return &model.OrgRole{Slug: "admin"}, nil
			},
		},
		&fakes.UsersService{},
		&testutil.FakeEmailProducer{},
		eventtest.NewProducer(events),
	)

	_, err := svc.UpdateMember(
		context.Background(),
		orgSlug,
		userSlug,
		model.UserEventActor(actorSlug),
		model.UpdateMember{Role: "admin"},
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeMember, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, userSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionUpdate, got.Action)
	require.Equal(t, "admin", got.Metadata["role_slug"])
}

func TestRemoveMemberEmitsDeleteEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	userSlug := must.Slug("alice")
	actorSlug := must.Slug("admin-user")
	events := &fakeMembersEventRecorder{}
	repo := &testutil.FakeMembersRepo{
		FindFn: func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
			return stubMember(userSlug, "alice@example.com", "viewer"), nil
		},
		CountByOrganizationFn: func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (int64, error) {
			return 2, nil
		},
		RemoveFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotUserSlug slug.Slug) error {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, userSlug, gotUserSlug)
			return nil
		},
	}
	svc := newTestServiceWithDeps(
		repo,
		&testutil.FakeMemberInvitationsRepo{},
		&testutil.FakeOrgsRepo{},
		&fakes.RolesService{},
		&fakes.UsersService{},
		&testutil.FakeEmailProducer{},
		eventtest.NewProducer(events),
	)

	err := svc.RemoveMember(context.Background(), orgSlug, userSlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeMember, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, userSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionDelete, got.Action)
	require.Equal(t, "viewer", got.Metadata["role_slug"])
}

func TestUpdateMember(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		userSlug        slug.Slug
		update          model.UpdateMember
		setup           func(repo *testutil.FakeMembersRepo)
		wantErr         error
		wantErrContains string
	}{
		{
			name:     "UpdatesMemberRoleSuccessfully",
			orgSlug:  must.Slug("my-org"),
			userSlug: must.Slug("alice"),
			update:   model.UpdateMember{Role: "admin"},
			setup: func(repo *testutil.FakeMembersRepo) {
				repo.UpdateFn = func(_ context.Context, _ postgres.DBTX, orgSlug, userSlug, roleSlug slug.Slug) error {
					assert.Equal(t, must.Slug("my-org"), orgSlug)
					assert.Equal(t, must.Slug("alice"), userSlug)
					assert.Equal(t, slug.Slug("admin"), roleSlug)
					return nil
				}
			},
		},
		{
			name:     "ReturnsErrorForInvalidRole",
			orgSlug:  must.Slug("my-org"),
			userSlug: must.Slug("alice"),
			update:   model.UpdateMember{Role: "superadmin"},
			setup:    func(_ *testutil.FakeMembersRepo) {},
			wantErr:  ErrInvalidRole,
		},
		{
			name:     "PropagatesRepositoryError",
			orgSlug:  must.Slug("my-org"),
			userSlug: must.Slug("alice"),
			update:   model.UpdateMember{Role: "admin"},
			setup: func(repo *testutil.FakeMembersRepo) {
				repo.UpdateFn = func(_ context.Context, _ postgres.DBTX, _, _, _ slug.Slug) error {
					return fmt.Errorf("db error")
				}
			},
			wantErrContains: "db error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeMembersRepo{}
			tt.setup(repo)
			svc := newTestService(repo)

			_, err := svc.UpdateMember(
				context.Background(),
				tt.orgSlug,
				tt.userSlug,
				model.UserEventActor(must.Slug("admin-user")),
				tt.update,
			)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestUpdateMemberPreservesLastAdmin(t *testing.T) {
	t.Parallel()
	for _, count := range []int64{1, 2} {
		t.Run(fmt.Sprintf("Admins%d", count), func(t *testing.T) {
			t.Parallel()
			updated := false
			repo := &testutil.FakeMembersRepo{
				FindFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*members.Member, error) {
					return stubMember("alice", "alice@example.com", "admin"), nil
				},
				CountByRoleFn: func(_ context.Context, _ postgres.DBTX, org, role slug.Slug) (int64, error) {
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, adminRoleSlug, role)
					return count, nil
				},
				UpdateFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) error {
					updated = true
					return nil
				},
			}
			_, err := newTestService(repo).UpdateMember(t.Context(), "acme", "alice", model.UserEventActor("alice"), model.UpdateMember{Role: "viewer"})
			if count == 1 {
				require.ErrorIs(t, err, ErrCannotRemoveLastAdmin)
				require.False(t, updated)
			} else {
				require.NoError(t, err)
				require.True(t, updated)
			}
		})
	}
}

func TestRemoveMember(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		userSlug        slug.Slug
		setup           func(repo *testutil.FakeMembersRepo)
		wantErrContains string
	}{
		{
			name:     "RemovesMemberSuccessfully",
			orgSlug:  must.Slug("my-org"),
			userSlug: must.Slug("alice"),
			setup: func(repo *testutil.FakeMembersRepo) {
				repo.FindFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
					return stubMember("alice", "alice@example.com", "viewer"), nil
				}
				repo.CountByOrganizationFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (int64, error) {
					return 2, nil
				}
				repo.RemoveFn = func(_ context.Context, _ postgres.DBTX, orgSlug, userSlug slug.Slug) error {
					assert.Equal(t, must.Slug("my-org"), orgSlug)
					assert.Equal(t, must.Slug("alice"), userSlug)
					return nil
				}
			},
		},
		{
			name:     "ReturnsErrorWhenRemovingLastMember",
			orgSlug:  must.Slug("my-org"),
			userSlug: must.Slug("alice"),
			setup: func(repo *testutil.FakeMembersRepo) {
				repo.FindFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
					return stubMember("alice", "alice@example.com", "admin"), nil
				}
				repo.CountByOrganizationFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (int64, error) {
					return 1, nil
				}
			},
			wantErrContains: "cannot remove owner",
		},
		{
			name:     "ReturnsErrorWhenRemovingLastAdmin",
			orgSlug:  must.Slug("my-org"),
			userSlug: must.Slug("alice"),
			setup: func(repo *testutil.FakeMembersRepo) {
				repo.FindFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
					return stubMember("alice", "alice@example.com", "admin"), nil
				}
				repo.CountByOrganizationFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (int64, error) {
					return 2, nil
				}
				repo.CountByRoleFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (int64, error) {
					return 1, nil
				}
			},
			wantErrContains: "cannot remove owner",
		},
		{
			name:     "PropagatesRepositoryError",
			orgSlug:  must.Slug("my-org"),
			userSlug: must.Slug("alice"),
			setup: func(repo *testutil.FakeMembersRepo) {
				repo.FindFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*members.Member, error) {
					return stubMember("alice", "alice@example.com", "viewer"), nil
				}
				repo.CountByOrganizationFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (int64, error) {
					return 2, nil
				}
				repo.RemoveFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) error {
					return fmt.Errorf("db error")
				}
			},
			wantErrContains: "db error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := &testutil.FakeMembersRepo{}
			tt.setup(repo)
			svc := newTestService(repo)

			err := svc.RemoveMember(
				context.Background(),
				tt.orgSlug,
				tt.userSlug,
				model.UserEventActor(must.Slug("admin-user")),
			)
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
		})
	}
}
