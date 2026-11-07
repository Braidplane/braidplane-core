package authorization

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
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
			name:      "SuperAdminHasWildcardAccessToEverything",
			userSlug:  superAdminSlug,
			userRole:  model.RoleSuperAdmin,
			action:    model.ActionDelete,
			resource:  model.ResourceDataSources,
			wantAllow: true,
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

			fakeRolesService := &fakes.RolesService{}

			svc, err := New(fakeUsersService, fakeRolesService)
			require.NoError(t, err)

			input := Input{
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

func TestService_Can_OrgScoped(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("user")
	orgSlug := must.Slug("acme-corp")

	tests := []struct {
		name        string
		permissions []model.RolePermission
		action      model.Action
		resource    fmt.Stringer
		wantAllow   bool
	}{
		{name: "ConsumerResourceUsesExactPermission", permissions: []model.RolePermission{{Resource: "consumer_records", Action: "read"}}, action: model.Read, resource: consumerResource("consumer_records"), wantAllow: true},
		{name: "ConsumerResourceRequiresPermission", permissions: []model.RolePermission{{Resource: "other_records", Action: "read"}}, action: model.Read, resource: consumerResource("consumer_records"), wantAllow: false},
		{name: "MissingResourceFailsClosed", action: model.Read, wantAllow: false},
		{
			name: "UserWithWildcardPermissionCanAccessAnything",
			permissions: []model.RolePermission{
				{Resource: "*", Action: "*"},
			},
			action:    model.ActionDelete,
			resource:  model.ResourceDataSources,
			wantAllow: true,
		},
		{
			name: "UserWithResourceWildcardCanPerformAnyActionOnResource",
			permissions: []model.RolePermission{
				{Resource: model.ResourceDataSources.String(), Action: "*"},
			},
			action:    model.ActionDelete,
			resource:  model.ResourceDataSources,
			wantAllow: true,
		},
		{
			name: "UserWithSpecificPermissionCanPerformAction",
			permissions: []model.RolePermission{
				{Resource: model.ResourceDataSources.String(), Action: model.ActionRead.String()},
			},
			action:    model.ActionRead,
			resource:  model.ResourceDataSources,
			wantAllow: true,
		},
		{
			name: "UserWithoutPermissionCannotPerformAction",
			permissions: []model.RolePermission{
				{Resource: model.ResourceDataSources.String(), Action: model.ActionRead.String()},
			},
			action:    model.ActionDelete,
			resource:  model.ResourceDataSources,
			wantAllow: false,
		},
		{
			name: "UserWithDifferentResourcePermissionCannotAccessResource",
			permissions: []model.RolePermission{
				{Resource: model.ResourceQueries.String(), Action: model.ActionRead.String()},
			},
			action:    model.ActionRead,
			resource:  model.ResourceDataSources,
			wantAllow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeUsersService := &fakes.UsersService{
				GetUserBySlugFn: func(ctx context.Context, slugParam slug.Slug) (*model.User, error) {
					return &model.User{
						Slug:       userSlug,
						Role:       model.RoleUser,
						VerifiedAt: time.Now(),
					}, nil
				},
			}

			fakeRolesService := &fakes.RolesService{
				GetUserPermissionsInOrgFn: func(ctx context.Context, org, user slug.Slug) ([]model.RolePermission, error) {
					return tt.permissions, nil
				},
			}

			svc, err := New(fakeUsersService, fakeRolesService)
			require.NoError(t, err)

			input := Input{
				UserSlug: userSlug,
				OrgSlug:  &orgSlug,
				Action:   tt.action,
				Resource: tt.resource,
			}

			allowed, err := svc.Can(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, tt.wantAllow, allowed)
		})
	}
}

