package authorization

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

func TestService_Can_SystemLevel(t *testing.T) {
	t.Parallel()

	superAdminSlug := must.Slug("super-admin-user")
	userSlug := must.Slug("regular-user")

	tests := []struct {
		name              string
		userSlug          slug.Slug
		userRole          model.Role
		action            model.Action
		resource          model.Resource
		resourceOwnerSlug *slug.Slug
		wantAllow         bool
	}{
		{
			name:      "SuperAdminCanUpdateUserOutsideOrganization",
			userSlug:  superAdminSlug,
			userRole:  model.RoleSuperAdmin,
			action:    model.ActionUpdate,
			resource:  model.ResourceUsers,
			wantAllow: true,
		},
		{
			name:      "SuperAdminCannotAddressTenantResourcesWithoutOrganization",
			userSlug:  superAdminSlug,
			userRole:  model.RoleSuperAdmin,
			action:    model.ActionDelete,
			resource:  model.ResourceDataSources,
			wantAllow: false,
		},
		{
			name:      "UserCanCreateOrganization",
			userSlug:  userSlug,
			userRole:  model.RoleUser,
			action:    model.ActionCreate,
			resource:  model.ResourceOrganizations,
			wantAllow: true,
		},
		{
			name:      "UserCanReadOrganizations",
			userSlug:  userSlug,
			userRole:  model.RoleUser,
			action:    model.ActionRead,
			resource:  model.ResourceOrganizations,
			wantAllow: true,
		},
		{
			name:              "UserCanReadOwnProfile",
			userSlug:          userSlug,
			userRole:          model.RoleUser,
			action:            model.ActionRead,
			resource:          model.ResourceProfile,
			resourceOwnerSlug: &userSlug,
			wantAllow:         true,
		},
		{
			name:              "UserCanUpdateOwnProfile",
			userSlug:          userSlug,
			userRole:          model.RoleUser,
			action:            model.ActionUpdate,
			resource:          model.ResourceProfile,
			resourceOwnerSlug: &userSlug,
			wantAllow:         true,
		},
		{
			name:      "UserCannotDeleteOrganizations",
			userSlug:  userSlug,
			userRole:  model.RoleUser,
			action:    model.ActionDelete,
			resource:  model.ResourceOrganizations,
			wantAllow: false,
		},
		{
			name: "UserCanDeleteOwnProfile", userSlug: userSlug, userRole: model.RoleUser,
			action: model.ActionDelete, resource: model.ResourceProfile, resourceOwnerSlug: &userSlug, wantAllow: true,
		},
		{
			name: "UserCannotDeleteAnotherProfile", userSlug: userSlug, userRole: model.RoleUser,
			action: model.ActionDelete, resource: model.ResourceProfile, resourceOwnerSlug: &superAdminSlug, wantAllow: false,
		},
		{
			name:      "UserCannotUpdateUserOutsideOrganization",
			userSlug:  userSlug,
			userRole:  model.RoleUser,
			action:    model.ActionUpdate,
			resource:  model.ResourceUsers,
			wantAllow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeUsersService := &fakes.UsersService{
				GetUserBySlugFn: func(ctx context.Context, userSlug slug.Slug) (*model.User, error) {
					if userSlug != tt.userSlug {
						return nil, userssvc.ErrUserNotFound
					}
					return &model.User{
						Slug:       userSlug,
						Role:       tt.userRole,
						VerifiedAt: time.Now(),
					}, nil
				},
			}

			svc, err := New(fakeUsersService, nil)
			require.NoError(t, err)

			operation := map[model.Resource]map[model.Action]string{
				model.ResourceUsers:         {model.Read: "instanceUserAdministration", model.Update: "instanceUserAdministration"},
				model.ResourceOrganizations: {model.Read: "listOrganizations", model.Create: "createOrganization"},
				model.ResourceProfile:       {model.Read: "getUsersProfile", model.Update: "patchUsersProfile", model.Delete: "requestAccountDeletion"},
			}[tt.resource][tt.action]
			input := Input{
				Operation: operation, Source: SourceSession,
				UserSlug:          tt.userSlug,
				Action:            tt.action,
				Resource:          tt.resource,
				ResourceOwnerSlug: tt.resourceOwnerSlug,
			}

			allowed, err := svc.Can(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, tt.wantAllow, allowed)
		})
	}
}

