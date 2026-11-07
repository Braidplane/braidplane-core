package e2e

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/executionrequests"
)

// ClaimNext polls globally, so its tests must not claim retained acceptance requests.
func executionClaimPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return e2edb.OpenIsolatedPostgresPool(t, "execution request isolated queue")
}

func TestExecutionRequestCrashReplayAndCancellationBoundaries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-Postgres execution request test in short mode")
	}
	ctx := context.Background()
	pool := executionClaimPool(t)
	repo := executionrequests.New()
	org := slug.Slug(fmt.Sprintf("execution-capability-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
		require.NoError(t, cleanupErr)
	})

	create := func(kind string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{
			ID: id, OrganizationSlug: org.String(), Kind: kind,
			RequestCiphertext: []byte("encrypted"), DeadlineAt: time.Now().Add(time.Minute),
		}, time.Now().Add(time.Hour)))
		return id
	}

	writeID := create("query")
	claimed, ok, err := repo.Claim(ctx, pool, writeID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, executionrequests.StateRunning, claimed.State)
	_, ok, err = repo.Claim(ctx, pool, writeID)
	require.NoError(t, err)
	require.False(t, ok, "an active lease cannot be claimed concurrently")
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET lease_expires_at = NOW() - INTERVAL '1 second' WHERE id = $1`, writeID)
	require.NoError(t, err)
	claimed, ok, err = repo.Claim(ctx, pool, writeID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, executionrequests.StateAmbiguous, claimed.State, "crash retry must not repeat a possible side effect")

	pureID := create("transform")
	proofID := create("proof_metadata")
	_, ok, err = repo.Claim(ctx, pool, proofID)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET lease_expires_at = NOW() - INTERVAL '1 second' WHERE id = $1`, proofID)
	require.NoError(t, err)
	claimed, ok, err = repo.Claim(ctx, pool, proofID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, executionrequests.StateAmbiguous, claimed.State, "read-only Proof cannot replay its reserved provider budget")
	_, ok, err = repo.Claim(ctx, pool, proofID)
	require.NoError(t, err)
	require.False(t, ok)

	_, ok, err = repo.Claim(ctx, pool, pureID)
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, err = repo.Claim(ctx, pool, pureID)
	require.NoError(t, err)
	require.False(t, ok, "pure work also respects the active lease")
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET lease_expires_at = NOW() - INTERVAL '1 second' WHERE id = $1`, pureID)
	require.NoError(t, err)
	claimed, ok, err = repo.Claim(ctx, pool, pureID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, executionrequests.StateRunning, claimed.State, "pure operations may be reclaimed")

	cancelID := create("connection_test")
	cancelClaim, ok, err := repo.Claim(ctx, pool, cancelID)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = repo.Cancel(ctx, pool, cancelID)
	require.NoError(t, err)
	cancelled, err := repo.Heartbeat(ctx, pool, cancelID, cancelClaim.ClaimToken)
	require.NoError(t, err)
	require.True(t, cancelled, "heartbeat must observe durable cancellation")
	require.NoError(t, repo.Complete(ctx, pool, cancelID, cancelClaim.ClaimToken, []byte("late result"), true))
	var state executionrequests.State
	var resultIsNull bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT state, result_ciphertext IS NULL
		FROM execution_requests
		WHERE id = $1
	`, cancelID).Scan(&state, &resultIsNull))
	require.Equal(t, executionrequests.StateCancelled, state)
	require.True(t, resultIsNull, "cancelled work must not retain a late result")

	completedWriteID := create("query")
	completedWrite, ok, err := repo.Claim(ctx, pool, completedWriteID)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = repo.Cancel(ctx, pool, completedWriteID)
	require.NoError(t, err)
	require.NoError(t, repo.Complete(ctx, pool, completedWriteID, completedWrite.ClaimToken, []byte("encrypted success"), true))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT state, result_ciphertext IS NULL
		FROM execution_requests
		WHERE id = $1
	`, completedWriteID).Scan(&state, &resultIsNull))
	require.Equal(t, executionrequests.StateSucceeded, state, "a completed side effect must not be erased by a racing cancellation")
	require.False(t, resultIsNull, "the caller needs the durable success result to avoid a duplicate retry")

	ambiguousWriteID := create("git_write")
	ambiguousWrite, ok, err := repo.Claim(ctx, pool, ambiguousWriteID)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = repo.Cancel(ctx, pool, ambiguousWriteID)
	require.NoError(t, err)
	require.NoError(t, repo.Complete(ctx, pool, ambiguousWriteID, ambiguousWrite.ClaimToken, []byte("cancelled failure"), false))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT state, result_ciphertext IS NULL
		FROM execution_requests
		WHERE id = $1
	`, ambiguousWriteID).Scan(&state, &resultIsNull))
	require.Equal(t, executionrequests.StateAmbiguous, state, "in-flight cancellation cannot prove a side effect did not happen")
	require.True(t, resultIsNull, "ambiguous work must not expose an ordinary retryable result")

	staleID := create("connection_test")
	_, ok, err = repo.Claim(ctx, pool, staleID)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = repo.Cancel(ctx, pool, staleID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET lease_expires_at = NOW() - INTERVAL '1 second' WHERE id = $1`, staleID)
	require.NoError(t, err)
	nextID := create("transform")
	claimed, ok, err = repo.ClaimNext(ctx, pool)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, staleID, claimed.ID)
	require.Equal(t, executionrequests.StateCancelled, claimed.State)
	claimed, ok, err = repo.ClaimNext(ctx, pool)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, nextID, claimed.ID, "terminalized stale work must not starve later pending work")
	require.Equal(t, executionrequests.StateRunning, claimed.State)
}

func TestProofCompletionRejectsStaleSuccess(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres Proof completion test in short mode")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "Proof completion capability integration test")
	repo := executionrequests.New()
	org := slug.Slug(fmt.Sprintf("proof-completion-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
		require.NoError(t, cleanupErr)
	})

	tests := map[string]string{
		"cancelled":     `UPDATE execution_requests SET cancel_requested_at = clock_timestamp() WHERE id = $1`,
		"deadline":      `UPDATE execution_requests SET deadline_at = clock_timestamp() - INTERVAL '1 second' WHERE id = $1`,
		"expired lease": `UPDATE execution_requests SET lease_expires_at = clock_timestamp() - INTERVAL '1 second' WHERE id = $1`,
	}
	for name, expire := range tests {
		t.Run(name, func(t *testing.T) {
			id := uuid.New()
			require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{
				ID: id, OrganizationSlug: org.String(), Kind: "proof_metadata",
				RequestCiphertext: []byte("encrypted"), DeadlineAt: time.Now().Add(time.Minute),
			}, time.Now().Add(time.Hour)))
			claim, claimed, err := repo.Claim(ctx, pool, id)
			require.NoError(t, err)
			require.True(t, claimed)
			_, err = pool.Exec(ctx, expire, id)
			require.NoError(t, err)

			require.NoError(t, repo.Complete(ctx, pool, id, claim.ClaimToken, []byte("late success"), true))
			var state executionrequests.State
			var resultIsNull bool
			require.NoError(t, pool.QueryRow(ctx, `
				SELECT state, result_ciphertext IS NULL
				FROM execution_requests
				WHERE id = $1
			`, id).Scan(&state, &resultIsNull))
			require.Equal(t, executionrequests.StateAmbiguous, state)
			require.True(t, resultIsNull)
		})
	}
}

func TestProofCompletionUsesPostLockDatabaseTime(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres Proof completion lock test in short mode")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool := e2edb.OpenPostgresPool(t, "Proof completion lock integration test")
	repo := executionrequests.New()
	org := slug.Slug(fmt.Sprintf("proof-completion-lock-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
		require.NoError(t, cleanupErr)
	})

	id := uuid.New()
	require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{
		ID: id, OrganizationSlug: org.String(), Kind: "proof_metadata",
		RequestCiphertext: []byte("encrypted"), DeadlineAt: time.Now().Add(time.Minute),
	}, time.Now().Add(time.Hour)))
	claim, claimed, err := repo.Claim(ctx, pool, id)
	require.NoError(t, err)
	require.True(t, claimed)

	locker, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = locker.Rollback(context.Background()) })
	_, err = locker.Exec(ctx, `SELECT 1 FROM execution_requests WHERE id = $1 FOR UPDATE`, id)
	require.NoError(t, err)

	completer, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer func() {
		cancel()
		_ = locker.Rollback(context.Background())
		completer.Release()
	}()
	completePID := completer.Conn().PgConn().PID()
	completeErr := make(chan error, 1)
	go func() {
		completeErr <- repo.Complete(ctx, completer, id, claim.ClaimToken, []byte("late success"), true)
	}()
	require.Eventually(t, func() bool {
		var blocked bool
		queryErr := locker.QueryRow(ctx, `
			SELECT wait_event_type = 'Lock'
			FROM pg_stat_activity
			WHERE pid = $1
		`, completePID).Scan(&blocked)
		return queryErr == nil && blocked
	}, 5*time.Second, 10*time.Millisecond)

	_, err = locker.Exec(ctx, `
		UPDATE execution_requests
		SET deadline_at = clock_timestamp() - INTERVAL '1 millisecond'
		WHERE id = $1
	`, id)
	require.NoError(t, err)
	require.NoError(t, locker.Commit(ctx))
	require.NoError(t, <-completeErr)

	var state executionrequests.State
	var resultIsNull bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT state, result_ciphertext IS NULL
		FROM execution_requests
		WHERE id = $1
	`, id).Scan(&state, &resultIsNull))
	require.Equal(t, executionrequests.StateAmbiguous, state)
	require.True(t, resultIsNull)
}