func TestService_Can_OrgAdminMembershipDoesNotGrantSystemSuperAdminPrivileges(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("organization-admin-user")
	orgSlug := must.Slug("acme-corp")
	orgPermissionsRequested := false

	fakeUsersService := &fakes.UsersService{
		GetUserBySlugFn: func(ctx context.Context, slugParam slug.Slug) (*model.User, error) {
			return &model.User{
				Slug:       userSlug,
				Role:       model.RoleUser,
				VerifiedAt: time.Now(),
			}, nil
		},
	}
	fakeRolesService := &fakes.RolesService{
		GetUserPermissionsInOrgFn: func(ctx context.Context, org, user slug.Slug) ([]model.RolePermission, error) {
			orgPermissionsRequested = true
			require.Equal(t, orgSlug, org)
			require.Equal(t, userSlug, user)
			return []model.RolePermission{{Resource: "*", Action: "*"}}, nil
		},
	}

	svc, err := New(fakeUsersService, fakeRolesService)
	require.NoError(t, err)

	allowed, err := svc.Can(context.Background(), Input{
		UserSlug: userSlug,
		OrgSlug:  &orgSlug,
		Action:   model.ActionDelete,
		Resource: model.ResourceDataSources,
	})

	require.NoError(t, err)
	require.True(t, allowed)

	orgPermissionsRequested = false
	allowed, err = svc.Can(context.Background(), Input{
		UserSlug: userSlug,
		Action:   model.ActionUpdate,
		Resource: model.ResourceUsers,
	})

	require.NoError(t, err)
	require.False(t, allowed)
	require.False(t, orgPermissionsRequested)
}

func TestService_Can_SuperAdminDoesNotBypassOrgMembership(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("super-admin-user")
	orgSlug := must.Slug("acme-corp")
	fakeUsersService := &fakes.UsersService{
		GetUserBySlugFn: func(ctx context.Context, slugParam slug.Slug) (*model.User, error) {
			return &model.User{
				Slug:       userSlug,
				Role:       model.RoleSuperAdmin,
				VerifiedAt: time.Now(),
			}, nil
		},
	}
	fakeRolesService := &fakes.RolesService{
		GetUserPermissionsInOrgFn: func(ctx context.Context, org, user slug.Slug) ([]model.RolePermission, error) {
			require.Equal(t, orgSlug, org)
			require.Equal(t, userSlug, user)
			return nil, rolessvc.ErrUserNotMember
		},
	}

	svc, err := New(fakeUsersService, fakeRolesService)
	require.NoError(t, err)

	allowed, err := svc.Can(context.Background(), Input{
		UserSlug: userSlug,
		OrgSlug:  &orgSlug,
		Action:   model.ActionDelete,
		Resource: model.ResourceDataSources,
	})

	require.NoError(t, err)
	require.False(t, allowed)
}

func TestService_Can_APIKeyRoleNotFoundIsDenied(t *testing.T) {
	t.Parallel()

	orgSlug := must.Slug("acme-corp")
	apiKeySlug := must.Slug("integration-key")
	roleSlug := must.Slug("archived-role")
	permissionsListed := false

	fakeRolesService := &fakes.RolesService{
		GetRoleFn: func(ctx context.Context, org, role slug.Slug) (*model.OrgRole, error) {
			require.Equal(t, orgSlug, org)
			require.Equal(t, roleSlug, role)
			return nil, rolessvc.ErrRoleNotFound
		},
		ListRolePermissionsFn: func(ctx context.Context, org, role slug.Slug) ([]model.RolePermission, error) {
			permissionsListed = true
			return []model.RolePermission{{Resource: "*", Action: "*"}}, nil
		},
	}

	svc, err := New(&fakes.UsersService{}, fakeRolesService)
	require.NoError(t, err)

	allowed, err := svc.Can(context.Background(), Input{
		APIKeySlug:     apiKeySlug,
		APIKeyRoleSlug: roleSlug,
		OrgSlug:        &orgSlug,
		Action:         model.ActionRead,
		Resource:       model.ResourceDataSources,
	})

	require.NoError(t, err)
	require.False(t, allowed)
	require.False(t, permissionsListed)
}

