package orgsvc

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
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

var stubTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func stubOrg(name string, slug slug.Slug) *organizations.Organization {
	return &organizations.Organization{
		Name:      name,
		Slug:      slug,
		Settings:  []byte("{}"),
		CreatedAt: stubTime,
		UpdatedAt: stubTime,
	}
}

func newTestService(
	orgsRepo *testutil.FakeOrgsRepo,
	membersService *fakes.MembersService,
) Service {
	return New(
		testutil.FakeTransactionManager{},
		orgsRepo,
		&fakeDefaultEnvironmentsService{},
		membersService,
		&fakes.RolesService{},
		eventtest.NewProducer(&fakeOrgEventRecorder{}),
		&fakes.AdmissionService{},
	)
}

type fakeDefaultEnvironmentsService struct {
	createDefaultEnvironmentsFn func(context.Context, slug.Slug) error
}

func (f *fakeDefaultEnvironmentsService) CreateDefaultEnvironments(ctx context.Context, orgSlug slug.Slug) error {
	if f.createDefaultEnvironmentsFn != nil {
		return f.createDefaultEnvironmentsFn(ctx, orgSlug)
	}
	return nil
}

func TestCreateOrganization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		userSlug          slug.Slug
		newOrg            model.NewOrganization
		setup             func(orgs *testutil.FakeOrgsRepo, mbrs *fakes.MembersService)
		setupEnvironments func(t *testing.T, environmentsSvc *fakeDefaultEnvironmentsService)
		setupRoles        func(t *testing.T, rolesSvc *fakes.RolesService)
		wantSlug          slug.Slug
		wantErr           error
		wantErrContains   string
	}{
		{
			name:     "CreatesOrganizationAndAddsCallerAsAdmin",
			userSlug: must.Slug("alice"),
			newOrg:   model.NewOrganization{Name: "My Org", Slug: "my-org"},
			setup: func(orgs *testutil.FakeOrgsRepo, mbrs *fakes.MembersService) {
				orgs.CreateFn = func(_ context.Context, _ postgres.DBTX, p *organizations.CreateParams) (*organizations.Organization, error) {
					assert.Equal(t, "My Org", p.Name)
					assert.Equal(t, must.Slug("my-org"), p.Slug)
					return stubOrg("My Org", must.Slug("my-org")), nil
				}
				mbrs.CreateBootstrapMemberFn = func(_ context.Context, orgSlug slug.Slug, newMember model.NewMember) (*model.Member, error) {
					assert.Equal(t, must.Slug("my-org"), orgSlug)
					assert.Equal(t, must.Slug("alice"), newMember.UserSlug)
					assert.Equal(t, slug.Slug("admin"), newMember.Role)
					return &model.Member{
						UserSlug: "alice",
						Role:     "admin",
						Timestamps: model.Timestamps{
							CreatedAt: stubTime,
							UpdatedAt: stubTime,
						},
					}, nil
				}
			},
			wantSlug: must.Slug("my-org"),
		},
		{
			name:     "CreatesDefaultEnvironments",
			userSlug: must.Slug("alice"),
			newOrg:   model.NewOrganization{Name: "My Org", Slug: "my-org"},
			setup: func(orgs *testutil.FakeOrgsRepo, mbrs *fakes.MembersService) {
				orgs.CreateFn = func(_ context.Context, _ postgres.DBTX, _ *organizations.CreateParams) (*organizations.Organization, error) {
					return stubOrg("My Org", must.Slug("my-org")), nil
				}
				mbrs.CreateBootstrapMemberFn = func(_ context.Context, _ slug.Slug, _ model.NewMember) (*model.Member, error) {
					return &model.Member{}, nil
				}
			},
			setupEnvironments: func(t *testing.T, environmentsSvc *fakeDefaultEnvironmentsService) {
				defaultEnvironmentsCreated := false

				environmentsSvc.createDefaultEnvironmentsFn = func(_ context.Context, orgSlug slug.Slug) error {
					assert.Equal(t, must.Slug("my-org"), orgSlug)
					defaultEnvironmentsCreated = true
					return nil
				}

				t.Cleanup(func() {
					assert.True(t, defaultEnvironmentsCreated, "CreateDefaultEnvironments should be called")
				})
			},
			wantSlug: must.Slug("my-org"),
		},
		{
			name:     "PropagatesDefaultEnvironmentCreationError",
			userSlug: must.Slug("alice"),
			newOrg:   model.NewOrganization{Name: "My Org", Slug: "my-org"},
			setup: func(orgs *testutil.FakeOrgsRepo, _ *fakes.MembersService) {
				orgs.CreateFn = func(_ context.Context, _ postgres.DBTX, _ *organizations.CreateParams) (*organizations.Organization, error) {
					return stubOrg("My Org", must.Slug("my-org")), nil
				}
			},
			setupEnvironments: func(_ *testing.T, environmentsSvc *fakeDefaultEnvironmentsService) {
				environmentsSvc.createDefaultEnvironmentsFn = func(context.Context, slug.Slug) error {
					return fmt.Errorf("default environment seed failed")
				}
			},
			wantErrContains: "seed default environments",
		},
		{
			name:     "CreatesDefaultRolesWithPermissions",
			userSlug: must.Slug("alice"),
			newOrg:   model.NewOrganization{Name: "My Org", Slug: "my-org"},
			setup: func(orgs *testutil.FakeOrgsRepo, mbrs *fakes.MembersService) {
				orgs.CreateFn = func(_ context.Context, _ postgres.DBTX, p *organizations.CreateParams) (*organizations.Organization, error) {
					return stubOrg("My Org", must.Slug("my-org")), nil
				}
				mbrs.CreateBootstrapMemberFn = func(_ context.Context, _ slug.Slug, _ model.NewMember) (*model.Member, error) {
					return &model.Member{}, nil
				}
			},
			setupRoles: func(t *testing.T, rolesSvc *fakes.RolesService) {
				defaultRolesCreated := false

				rolesSvc.CreateDefaultRolesFn = func(_ context.Context, orgSlug slug.Slug) error {
					assert.Equal(t, must.Slug("my-org"), orgSlug)
					defaultRolesCreated = true
					return nil
				}

				// Verify CreateDefaultRoles was called
				t.Cleanup(func() {
					assert.True(t, defaultRolesCreated, "CreateDefaultRoles should be called")
				})
			},
			wantSlug: must.Slug("my-org"),
		},
		{
			name:            "ReturnsValidationErrorForEmptyName",
			userSlug:        must.Slug("alice"),
			newOrg:          model.NewOrganization{Slug: "my-org"},
			setup:           func(_ *testutil.FakeOrgsRepo, _ *fakes.MembersService) {},
			wantErrContains: "validation failed",
		},
		{
			name:     "AutoGeneratesSlugFromName",
			userSlug: must.Slug("alice"),
			newOrg:   model.NewOrganization{Name: "My Org"},
			setup: func(orgs *testutil.FakeOrgsRepo, mbrs *fakes.MembersService) {
				orgs.CreateFn = func(_ context.Context, _ postgres.DBTX, p *organizations.CreateParams) (*organizations.Organization, error) {
					return stubOrg(p.Name, p.Slug), nil
				}
				mbrs.CreateBootstrapMemberFn = func(_ context.Context, _ slug.Slug, _ model.NewMember) (*model.Member, error) {
					return &model.Member{}, nil
				}
			},
			wantSlug: "my-org",
		},
		{
			name:     "FailsClosedWhenSlugLookupReturnsUnexpectedError",
			userSlug: must.Slug("alice"),
			newOrg:   model.NewOrganization{Name: "My Org", Slug: "my-org"},
			setup: func(orgs *testutil.FakeOrgsRepo, _ *fakes.MembersService) {
				orgs.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (*organizations.Organization, error) {
					return nil, xerror.Unexpected().Detail("organization slug lookup failed")
				}
				orgs.CreateFn = func(_ context.Context, _ postgres.DBTX, _ *organizations.CreateParams) (*organizations.Organization, error) {
					return nil, xerror.Unexpected().Detail("unexpected create organization call")
				}
			},
			wantErrContains: "organization slug lookup failed",
		},
		{
			name:     "PropagatesMemberCreationError",
			userSlug: must.Slug("alice"),
			newOrg:   model.NewOrganization{Name: "My Org", Slug: "my-org"},
			setup: func(orgs *testutil.FakeOrgsRepo, mbrs *fakes.MembersService) {
				orgs.CreateFn = func(_ context.Context, _ postgres.DBTX, _ *organizations.CreateParams) (*organizations.Organization, error) {
					return stubOrg("My Org", must.Slug("my-org")), nil
				}
				mbrs.CreateBootstrapMemberFn = func(_ context.Context, _ slug.Slug, _ model.NewMember) (*model.Member, error) {
					return nil, fmt.Errorf("db error")
				}
			},
			wantErrContains: "create admin member",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgsRepo := &testutil.FakeOrgsRepo{}
			membersService := &fakes.MembersService{}
			environmentsSvc := &fakeDefaultEnvironmentsService{}
			rolesSvc := &fakes.RolesService{}
			membersService.CreateMemberFn = func(context.Context, slug.Slug, model.EventActor, model.NewMember) (*model.Member, error) {
				return nil, fmt.Errorf("regular member create called for bootstrap seed")
			}
			orgsRepo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (*organizations.Organization, error) {
				return nil, organizations.ErrNotFound
			}

			tt.setup(orgsRepo, membersService)
			if tt.setupEnvironments != nil {
				tt.setupEnvironments(t, environmentsSvc)
			}
			if tt.setupRoles != nil {
				tt.setupRoles(t, rolesSvc)
			}

			svc := New(
				testutil.FakeTransactionManager{},
				orgsRepo,
				environmentsSvc,
				membersService,
				rolesSvc,
				eventtest.NewProducer(&fakeOrgEventRecorder{}),
				&fakes.AdmissionService{},
			)

			got, err := svc.CreateOrganization(
				context.Background(),
				tt.userSlug,
				model.UserEventActor(tt.userSlug),
				tt.newOrg,
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
			assert.Equal(t, tt.wantSlug, got.Slug)
		})
	}
}