func TestExecutionRequestRiverDeliverySnapshot(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres River delivery test in short mode")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "execution request River delivery integration test")
	repo := executionrequests.New()
	org := slug.Slug(fmt.Sprintf("execution-river-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(
			context.Background(),
			`DELETE FROM organizations WHERE slug = $1`,
			org,
		)
		require.NoError(t, cleanupErr)
	})

	id := uuid.New()
	require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{
		ID: id, OrganizationSlug: org.String(), Kind: "transform",
		RequestCiphertext: []byte("encrypted"), DeadlineAt: time.Now().Add(time.Minute),
	}, time.Now().Add(time.Hour)))
	_, claimed, err := repo.Claim(ctx, pool, id)
	require.NoError(t, err)
	require.True(t, claimed)

	snapshot, found, err := repo.FindForDelivery(ctx, pool, id)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, id, snapshot.Request.ID)
	require.Equal(t, executionrequests.StateRunning, snapshot.Request.State)
	require.NotNil(t, snapshot.Request.LeaseExpiresAt)
	require.True(t, snapshot.Request.LeaseExpiresAt.After(snapshot.ObservedAt))

	locker, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = locker.Rollback(context.Background()) })
	_, err = locker.Exec(ctx, `SELECT 1 FROM execution_requests WHERE id = $1 FOR UPDATE`, id)
	require.NoError(t, err)

	type snapshotResult struct {
		snapshot executionrequests.DeliverySnapshot
		found    bool
		err      error
	}
	transactionStarted := make(chan struct{})
	resultCh := make(chan snapshotResult, 1)
	go func() {
		tx, beginErr := pool.Begin(ctx)
		close(transactionStarted)
		if beginErr != nil {
			resultCh <- snapshotResult{err: beginErr}
			return
		}
		defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(ctx)

		lockedSnapshot, lockedFound, findErr := repo.FindForDelivery(ctx, tx, id)
		if findErr == nil {
			findErr = tx.Commit(ctx)
		}
		resultCh <- snapshotResult{
			snapshot: lockedSnapshot,
			found:    lockedFound,
			err:      findErr,
		}
	}()
	<-transactionStarted

	var lockReleasedAt time.Time
	require.NoError(t, locker.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&lockReleasedAt))
	require.NoError(t, locker.Commit(ctx))

	lockedResult := <-resultCh
	require.NoError(t, lockedResult.err)
	require.True(t, lockedResult.found)
	require.False(
		t,
		lockedResult.snapshot.ObservedAt.Before(lockReleasedAt),
		"delivery observation must use the post-lock database clock",
	)

	_, found, err = repo.FindForDelivery(ctx, pool, uuid.New())
	require.NoError(t, err)
	require.False(t, found)
}

