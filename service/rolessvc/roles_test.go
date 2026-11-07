package rolessvc

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
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	"github.com/Braidplane/braidplane-core/postgres/roles"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

var stubTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func stubRole(s slug.Slug, name string, isDefault bool) *roles.Role {
	return &roles.Role{
		Slug:      s,
		Name:      name,
		IsDefault: isDefault,
		CreatedAt: stubTime,
		UpdatedAt: stubTime,
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

type fakeRolesEventRecorder struct {
	err   error
	calls []eventssvc.CreateEventParams
}

func (f *fakeRolesEventRecorder) CreateEvent(
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

func newTestService(
	rolesRepo *testutil.FakeRolesRepo,
	permsRepo *testutil.FakePermissionsRepo,
) Service {
	return New(
		testutil.FakeTransactionManager{},
		rolesRepo,
		permsRepo,
		eventtest.NewProducer(&fakeRolesEventRecorder{}),
	)
}

func TestCreateRoleWithPermissions_FailsClosedWhenSlugLookupReturnsUnexpectedError(t *testing.T) {
	t.Parallel()

	rolesRepo := &testutil.FakeRolesRepo{
		FindBySlugFn: func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*roles.Role, error) {
			return nil, xerror.Unexpected().Detail("role slug lookup failed")
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, _ *roles.CreateParams) (*roles.Role, error) {
			return nil, xerror.Unexpected().Detail("unexpected create role call")
		},
	}
	permsRepo := &testutil.FakePermissionsRepo{}
	svc := newTestService(rolesRepo, permsRepo)

	_, err := svc.CreateRoleWithPermissions(
		context.Background(),
		must.Slug("my-org"),
		model.SystemEventActor(),
		model.NewRole{
			Name: "Editor",
			Slug: "editor",
		},
		nil,
	)

	require.ErrorContains(t, err, "role slug lookup failed")
}

func TestCreateDefaultRolesReconcilesExistingViewerNotificationPermissions(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	createCalls := 0
	added := []permissions.AddToRoleParams{}
	rolesRepo := &testutil.FakeRolesRepo{
		FindBySlugFn: func(_ context.Context, _ postgres.DBTX, _, roleSlug slug.Slug) (*roles.Role, error) {
			switch roleSlug {
			case must.Slug("admin"):
				return stubRole(roleSlug, "Admin", true), nil
			case must.Slug("viewer"):
				return stubRole(roleSlug, "Viewer", true), nil
			default:
				return nil, roles.ErrNotFound
			}
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, _ *roles.CreateParams) (*roles.Role, error) {
			createCalls++
			return nil, xerror.Unexpected().Detail("unexpected default role create")
		},
	}
	permsRepo := &testutil.FakePermissionsRepo{
		FindOrCreateFn: func(_ context.Context, _ postgres.DBTX, resources, actions []string) ([]*permissions.Permission, error) {
			require.Len(t, resources, 1)
			require.Len(t, actions, 1)
			return []*permissions.Permission{{Resource: resources[0], Action: actions[0]}}, nil
		},
		AddToRoleFn: func(_ context.Context, _ postgres.DBTX, params *permissions.AddToRoleParams) error {
			added = append(added, *params)
			return nil
		},
	}
	svc := newTestService(rolesRepo, permsRepo)

	err := svc.CreateDefaultRoles(context.Background(), orgSlug)

	require.NoError(t, err)
	require.Equal(t, 0, createCalls)
	require.Contains(t, added, permissions.AddToRoleParams{
		OrgSlug:  orgSlug,
		RoleSlug: must.Slug("viewer"),
		Resource: model.ResourceNotifications.String(),
		Action:   model.ActionRead.String(),
	})
	require.Contains(t, added, permissions.AddToRoleParams{
		OrgSlug:  orgSlug,
		RoleSlug: must.Slug("viewer"),
		Resource: model.ResourceAlertPolicies.String(),
		Action:   model.ActionRead.String(),
	})
	require.Contains(t, added, permissions.AddToRoleParams{
		OrgSlug:  orgSlug,
		RoleSlug: must.Slug("viewer"),
		Resource: model.ResourceAlertDestinations.String(),
		Action:   model.ActionRead.String(),
	})
}

func TestCreateDefaultRolesRejectsCustomDefaultRoleSlugs(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")

	tests := []struct {
		name       string
		customSlug slug.Slug
	}{
		{name: "Admin", customSlug: must.Slug("admin")},
		{name: "Viewer", customSlug: must.Slug("viewer")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			added := []permissions.AddToRoleParams{}
			rolesRepo := &testutil.FakeRolesRepo{
				FindBySlugFn: func(_ context.Context, _ postgres.DBTX, _, roleSlug slug.Slug) (*roles.Role, error) {
					if roleSlug == tc.customSlug {
						return stubRole(roleSlug, "Custom "+tc.name, false), nil
					}
					return stubRole(roleSlug, tc.name, true), nil
				},
				CreateFn: func(_ context.Context, _ postgres.DBTX, _ *roles.CreateParams) (*roles.Role, error) {
					return nil, xerror.Unexpected().Detail("unexpected default role create")
				},
			}
			permsRepo := &testutil.FakePermissionsRepo{
				FindOrCreateFn: func(_ context.Context, _ postgres.DBTX, resources, actions []string) ([]*permissions.Permission, error) {
					require.Len(t, resources, 1)
					require.Len(t, actions, 1)
					return []*permissions.Permission{{Resource: resources[0], Action: actions[0]}}, nil
				},
				AddToRoleFn: func(_ context.Context, _ postgres.DBTX, params *permissions.AddToRoleParams) error {
					added = append(added, *params)
					return nil
				},
			}
			svc := newTestService(rolesRepo, permsRepo)

			err := svc.CreateDefaultRoles(context.Background(), orgSlug)

			require.ErrorIs(t, err, ErrInvalidInput)
			for _, params := range added {
				require.NotEqual(t, tc.customSlug, params.RoleSlug)
			}
		})
	}
}

func TestCreateDefaultRolesUsesCanonicalDefaultRoleWhenCreateRaces(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	findAdminCalls := 0
	ensureCalls := 0
	added := []permissions.AddToRoleParams{}
	rolesRepo := &testutil.FakeRolesRepo{
		FindBySlugFn: func(_ context.Context, _ postgres.DBTX, _, roleSlug slug.Slug) (*roles.Role, error) {
			if roleSlug == must.Slug("admin") {
				findAdminCalls++
				return nil, roles.ErrNotFound
			}
			return stubRole(roleSlug, "Viewer", true), nil
		},
		EnsureDefaultFn: func(_ context.Context, _ postgres.DBTX, params *roles.CreateParams) (*roles.Role, error) {
			ensureCalls++
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, must.Slug("admin"), params.Slug)
			return stubRole(params.Slug, params.Name, true), nil
		},
	}
	permsRepo := &testutil.FakePermissionsRepo{
		FindOrCreateFn: func(_ context.Context, _ postgres.DBTX, resources, actions []string) ([]*permissions.Permission, error) {
			require.Len(t, resources, 1)
			require.Len(t, actions, 1)
			return []*permissions.Permission{{Resource: resources[0], Action: actions[0]}}, nil
		},
		AddToRoleFn: func(_ context.Context, _ postgres.DBTX, params *permissions.AddToRoleParams) error {
			added = append(added, *params)
			return nil
		},
	}
	svc := newTestService(rolesRepo, permsRepo)

	err := svc.CreateDefaultRoles(context.Background(), orgSlug)

	require.NoError(t, err)
	require.Equal(t, 1, findAdminCalls)
	require.Equal(t, 1, ensureCalls)
	require.Contains(t, added, permissions.AddToRoleParams{
		OrgSlug:  orgSlug,
		RoleSlug: must.Slug("admin"),
		Resource: "*",
		Action:   "*",
	})
}

