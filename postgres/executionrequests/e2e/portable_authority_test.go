package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/postgres/executionrequests"
)

func TestPortableExecutorDispatchAuthority(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
	pool := executionClaimPool(t)
	repo := executionrequests.New()
	var organization int
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO organizations(slug,name) VALUES('portable','Portable') RETURNING id`).Scan(&organization))
	var credential int64
	var revision time.Time
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload) VALUES($1,'secret','static_secret','postgres','active','\x01') RETURNING id,updated_at`, organization).Scan(&credential, &revision))
	_, err := pool.Exec(ctx, `INSERT INTO data_sources(organization_id,credential_id,slug,name,type,config,is_active) VALUES($1,$2,'db','DB','postgres','{}',true),($1,$2,'unrelated','Other','postgres','{}',true)`, organization, credential)
	require.NoError(t, err)
	id := uuid.New()
	deadline := time.Now().Add(time.Minute)
	require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{ID: id, OrganizationSlug: "portable", DataSourceSlug: "db", Kind: "query", RequestCiphertext: []byte("synthetic-envelope"), DeadlineAt: deadline, QueueDeadlineAt: &deadline, ConcurrencyLimit: 1, QueueCapacity: 1}, deadline.Add(time.Hour)))
	claim, claimed, err := repo.Claim(ctx, pool, id)
	require.NoError(t, err)
	require.True(t, claimed)
	name := "secret"
	expected := executionrequests.DispatchAuthority{RequestID: id, ClaimToken: claim.ClaimToken, Organization: "portable", DataSource: "db", Type: "postgres", CredentialSlug: &name, CredentialUpdatedAt: &revision}
	role := pgx.Identifier{fmt.Sprintf("portable_executor_%d", time.Now().UnixNano())}.Sanitize()
	_, err = pool.Exec(ctx, "CREATE ROLE "+role+" NOLOGIN")
	require.NoError(t, err)
	capability := "public.check_connector_dispatch(UUID,UUID,TEXT,TEXT,TEXT,JSONB,TEXT,TIMESTAMPTZ,BOOLEAN)"
	_, err = pool.Exec(ctx, "GRANT EXECUTE ON FUNCTION "+capability+" TO "+role)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "REVOKE ALL ON FUNCTION "+capability+" FROM "+role)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), "DROP ROLE "+role)
		require.NoError(t, err)
	})
	t.Run("authority capability grants no storage access", func(t *testing.T) {
		for _, table := range []string{"credentials", "credential_oauth_grants", "data_sources", "user_sessions", "execution_requests", "river_job"} {
			t.Run(table, func(t *testing.T) {
				tx, err := pool.Begin(ctx)
				require.NoError(t, err)
				defer func() { require.NoError(t, tx.Rollback(ctx)) }()
				_, err = tx.Exec(ctx, "SET LOCAL ROLE "+role)
				require.NoError(t, err)
				_, err = tx.Exec(ctx, "SELECT * FROM "+pgx.Identifier{"public", table}.Sanitize()+" LIMIT 0")
				var denied *pgconn.PgError
				require.ErrorAs(t, err, &denied)
				require.Equal(t, "42501", denied.Code)
			})
		}
	})
	for _, tc := range []struct {
		name, mutation string
		alter          func(*executionrequests.DispatchAuthority)
		allowed        bool
	}{
		{name: "matching delivery", allowed: true},
		{name: "unrelated tenant", alter: func(a *executionrequests.DispatchAuthority) { a.Organization = "foreign" }},
		{name: "unrelated resource with same credential", alter: func(a *executionrequests.DispatchAuthority) { a.DataSource = "unrelated" }},
		{name: "unknown delivery", alter: func(a *executionrequests.DispatchAuthority) { a.RequestID = uuid.New() }},
		{name: "unrelated claim", alter: func(a *executionrequests.DispatchAuthority) { a.ClaimToken = uuid.New() }},
		{name: "pending delivery", mutation: `UPDATE execution_requests SET state='pending'`},
		{name: "replayed completed delivery", mutation: `UPDATE execution_requests SET state='succeeded'`},
		{name: "cancelled delivery", mutation: `UPDATE execution_requests SET cancel_requested_at=clock_timestamp()`},
		{name: "expired deadline", mutation: `UPDATE execution_requests SET deadline_at=clock_timestamp()-INTERVAL '1 second'`},
		{name: "expired lease", mutation: `UPDATE execution_requests SET lease_expires_at=clock_timestamp()-INTERVAL '1 second'`},
		{name: "revoked credential", mutation: `UPDATE credentials SET status='revoked'`},
		{name: "rotated credential", mutation: `UPDATE credentials SET updated_at=updated_at+INTERVAL '1 second'`},
		{name: "deleted source", mutation: `UPDATE data_sources SET deleted_at=clock_timestamp()`},
		{name: "inactive source", mutation: `UPDATE data_sources SET is_active=false`},
		{name: "missing OAuth grant", mutation: `UPDATE credentials SET kind='oauth2_grant'`},
		{name: "expired OAuth grant", mutation: `WITH changed AS (UPDATE credentials SET kind='oauth2_grant' RETURNING id) INSERT INTO credential_oauth_grants(credential_id,provider,encrypted_token_set,access_token_expires_at) SELECT id,'synthetic','\x01',clock_timestamp()-INTERVAL '1 second' FROM changed`},
		{name: "active OAuth grant", mutation: `WITH changed AS (UPDATE credentials SET kind='oauth2_grant' RETURNING id) INSERT INTO credential_oauth_grants(credential_id,provider,encrypted_token_set,access_token_expires_at) SELECT id,'synthetic','\x01',clock_timestamp()+INTERVAL '1 hour' FROM changed`, allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { require.NoError(t, tx.Rollback(ctx)) }()
			if tc.mutation != "" {
				_, err = tx.Exec(ctx, tc.mutation)
				require.NoError(t, err)
			}
			_, err = tx.Exec(ctx, "SET LOCAL ROLE "+role)
			require.NoError(t, err)
			authority := expected
			if tc.alter != nil {
				tc.alter(&authority)
			}
			allowed, err := repo.CheckDispatch(ctx, tx, authority)
			require.NoError(t, err)
			require.Equal(t, tc.allowed, allowed)
		})
	}
	// Rotation stops new dispatches; it cannot retract credentials already released to a running operation.
	allowed, err := repo.CheckDispatch(ctx, pool, expected)
	require.NoError(t, err)
	require.True(t, allowed)
	_, err = pool.Exec(ctx, `UPDATE credentials SET updated_at=updated_at+INTERVAL '1 second'`)
	require.NoError(t, err)
	allowed, err = repo.CheckDispatch(ctx, pool, expected)
	require.NoError(t, err)
	require.False(t, allowed)
	require.NoError(t, repo.Complete(ctx, pool, id, claim.ClaimToken, []byte("synthetic-result"), true))
	request, err := repo.Find(ctx, pool, id, "portable")
	require.NoError(t, err)
	require.Equal(t, executionrequests.StateSucceeded, request.State)
	require.Equal(t, []byte("synthetic-result"), request.ResultCiphertext)
}