func TestExecutionRequestDeleteExpiredEnvelopes(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres execution request expiry test in short mode")
	}
	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "execution request expiry integration test")
	repo := executionrequests.New()
	org := slug.Slug(fmt.Sprintf("execution-expiry-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
		require.NoError(t, cleanupErr)
	})

	expiredID := uuid.New()
	futureID := uuid.New()
	for _, testCase := range []struct {
		id        uuid.UUID
		expiresAt time.Time
	}{
		{id: expiredID, expiresAt: time.Now().Add(-time.Minute)},
		{id: futureID, expiresAt: time.Now().Add(time.Hour)},
	} {
		require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{
			ID: testCase.id, OrganizationSlug: org.String(), Kind: "transform",
			RequestCiphertext: []byte("encrypted"), DeadlineAt: time.Now().Add(time.Minute),
		}, testCase.expiresAt))
	}

	deleted, err := repo.DeleteExpired(ctx, pool)
	require.NoError(t, err)
	// Maintenance also expires rows left by other fixtures in this shared database.
	require.GreaterOrEqual(t, deleted, int64(1))

	var expiredCount, futureCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM execution_requests WHERE id = $1`, expiredID).Scan(&expiredCount))
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM execution_requests WHERE id = $1`, futureID).Scan(&futureCount))
	require.Zero(t, expiredCount)
	require.Equal(t, 1, futureCount)
}