func TestCreateRoleWithPermissionsEmitsCreateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	roleSlug := must.Slug("editor")
	actorSlug := must.Slug("alice")
	events := &fakeRolesEventRecorder{}
	rolesRepo := &testutil.FakeRolesRepo{
		FindBySlugFn: func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*roles.Role, error) {
			return nil, roles.ErrNotFound
		},
		CreateFn: func(_ context.Context, _ postgres.DBTX, params *roles.CreateParams) (*roles.Role, error) {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, roleSlug, params.Slug)
			return stubRole(params.Slug, params.Name, params.IsDefault), nil
		},
	}
	permsRepo := &testutil.FakePermissionsRepo{
		ClearRolePermissionsFn: func(_ context.Context, _ postgres.DBTX, params *permissions.ClearRolePermissionsParams) error {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, roleSlug, params.RoleSlug)
			return nil
		},
		FindOrCreateFn: func(_ context.Context, _ postgres.DBTX, resources, actions []string) ([]*permissions.Permission, error) {
			require.Len(t, resources, 1)
			require.Len(t, actions, 1)
			return []*permissions.Permission{{Resource: resources[0], Action: actions[0]}}, nil
		},
		AddToRoleFn: func(_ context.Context, _ postgres.DBTX, params *permissions.AddToRoleParams) error {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, roleSlug, params.RoleSlug)
			return nil
		},
	}
	svc := New(
		testutil.FakeTransactionManager{},
		rolesRepo,
		permsRepo,
		eventtest.NewProducer(events),
	)

	_, err := svc.CreateRoleWithPermissions(
		context.Background(),
		orgSlug,
		model.UserEventActor(actorSlug),
		model.NewRole{Name: "Editor", Slug: roleSlug},
		[]model.RolePermission{{Resource: "projects", Action: "read"}},
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.NotEmpty(t, got.Slug)
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeRole, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, roleSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionCreate, got.Action)
	require.Equal(t, 1, got.Metadata["permission_count"])
}