type fakeOrgEventRecorder struct {
	calls []eventssvc.CreateEventParams
	err   error
}

func (f *fakeOrgEventRecorder) CreateEvent(
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
		Metadata:   map[string]any{},
	}, nil
}

func TestCreateOrganizationEmitsCreateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	userSlug := must.Slug("alice")
	events := &fakeOrgEventRecorder{}
	orgsRepo := &testutil.FakeOrgsRepo{
		FindBySlugFn: func(context.Context, postgres.DBTX, slug.Slug) (*organizations.Organization, error) {
			return nil, organizations.ErrNotFound
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, params *organizations.CreateParams) (*organizations.Organization, error) {
			return stubOrg(params.Name, params.Slug), nil
		},
	}
	membersService := &fakes.MembersService{
		CreateMemberFn: func(context.Context, slug.Slug, model.EventActor, model.NewMember) (*model.Member, error) {
			return nil, fmt.Errorf("regular member create called for bootstrap seed")
		},
		CreateBootstrapMemberFn: func(context.Context, slug.Slug, model.NewMember) (*model.Member, error) {
			return &model.Member{}, nil
		},
	}
	rolesSvc := &fakes.RolesService{
		CreateDefaultRolesFn: func(context.Context, slug.Slug) error {
			return nil
		},
	}

	svc := New(
		testutil.FakeTransactionManager{},
		orgsRepo,
		&fakeDefaultEnvironmentsService{},
		membersService,
		rolesSvc,
		eventtest.NewProducer(events),
		&fakes.AdmissionService{},
	)

	_, err := svc.CreateOrganization(
		context.Background(),
		userSlug,
		model.UserEventActor(userSlug),
		model.NewOrganization{
			Name: "My Org",
			Slug: orgSlug,
		},
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, userSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeOrganization, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, orgSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionCreate, got.Action)
	require.Empty(t, got.Metadata)
}

func TestUpdateOrganizationEmitsUpdateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	actorSlug := must.Slug("alice")
	events := &fakeOrgEventRecorder{}
	orgsRepo := &testutil.FakeOrgsRepo{
		UpdateFn: func(_ context.Context, _ postgres.DBTX, params *organizations.UpdateParams) error {
			require.Equal(t, orgSlug, params.Slug)
			require.Equal(t, "Renamed Org", params.Name)
			return nil
		},
		FindBySlugFn: func(_ context.Context, _ postgres.DBTX, got slug.Slug) (*organizations.Organization, error) {
			require.Equal(t, orgSlug, got)
			return stubOrg("Renamed Org", orgSlug), nil
		},
	}

	svc := New(
		testutil.FakeTransactionManager{},
		orgsRepo,
		&fakeDefaultEnvironmentsService{},
		&fakes.MembersService{},
		&fakes.RolesService{},
		eventtest.NewProducer(events),
		&fakes.AdmissionService{},
	)

	_, err := svc.UpdateOrganization(
		context.Background(),
		orgSlug,
		model.UserEventActor(actorSlug),
		model.UpdateOrganization{Name: "Renamed Org"},
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeOrganization, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, orgSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionUpdate, got.Action)
	require.Empty(t, got.Metadata)
}

