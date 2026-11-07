package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/postgres/retainedfiles"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func freshPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := e2edb.OpenPostgresPool(t, "retained file capabilities")
	name := fmt.Sprintf("retained_files_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	_, err := admin.Exec(t.Context(), "CREATE DATABASE "+identifier)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		require.NoError(t, err)
	})
	config := admin.Config().Copy()
	config.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	paths, err := filepath.Glob("../../migrations/*.up.sql")
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		_, err = pool.Exec(t.Context(), string(body))
		require.NoError(t, err, filepath.Base(path))
	}
	return pool
}

func TestRetainedFileCapabilities(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	pool := freshPool(t)
	ctx := t.Context()
	repo := retainedfiles.New()
	requests := executionrequests.New()
	for _, org := range []string{"alpha", "beta"} {
		_, err := pool.Exec(ctx, `INSERT INTO organizations(slug,name) VALUES($1,$1)`, org)
		require.NoError(t, err)
	}
	claim := func(org string) filestream.Claim {
		t.Helper()
		id := uuid.New()
		require.NoError(t, requests.Create(ctx, pool, executionrequests.Request{ID: id, OrganizationSlug: org, Kind: "query", RequestCiphertext: []byte("isolated encrypted fixture"), DeadlineAt: time.Now().Add(time.Minute)}, time.Now().Add(time.Hour)))
		request, ok, err := requests.Claim(ctx, pool, id)
		require.NoError(t, err)
		require.True(t, ok)
		return filestream.Claim{RequestID: id, ClaimToken: request.ClaimToken, Organization: slug.Slug(org)}
	}
	source := claim("alpha")
	consumer := claim("alpha")
	other := claim("beta")
	require.NoError(t, repo.Begin(ctx, pool, source, 5, time.Now().Add(time.Hour)))
	_, err := repo.Read(ctx, pool, consumer, source.RequestID)
	require.ErrorIs(t, err, filestream.ErrUnavailable, "unsealed input cannot be used")
	ciphertext := bytes.Repeat([]byte{1}, 64)
	require.ErrorIs(t, repo.Append(ctx, pool, source, 1, 5, ciphertext), filestream.ErrUnavailable, "out-of-order chunk")
	require.ErrorIs(t, repo.Append(ctx, pool, source, 0, 6, ciphertext), filestream.ErrUnavailable, "above the total byte limit")
	require.NoError(t, repo.Append(ctx, pool, source, 0, 5, ciphertext))
	require.ErrorIs(t, repo.Append(ctx, pool, source, 1, 1, ciphertext), filestream.ErrUnavailable, "no bytes above the effective limit")
	digest := strings.Repeat("a", 64)
	require.ErrorIs(t, repo.Seal(ctx, pool, source, 4, digest), filestream.ErrUnavailable)
	require.NoError(t, repo.Seal(ctx, pool, source, 5, digest))
	require.ErrorIs(t, repo.Append(ctx, pool, source, 1, 1, ciphertext), filestream.ErrUnavailable, "sealed input is immutable")
	require.NoError(t, requests.Complete(ctx, pool, source.RequestID, source.ClaimToken, []byte("encrypted result"), true))
	manifest, err := repo.Read(ctx, pool, consumer, source.RequestID)
	require.NoError(t, err)
	require.Equal(t, int64(5), manifest.SizeBytes)
	require.Equal(t, int32(1), manifest.ChunkCount)
	prepared := claim("alpha")
	_, err = pool.Exec(ctx, `UPDATE execution_requests SET kind='prepare_s3_write' WHERE id=$1`, prepared.RequestID)
	require.NoError(t, err)
	_, err = repo.Read(ctx, pool, prepared, source.RequestID)
	require.NoError(t, err)
	require.ErrorIs(t, repo.Begin(ctx, pool, prepared, 10, time.Now().Add(time.Hour)), filestream.ErrUnavailable, "preparation may validate retained input but cannot produce it")
	body, err := repo.ReadChunk(ctx, pool, consumer, source.RequestID, 0)
	require.NoError(t, err)
	require.Equal(t, ciphertext, body)
	_, err = repo.Read(ctx, pool, other, source.RequestID)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
	_, err = repo.ReadChunk(ctx, pool, other, source.RequestID, 0)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
	forged := consumer
	forged.Organization = "beta"
	_, err = repo.Read(ctx, pool, forged, source.RequestID)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
	forged = consumer
	forged.ClaimToken = uuid.New()
	_, err = repo.Read(ctx, pool, forged, source.RequestID)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
	for _, scenario := range []string{"cancelled", "expired", "lease", "replaced", "completed"} {
		t.Run(scenario, func(t *testing.T) {
			writer := claim("alpha")
			reader := claim("alpha")
			require.NoError(t, repo.Begin(ctx, pool, writer, 10, time.Now().Add(time.Hour)))
			var update string
			switch scenario {
			case "cancelled":
				update = "cancel_requested_at=clock_timestamp()"
			case "expired":
				update = "deadline_at=clock_timestamp()-interval '1 second'"
			case "lease":
				update = "lease_expires_at=clock_timestamp()-interval '1 second'"
			case "replaced":
				update = "claim_token=gen_random_uuid()"
			case "completed":
				update = "state='succeeded'"
			}
			_, err := pool.Exec(ctx, "UPDATE execution_requests SET "+update+" WHERE id IN ($1,$2)", writer.RequestID, reader.RequestID)
			require.NoError(t, err)
			require.ErrorIs(t, repo.Append(ctx, pool, writer, 0, 1, ciphertext), filestream.ErrUnavailable)
			require.ErrorIs(t, repo.Seal(ctx, pool, writer, 0, digest), filestream.ErrUnavailable)
			_, err = repo.Read(ctx, pool, reader, source.RequestID)
			require.ErrorIs(t, err, filestream.ErrUnavailable)
			_, err = repo.ReadChunk(ctx, pool, reader, source.RequestID, 0)
			require.ErrorIs(t, err, filestream.ErrUnavailable)
		})
	}
	_, err = pool.Exec(ctx, `UPDATE retained_file_streams SET expires_at=clock_timestamp()-interval '1 second' WHERE request_id=$1`, source.RequestID)
	require.NoError(t, err)
	_, err = repo.Read(ctx, pool, consumer, source.RequestID)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
	_, err = repo.ReadChunk(ctx, pool, consumer, source.RequestID, 0)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
	_, err = pool.Exec(ctx, `DELETE FROM execution_requests WHERE id=$1`, source.RequestID)
	require.NoError(t, err)
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM retained_file_chunks WHERE request_id=$1`, source.RequestID).Scan(&count))
	require.Zero(t, count)
}

func TestRetainedFileRestrictedExecutor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	pool := freshPool(t)
	ctx := t.Context()
	repo := retainedfiles.New()
	requests := executionrequests.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(slug,name) VALUES('fixture','fixture')`)
	require.NoError(t, err)
	id := uuid.New()
	require.NoError(t, requests.Create(ctx, pool, executionrequests.Request{ID: id, OrganizationSlug: "fixture", Kind: "query", RequestCiphertext: []byte("encrypted fixture"), DeadlineAt: time.Now().Add(time.Minute)}, time.Now().Add(time.Hour)))
	request, ok, err := requests.Claim(ctx, pool, id)
	require.NoError(t, err)
	require.True(t, ok)
	claim := filestream.Claim{RequestID: id, ClaimToken: request.ClaimToken, Organization: "fixture"}
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	role := pgx.Identifier{fmt.Sprintf("retained_executor_%d", time.Now().UnixNano())}.Sanitize()
	_, err = tx.Exec(ctx, "CREATE ROLE "+role+" NOLOGIN; GRANT USAGE ON SCHEMA public TO "+role)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "SET LOCAL ROLE "+role)
	require.NoError(t, err)
	require.ErrorIs(t, repo.Preflight(ctx, tx), filestream.ErrUnavailable)
	_, err = tx.Exec(ctx, "RESET ROLE")
	require.NoError(t, err)
	for _, signature := range []string{"begin_retained_file(UUID,UUID,TEXT,BIGINT,TIMESTAMPTZ)", "append_retained_file_chunk(UUID,UUID,TEXT,INTEGER,INTEGER,BYTEA)", "seal_retained_file(UUID,UUID,TEXT,BIGINT,TEXT)", "read_retained_file(UUID,UUID,TEXT,UUID)", "read_retained_file_chunk(UUID,UUID,TEXT,UUID,INTEGER)", "discard_retained_file(UUID,UUID,TEXT)"} {
		_, err = tx.Exec(ctx, "GRANT EXECUTE ON FUNCTION public."+signature+" TO "+role)
		require.NoError(t, err)
	}
	_, err = tx.Exec(ctx, "SET LOCAL ROLE "+role)
	require.NoError(t, err)
	require.NoError(t, repo.Preflight(ctx, tx))
	require.NoError(t, repo.Begin(ctx, tx, claim, 10, time.Now().Add(time.Hour)))
	require.NoError(t, repo.Append(ctx, tx, claim, 0, 1, bytes.Repeat([]byte{1}, 64)))
	require.NoError(t, repo.Seal(ctx, tx, claim, 1, strings.Repeat("a", 64)))
	_, err = repo.Read(ctx, tx, claim, id)
	require.NoError(t, err)
	for _, query := range []string{"SELECT 1 FROM retained_file_streams", "SELECT 1 FROM file_transfer_scopes", "SELECT 1 FROM retained_file_chunks", "SELECT 1 FROM execution_requests", "SELECT retained_file_claim(NULL,NULL,NULL)"} {
		_, err = tx.Exec(ctx, "SAVEPOINT boundary_probe")
		require.NoError(t, err)
		_, err = tx.Exec(ctx, query)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr)
		require.Equal(t, "42501", pgErr.Code)
		_, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT boundary_probe")
		require.NoError(t, err)
	}
	require.NoError(t, repo.Discard(ctx, tx, claim))
}