func TestExecutionExpiryPreservesUnresolvedAccountProcessing(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres execution expiry test in short mode")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "account processing expiry")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback(context.Background())) }()
	name := slug.Slug(fmt.Sprintf("processing-expiry-%d", time.Now().UnixNano()))
	_, err = tx.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, name)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO users (slug, email, role) VALUES ($1, $2, 'user')`, name, name.String()+"@example.com")
	require.NoError(t, err)
	repo := executionrequests.New()
	for _, tc := range []struct {
		state   string
		started bool
		retain  bool
		kind    string
	}{
		{"pending", false, false, "query"},
		{"cancelled", false, false, "query"},
		{"succeeded", true, false, "query"},
		{"failed", true, false, "query"},
		{"running", true, true, "query"},
		{"ambiguous", true, true, "query"},
		{"cancelled", true, true, "query"},
		{"running", true, true, "transform"},
	} {
		id := uuid.New()
		require.NoError(t, repo.Create(ctx, tx, executionrequests.Request{
			ID: id, OrganizationSlug: name.String(), Kind: tc.kind, RequestCiphertext: []byte("request"), DeadlineAt: time.Now().Add(time.Minute),
		}, time.Now().Add(-time.Minute)))
		require.NoError(t, repo.BindUser(ctx, tx, id, name.String(), name))
		_, err = tx.Exec(ctx, `UPDATE execution_requests SET state = $2,
			started_at = CASE WHEN $3 THEN clock_timestamp() ELSE NULL END,
			lease_expires_at = clock_timestamp() + INTERVAL '30 seconds',
			result_ciphertext = 'result'::bytea WHERE id = $1`, id, tc.state, tc.started)
		require.NoError(t, err)
		_, err = repo.DeleteExpired(ctx, tx)
		require.NoError(t, err)
		var exists bool
		require.NoError(t, tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM execution_requests WHERE id = $1)`, id).Scan(&exists))
		require.Equal(t, tc.retain, exists, "state=%s started=%t", tc.state, tc.started)
		if !tc.retain {
			continue
		}
		var state string
		var payloadCleared, attributed, cancellationRequested bool
		var updatedAt time.Time
		require.NoError(t, tx.QueryRow(ctx, `SELECT r.state,
			octet_length(r.request_ciphertext) = 0 AND r.result_ciphertext IS NULL,
			u.slug = $2 AND o.slug = $2, r.cancel_requested_at IS NOT NULL, r.updated_at
			FROM execution_requests r JOIN users u ON u.id = r.initiating_user_id
			JOIN organizations o ON o.id = r.organization_id WHERE r.id = $1`, id, name).
			Scan(&state, &payloadCleared, &attributed, &cancellationRequested, &updatedAt))
		require.Equal(t, tc.state, state)
		require.True(t, payloadCleared)
		require.True(t, attributed)
		require.True(t, cancellationRequested)
		_, err = repo.DeleteExpired(ctx, tx)
		require.NoError(t, err)
		var retriedAt time.Time
		require.NoError(t, tx.QueryRow(ctx, `SELECT updated_at FROM execution_requests WHERE id = $1`, id).Scan(&retriedAt))
		require.Equal(t, updatedAt, retriedAt, "scrub retries must not rewrite retained metadata")
		if tc.kind == "transform" {
			_, err = tx.Exec(ctx, `UPDATE execution_requests SET lease_expires_at = clock_timestamp() - INTERVAL '1 second' WHERE id = $1`, id)
			require.NoError(t, err)
			claimed, found, err := repo.Claim(ctx, tx, id)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionrequests.StateCancelled, claimed.State, "expired payload must never be replayed")
			_, err = repo.DeleteExpired(ctx, tx)
			require.NoError(t, err)
			require.NoError(t, tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM execution_requests WHERE id = $1)`, id).Scan(&exists))
			require.True(t, exists, "lease cancellation is not executor acknowledgement")
		} else if tc.state == "running" {
			var claimToken uuid.UUID
			require.NoError(t, tx.QueryRow(ctx, `SELECT claim_token FROM execution_requests WHERE id=$1`, id).Scan(&claimToken))
			require.NoError(t, repo.Complete(ctx, tx, id, claimToken, []byte("late result"), true))
			require.NoError(t, tx.QueryRow(ctx, `SELECT result_ciphertext IS NULL FROM execution_requests WHERE id = $1`, id).Scan(&payloadCleared))
			require.True(t, payloadCleared, "late completion must not restore expired results before the next retention sweep")
			_, err = repo.DeleteExpired(ctx, tx)
			require.NoError(t, err)
			require.NoError(t, tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM execution_requests WHERE id = $1)`, id).Scan(&exists))
			require.False(t, exists, "acknowledged completion can expire")
		}
	}
}

func TestExecutionCompletionHonorsPayloadExpiryWithoutSweep(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres execution completion expiry test in short mode")
	}
	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "execution completion payload expiry")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback(context.Background())) }()
	org := slug.Slug(fmt.Sprintf("completion-expiry-%d", time.Now().UnixNano()))
	_, err = tx.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, org)
	require.NoError(t, err)
	repo := executionrequests.New()
	for _, kind := range []string{"query", "transform", "proof_metadata"} {
		for _, succeeded := range []bool{false, true} {
			for _, expired := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/succeeded=%t/expired=%t", kind, succeeded, expired), func(t *testing.T) {
					id := uuid.New()
					require.NoError(t, repo.Create(ctx, tx, executionrequests.Request{
						ID: id, OrganizationSlug: org.String(), Kind: kind,
						RequestCiphertext: []byte("request"), DeadlineAt: time.Now().Add(time.Minute),
					}, time.Now().Add(time.Hour)))
					claim, claimed, err := repo.Claim(ctx, tx, id)
					require.NoError(t, err)
					require.True(t, claimed)
					if expired {
						// Expiry after transaction start catches use of NOW() instead of wall time.
						var afterTransactionStart bool
						require.NoError(t, tx.QueryRow(ctx, `UPDATE execution_requests
							SET expires_at = clock_timestamp() WHERE id = $1
							RETURNING expires_at > CURRENT_TIMESTAMP`, id).Scan(&afterTransactionStart))
						require.True(t, afterTransactionStart)
					}
					require.NoError(t, repo.Complete(ctx, tx, id, claim.ClaimToken, []byte("result"), succeeded))
					var state executionrequests.State
					var result []byte
					require.NoError(t, tx.QueryRow(ctx, `SELECT state, result_ciphertext
						FROM execution_requests WHERE id = $1`, id).Scan(&state, &result))
					if succeeded {
						require.Equal(t, executionrequests.StateSucceeded, state)
					} else {
						require.Equal(t, executionrequests.StateFailed, state)
					}
					if expired {
						require.Nil(t, result, "expiry must apply even before the retention worker runs")
					} else {
						require.Equal(t, []byte("result"), result)
					}
				})
			}
		}
	}
}