func TestUpdateRoleWithPermissionsEmitsUpdateEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	roleSlug := must.Slug("editor")
	actorSlug := must.Slug("alice")
	events := &fakeRolesEventRecorder{}
	rolesRepo := &testutil.FakeRolesRepo{
		FindBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotRoleSlug slug.Slug) (*roles.Role, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, roleSlug, gotRoleSlug)
			return stubRole(roleSlug, "Editor", false), nil
		},
		UpdateFn: func(_ context.Context, _ postgres.DBTX, params *roles.UpdateParams) error {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, roleSlug, params.Slug)
			require.Equal(t, "Editor renamed", params.Name)
			return nil
		},
	}
	permsRepo := &testutil.FakePermissionsRepo{
		ClearRolePermissionsFn: func(_ context.Context, _ postgres.DBTX, params *permissions.ClearRolePermissionsParams) error {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, roleSlug, params.RoleSlug)
			return nil
		},
		FindOrCreateFn: func(_ context.Context, _ postgres.DBTX, resources, actions []string) ([]*permissions.Permission, error) {
			require.Len(t, resources, 1)
			require.Len(t, actions, 1)
			return []*permissions.Permission{{Resource: resources[0], Action: actions[0]}}, nil
		},
		AddToRoleFn: func(_ context.Context, _ postgres.DBTX, params *permissions.AddToRoleParams) error {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, roleSlug, params.RoleSlug)
			return nil
		},
	}
	svc := New(
		testutil.FakeTransactionManager{},
		rolesRepo,
		permsRepo,
		eventtest.NewProducer(events),
	)

	err := svc.UpdateRoleWithPermissions(
		context.Background(),
		orgSlug,
		roleSlug,
		model.UserEventActor(actorSlug),
		model.UpdateRole{Name: "Editor renamed"},
		[]model.RolePermission{
			{Resource: "projects", Action: "read"},
			{Resource: "queries", Action: "write"},
		},
	)
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventClassSecurityAudit, got.EventClass)
	require.Equal(t, model.CurrentEventSchemaVersion, got.SchemaVersion)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeRole, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, roleSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionUpdate, got.Action)
	require.Equal(t, 2, got.Metadata["permission_count"])
}

func TestDeleteRoleEmitsDeleteEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	roleSlug := must.Slug("editor")
	actorSlug := must.Slug("alice")
	events := &fakeRolesEventRecorder{}
	rolesRepo := &testutil.FakeRolesRepo{
		FindBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotRoleSlug slug.Slug) (*roles.Role, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, roleSlug, gotRoleSlug)
			return stubRole(roleSlug, "Editor", false), nil
		},
		DeleteFn: func(_ context.Context, _ postgres.DBTX, params *roles.DeleteParams) error {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, roleSlug, params.Slug)
			return nil
		},
	}
	svc := New(
		testutil.FakeTransactionManager{},
		rolesRepo,
		&testutil.FakePermissionsRepo{},
		eventtest.NewProducer(events),
	)

	err := svc.DeleteRole(context.Background(), orgSlug, roleSlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeRole, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, roleSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionDelete, got.Action)
	require.Empty(t, got.Metadata)
}