func TestService_Can_OrgLevelPermissions(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("user")
	orgSlug := must.Slug("acme-corp")

	tests := []struct {
		name        string
		permissions []model.RolePermission
		action      model.Action
		resource    fmt.Stringer
		wantAllow   bool
	}{
		{
			name: "UserWithResourceSpecificPermissionCanAccessThatResource",
			permissions: []model.RolePermission{
				{
					Resource: model.ResourceDataSources.String(),
					Action:   model.ActionRead.String(),
				},
			},
			action:    model.ActionRead,
			resource:  model.ResourceDataSources,
			wantAllow: true,
		},
		{
			name: "UserWithResourceSpecificPermissionCanAccessOtherResourceAtOrgLevel",
			permissions: []model.RolePermission{
				{
					Resource: model.ResourceDataSources.String(),
					Action:   model.ActionRead.String(),
				},
			},
			action:    model.ActionRead,
			resource:  model.ResourceDataSources,
			wantAllow: true,
		},
		{
			name: "UserWithGeneralPermissionCanAccessAnyResource",
			permissions: []model.RolePermission{
				{
					Resource: model.ResourceDataSources.String(),
					Action:   model.ActionRead.String(),
				},
			},
			action:    model.ActionRead,
			resource:  model.ResourceDataSources,
			wantAllow: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeUsersService := &fakes.UsersService{
				GetUserBySlugFn: func(ctx context.Context, slugParam slug.Slug) (*model.User, error) {
					return &model.User{
						Slug:       userSlug,
						Role:       model.RoleUser,
						VerifiedAt: time.Now(),
					}, nil
				},
			}

			fakeRolesService := &fakes.RolesService{
				GetUserPermissionsInOrgFn: func(ctx context.Context, org, user slug.Slug) ([]model.RolePermission, error) {
					return tt.permissions, nil
				},
			}

			svc, err := New(fakeUsersService, fakeRolesService)
			require.NoError(t, err)

			input := Input{
				UserSlug: userSlug,
				OrgSlug:  &orgSlug,
				Action:   tt.action,
				Resource: tt.resource,
			}

			allowed, err := svc.Can(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, tt.wantAllow, allowed)
		})
	}
}

func TestService_Can_OwnershipChecks(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("user")
	otherUserSlug := must.Slug("other-user")

	tests := []struct {
		name              string
		userSlug          slug.Slug
		resourceOwnerSlug slug.Slug
		action            model.Action
		resource          model.Resource
		wantAllow         bool
	}{
		{
			name:              "UserCanReadOwnProfileViaOwnership",
			userSlug:          userSlug,
			resourceOwnerSlug: userSlug,
			action:            model.ActionRead,
			resource:          model.ResourceProfile,
			wantAllow:         true,
		},
		{
			name:              "UserCanUpdateOwnProfileViaOwnership",
			userSlug:          userSlug,
			resourceOwnerSlug: userSlug,
			action:            model.ActionUpdate,
			resource:          model.ResourceProfile,
			wantAllow:         true,
		},
		{
			name:              "UserCannotReadOtherUserProfile",
			userSlug:          userSlug,
			resourceOwnerSlug: otherUserSlug,
			action:            model.ActionRead,
			resource:          model.ResourceProfile,
			wantAllow:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeUsersService := &fakes.UsersService{
				GetUserBySlugFn: func(ctx context.Context, slugParam slug.Slug) (*model.User, error) {
					return &model.User{
						Slug:       tt.userSlug,
						Role:       model.RoleUser,
						VerifiedAt: time.Now(),
					}, nil
				},
			}

			fakeRolesService := &fakes.RolesService{}

			svc, err := New(fakeUsersService, fakeRolesService)
			require.NoError(t, err)

			input := Input{
				UserSlug:          tt.userSlug,
				Action:            tt.action,
				Resource:          tt.resource,
				ResourceOwnerSlug: &tt.resourceOwnerSlug,
			}

			allowed, err := svc.Can(context.Background(), input)
			require.NoError(t, err)
			require.Equal(t, tt.wantAllow, allowed)
		})
	}
}

