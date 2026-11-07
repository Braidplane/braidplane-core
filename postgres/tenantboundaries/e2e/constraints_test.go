package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
)

type identityBoundaryFixture struct {
	prefix           string
	organizationAID  int32
	organizationBID  int32
	roleAID          int32
	roleBID          int32
	userBID          int32
	unassignedUserID int32
	memberAID        int32
	memberBID        int32
	invitationAID    int32
}

func seedIdentityBoundaryFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) identityBoundaryFixture {
	t.Helper()

	prefix := fmt.Sprintf("tenant-boundary-%d", time.Now().UnixNano())
	fixture := identityBoundaryFixture{prefix: prefix}

	// Seed directly because this database-boundary test must attempt pairings
	// that service-layer APIs reject before they reach PostgreSQL.
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO organizations (slug, name)
		VALUES ($1, $2)
		RETURNING id
	`, prefix+"-a", "Tenant Boundary A").Scan(&fixture.organizationAID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO organizations (slug, name)
		VALUES ($1, $2)
		RETURNING id
	`, prefix+"-b", "Tenant Boundary B").Scan(&fixture.organizationBID))

	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO roles (organization_id, slug, name)
		VALUES ($1, $2, $3)
		RETURNING id
	`, fixture.organizationAID, prefix+"-role-a", "Role A").Scan(&fixture.roleAID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO roles (organization_id, slug, name)
		VALUES ($1, $2, $3)
		RETURNING id
	`, fixture.organizationBID, prefix+"-role-b", "Role B").Scan(&fixture.roleBID))

	userAID := insertBoundaryUser(t, ctx, pool, prefix+"-user-a")
	fixture.userBID = insertBoundaryUser(t, ctx, pool, prefix+"-user-b")
	fixture.unassignedUserID = insertBoundaryUser(t, ctx, pool, prefix+"-unassigned")

	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO organization_members (organization_id, user_id, role_id)
		VALUES ($1, $2, $3)
		RETURNING id
	`, fixture.organizationAID, userAID, fixture.roleAID).Scan(&fixture.memberAID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO organization_members (organization_id, user_id, role_id)
		VALUES ($1, $2, $3)
		RETURNING id
	`, fixture.organizationBID, fixture.userBID, fixture.roleBID).Scan(&fixture.memberBID))

	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO organization_member_invitations (
			organization_id, slug, email, role_id, token_hash, expires_at
		) VALUES ($1, $2, $3, $4, $5, NOW() + INTERVAL '1 day')
		RETURNING id
	`,
		fixture.organizationAID,
		prefix+"-invitation-a",
		prefix+"-invitation-a@example.com",
		fixture.roleAID,
		prefix+"-invitation-a-token",
	).Scan(&fixture.invitationAID))

	cleanupContext := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, err := pool.Exec(cleanupContext, `
			DELETE FROM api_keys
			WHERE organization_id IN ($1, $2)
		`, fixture.organizationAID, fixture.organizationBID)
		require.NoError(t, err)

		_, err = pool.Exec(cleanupContext, `
			DELETE FROM organization_member_invitations
			WHERE organization_id IN ($1, $2)
		`, fixture.organizationAID, fixture.organizationBID)
		require.NoError(t, err)

		_, err = pool.Exec(cleanupContext, `
			DELETE FROM organization_members
			WHERE organization_id IN ($1, $2)
		`, fixture.organizationAID, fixture.organizationBID)
		require.NoError(t, err)

		_, err = pool.Exec(cleanupContext, `
			DELETE FROM organizations
			WHERE id IN ($1, $2)
		`, fixture.organizationAID, fixture.organizationBID)
		require.NoError(t, err)

		_, err = pool.Exec(cleanupContext, `
			DELETE FROM users
			WHERE id IN ($1, $2, $3)
		`, userAID, fixture.userBID, fixture.unassignedUserID)
		require.NoError(t, err)
	})

	return fixture
}

func insertBoundaryUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, prefix string) int32 {
	t.Helper()

	var id int32
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO users (slug, email, role)
		VALUES ($1, $2, 'user')
		RETURNING id
	`, prefix, prefix+"@example.com").Scan(&id))
	return id
}

func TestIdentityRelationshipsRejectCrossOrganizationPairings(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres tenant boundary constraint test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "identity tenant boundary constraint test")
	fixture := seedIdentityBoundaryFixture(t, ctx, pool)

	tests := []struct {
		name           string
		statement      string
		args           []any
		constraintName string
	}{
		{
			name: "OrganizationMemberRole",
			statement: `
				INSERT INTO organization_members (organization_id, user_id, role_id)
				VALUES ($1, $2, $3)
			`,
			args:           []any{fixture.organizationAID, fixture.unassignedUserID, fixture.roleBID},
			constraintName: "fk_organization_members_org_role",
		},
		{
			name: "InvitationRole",
			statement: `
				INSERT INTO organization_member_invitations (
					organization_id, slug, email, role_id, token_hash, expires_at
				) VALUES ($1, $2, $3, $4, $5, NOW() + INTERVAL '1 day')
			`,
			args: []any{
				fixture.organizationAID,
				fixture.prefix + "-wrong-role",
				fixture.prefix + "-wrong-role@example.com",
				fixture.roleBID,
				fixture.prefix + "-wrong-role-token",
			},
			constraintName: "fk_organization_member_invitations_org_role",
		},
		{
			name: "InvitationAcceptedMember",
			statement: `
				INSERT INTO organization_member_invitations (
					organization_id, slug, email, role_id, accepted_member_id,
					token_hash, expires_at
				) VALUES ($1, $2, $3, $4, $5, $6, NOW() + INTERVAL '1 day')
			`,
			args: []any{
				fixture.organizationAID,
				fixture.prefix + "-wrong-member",
				fixture.prefix + "-wrong-member@example.com",
				fixture.roleAID,
				fixture.memberBID,
				fixture.prefix + "-wrong-member-token",
			},
			constraintName: "fk_organization_member_invitations_org_accepted_member",
		},
		{
			name: "APIKeyCreator",
			statement: `
				INSERT INTO api_keys (
					organization_id, slug, name, token_hash, token_prefix,
					created_by_member_id, role_id
				) VALUES ($1, $2, $3, $4, $5, $6, $7)
			`,
			args: []any{
				fixture.organizationAID,
				fixture.prefix + "-wrong-creator",
				"Wrong creator",
				fixture.prefix + "-wrong-creator-hash",
				"wrong-creator",
				fixture.memberBID,
				fixture.roleAID,
			},
			constraintName: "fk_api_keys_org_created_by_member",
		},
		{
			name: "APIKeyRole",
			statement: `
				INSERT INTO api_keys (
					organization_id, slug, name, token_hash, token_prefix,
					created_by_member_id, role_id
				) VALUES ($1, $2, $3, $4, $5, $6, $7)
			`,
			args: []any{
				fixture.organizationAID,
				fixture.prefix + "-wrong-api-role",
				"Wrong API role",
				fixture.prefix + "-wrong-api-role-hash",
				"wrong-role",
				fixture.memberAID,
				fixture.roleBID,
			},
			constraintName: "fk_api_keys_org_role",
		},
	}

	for _, test := range tests { // Subtests share one real-Postgres fixture.
		t.Run(test.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, test.statement, test.args...)
			require.Error(t, err)

			var postgresError *pgconn.PgError
			require.ErrorAs(t, err, &postgresError)
			require.Equal(t, "23503", postgresError.Code)
			require.Equal(t, test.constraintName, postgresError.ConstraintName)
		})
	}
}
