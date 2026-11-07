package e2e

import (
	"crypto/subtle"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/postgres/executionrequests"
)

func TestPortableExecutorRejectsStaleCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	pool := executionClaimPool(t)
	ctx := t.Context()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(slug,name) VALUES('fence-probe','Fence probe')`)
	require.NoError(t, err)
	repo := executionrequests.New()
	id := uuid.New()
	require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{ID: id, OrganizationSlug: "fence-probe", Kind: "transform", RequestCiphertext: []byte("synthetic"), DeadlineAt: time.Now().Add(time.Minute)}, time.Now().Add(time.Hour)))
	original, claimed, err := repo.Claim(ctx, pool, id)
	require.NoError(t, err)
	require.True(t, claimed)
	var leaseBefore, leaseAfter time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT lease_expires_at FROM execution_requests WHERE id=$1`, id).Scan(&leaseBefore))
	var refused bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT heartbeat_execution_request($1,NULL)`, id).Scan(&refused))
	require.True(t, refused, "a missing token must not renew a claim")
	require.NoError(t, pool.QueryRow(ctx, `SELECT lease_expires_at FROM execution_requests WHERE id=$1`, id).Scan(&leaseAfter))
	require.Equal(t, leaseBefore, leaseAfter)
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET lease_expires_at=clock_timestamp()-INTERVAL '1 second' WHERE id=$1`, id)
	require.NoError(t, err)
	cancelled, err := repo.Heartbeat(ctx, pool, id, original.ClaimToken)
	require.NoError(t, err)
	require.True(t, cancelled, "an expired claim cannot revive its lease")
	require.Error(t, repo.Complete(ctx, pool, id, original.ClaimToken, []byte("expired-result"), true))
	replacement, claimed, err := repo.Claim(ctx, pool, id)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Zero(t, subtle.ConstantTimeCompare(original.ClaimToken.UUID[:], replacement.ClaimToken.UUID[:]), "reclaim must replace the internal capability")
	cancelled, err = repo.Heartbeat(ctx, pool, id, original.ClaimToken)
	require.NoError(t, err)
	require.True(t, cancelled)
	require.Error(t, repo.Complete(ctx, pool, id, original.ClaimToken, []byte("old-executor-result"), true), "a stale executor must not complete reclaimed work")
	require.NoError(t, repo.Complete(ctx, pool, id, replacement.ClaimToken, []byte("current-result"), true))
	require.Error(t, repo.Complete(ctx, pool, id, replacement.ClaimToken, []byte("replayed-result"), true))
	request, err := repo.Find(ctx, pool, id, "fence-probe")
	require.NoError(t, err)
	require.Equal(t, []byte("current-result"), request.ResultCiphertext)
}

func TestPortableExecutorRejectsUnfencedContract(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	pool := executionClaimPool(t)
	ctx := t.Context()
	repo := executionrequests.New()
	require.NoError(t, repo.Preflight(ctx, pool))
	require.NoError(t, repo.PreflightDelivery(ctx, pool))
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback(ctx)) }()
	_, err = tx.Exec(ctx, `ALTER FUNCTION heartbeat_execution_request(UUID, UUID) RENAME TO incompatible_heartbeat;
		CREATE FUNCTION heartbeat_execution_request(UUID) RETURNS BOOLEAN LANGUAGE sql AS 'SELECT FALSE'`)
	require.NoError(t, err)
	require.ErrorContains(t, repo.Preflight(ctx, tx), "execution request capabilities are unavailable")
	require.ErrorContains(t, repo.PreflightDelivery(ctx, tx), "River execution delivery capabilities are unavailable")
}
