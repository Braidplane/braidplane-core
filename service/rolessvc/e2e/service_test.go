package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	roleRepo "github.com/Braidplane/braidplane-core/postgres/roles"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
)

// TestRoleArchivalLifecyclePreservesActiveAccessAndTenantIsolation verifies the real SQL-backed
// service behavior for role archive, archived-only listing, permission inspection, and restore.
func TestRoleArchivalLifecyclePreservesActiveAccessAndTenantIsolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres role archival E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "role archival service integration test")
	svc := newRoleArchivalService(pool)

	orgASlug := uniqueRoleArchivalSlug("role-archive-org-a")
	orgBSlug := uniqueRoleArchivalSlug("role-archive-org-b")
	orgConflictSlug := uniqueRoleArchivalSlug("role-archive-org-conflict")
	createRoleArchivalOrganization(t, ctx, pool, orgASlug, "Role Archive Org A")
	createRoleArchivalOrganization(t, ctx, pool, orgBSlug, "Role Archive Org B")
	createRoleArchivalOrganization(t, ctx, pool, orgConflictSlug, "Role Archive Conflict Org")

	actor := model.UserEventActor(must.Slug("role-archive-actor"))
	permissionsList := []model.RolePermission{
		{Resource: model.ResourceRoles.String(), Action: model.ActionRead.String()},
		{Resource: model.ResourceAPIKeys.String(), Action: model.ActionCreate.String()},
	}

	archivable, err := svc.CreateRoleWithPermissions(ctx, orgASlug, actor, model.NewRole{
		Name: "Ops Editor",
		Slug: must.Slug("ops-editor"),
	}, permissionsList)
	require.NoError(t, err)

	_, err = svc.CreateRoleWithPermissions(ctx, orgASlug, actor, model.NewRole{
		Name: "Active Peer",
		Slug: must.Slug("active-peer"),
	}, []model.RolePermission{{Resource: model.ResourceRoles.String(), Action: model.ActionUpdate.String()}})
	require.NoError(t, err)

	orgBRole, err := svc.CreateRoleWithPermissions(ctx, orgBSlug, actor, model.NewRole{
		Name: "Ops Editor",
		Slug: must.Slug("ops-editor"),
	}, permissionsList)
	require.NoError(t, err)

	activeBeforeArchive, err := listRoleArchivalRoles(ctx, svc, orgASlug, false, false)
	require.NoError(t, err)
	assert.ElementsMatch(t, []slug.Slug{archivable.Slug, must.Slug("active-peer")}, roleArchivalSlugs(activeBeforeArchive.Items))

	require.NoError(t, svc.DeleteRole(ctx, orgASlug, archivable.Slug, actor))

	_, err = svc.GetRole(ctx, orgASlug, archivable.Slug)
	require.True(t, xerror.Is(err, rolessvc.ErrRoleNotFound), "expected archived role to be hidden, got %v", err)

	activeAfterArchive, err := listRoleArchivalRoles(ctx, svc, orgASlug, false, false)
	require.NoError(t, err)
	assert.ElementsMatch(t, []slug.Slug{must.Slug("active-peer")}, roleArchivalSlugs(activeAfterArchive.Items))

	archivedOnly, err := listRoleArchivalRoles(ctx, svc, orgASlug, true, true)
	require.NoError(t, err)
	require.Len(t, archivedOnly.Items, 1)
	assert.Equal(t, archivable.Slug, archivedOnly.Items[0].Slug)
	assert.NotNil(t, archivedOnly.Items[0].DeletedAt)

	allRoles, err := listRoleArchivalRoles(ctx, svc, orgASlug, true, false)
	require.NoError(t, err)
	assert.ElementsMatch(t, []slug.Slug{archivable.Slug, must.Slug("active-peer")}, roleArchivalSlugs(allRoles.Items))

	err = svc.UpdateRoleWithPermissions(ctx, orgASlug, archivable.Slug, actor, model.UpdateRole{
		Name: "Renamed Archived Role",
	}, permissionsList)
	require.True(t, xerror.Is(err, rolessvc.ErrRoleNotFound), "expected archived role update to be rejected, got %v", err)

	archivedPermissions, err := svc.ListRolePermissions(ctx, orgASlug, archivable.Slug)
	require.NoError(t, err)
	assert.ElementsMatch(t, permissionsList, archivedPermissions)

	err = svc.RestoreRole(ctx, orgBSlug, orgBRole.Slug, actor)
	require.True(t, xerror.Is(err, rolessvc.ErrRoleNotFound), "expected active role restore to be rejected, got %v", err)

	conflictingOriginal, err := svc.CreateRoleWithPermissions(ctx, orgConflictSlug, actor, model.NewRole{
		Name: "Reusable Role",
		Slug: must.Slug("reusable-role"),
	}, permissionsList)
	require.NoError(t, err)
	require.NoError(t, svc.DeleteRole(ctx, orgConflictSlug, conflictingOriginal.Slug, actor))

	conflictingReplacement, err := svc.CreateRoleWithPermissions(ctx, orgConflictSlug, actor, model.NewRole{
		Name: "Reusable Role Replacement",
		Slug: conflictingOriginal.Slug,
	}, []model.RolePermission{{Resource: model.ResourceRoles.String(), Action: model.ActionUpdate.String()}})
	require.NoError(t, err)
	assert.Equal(t, conflictingOriginal.Slug, conflictingReplacement.Slug)

	err = svc.RestoreRole(ctx, orgConflictSlug, conflictingOriginal.Slug, actor)
	require.True(t, xerror.Is(err, rolessvc.ErrRoleSlugAlreadyExists), "expected restore slug conflict, got %v", err)

	orgBActiveRole, err := svc.GetRole(ctx, orgBSlug, orgBRole.Slug)
	require.NoError(t, err)
	assert.Nil(t, orgBActiveRole.DeletedAt)

	require.NoError(t, svc.RestoreRole(ctx, orgASlug, archivable.Slug, actor))

	restored, err := svc.GetRole(ctx, orgASlug, archivable.Slug)
	require.NoError(t, err)
	assert.Nil(t, restored.DeletedAt)

	archivedAfterRestore, err := listRoleArchivalRoles(ctx, svc, orgASlug, true, true)
	require.NoError(t, err)
	assert.Empty(t, archivedAfterRestore.Items)

	orgBStillActive, err := svc.GetRole(ctx, orgBSlug, orgBRole.Slug)
	require.NoError(t, err)
	assert.Nil(t, orgBStillActive.DeletedAt)
}