func TestListOrganizations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		userSlug        slug.Slug
		setup           func(orgs *testutil.FakeOrgsRepo)
		want            []model.OrganizationWithRole
		wantErrContains string
	}{
		{
			name:     "ReturnsOrganizationsTheUserBelongsTo",
			userSlug: must.Slug("alice"),
			setup: func(orgs *testutil.FakeOrgsRepo) {
				orgs.ListByUserFn = func(_ context.Context, _ postgres.DBTX, userSlugParam slug.Slug, _ model.ListRequest) ([]*organizations.OrganizationWithRole, error) {
					assert.Equal(t, must.Slug("alice"), userSlugParam)
					return []*organizations.OrganizationWithRole{
						{
							Organization: *stubOrg("Org A", must.Slug("org-a")),
							MemberRole:   "admin",
						},
						{
							Organization: *stubOrg("Org B", must.Slug("org-b")),
							MemberRole:   "member",
						},
					}, nil
				}
			},
			want: []model.OrganizationWithRole{
				{
					Organization: model.Organization{
						Name: "Org A", Slug: "org-a", Settings: []byte("{}"),
						Timestamps: model.Timestamps{CreatedAt: stubTime, UpdatedAt: stubTime},
					},
					MemberRole: "admin",
				},
				{
					Organization: model.Organization{
						Name: "Org B", Slug: "org-b", Settings: []byte("{}"),
						Timestamps: model.Timestamps{CreatedAt: stubTime, UpdatedAt: stubTime},
					},
					MemberRole: "member",
				},
			},
		},
		{
			name:     "ReturnsEmptySliceWhenUserHasNoOrganizations",
			userSlug: must.Slug("bob"),
			setup: func(orgs *testutil.FakeOrgsRepo) {
				orgs.ListByUserFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ model.ListRequest) ([]*organizations.OrganizationWithRole, error) {
					return []*organizations.OrganizationWithRole{}, nil
				}
			},
			want: []model.OrganizationWithRole{},
		},
		{
			name:     "PropagatesRepositoryError",
			userSlug: must.Slug("alice"),
			setup: func(orgs *testutil.FakeOrgsRepo) {
				orgs.ListByUserFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ model.ListRequest) ([]*organizations.OrganizationWithRole, error) {
					return nil, fmt.Errorf("db down")
				}
			},
			wantErrContains: "db down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgsRepo := &testutil.FakeOrgsRepo{}
			tt.setup(orgsRepo)
			svc := newTestService(orgsRepo, &fakes.MembersService{})

			got, err := svc.ListOrganizations(
				context.Background(),
				tt.userSlug,
				model.ListRequest{},
			)
			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Items)
		})
	}
}

