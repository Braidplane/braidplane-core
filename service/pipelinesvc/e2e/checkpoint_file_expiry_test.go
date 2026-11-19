package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/postgres"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/postgres/retainedfiles"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func expireFileInputs(ctx context.Context, txm postgres.TransactionManager) error {
	return txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		cutoff, err := requestrepo.New().LockExpired(ctx, db)
		if err != nil {
			return err
		}
		_, err = requestrepo.New().DeleteExpired(ctx, db, cutoff)
		return err
	})
}

func checkCheckpointExpiryRace(t *testing.T, parent context.Context, pool *pgxpool.Pool, txm postgres.TransactionManager, runs pipelinerun.RunStore, org, project, environment slug.Slug) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	checkpoint, err := runs.GetCheckpoint(ctx, org, project, environment, "sql-transfer")
	require.NoError(t, err)
	content, ok := checkpoint.Results["read"].Payload()
	require.True(t, ok)
	require.NotNil(t, content.FileReference)
	source := content.FileReference.ID
	_, err = pool.Exec(ctx, `DELETE FROM pipeline_checkpoint_files WHERE request_id=$1`, source)
	require.NoError(t, err)
	var expires time.Time
	require.NoError(t, pool.QueryRow(ctx, `WITH updated AS (UPDATE execution_requests SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING expires_at) UPDATE retained_file_streams SET expires_at=(SELECT expires_at FROM updated) WHERE request_id=$1 RETURNING expires_at`, source).Scan(&expires))
	ready, release := make(chan struct{}), make(chan struct{})
	pinned := make(chan error, 1)
	go func() {
		pinned <- txm.RunInTx(ctx, func(ctx context.Context, _ postgres.DBTX) error {
			if err := runs.SaveCheckpoint(ctx, org, project, environment, "sql-transfer", *checkpoint); err != nil {
				return err
			}
			close(ready)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-ready:
	case err := <-pinned:
		t.Fatalf("pin failed before expiry: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	timer := time.NewTimer(time.Until(expires) + 20*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	connection, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer connection.Release()
	var expiryPID int
	require.NoError(t, connection.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&expiryPID))
	expired := make(chan error, 1)
	go func() {
		tx, err := connection.Begin(ctx)
		if err != nil {
			expired <- err
			return
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		cutoff, err := requestrepo.New().LockExpired(ctx, tx)
		if err == nil {
			_, err = requestrepo.New().DeleteExpired(ctx, tx, cutoff)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		expired <- err
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, expiryPID).Scan(&waiting)
		return err == nil && waiting
	}, 5*time.Second, 10*time.Millisecond, "maintenance must overlap the uncommitted pin")
	close(release)
	require.NoError(t, <-pinned)
	require.NoError(t, <-expired)
	var files, requests int
	require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM retained_file_streams WHERE request_id=$1),(SELECT count(*) FROM execution_requests WHERE id=$1)`, source).Scan(&files, &requests))
	require.Equal(t, 1, files, "a pin committed across expiry must preserve its stream")
	require.Equal(t, 1, requests, "request expiry must not cascade through a committed pin")
	restored, err := runs.GetCheckpoint(ctx, org, project, environment, "sql-transfer")
	require.NoError(t, err)
	restoredContent, _ := restored.Results["read"].Payload()
	require.Equal(t, content.FileReference, restoredContent.FileReference)
}

func checkCheckpointRetentionPolicy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, txm postgres.TransactionManager, runs pipelinerun.RunStore, org, project, environment slug.Slug) {
	t.Helper()
	checkpoint, err := runs.GetCheckpoint(ctx, org, project, environment, "sql-transfer")
	require.NoError(t, err)
	content, _ := checkpoint.Results["read"].Payload()
	require.NotNil(t, content.FileReference)
	source := content.FileReference.ID
	_, err = pool.Exec(ctx, `UPDATE pipeline_checkpoint SET created_at=clock_timestamp()-interval '10 days' WHERE id IN (SELECT checkpoint_id FROM pipeline_checkpoint_files WHERE request_id=$1)`, source)
	require.NoError(t, err)
	defer func() {
		_, err := pool.Exec(context.WithoutCancel(ctx), `UPDATE organizations SET settings=settings-'retention' WHERE slug=$1`, org)
		require.NoError(t, err)
		_, err = pool.Exec(context.WithoutCancel(ctx), `UPDATE pipeline_checkpoint SET created_at=clock_timestamp() WHERE id IN (SELECT checkpoint_id FROM pipeline_checkpoint_files WHERE request_id=$1)`, source)
		require.NoError(t, err)
	}()
	for _, days := range []int{30, 3, 30} {
		_, err = pool.Exec(ctx, `UPDATE organizations SET settings=jsonb_set(settings,'{retention}',jsonb_build_object('replay_checkpoint_retention_days',$2::integer)) WHERE slug=$1`, org, days)
		require.NoError(t, err)
		restored, err := runs.GetCheckpoint(ctx, org, project, environment, "sql-transfer")
		require.NoError(t, err)
		payload, _ := restored.Results["read"].Payload()
		if days == 3 {
			require.Nil(t, payload.FileReference)
		} else {
			require.Equal(t, content.FileReference, payload.FileReference)
		}
	}
	require.NoError(t, expireFileInputs(ctx, txm))
	requests := requestrepo.New()
	id := uuid.New()
	// Claim the synthetic reader before the broker pollers can see it.
	var reader requestrepo.Request
	require.NoError(t, txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		if err := requests.Create(ctx, db, requestrepo.Request{ID: id, OrganizationSlug: org.String(), Kind: "prepare_s3_write", RequestCiphertext: []byte("synthetic policy reader"), DeadlineAt: time.Now().Add(time.Minute)}, time.Now().Add(time.Hour)); err != nil {
			return err
		}
		var claimed bool
		var err error
		reader, claimed, err = requests.Claim(ctx, db, id)
		if err != nil {
			return err
		}
		require.True(t, claimed)
		return nil
	}))
	defer func() {
		_, err := pool.Exec(context.WithoutCancel(ctx), `DELETE FROM execution_requests WHERE id=$1`, id)
		require.NoError(t, err)
	}()
	claim := filestream.Claim{RequestID: id, ClaimToken: reader.ClaimToken, Organization: org}
	files := retainedfiles.New()
	sourceID, err := uuid.FromString(source)
	require.NoError(t, err)
	manifest, err := files.Read(ctx, pool, claim, sourceID)
	require.NoError(t, err)
	require.Equal(t, content.FileReference.SizeBytes, manifest.SizeBytes)
	body, err := files.ReadChunk(ctx, pool, claim, sourceID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, body)
	_, err = pool.Exec(ctx, `UPDATE organizations SET settings=jsonb_set(settings,'{retention}',jsonb_build_object('replay_checkpoint_retention_days',3)) WHERE slug=$1`, org)
	require.NoError(t, err)
	_, err = files.Read(ctx, pool, claim, sourceID)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
	_, err = files.ReadChunk(ctx, pool, claim, sourceID, 0)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
}