func TestExecutionRequestClaimNextScalesAcrossConcurrentPollers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-Postgres execution request concurrency test in short mode")
	}
	ctx := context.Background()
	pool := executionClaimPool(t)
	org := slug.Slug(fmt.Sprintf("execution-scale-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
		require.NoError(t, cleanupErr)
	})

	const requestCount = 24
	for range requestCount {
		require.NoError(t, executionrequests.New().Create(ctx, pool, executionrequests.Request{
			ID: uuid.New(), OrganizationSlug: org.String(), Kind: "transform",
			RequestCiphertext: []byte("encrypted"), DeadlineAt: time.Now().Add(time.Minute),
		}, time.Now().Add(time.Hour)))
	}

	claimedIDs := make(chan uuid.UUID, requestCount)
	errors := make(chan error, requestCount)
	var wg sync.WaitGroup
	for range requestCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, ok, claimErr := executionrequests.New().ClaimNext(ctx, pool)
			if claimErr != nil {
				errors <- claimErr
				return
			}
			if !ok {
				errors <- fmt.Errorf("no request was available")
				return
			}
			claimedIDs <- claimed.ID
		}()
	}
	wg.Wait()
	close(errors)
	close(claimedIDs)

	for claimErr := range errors {
		require.NoError(t, claimErr)
	}
	unique := make(map[uuid.UUID]struct{}, requestCount)
	for id := range claimedIDs {
		unique[id] = struct{}{}
	}
	require.Len(t, unique, requestCount, "concurrent pollers must claim each envelope exactly once")
}