func TestService_Can_EdgeCases(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("user")
	orgSlug := must.Slug("acme-corp")

	tests := []struct {
		name               string
		userFn             func(ctx context.Context, slugParam slug.Slug) (*model.User, error)
		permissionsFn      func(ctx context.Context, org, user slug.Slug) ([]model.RolePermission, error)
		action             model.Action
		resource           model.Resource
		expectedAllow      bool
		expectedErrContain string
	}{
		{
			name: "UnverifiedUserIsDeniedAccess",
			userFn: func(ctx context.Context, slugParam slug.Slug) (*model.User, error) {
				return &model.User{
					Slug:       userSlug,
					Role:       model.RoleUser,
					VerifiedAt: time.Time{}, // Zero value = unverified
				}, nil
			},
			action:        model.ActionRead,
			resource:      model.ResourceDataSources,
			expectedAllow: false,
		},
		{
			name: "UserNotFoundIsDeniedAccess",
			userFn: func(ctx context.Context, slugParam slug.Slug) (*model.User, error) {
				return nil, userssvc.ErrUserNotFound
			},
			action:        model.ActionRead,
			resource:      model.ResourceDataSources,
			expectedAllow: false,
		},
		{
			name: "UserNotMemberOfOrgIsDeniedAccess",
			userFn: func(ctx context.Context, slugParam slug.Slug) (*model.User, error) {
				return &model.User{
					Slug:       userSlug,
					Role:       model.RoleUser,
					VerifiedAt: time.Now(),
				}, nil
			},
			permissionsFn: func(ctx context.Context, org, user slug.Slug) ([]model.RolePermission, error) {
				return nil, rolessvc.ErrUserNotMember
			},
			action:        model.ActionRead,
			resource:      model.ResourceDataSources,
			expectedAllow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeUsersService := &fakes.UsersService{
				GetUserBySlugFn: tt.userFn,
			}

			fakeRolesService := &fakes.RolesService{}
			if tt.permissionsFn != nil {
				fakeRolesService.GetUserPermissionsInOrgFn = tt.permissionsFn
			}

			svc, err := New(fakeUsersService, fakeRolesService)
			require.NoError(t, err)

			input := Input{
				UserSlug: userSlug,
				OrgSlug:  &orgSlug,
				Action:   tt.action,
				Resource: tt.resource,
			}

			allowed, err := svc.Can(context.Background(), input)

			if tt.expectedErrContain != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.expectedErrContain)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.expectedAllow, allowed)
			}
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
			allowed, err := CanCapturedCompletionAuthority(t.Context(), model.CompletionAuthoritySnapshot{Valid: tt.valid, Permissions: tt.permissions})
			require.NoError(t, err)
			require.Equal(t, tt.allowed, allowed)
		})
	}
}

func TestService_AdminIdentityRechecksGlobalRole(t *testing.T) {
	t.Parallel()
	role := model.RoleSuperAdmin
	users := &fakes.UsersService{GetUserBySlugFn: func(context.Context, slug.Slug) (*model.User, error) {
		return &model.User{Slug: "operator", Role: role, VerifiedAt: time.Now()}, nil
	}}
	roles := &fakes.RolesService{GetUserPermissionsInOrgFn: func(context.Context, slug.Slug, slug.Slug) ([]model.RolePermission, error) {
		t.Fatal("system identity must not request organization permissions")
		return nil, nil
	}}
	svc, err := New(users, roles)
	require.NoError(t, err)
	input := Input{UserSlug: "operator", Action: model.Read, Resource: model.ResourceUsers}
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