func TestCapturedCompletionAuthorityUsesExistingPolicy(t *testing.T) {
	for _, tt := range []struct {
		name        string
		valid       bool
		permissions []string
		allowed     bool
	}{
		{name: "full wildcard", valid: true, permissions: []string{"*:*"}, allowed: true},
		{name: "resource wildcards", valid: true, permissions: []string{"pipelines:*", "pipeline_triggers:*"}, allowed: true},
		{name: "exact", valid: true, permissions: []string{"pipelines:read", "pipeline_triggers:update", "pipelines:execute"}, allowed: true},
		{name: "read only", valid: true, permissions: []string{"pipelines:read", "pipeline_triggers:update"}},
		{name: "no upstream read", valid: true, permissions: []string{"pipelines:execute", "pipeline_triggers:update"}},
		{name: "no trigger management", valid: true, permissions: []string{"pipelines:*"}},
		{name: "invalid principal", permissions: []string{"*:*"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			facts := model.AuthorizationFacts{Valid: true, OrganizationID: "1", Bindings: []model.AuthorizationBinding{{Scope: "organization", ScopeID: "1", Permissions: tt.permissions}}}
			allowed, err := CanCapturedCompletionAuthority(t.Context(), "org", model.CompletionAuthoritySnapshot{Valid: tt.valid, PrincipalID: "member:1", Scoped: &model.CompletionAuthorizationFacts{Upstream: facts, Trigger: facts, Downstream: facts}})
			require.NoError(t, err)
			require.Equal(t, tt.allowed, allowed)
		})
	}
}

func TestCompletionSnapshotWithoutBoundEditorIsDenied(t *testing.T) {
	allowed, err := CanCapturedCompletionAuthority(t.Context(), "org", model.CompletionAuthoritySnapshot{Valid: true, Permissions: []string{"*:*"}})
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestService_AdminIdentityRechecksGlobalRole(t *testing.T) {
	t.Parallel()
	role := model.RoleSuperAdmin
	users := &fakes.UsersService{GetUserBySlugFn: func(context.Context, slug.Slug) (*model.User, error) {
		return &model.User{Slug: "operator", Role: role, VerifiedAt: time.Now()}, nil
	}}
	svc, err := New(users, nil)
	require.NoError(t, err)
	input := Input{Operation: "instanceUserAdministration", Source: SourceSession, UserSlug: "operator", Action: model.Read, Resource: model.ResourceUsers}
	allowed, err := svc.Can(t.Context(), input)
	require.NoError(t, err)
	require.True(t, allowed)
	role = model.RoleUser
	allowed, err = svc.Can(t.Context(), input)
	require.NoError(t, err)
	require.False(t, allowed)
}

type consumerResource string

func (r consumerResource) String() string { return string(r) }

func TestCanResolvesCurrentAuthority(t *testing.T) {
	org := slug.Slug("acme")
	target := model.AuthorizationTarget{Kind: "pipelines", Slug: "sync", ProjectSlug: "project"}
	for _, machine := range []bool{false, true} {
		input := Input{Operation: "getPipeline", OrgSlug: &org, Resource: model.ResourcePipelines, Action: model.Read, Target: target}
		if machine {
			input.Source = SourceAPIKey
			input.APIKeySlug = "robot"
			input.APIKeyRoleSlug = "caller-asserted"
		} else {
			input.Source = SourceSession
			input.UserSlug = "alice"
		}
		facts := model.AuthorizationFacts{Valid: true, OrganizationID: "1", ProjectID: "2", EnvironmentID: "3", TargetID: "pipelines:4"}
		var resolveErr error
		svc, err := New(nil, func(_ context.Context, tenant, user, key slug.Slug, selector model.AuthorizationTarget) (model.AuthorizationFacts, error) {
			require.Equal(t, org, tenant)
			require.Equal(t, input.UserSlug, user)
			require.Equal(t, input.APIKeySlug, key)
			require.Equal(t, target, selector)
			return facts, resolveErr
		})
		require.NoError(t, err)
		check := func(want bool) {
			t.Helper()
			got, err := svc.Can(t.Context(), input)
			require.NoError(t, err)
			require.Equal(t, want, got)
		}
		for _, binding := range []model.AuthorizationBinding{
			{Scope: "resource", ScopeID: "pipelines:4", Permissions: []string{"pipelines:read"}},
			{Scope: "project", ScopeID: "2", Permissions: []string{"pipelines:*"}},
			{Scope: "environment", ScopeID: "3", Permissions: []string{"*:*"}},
			{Scope: "organization", ScopeID: "1", Permissions: []string{"pipelines:read"}},
		} {
			facts.Bindings = []model.AuthorizationBinding{binding}
			check(true)
			facts.Bindings[0].ScopeID = "wrong"
			check(false)
		}
		facts.Bindings = append(facts.Bindings, model.AuthorizationBinding{Scope: "organization", ScopeID: "1", Permissions: []string{"*:*"}})
		check(true)
		facts.Valid = false
		check(false)
		facts.Valid = true
		input.Resource = model.ResourceUsers
		check(false)
		input.Resource = model.ResourceProfile
		check(false)
		input.Resource = model.ResourcePipelines
		input.Action = model.Purge
		check(false)
		input.Action = model.Read
		facts.TargetID = ""
		input.Operation = ""
		input.Resource = consumerResource("consumer_records")
		check(false)
		facts.Bindings[1].Permissions = []string{"consumer_records:read"}
		check(true)
		facts.Bindings = nil
		check(false)
		resolveErr = xerror.Unexpected().Detail("unavailable")
		allowed, err := svc.Can(t.Context(), input)
		require.ErrorIs(t, err, resolveErr)
		require.False(t, allowed)
	}
}

func TestProjectBindingDoesNotGrantSharedSourceAccess(t *testing.T) {
	org := slug.Slug("org")
	facts := model.AuthorizationFacts{
		Valid: true, OrganizationID: "1", TargetID: "projects:2", ProjectID: "2",
		Bindings: []model.AuthorizationBinding{{Scope: "project", ScopeID: "2", Permissions: []string{"*:*"}}},
	}
	svc, err := New(nil, func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		return facts, nil
	})
	require.NoError(t, err)
	input := Input{Operation: "listDataSourceRefs", Source: SourceSession, OrgSlug: &org, UserSlug: "alice", Resource: model.ResourceDataSources, Action: model.Read, Target: model.AuthorizationTarget{Kind: "projects", Slug: "inventory"}}
	check := func(want bool) {
		t.Helper()
		allowed, err := svc.Can(t.Context(), input)
		require.NoError(t, err)
		require.Equal(t, want, allowed)
	}
	check(false)
	input.ProjectDataSourceBindings = true
	check(true)
	input.Target = model.AuthorizationTarget{Kind: "datasources", Slug: "shared-source"}
	facts.TargetID = "datasources:2"
	facts.ProjectID = ""
	check(false)
}