func TestListOrganizations_BackwardPagination(t *testing.T) {
	t.Parallel()

	orgsRepo := &testutil.FakeOrgsRepo{}
	orgsRepo.ListByUserFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_ slug.Slug,
		req model.ListRequest,
	) ([]*organizations.OrganizationWithRole, error) {
		assert.Equal(t, model.ListOrderAsc, req.Order)
		assert.Equal(t, 3, req.Limit)

		t1 := stubTime.Add(-2 * time.Hour)
		t2 := stubTime.Add(-1 * time.Hour)
		t3 := stubTime

		return []*organizations.OrganizationWithRole{
			{
				Organization: organizations.Organization{
					Name:      "Org A",
					Slug:      must.Slug("org-a"),
					CreatedAt: t1,
					UpdatedAt: t1,
				},
				MemberRole: "member",
			},
			{
				Organization: organizations.Organization{
					Name:      "Org B",
					Slug:      must.Slug("org-b"),
					CreatedAt: t2,
					UpdatedAt: t2,
				},
				MemberRole: "member",
			},
			{
				Organization: organizations.Organization{
					Name:      "Org C",
					Slug:      must.Slug("org-c"),
					CreatedAt: t3,
					UpdatedAt: t3,
				},
				MemberRole: "member",
			},
		}, nil
	}

	svc := newTestService(orgsRepo, &fakes.MembersService{})
	result, err := svc.ListOrganizations(
		context.Background(),
		must.Slug("alice"),
		model.ListRequest{
			Limit:     2,
			Cursor:    "2026-01-01T00:00:00Z_org-z",
			SortBy:    model.ListSortCreatedAt,
			Order:     model.ListOrderDesc,
			Direction: model.ListDirectionPrev,
		},
	)

	require.NoError(t, err)
	require.Len(t, result.Items, 2)
	assert.Equal(t, must.Slug("org-b"), result.Items[0].Slug)
	assert.Equal(t, must.Slug("org-a"), result.Items[1].Slug)
	assert.True(t, result.HasMore)
	assert.NotNil(t, result.NextCursor)
	assert.NotNil(t, result.PrevCursor)
}

