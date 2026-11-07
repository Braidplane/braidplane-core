package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/postgres/executionrequests"
)

func TestOrganizationFileAndConnectorAdmission(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := t.Context()
	pool := executionClaimPool(t)
	second, err := pgxpool.NewWithConfig(ctx, pool.Config().Copy())
	require.NoError(t, err)
	t.Cleanup(second.Close)
	repo := executionrequests.New()
	for _, org := range []string{"fixture", "other"} {
		_, err = pool.Exec(ctx, `WITH org AS (INSERT INTO organizations(slug,name) VALUES($1,$1) RETURNING id), cred AS (INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload) SELECT id,'credential','static_secret','postgres','active','\x01' FROM org RETURNING id,organization_id) INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active,read_only) SELECT organization_id,id,'Source '||n,'source-'||n,'postgres','{}',true,true FROM cred CROSS JOIN generate_series(1,70) n`, org)
		require.NoError(t, err)
	}
	transferDeadline := time.Now().Add(time.Minute)
	createScoped := func(org string, source int, file bool, connectors, files int, scope uuid.UUID) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if scope == uuid.Nil {
			scope = id
		}
		deadline := transferDeadline
		require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{ID: id, OrganizationSlug: org, DataSourceSlug: fmt.Sprintf("source-%d", source), Kind: "query", RequestCiphertext: []byte("encrypted"), DeadlineAt: deadline, QueueDeadlineAt: &deadline, ConcurrencyLimit: 12, QueueCapacity: 12}, deadline.Add(time.Hour)))
		require.NoError(t, repo.BindOrganizationLimits(ctx, pool, id, org, connectors, files, file, scope, deadline))
		return id
	}
	create := func(org string, source int, file bool, connectors, files int) uuid.UUID {
		return createScoped(org, source, file, connectors, files, uuid.Nil)
	}
	release := func(id uuid.UUID, org string) {
		t.Helper()
		require.NoError(t, repo.ReleaseFileTransfer(ctx, pool, id, org))
		ids, err := repo.ListFileTransferRequests(ctx, pool, id, org)
		require.NoError(t, err)
		for _, requestID := range ids {
			_, err := repo.Cancel(ctx, pool, requestID)
			require.NoError(t, err)
		}
	}
	claim := func(id uuid.UUID, want bool) executionrequests.Request {
		t.Helper()
		request, ok, err := repo.Claim(ctx, second, id)
		require.NoError(t, err)
		require.Equal(t, want, ok)
		return request
	}
	first := claim(create("fixture", 1, true, 2, 1), true)
	nextFile := create("fixture", 2, true, 2, 1)
	claim(nextFile, false)
	ordinary := claim(create("fixture", 3, false, 2, 1), true)
	waiting := create("fixture", 4, false, 2, 1)
	claim(waiting, false)
	other := claim(create("other", 1, true, 2, 1), true)
	require.NoError(t, repo.Complete(ctx, pool, other.ID, other.ClaimToken, []byte("done"), true))
	release(other.ID, "other")
	cancelled, err := repo.Cancel(ctx, pool, first.ID)
	require.NoError(t, err)
	require.False(t, cancelled, "a running request still needs provider cancellation")
	claim(nextFile, false) // Cancellation must not free a still-running provider's slot.
	require.NoError(t, repo.Complete(ctx, pool, first.ID, first.ClaimToken, []byte("cancelled"), false))
	release(first.ID, "fixture")
	claim(nextFile, false) // Cancelled writes remain ambiguous until their cleanup allowance expires.
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET lease_expires_at=clock_timestamp()-interval '11 seconds' WHERE id=$1`, first.ID)
	require.NoError(t, err)
	next := claim(nextFile, true)
	claim(waiting, false)
	require.NoError(t, repo.Complete(ctx, pool, ordinary.ID, ordinary.ClaimToken, []byte("done"), true))
	admitted := claim(waiting, true)
	require.NoError(t, repo.Complete(ctx, pool, admitted.ID, admitted.ClaimToken, []byte("done"), true))

	// A lost worker holds the slot through the provider's bounded cleanup grace.
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, next.ID)
	require.NoError(t, err)
	expired := claim(next.ID, true)
	require.Equal(t, executionrequests.StateAmbiguous, expired.State)
	release(next.ID, "fixture")
	replacement := create("fixture", 5, true, 2, 1)
	claim(replacement, false)
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET lease_expires_at=clock_timestamp()-interval '11 seconds' WHERE id=$1`, next.ID)
	require.NoError(t, err)
	recovered := claim(replacement, true)
	require.NoError(t, repo.Complete(ctx, pool, recovered.ID, recovered.ClaimToken, []byte("done"), true))
	release(recovered.ID, "fixture")

	// Competing workers and distinct data sources share one organization cap.
	ids := make([]uuid.UUID, 8)
	for i := range ids {
		ids[i] = create("fixture", i+1, true, 3, 2)
	}
	start := make(chan struct{})
	results := make(chan executionrequests.Request, len(ids))
	errors := make(chan error, len(ids))
	var workers sync.WaitGroup
	for i, id := range ids {
		workers.Add(1)
		go func(db *pgxpool.Pool, id uuid.UUID) {
			defer workers.Done()
			<-start
			r, ok, err := repo.Claim(ctx, db, id)
			if err != nil {
				errors <- err
				return
			}
			if ok {
				results <- r
			}
		}([]*pgxpool.Pool{pool, second}[i%2], id)
	}
	close(start)
	workers.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var running []executionrequests.Request
	for r := range results {
		running = append(running, r)
	}
	require.Len(t, running, 2, "native file cap is distinct from the larger connector cap")
	for _, r := range running {
		require.NoError(t, repo.Complete(ctx, pool, r.ID, r.ClaimToken, []byte("done"), true))
		release(r.ID, "fixture")
	}
	// Expired queued requests terminalize even while admission is saturated.
	for _, id := range ids {
		_, err := pool.Exec(ctx, `UPDATE execution_requests SET queue_deadline_at=clock_timestamp()-interval '1 second' WHERE id=$1 AND state='pending'`, id)
		require.NoError(t, err)
	}
	for _, id := range ids {
		r, ok, err := repo.Claim(context.Background(), pool, id)
		require.NoError(t, err)
		if ok {
			require.Equal(t, executionrequests.StateCancelled, r.State)
		}
	}
	source := claim(create("fixture", 9, true, 2, 1), true)
	require.NoError(t, repo.Complete(ctx, pool, source.ID, source.ClaimToken, []byte("retained source"), true))
	competitor := create("fixture", 9, true, 2, 1)
	claim(competitor, false)
	for ds := 13; ds < 69; ds++ {
		_ = create("fixture", ds, true, 2, 1)
	}
	sinkID := createScoped("fixture", 9, true, 2, 1, source.ID)
	sink, sinkClaimed, err := repo.ClaimNext(ctx, pool)
	require.NoError(t, err)
	require.True(t, sinkClaimed)
	require.Equal(t, sinkID, sink.ID, "polling must find the admitted continuation beyond fifty blocked new transfer heads")
	require.NoError(t, repo.Complete(ctx, pool, sink.ID, sink.ClaimToken, []byte("delivered"), true))
	claim(competitor, false)
	release(source.ID, "other")
	claim(competitor, false)
	crossTenant, err := pool.Begin(ctx)
	require.NoError(t, err)
	crossRequest := uuid.New()
	require.NoError(t, repo.Create(ctx, crossTenant, executionrequests.Request{ID: crossRequest, OrganizationSlug: "other", DataSourceSlug: "source-2", Kind: "query", RequestCiphertext: []byte("encrypted"), DeadlineAt: transferDeadline, QueueDeadlineAt: &transferDeadline, ConcurrencyLimit: 2, QueueCapacity: 2}, transferDeadline.Add(time.Hour)))
	require.Error(t, repo.BindOrganizationLimits(ctx, crossTenant, crossRequest, "other", 2, 1, true, source.ID, transferDeadline), "another tenant cannot reuse an existing transfer scope")
	require.NoError(t, crossTenant.Rollback(ctx))

	pendingContinuation := createScoped("fixture", 9, true, 2, 1, source.ID)
	release(source.ID, "fixture")
	cancelledContinuation, err := repo.Find(ctx, pool, pendingContinuation, "fixture")
	require.NoError(t, err)
	require.Equal(t, executionrequests.StateCancelled, cancelledContinuation.State)
	following := claim(competitor, true)
	require.NoError(t, repo.Complete(ctx, pool, following.ID, following.ClaimToken, []byte("done"), true))
	// A vanished attempt has a finite deadline even if its caller cannot release it.
	_, err = pool.Exec(ctx, `UPDATE file_transfer_scopes SET deadline_at=clock_timestamp()-interval '11 seconds' WHERE scope_id=$1`, following.ID)
	require.NoError(t, err)
	afterLoss := claim(create("fixture", 12, true, 2, 1), true)
	require.NoError(t, repo.Complete(ctx, pool, afterLoss.ID, afterLoss.ClaimToken, []byte("done"), true))
	release(afterLoss.ID, "fixture")
	_, err = pool.Exec(ctx, `UPDATE file_transfer_scopes SET expires_at=clock_timestamp()-interval '1 second' WHERE scope_id=$1`, source.ID)
	require.NoError(t, err)
	require.NoError(t, repo.DeleteExpiredFileTransfers(ctx, pool))
	var remaining int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM file_transfer_scopes WHERE scope_id=$1`, source.ID).Scan(&remaining))
	require.Zero(t, remaining)

	pendingID := create("fixture", 12, true, 2, 1)
	locked, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = locked.Rollback(context.Background()) }()
	_, err = locked.Exec(ctx, `SELECT id FROM execution_requests WHERE id=$1 FOR UPDATE`, pendingID)
	require.NoError(t, err)
	releaseCtx, cancelRelease := context.WithTimeout(ctx, 5*time.Second)
	defer cancelRelease()
	released := make(chan error, 1)
	go func() { released <- repo.ReleaseFileTransfer(releaseCtx, pool, pendingID, "fixture") }()
	select {
	case err := <-released:
		require.NoError(t, err)
	case <-releaseCtx.Done():
		t.Fatal("admission closure must not wait on a held request row")
	}
	terminal, claimed, err := repo.Claim(ctx, locked, pendingID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, executionrequests.StateCancelled, terminal.State, "a released scope terminalizes without provider dispatch")
	require.NoError(t, locked.Commit(ctx))
	pendingRequestIDs, err := repo.ListFileTransferRequests(ctx, pool, pendingID, "fixture")
	require.NoError(t, err)
	for _, id := range pendingRequestIDs {
		_, err := repo.Cancel(ctx, pool, id)
		require.NoError(t, err)
	}
	cancelledPending, err := repo.Find(ctx, pool, pendingID, "fixture")
	require.NoError(t, err)
	require.Equal(t, executionrequests.StateCancelled, cancelledPending.State)

	// Claim admission reads scope state through the existing capability, without
	// adding executor table privileges.
	restrictedID := create("fixture", 12, true, 2, 1)
	restricted, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = restricted.Rollback(context.Background()) }()
	role := pgx.Identifier{fmt.Sprintf("file_claim_%d", time.Now().UnixNano())}.Sanitize()
	_, err = restricted.Exec(ctx, "CREATE ROLE "+role+" NOLOGIN; GRANT USAGE ON SCHEMA public TO "+role+"; GRANT EXECUTE ON FUNCTION claim_execution_request(UUID) TO "+role+"; SET LOCAL ROLE "+role)
	require.NoError(t, err)
	_, ok, err := repo.Claim(ctx, restricted, restrictedID)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = restricted.Exec(ctx, "SAVEPOINT table_boundary")
	require.NoError(t, err)
	_, err = restricted.Exec(ctx, "SELECT 1 FROM file_transfer_scopes")
	require.Error(t, err)
	_, err = restricted.Exec(ctx, "ROLLBACK TO SAVEPOINT table_boundary")
	require.NoError(t, err)
}

func TestDeniedPollerCandidatesReleaseLocks(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	pool := executionClaimPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `WITH org AS (INSERT INTO organizations(slug,name) VALUES('lock-fixture','Lock fixture') RETURNING id), cred AS (INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload) SELECT id,'credential','static_secret','postgres','active','\x01' FROM org RETURNING id,organization_id) INSERT INTO data_sources(organization_id,credential_id,name,slug,type,config,is_active,read_only) SELECT organization_id,id,n,n,'postgres','{}',true,true FROM cred CROSS JOIN unnest(ARRAY['first','second']) n`)
	require.NoError(t, err)
	repo := executionrequests.New()
	deadline := time.Now().Add(time.Minute)
	create := func(source string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		require.NoError(t, repo.Create(ctx, pool, executionrequests.Request{ID: id, OrganizationSlug: "lock-fixture", DataSourceSlug: source, Kind: "query", RequestCiphertext: []byte("encrypted"), DeadlineAt: deadline, QueueDeadlineAt: &deadline, ConcurrencyLimit: 2, QueueCapacity: 2}, deadline.Add(time.Hour)))
		require.NoError(t, repo.BindOrganizationLimits(ctx, pool, id, "lock-fixture", 2, 1, true, id, deadline))
		return id
	}
	first, second := create("first"), create("second")
	gate, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = gate.Rollback(context.Background()) }()
	_, err = gate.Exec(ctx, `SELECT id FROM execution_requests WHERE id=$1 FOR UPDATE`, first)
	require.NoError(t, err)
	var gatePID int32
	require.NoError(t, gate.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&gatePID))
	poller, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = poller.Rollback(context.Background()) }()
	var pollerPID int32
	require.NoError(t, poller.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pollerPID))
	type polled struct {
		claimed bool
		err     error
	}
	done := make(chan polled, 1)
	go func() { _, claimed, err := repo.ClaimNext(ctx, poller); done <- polled{claimed, err} }()
	require.Eventually(t, func() bool {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT $1=ANY(pg_blocking_pids($2))`, gatePID, pollerPID).Scan(&blocked)
		return err == nil && blocked
	}, 5*time.Second, 5*time.Millisecond, "candidate snapshot must precede the competing admission")
	competing, claimed, err := repo.Claim(ctx, pool, second)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repo.Complete(ctx, pool, second, competing.ClaimToken, []byte("retained input"), true))
	require.NoError(t, gate.Commit(ctx))
	select {
	case outcome := <-done:
		require.NoError(t, outcome.err)
		require.False(t, outcome.claimed)
	case <-ctx.Done():
		t.Fatal("declined candidates did not finish")
	}
	// Keep the poll transaction open: no denied candidate may keep its row or
	// organization lock and deadlock a different poller's continuation.
	probe, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = probe.Rollback(context.Background()) }()
	_, err = probe.Exec(ctx, `SELECT id FROM execution_requests WHERE id=$1 FOR UPDATE NOWAIT`, first)
	require.NoError(t, err)
	_, err = probe.Exec(ctx, `SELECT id FROM organizations WHERE slug='lock-fixture' FOR NO KEY UPDATE NOWAIT`)
	require.NoError(t, err)
}