// newRoleArchivalService wires the role service with real repositories and a no-op event producer.
func newRoleArchivalService(pool *pgxpool.Pool) rolessvc.Service {
	txm := postgres.NewTransactionManager(pool)
	return rolessvc.New(txm, roleRepo.NewRepository(), permissions.NewRepository(), eventtest.NewProducer(nil))
}

// createRoleArchivalOrganization inserts an isolated test organization and registers cleanup.
func createRoleArchivalOrganization(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgSlug slug.Slug,
	name string,
) {
	t.Helper()

	_, err := pool.Exec(ctx, `INSERT INTO organizations (name, slug) VALUES ($1, $2)`, name, orgSlug)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		_, err := pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, err)
	})
}

// listRoleArchivalRoles requests a deterministic role list for lifecycle assertions.
func listRoleArchivalRoles(
	ctx context.Context,
	svc rolessvc.Service,
	orgSlug slug.Slug,
	includeDeleted bool,
	onlyDeleted bool,
) (model.ListResponse[model.OrgRole], error) {
	return svc.ListRoles(ctx, orgSlug, model.ListRequest{
		Limit:  10,
		SortBy: model.ListSortCreatedAt,
		Order:  model.ListOrderAsc,
	}, includeDeleted, onlyDeleted)
}

// roleArchivalSlugs extracts role slugs from a service list response.
func roleArchivalSlugs(roles []model.OrgRole) []slug.Slug {
	slugs := make([]slug.Slug, len(roles))
	for i, role := range roles {
		slugs[i] = role.Slug
	}
	return slugs
}

// uniqueRoleArchivalSlug returns a deterministic-prefix slug that is unique for this test process.
func uniqueRoleArchivalSlug(prefix string) slug.Slug {
	return must.Slug(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()))
}