func TestExecutionRequestClaimNextSkipsSaturatedHeadsBeyondPollLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-Postgres execution request admission test in short mode")
	}

	ctx := context.Background()
	pool := executionClaimPool(t)
	org := slug.Slug(fmt.Sprintf("execution-saturated-%d", time.Now().UnixNano()))
	var organizationID int
	require.NoError(t, pool.QueryRow(
		ctx,
		`INSERT INTO organizations (slug, name) VALUES ($1, $1) RETURNING id`,
		org,
	).Scan(&organizationID))
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = $1`, organizationID)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, organizationID)
		require.NoError(t, cleanupErr)
	})

	var credentialID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO credentials (
			organization_id, slug, kind, credential_type, status, encrypted_payload
		) VALUES ($1, 'saturated-credential', 'static_secret', 'postgres', 'active', '\x01')
		RETURNING id
	`, organizationID).Scan(&credentialID))

	repo := executionrequests.New()
	create := func(t *testing.T, dataSourceSlug string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		deadline := time.Now().Add(10 * time.Minute)
		require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{
			ID:                id,
			OrganizationSlug:  org.String(),
			DataSourceSlug:    dataSourceSlug,
			Kind:              "query",
			RequestCiphertext: []byte("encrypted"),
			DeadlineAt:        deadline,
			QueueDeadlineAt:   &deadline,
			ConcurrencyLimit:  1,
			QueueCapacity:     1,
		}, deadline.Add(time.Hour)))
		return id
	}

	const saturatedSourceCount = 51
	for i := range saturatedSourceCount {
		dataSourceSlug := fmt.Sprintf("saturated-source-%02d", i)
		_, err := pool.Exec(ctx, `
			INSERT INTO data_sources (
				organization_id, credential_id, name, slug, type, config, is_active, read_only
			) VALUES ($1, $2, $3, $3, 'postgres', '{}', true, true)
		`, organizationID, credentialID, dataSourceSlug)
		require.NoError(t, err)

		runningID := create(t, dataSourceSlug)
		_, claimed, err := repo.Claim(ctx, pool, runningID)
		require.NoError(t, err)
		require.True(t, claimed)
		_ = create(t, dataSourceSlug)
	}

	_, err := pool.Exec(ctx, `
		UPDATE execution_requests
		SET created_at = TIMESTAMPTZ '2000-01-01 00:00:00+00'
		WHERE organization_id = $1 AND state = 'pending'
	`, organizationID)
	require.NoError(t, err)

	const eligibleDataSourceSlug = "eligible-source"
	_, err = pool.Exec(ctx, `
		INSERT INTO data_sources (
			organization_id, credential_id, name, slug, type, config, is_active, read_only
		) VALUES ($1, $2, $3, $3, 'postgres', '{}', true, true)
	`, organizationID, credentialID, eligibleDataSourceSlug)
	require.NoError(t, err)
	eligibleID := create(t, eligibleDataSourceSlug)
	_, err = pool.Exec(ctx, `
		UPDATE execution_requests
		SET created_at = TIMESTAMPTZ '2000-01-02 00:00:00+00'
		WHERE id = $1 AND organization_id = $2
	`, eligibleID, organizationID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		UPDATE execution_requests
		SET lease_expires_at = LEAST(
			deadline_at,
			clock_timestamp() + INTERVAL '5 minutes'
		)
		WHERE organization_id = $1 AND state = 'running'
	`, organizationID)
	require.NoError(t, err)

	claimed, ok, err := repo.ClaimNext(ctx, pool)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(
		t,
		eligibleID,
		claimed.ID,
		"more than one poll batch of older saturated heads must not starve eligible work",
	)
}

func TestExecutionRequestDistributedDataSourceAdmission(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres distributed admission test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "distributed execution admission")
	secondPool, err := pgxpool.New(ctx, pool.Config().ConnString())
	require.NoError(t, err)
	t.Cleanup(secondPool.Close)

	org := slug.Slug(fmt.Sprintf("execution-admission-%d", time.Now().UnixNano()))
	var organizationID int
	require.NoError(t, pool.QueryRow(
		ctx,
		`INSERT INTO organizations (slug, name) VALUES ($1, $1) RETURNING id`,
		org,
	).Scan(&organizationID))
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = $1`, organizationID)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, organizationID)
		require.NoError(t, cleanupErr)
	})

	var credentialID int64
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO credentials (
			organization_id, slug, kind, credential_type, status, encrypted_payload
		) VALUES ($1, 'admission-credential', 'static_secret', 'postgres', 'active', '\x01')
		RETURNING id
	`, organizationID).Scan(&credentialID))
	_, err = pool.Exec(ctx, `
		INSERT INTO data_sources (
			organization_id, credential_id, name, slug, type, config, is_active, read_only
		) VALUES
			($1, $2, 'Admission source', 'admission-source', 'postgres', '{}', true, true),
			($1, $2, 'Contention source', 'contention-source', 'postgres', '{}', true, true)
	`, organizationID, credentialID)
	require.NoError(t, err)

	repo := executionrequests.New()
	createFor := func(
		t *testing.T,
		dataSourceSlug string,
		deadline time.Time,
		concurrencyLimit,
		queueCapacity int,
	) uuid.UUID {
		t.Helper()
		id := uuid.New()
		queueDeadline := deadline
		require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{
			ID:                id,
			OrganizationSlug:  org.String(),
			DataSourceSlug:    dataSourceSlug,
			Kind:              "query",
			RequestCiphertext: []byte("encrypted"),
			DeadlineAt:        deadline,
			QueueDeadlineAt:   &queueDeadline,
			ConcurrencyLimit:  concurrencyLimit,
			QueueCapacity:     queueCapacity,
		}, deadline.Add(time.Hour)))
		return id
	}
	create := func(t *testing.T, deadline time.Time, concurrencyLimit, queueCapacity int) uuid.UUID {
		t.Helper()
		return createFor(t, "admission-source", deadline, concurrencyLimit, queueCapacity)
	}

	const (
		contentionRequestCount = 8
		contentionLimit        = 3
	)
	contentionIDs := make([]uuid.UUID, 0, contentionRequestCount)
	for range contentionRequestCount {
		contentionIDs = append(
			contentionIDs,
			createFor(t, "contention-source", time.Now().Add(time.Minute), contentionLimit, contentionRequestCount),
		)
	}

	start := make(chan struct{})
	claimedIDs := make(chan executionrequests.Request, contentionRequestCount)
	claimErrors := make(chan error, contentionRequestCount)
	var claimers sync.WaitGroup
	var admitted atomic.Int32
	claimCtx, cancelClaims := context.WithTimeout(ctx, time.Second)
	defer cancelClaims()
	for i, id := range contentionIDs {
		claimers.Add(1)
		go func(db *pgxpool.Pool, requestID uuid.UUID) {
			defer claimers.Done()
			<-start
			for admitted.Load() < contentionLimit {
				claimed, ok, claimErr := repo.Claim(claimCtx, db, requestID)
				if claimErr != nil {
					claimErrors <- claimErr
					return
				}
				if ok {
					claimedIDs <- claimed
					admitted.Add(1)
					return
				}
				select {
				case <-claimCtx.Done():
					claimErrors <- claimCtx.Err()
					return
				case <-time.After(time.Millisecond):
				}
			}
		}([]*pgxpool.Pool{pool, secondPool}[i%2], id)
	}
	close(start)
	claimers.Wait()
	close(claimErrors)
	close(claimedIDs)

	for claimErr := range claimErrors {
		require.NoError(t, claimErr)
	}
	claimedSet := make(map[uuid.UUID]uuid.UUID, contentionLimit)
	for claim := range claimedIDs {
		claimedSet[claim.ID] = claim.ClaimToken
	}
	require.Len(t, claimedSet, contentionLimit, "replicas must admit exactly the aggregate configured limit")

	var runningCount, pendingCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE request.state = 'running'),
			COUNT(*) FILTER (WHERE request.state = 'pending')
		FROM execution_requests AS request
		JOIN data_sources AS data_source ON data_source.id = request.data_source_id
		WHERE data_source.organization_id = $1
		  AND data_source.slug = 'contention-source'
	`, organizationID).Scan(&runningCount, &pendingCount))
	require.Equal(t, contentionLimit, runningCount)
	require.Equal(t, contentionRequestCount-contentionLimit, pendingCount)

	for _, id := range contentionIDs {
		if token, claimed := claimedSet[id]; claimed {
			require.NoError(t, repo.Complete(ctx, pool, id, token, []byte("done"), true))
			continue
		}
		cancelled, cancelErr := repo.Cancel(ctx, pool, id)
		require.NoError(t, cancelErr)
		require.True(t, cancelled)
	}

	firstID := create(t, time.Now().Add(time.Minute), 1, 3)
	secondID := create(t, time.Now().Add(time.Minute), 1, 3)

	_, claimed, err := repo.Claim(ctx, pool, secondID)
	require.NoError(t, err)
	require.False(t, claimed, "later work must not overtake the data-source FIFO head")

	first, claimed, err := repo.Claim(ctx, pool, firstID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, executionrequests.StateRunning, first.State)

	_, claimed, err = repo.Claim(ctx, secondPool, secondID)
	require.NoError(t, err)
	require.False(t, claimed, "a second replica must observe the aggregate running limit")

	require.NoError(t, secondPool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE state = 'running'),
			COUNT(*) FILTER (WHERE state = 'pending')
		FROM execution_requests
		WHERE id IN ($1, $2)
	`, firstID, secondID).Scan(&runningCount, &pendingCount))
	require.Equal(t, 1, runningCount)
	require.Equal(t, 1, pendingCount)

	require.NoError(t, repo.Complete(ctx, pool, firstID, first.ClaimToken, []byte("done"), true))
	second, claimed, err := repo.Claim(ctx, secondPool, secondID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, executionrequests.StateRunning, second.State)

	queuedID := create(t, time.Now().Add(time.Minute), 1, 3)
	queueFullID := uuid.New()
	queueFullDeadline := time.Now().Add(time.Minute)
	err = repo.Create(ctx, secondPool, executionrequests.Request{
		ID:                queueFullID,
		OrganizationSlug:  org.String(),
		DataSourceSlug:    "admission-source",
		Kind:              "query",
		RequestCiphertext: []byte("encrypted"),
		DeadlineAt:        queueFullDeadline,
		QueueDeadlineAt:   &queueFullDeadline,
		ConcurrencyLimit:  1,
		QueueCapacity:     1,
	}, time.Now().Add(time.Hour))
	require.Error(t, err)
	require.True(t, xerror.Is(err, xerror.ExecutionConcurrencyLimitExceeded()), "got %v", err)

	expiredID := create(t, time.Now().Add(-time.Second), 1, 3)
	expired, claimed, err := repo.Claim(ctx, secondPool, expiredID)
	require.NoError(t, err)
	require.True(t, claimed, "expired pending work must be returned as a terminal claim")
	require.Equal(t, executionrequests.StateCancelled, expired.State)

	queueTimedOutID := uuid.New()
	requestDeadline := time.Now().Add(time.Minute)
	queueDeadline := time.Now().Add(-time.Second)
	require.NoError(t, repo.Create(ctx, secondPool, executionrequests.Request{
		ID:                queueTimedOutID,
		OrganizationSlug:  org.String(),
		DataSourceSlug:    "admission-source",
		Kind:              "query",
		RequestCiphertext: []byte("encrypted"),
		DeadlineAt:        requestDeadline,
		QueueDeadlineAt:   &queueDeadline,
		ConcurrencyLimit:  1,
		QueueCapacity:     3,
	}, requestDeadline.Add(time.Hour)))
	queueTimedOut, claimed, err := repo.Claim(ctx, secondPool, queueTimedOutID)
	require.NoError(t, err)
	require.True(t, claimed, "queue timeout must terminalize before request deadline")
	require.Equal(t, executionrequests.StateCancelled, queueTimedOut.State)

	_, err = pool.Exec(
		ctx,
		`UPDATE execution_requests
		 SET lease_expires_at = clock_timestamp() - INTERVAL '1 second'
		 WHERE id = $1`,
		secondID,
	)
	require.NoError(t, err)
	stale, claimed, err := repo.Claim(ctx, secondPool, secondID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(
		t,
		executionrequests.StateAmbiguous,
		stale.State,
		"stale running query must bypass pending admission and preserve ambiguity recovery",
	)

	queued, claimed, err := repo.Claim(ctx, secondPool, queuedID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, executionrequests.StateRunning, queued.State)
}

func TestDispatchAuthorityRejectsRevocationAndDrift(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := t.Context()
	pool := executionClaimPool(t)
	org := slug.Slug("dispatch-authority")
	var organization int
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO organizations(slug,name) VALUES($1,$1) RETURNING id`, org).Scan(&organization))
	var credential int64
	var revision time.Time
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload) VALUES($1,'secret','static_secret','postgres','active','\x01') RETURNING id,updated_at`, organization).Scan(&credential, &revision))
	_, err := pool.Exec(ctx, `INSERT INTO data_sources(organization_id,credential_id,slug,name,type,config,is_active,read_only) VALUES($1,$2,'db','DB','postgres','{"host":"original"}',true,false)`, organization, credential)
	require.NoError(t, err)
	repo := executionrequests.New()
	id := uuid.New()
	deadline := time.Now().Add(time.Minute)
	require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{ID: id, OrganizationSlug: org.String(), DataSourceSlug: "db", Kind: "query", RequestCiphertext: []byte("encrypted"), DeadlineAt: deadline, QueueDeadlineAt: &deadline, ConcurrencyLimit: 1, QueueCapacity: 1}, deadline.Add(time.Hour)))
	claim, claimed, err := repo.Claim(ctx, pool, id)
	require.NoError(t, err)
	require.True(t, claimed)
	name := "secret"
	expected := executionrequests.DispatchAuthority{RequestID: id, ClaimToken: claim.ClaimToken, Organization: org.String(), DataSource: "db", Type: "postgres", Config: []byte(`{"host":"original"}`), CredentialSlug: &name, CredentialUpdatedAt: &revision}
	check := func(want bool) {
		t.Helper()
		got, err := repo.CheckDispatch(ctx, pool, expected)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	check(true)
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET kind='prepare_s3_write' WHERE id=$1`, id)
	require.NoError(t, err)
	check(false)
	_, err = pool.Exec(ctx, `UPDATE data_sources SET type='s3' WHERE organization_id=$1`, organization)
	require.NoError(t, err)
	expected.Type = "s3"
	check(true)
	expected.Organization = "other"
	check(false)
	expected.Organization = org.String()
	_, err = pool.Exec(ctx, `UPDATE credentials SET status='revoked' WHERE id=$1`, credential)
	require.NoError(t, err)
	check(false)
	_, err = pool.Exec(ctx, `UPDATE credentials SET status='active' WHERE id=$1`, credential)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET kind='query' WHERE id=$1`, id)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE data_sources SET type='postgres' WHERE organization_id=$1`, organization)
	require.NoError(t, err)
	expected.Type = "postgres"
	check(true)
	expected.Config = nil // Flat requests must retain the admitted credential too.
	expected.CredentialSlug = nil
	check(false)
	expected.CredentialSlug = &name
	check(true)
	expected.Config = []byte(`{"host":"original"}`)
	role := pgx.Identifier{fmt.Sprintf("dispatch_executor_%d", time.Now().UnixNano())}.Sanitize()
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
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "SET LOCAL ROLE "+role)
	require.NoError(t, err)
	permitted, err := repo.CheckDispatch(ctx, tx, expected)
	require.NoError(t, err)
	require.True(t, permitted)
	var tenantRead, credentialRead bool
	require.NoError(t, tx.QueryRow(ctx, `SELECT has_table_privilege(current_user,'public.data_sources','SELECT'),has_table_privilege(current_user,'public.credentials','SELECT')`).Scan(&tenantRead, &credentialRead))
	require.False(t, tenantRead)
	require.False(t, credentialRead)
	require.NoError(t, tx.Rollback(ctx))

	expected.Organization = "other"
	check(false)
	expected.Organization = org.String()
	expected.RequestID = uuid.New()
	check(false)
	expected.RequestID = id
	_, err = pool.Exec(ctx, `UPDATE data_sources SET config='{"host":"replacement"}' WHERE slug='db'`)
	require.NoError(t, err)
	check(false)
	_, err = pool.Exec(ctx, `UPDATE data_sources SET config='{"host":"original"}',read_only=true WHERE slug='db'`)
	require.NoError(t, err)
	check(false)
	expected.ReadOnly = true
	check(true)
	_, err = pool.Exec(ctx, `UPDATE data_sources SET is_active=false WHERE slug='db'`)
	require.NoError(t, err)
	check(false)
	_, err = pool.Exec(ctx, `UPDATE data_sources SET is_active=true WHERE slug='db'`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE credentials SET status='revoked' WHERE id=$1`, credential)
	require.NoError(t, err)
	check(false)
	var rotated time.Time
	require.NoError(t, pool.QueryRow(ctx, `UPDATE credentials SET status='active',updated_at=updated_at+INTERVAL '1 second' WHERE id=$1 RETURNING updated_at`, credential).Scan(&rotated))
	check(false)
	expected.Config = nil
	check(false)
	expected.CredentialUpdatedAt = &rotated
	check(true)
	var replacement int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload) VALUES($1,'replacement','static_secret','postgres','active','\x01') RETURNING id`, organization).Scan(&replacement))
	_, err = pool.Exec(ctx, `UPDATE data_sources SET credential_id=$1 WHERE organization_id=$2`, replacement, organization)
	require.NoError(t, err)
	check(false)
	_, err = pool.Exec(ctx, `UPDATE data_sources SET credential_id=$1 WHERE organization_id=$2`, credential, organization)
	require.NoError(t, err)
	check(true)
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET cancel_requested_at=NOW() WHERE id=$1`, id)
	require.NoError(t, err)
	check(false)
}