func TestRestoreRoleEmitsRestoreEvent(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	roleSlug := must.Slug("editor")
	actorSlug := must.Slug("alice")
	events := &fakeRolesEventRecorder{}
	rolesRepo := &testutil.FakeRolesRepo{
		RestoreFn: func(_ context.Context, _ postgres.DBTX, params *roles.RestoreParams) error {
			require.Equal(t, orgSlug, params.OrgSlug)
			require.Equal(t, roleSlug, params.Slug)
			return nil
		},
	}
	svc := New(
		testutil.FakeTransactionManager{},
		rolesRepo,
		&testutil.FakePermissionsRepo{},
		eventtest.NewProducer(events),
	)

	err := svc.RestoreRole(context.Background(), orgSlug, roleSlug, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	require.Len(t, events.calls, 1)

	got := events.calls[0]
	require.Equal(t, orgSlug, got.OrgSlug)
	require.Equal(t, model.EventActorTypeUser, got.ActorType)
	require.NotNil(t, got.ActorSlug)
	require.Equal(t, actorSlug, *got.ActorSlug)
	require.Equal(t, model.EventEntityTypeRole, got.EntityType)
	require.NotNil(t, got.EntitySlug)
	require.Equal(t, roleSlug, *got.EntitySlug)
	require.Equal(t, model.EventActionRestore, got.Action)
	require.Empty(t, got.Metadata)
}

func TestUpdateRoleWithPermissionsFailsClosedWhenAuditEventCannotBeRecorded(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	roleSlug := must.Slug("editor")
	events := &fakeRolesEventRecorder{err: xerror.Unexpected().Detail("event store down")}
	rolesRepo := &testutil.FakeRolesRepo{
		FindBySlugFn: func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*roles.Role, error) {
			return stubRole(roleSlug, "Editor", false), nil
		},
		UpdateFn: func(_ context.Context, _ postgres.DBTX, _ *roles.UpdateParams) error {
			return nil
		},
	}
	permsRepo := &testutil.FakePermissionsRepo{
		ClearRolePermissionsFn: func(context.Context, postgres.DBTX, *permissions.ClearRolePermissionsParams) error {
			return nil
		},
	}
	svc := New(
		testutil.FakeTransactionManager{},
		rolesRepo,
		permsRepo,
		eventtest.NewProducer(events),
	)

	err := svc.UpdateRoleWithPermissions(
		context.Background(),
		orgSlug,
		roleSlug,
		model.UserEventActor(must.Slug("alice")),
		model.UpdateRole{Name: "Editor renamed"},
		nil,
	)
	require.ErrorContains(t, err, "enqueue required role audit event")
	require.ErrorContains(t, err, "event store down")
	require.Len(t, events.calls, 1)
}

func TestUpdateRoleWithPermissionsRejectsDefaultRole(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("my-org")
	roleSlug := must.Slug("admin")
	rolesRepo := &testutil.FakeRolesRepo{
		FindBySlugFn: func(_ context.Context, _ postgres.DBTX, gotOrgSlug, gotRoleSlug slug.Slug) (*roles.Role, error) {
			require.Equal(t, orgSlug, gotOrgSlug)
			require.Equal(t, roleSlug, gotRoleSlug)
			return stubRole(roleSlug, "Admin", true), nil
		},
		UpdateFn: func(_ context.Context, _ postgres.DBTX, _ *roles.UpdateParams) error {
			t.Fatalf("default role update should not be persisted")
			return nil
		},
	}
	permsRepo := &testutil.FakePermissionsRepo{
		ClearRolePermissionsFn: func(context.Context, postgres.DBTX, *permissions.ClearRolePermissionsParams) error {
			t.Fatalf("default role permissions should not be replaced")
			return nil
		},
	}
	svc := newTestService(rolesRepo, permsRepo)

	err := svc.UpdateRoleWithPermissions(
		context.Background(),
		orgSlug,
		roleSlug,
		model.UserEventActor(must.Slug("alice")),
		model.UpdateRole{Name: "Admin renamed"},
		[]model.RolePermission{{Resource: "roles", Action: "read"}},
	)

	require.ErrorIs(t, err, ErrCannotUpdateDefaultRole)
}

func TestDeleteRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		roleSlug        slug.Slug
		setup           func(repo *testutil.FakeRolesRepo)
		wantErr         error
		wantErrContains string
	}{
		{
			name:     "DeletesRoleSuccessfully",
			orgSlug:  must.Slug("my-org"),
			roleSlug: must.Slug("editor"),
			setup: func(repo *testutil.FakeRolesRepo) {
				repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*roles.Role, error) {
					return stubRole(must.Slug("editor"), "Editor", false), nil
				}
				repo.DeleteFn = func(_ context.Context, _ postgres.DBTX, p *roles.DeleteParams) error {
					assert.Equal(t, must.Slug("my-org"), p.OrgSlug)
					assert.Equal(t, must.Slug("editor"), p.Slug)
					return nil
				}
			},
		},
		{
			name:     "FailsWhenRoleNotFound",
			orgSlug:  must.Slug("my-org"),
			roleSlug: must.Slug("nonexistent"),
			setup: func(repo *testutil.FakeRolesRepo) {
				repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*roles.Role, error) {
					return nil, roles.ErrNotFound
				}
			},
			wantErr: ErrRoleNotFound,
		},
		{
			name:     "FailsWhenDeletingDefaultRole",
			orgSlug:  must.Slug("my-org"),
			roleSlug: must.Slug("admin"),
			setup: func(repo *testutil.FakeRolesRepo) {
				repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*roles.Role, error) {
					return stubRole(must.Slug("admin"), "Admin", true), nil
				}
				repo.DeleteFn = func(_ context.Context, _ postgres.DBTX, _ *roles.DeleteParams) error {
					t.Fatalf("delete should not be called for default role")
					return nil
				}
			},
			wantErr: ErrCannotDeleteDefaultRole,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rolesRepo := &testutil.FakeRolesRepo{}
			permsRepo := &testutil.FakePermissionsRepo{}
			if tt.setup != nil {
				tt.setup(rolesRepo)
			}
			svc := newTestService(rolesRepo, permsRepo)

			err := svc.DeleteRole(
				context.Background(),
				tt.orgSlug,
				tt.roleSlug,
				model.UserEventActor(must.Slug("alice")),
			)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRestoreRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(repo *testutil.FakeRolesRepo)
		wantErr error
	}{
		{
			name: "RestoresRoleSuccessfully",
			setup: func(repo *testutil.FakeRolesRepo) {
				repo.RestoreFn = func(_ context.Context, _ postgres.DBTX, p *roles.RestoreParams) error {
					assert.Equal(t, must.Slug("my-org"), p.OrgSlug)
					assert.Equal(t, must.Slug("editor"), p.Slug)
					return nil
				}
			},
		},
		{
			name: "FailsWhenRoleNotFound",
			setup: func(repo *testutil.FakeRolesRepo) {
				repo.RestoreFn = func(_ context.Context, _ postgres.DBTX, _ *roles.RestoreParams) error {
					return roles.ErrNotFound
				}
			},
			wantErr: ErrRoleNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rolesRepo := &testutil.FakeRolesRepo{}
			if tt.setup != nil {
				tt.setup(rolesRepo)
			}
			svc := newTestService(rolesRepo, &testutil.FakePermissionsRepo{})

			err := svc.RestoreRole(
				context.Background(),
				must.Slug("my-org"),
				must.Slug("editor"),
				model.UserEventActor(must.Slug("alice")),
			)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestGetRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		roleSlug        slug.Slug
		setup           func(repo *testutil.FakeRolesRepo)
		want            *model.OrgRole
		wantErr         error
		wantErrContains string
	}{
		{
			name:     "RetrievesRoleSuccessfully",
			orgSlug:  must.Slug("my-org"),
			roleSlug: must.Slug("editor"),
			setup: func(repo *testutil.FakeRolesRepo) {
				repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, orgSlug, roleSlug slug.Slug) (*roles.Role, error) {
					assert.Equal(t, "my-org", orgSlug.String())
					assert.Equal(t, "editor", roleSlug.String())
					return stubRole(must.Slug("editor"), "Editor", false), nil
				}
			},
			want: &model.OrgRole{
				Slug:       must.Slug("editor"),
				Name:       "Editor",
				IsDefault:  false,
				Timestamps: model.Timestamps{CreatedAt: stubTime, UpdatedAt: stubTime},
			},
		},
		{
			name:     "FailsWhenRoleNotFound",
			orgSlug:  must.Slug("my-org"),
			roleSlug: must.Slug("nonexistent"),
			setup: func(repo *testutil.FakeRolesRepo) {
				repo.FindBySlugFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) (*roles.Role, error) {
					return nil, roles.ErrNotFound
				}
			},
			wantErr: ErrRoleNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rolesRepo := &testutil.FakeRolesRepo{}
			permsRepo := &testutil.FakePermissionsRepo{}
			if tt.setup != nil {
				tt.setup(rolesRepo)
			}
			svc := newTestService(rolesRepo, permsRepo)

			got, err := svc.GetRole(context.Background(), tt.orgSlug, tt.roleSlug)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestListRoles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		setup           func(repo *testutil.FakeRolesRepo)
		want            []model.OrgRole
		includeDeleted  bool
		onlyDeleted     bool
		wantErrContains string
	}{
		{
			name:    "ListsRolesForOrganization",
			orgSlug: must.Slug("my-org"),
			setup: func(repo *testutil.FakeRolesRepo) {
				repo.ListByOrganizationFn = func(_ context.Context, _ postgres.DBTX, orgSlug slug.Slug, _ model.ListRequest, includeDeleted, onlyDeleted bool) ([]*roles.Role, error) {
					assert.Equal(t, "my-org", orgSlug.String())
					assert.False(t, includeDeleted)
					assert.False(t, onlyDeleted)
					return []*roles.Role{
						stubRole(must.Slug("admin"), "Admin", true),
						stubRole(must.Slug("editor"), "Editor", false),
					}, nil
				}
			},
			want: []model.OrgRole{
				{
					Slug:       must.Slug("admin"),
					Name:       "Admin",
					IsDefault:  true,
					Timestamps: model.Timestamps{CreatedAt: stubTime, UpdatedAt: stubTime},
				},
				{
					Slug:       must.Slug("editor"),
					Name:       "Editor",
					IsDefault:  false,
					Timestamps: model.Timestamps{CreatedAt: stubTime, UpdatedAt: stubTime},
				},
			},
		},
		{
			name:           "IncludesArchivedRolesWhenRequested",
			orgSlug:        must.Slug("my-org"),
			includeDeleted: true,
			setup: func(repo *testutil.FakeRolesRepo) {
				deletedAt := stubTime.Add(time.Hour)
				repo.ListByOrganizationFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ model.ListRequest, includeDeleted, onlyDeleted bool) ([]*roles.Role, error) {
					assert.True(t, includeDeleted)
					assert.False(t, onlyDeleted)
					role := stubRole(must.Slug("archived"), "Archived", false)
					role.DeletedAt = &deletedAt
					return []*roles.Role{role}, nil
				}
			},
			want: []model.OrgRole{
				{
					Slug:       must.Slug("archived"),
					Name:       "Archived",
					IsDefault:  false,
					DeletedAt:  ptrTime(stubTime.Add(time.Hour)),
					Timestamps: model.Timestamps{CreatedAt: stubTime, UpdatedAt: stubTime},
				},
			},
		},
		{
			name:           "OnlyListsArchivedRolesWhenRequested",
			orgSlug:        must.Slug("my-org"),
			includeDeleted: true,
			onlyDeleted:    true,
			setup: func(repo *testutil.FakeRolesRepo) {
				deletedAt := stubTime.Add(time.Hour)
				repo.ListByOrganizationFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ model.ListRequest, includeDeleted, onlyDeleted bool) ([]*roles.Role, error) {
					assert.True(t, includeDeleted)
					assert.True(t, onlyDeleted)
					role := stubRole(must.Slug("archived"), "Archived", false)
					role.DeletedAt = &deletedAt
					return []*roles.Role{role}, nil
				}
			},
			want: []model.OrgRole{
				{
					Slug:       must.Slug("archived"),
					Name:       "Archived",
					IsDefault:  false,
					DeletedAt:  ptrTime(stubTime.Add(time.Hour)),
					Timestamps: model.Timestamps{CreatedAt: stubTime, UpdatedAt: stubTime},
				},
			},
		},
		{
			name:    "ReturnsEmptySliceWhenNoRolesExist",
			orgSlug: must.Slug("empty-org"),
			setup: func(repo *testutil.FakeRolesRepo) {
				repo.ListByOrganizationFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ model.ListRequest, _, _ bool) ([]*roles.Role, error) {
					return []*roles.Role{}, nil
				}
			},
			want: []model.OrgRole{},
		},
		{
			name:    "PropagatesRepositoryError",
			orgSlug: must.Slug("my-org"),
			setup: func(repo *testutil.FakeRolesRepo) {
				repo.ListByOrganizationFn = func(_ context.Context, _ postgres.DBTX, _ slug.Slug, _ model.ListRequest, _, _ bool) ([]*roles.Role, error) {
					return nil, fmt.Errorf("db down")
				}
			},
			wantErrContains: "db down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rolesRepo := &testutil.FakeRolesRepo{}
			permsRepo := &testutil.FakePermissionsRepo{}
			if tt.setup != nil {
				tt.setup(rolesRepo)
			}
			svc := newTestService(rolesRepo, permsRepo)

			got, err := svc.ListRoles(context.Background(), tt.orgSlug, model.ListRequest{}, tt.includeDeleted, tt.onlyDeleted)

			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Items)
		})
	}
}