func TestGetOrganization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		slug    slug.Slug
		setup   func(orgs *testutil.FakeOrgsRepo)
		want    *model.Organization
		wantErr error
	}{
		{
			name: "ReturnsOrganizationBySlug",
			slug: must.Slug("my-org"),
			setup: func(orgs *testutil.FakeOrgsRepo) {
				orgs.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, orgSlug slug.Slug) (*organizations.Organization, error) {
					assert.Equal(t, must.Slug("my-org"), orgSlug)
					return stubOrg("My Org", must.Slug("my-org")), nil
				}
			},
			want: &model.Organization{
				Name: "My Org", Slug: "my-org", Settings: []byte("{}"),
				Timestamps: model.Timestamps{CreatedAt: stubTime, UpdatedAt: stubTime},
			},
		},
		{
			name: "ReturnsNotFoundError",
			slug: must.Slug("nonexistent"),
			setup: func(orgs *testutil.FakeOrgsRepo) {
				orgs.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug) (*organizations.Organization, error) {
					return nil, organizations.ErrNotFound
				}
			},
			wantErr: ErrOrganizationNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgsRepo := &testutil.FakeOrgsRepo{}
			tt.setup(orgsRepo)
			svc := newTestService(orgsRepo, &fakes.MembersService{})

			got, err := svc.GetOrganization(context.Background(), tt.slug)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestUpdateOrganization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		slug            slug.Slug
		update          model.UpdateOrganization
		setup           func(orgs *testutil.FakeOrgsRepo)
		wantErr         error
		wantErrContains string
	}{
		{
			name:   "UpdatesOrganizationName",
			slug:   must.Slug("my-org"),
			update: model.UpdateOrganization{Name: "New Name"},
			setup: func(orgs *testutil.FakeOrgsRepo) {
				orgs.UpdateFn = func(_ context.Context, _ postgres.DBTX, p *organizations.UpdateParams) error {
					assert.Equal(t, must.Slug("my-org"), p.Slug)
					assert.Equal(t, "New Name", p.Name)
					return nil
				}
				orgs.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, s slug.Slug) (*organizations.Organization, error) {
					return stubOrg("New Name", must.Slug("my-org")), nil
				}
			},
		},
		{
			name:   "ReturnsNotFoundError",
			slug:   must.Slug("nonexistent"),
			update: model.UpdateOrganization{Name: "New Name"},
			setup: func(orgs *testutil.FakeOrgsRepo) {
				orgs.UpdateFn = func(_ context.Context, _ postgres.DBTX, _ *organizations.UpdateParams) error {
					return organizations.ErrNotFound
				}
			},
			wantErr: ErrOrganizationNotFound,
		},
		{
			name:            "ReturnsValidationErrorForEmptyName",
			slug:            must.Slug("my-org"),
			update:          model.UpdateOrganization{},
			setup:           func(_ *testutil.FakeOrgsRepo) {},
			wantErrContains: "validation failed",
		},
		{
			name:   "PropagatesRepositoryError",
			slug:   must.Slug("my-org"),
			update: model.UpdateOrganization{Name: "New Name"},
			setup: func(orgs *testutil.FakeOrgsRepo) {
				orgs.UpdateFn = func(_ context.Context, _ postgres.DBTX, _ *organizations.UpdateParams) error {
					return fmt.Errorf("db error")
				}
			},
			wantErrContains: "db error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgsRepo := &testutil.FakeOrgsRepo{}
			tt.setup(orgsRepo)
			svc := newTestService(orgsRepo, &fakes.MembersService{})

			_, err := svc.UpdateOrganization(
				context.Background(),
				tt.slug,
				model.UserEventActor(must.Slug("alice")),
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

func TestListOrganizationsPreservesCursorTimestampPrecision(t *testing.T) {
	t.Parallel()
	for _, sortBy := range []string{model.ListSortCreatedAt, model.ListSortUpdatedAt} {
		t.Run(sortBy, func(t *testing.T) {
			t.Parallel()
			newer := stubOrg("Newer", "newer")
			newer.CreatedAt = stubTime.Add(900123 * time.Microsecond)
			newer.UpdatedAt = newer.CreatedAt
			older := stubOrg("Older", "older")
			older.CreatedAt = stubTime.Add(100456 * time.Microsecond)
			older.UpdatedAt = older.CreatedAt
			repo := &testutil.FakeOrgsRepo{}
			repo.ListByUserFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, req model.ListRequest) ([]*organizations.OrganizationWithRole, error) {
				if req.Cursor == "" {
					return []*organizations.OrganizationWithRole{{Organization: *newer}, {Organization: *older}}, nil
				}
				require.Equal(t, newer.CreatedAt.Format(time.RFC3339Nano)+"_newer", req.Cursor)
				return []*organizations.OrganizationWithRole{{Organization: *older}}, nil
			}
			svc := newTestService(repo, &fakes.MembersService{})
			page1, err := svc.ListOrganizations(t.Context(), "alice", model.ListRequest{Limit: 1, SortBy: sortBy, Order: model.ListOrderDesc})
			require.NoError(t, err)
			require.NotNil(t, page1.NextCursor)
			page2, err := svc.ListOrganizations(t.Context(), "alice", model.ListRequest{Limit: 1, SortBy: sortBy, Order: model.ListOrderDesc, Cursor: *page1.NextCursor})
			require.NoError(t, err)
			require.Len(t, page2.Items, 1)
			require.NotNil(t, page2.PrevCursor)
			require.Equal(t, older.CreatedAt.Format(time.RFC3339Nano)+"_older", *page2.PrevCursor)
		})
	}
}