func TestScopedGrantCannotDelegateOrganizationAuthority(t *testing.T) {
	binding := model.AuthorizationBinding{Scope: "resource", ScopeID: "roles:2", Permissions: []string{"roles:*"}}
	facts := model.AuthorizationFacts{Valid: true, OrganizationID: "1", TargetID: "roles:2", Bindings: []model.AuthorizationBinding{binding}}
	for _, action := range []model.Action{model.Read, model.Update, model.Restore} {
		got, err := CanWithResolvedFacts(t.Context(), action, model.ResourceRoles, facts)
		require.NoError(t, err)
		require.Equal(t, action == model.Read, got)
	}
}

// Each tenant permission in the operation inventory must work for its original organization-wide grant.
func TestRegisteredTenantPermissionsHavePolicyRules(t *testing.T) {
	facts := model.AuthorizationFacts{Valid: true, OrganizationID: "10", Bindings: []model.AuthorizationBinding{{
		Scope: "organization", ScopeID: "10", Permissions: []string{"*:*"},
	}}}
	for id, op := range operationRules {
		if op.class != "tenant" {
			continue
		}
		for check := range op.selectors {
			pair, _, _ := strings.Cut(check, "@")
			name, verb, ok := strings.Cut(pair, ":")
			require.True(t, ok, id)
			resource, err := model.ResourceString(name)
			require.NoError(t, err, id)
			action, err := model.ActionString(verb)
			require.NoError(t, err, id)
			allowed, err := CanWithResolvedFacts(t.Context(), action, resource, facts)
			require.NoError(t, err, id)
			require.True(t, allowed, "%s: %s", id, pair)
		}
	}
}