func TestListRoles_BackwardPagination(t *testing.T) {
	t.Parallel()

	rolesRepo := &testutil.FakeRolesRepo{}
	permsRepo := &testutil.FakePermissionsRepo{}
	rolesRepo.ListByOrganizationFn = func(
		_ context.Context,
		_ postgres.DBTX,
		_ slug.Slug,
		req model.ListRequest,
		_ bool,
		_ bool,
	) ([]*roles.Role, error) {
		assert.Equal(t, model.ListOrderAsc, req.Order)
		assert.Equal(t, 3, req.Limit)

		return []*roles.Role{
			stubRole(must.Slug("admin"), "Admin", true),
			stubRole(must.Slug("editor"), "Editor", false),
			stubRole(must.Slug("viewer"), "Viewer", false),
		}, nil
	}

	svc := newTestService(rolesRepo, permsRepo)
	result, err := svc.ListRoles(context.Background(), must.Slug("my-org"), model.ListRequest{
		Limit:     2,
		Cursor:    "2026-01-01T00:00:00Z_role-z",
		SortBy:    model.ListSortCreatedAt,
		Order:     model.ListOrderDesc,
		Direction: model.ListDirectionPrev,
	}, false, false)

	require.NoError(t, err)
	require.Len(t, result.Items, 2)
	assert.Equal(t, must.Slug("editor"), result.Items[0].Slug)
	assert.Equal(t, must.Slug("admin"), result.Items[1].Slug)
	assert.True(t, result.HasMore)
	assert.NotNil(t, result.NextCursor)
	assert.NotNil(t, result.PrevCursor)
}

func TestListRolePermissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         slug.Slug
		roleSlug        slug.Slug
		setup           func(repo *testutil.FakePermissionsRepo)
		want            []model.RolePermission
		wantErrContains string
	}{
		{
			name:     "ListsRolePermissionsSuccessfully",
			orgSlug:  must.Slug("my-org"),
			roleSlug: must.Slug("editor"),
			setup: func(repo *testutil.FakePermissionsRepo) {
				repo.ListForRoleFn = func(_ context.Context, _ postgres.DBTX, orgSlug, roleSlug slug.Slug) ([]*permissions.Permission, error) {
					assert.Equal(t, "my-org", orgSlug.String())
					assert.Equal(t, "editor", roleSlug.String())
					return []*permissions.Permission{
						{Resource: "document", Action: "read"},
						{Resource: "document", Action: "write"},
					}, nil
				}
			},
			want: []model.RolePermission{
				{Resource: "document", Action: "read"},
				{Resource: "document", Action: "write"},
			},
		},
		{
			name:     "ReturnsEmptySliceWhenNoPermissions",
			orgSlug:  must.Slug("my-org"),
			roleSlug: must.Slug("editor"),
			setup: func(repo *testutil.FakePermissionsRepo) {
				repo.ListForRoleFn = func(_ context.Context, _ postgres.DBTX, _, _ slug.Slug) ([]*permissions.Permission, error) {
					return []*permissions.Permission{}, nil
				}
			},
			want: []model.RolePermission{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rolesRepo := &testutil.FakeRolesRepo{}
			permsRepo := &testutil.FakePermissionsRepo{}
			if tt.setup != nil {
				tt.setup(permsRepo)
			}
			svc := newTestService(rolesRepo, permsRepo)

			got, err := svc.ListRolePermissions(context.Background(), tt.orgSlug, tt.roleSlug)

			if tt.wantErrContains != "" {
				require.ErrorContains(t, err, tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
