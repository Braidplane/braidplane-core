package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/postgres/executionrequests"
)

func TestPollResponseProjectsEnvelopesByState(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := t.Context()
	pool := executionClaimPool(t)
	repo := executionrequests.New()
	id := uuid.New()
	const org = "poll-fixture"
	_, err := pool.Exec(ctx, `INSERT INTO organizations(slug,name) VALUES ($1,$1),('another-org','another-org')`, org)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO execution_requests(id, organization_id, kind, request_ciphertext, result_ciphertext, deadline_at, expires_at)
		SELECT $1, id, 'query', 'request'::bytea, 'result'::bytea, now(), now() FROM organizations WHERE slug=$2`, id, org)
	require.NoError(t, err)
	_, err = repo.PollResponse(ctx, pool, id, "another-org")
	require.ErrorContains(t, err, "execution request not found")

	for _, state := range []executionrequests.State{
		executionrequests.StatePending, executionrequests.StateRunning,
		executionrequests.StateCancelled, executionrequests.StateAmbiguous,
		executionrequests.StateSucceeded, executionrequests.StateFailed,
	} {
		t.Run(string(state), func(t *testing.T) {
			_, err := pool.Exec(ctx, `UPDATE execution_requests SET state=$2, kind='query',
				cancel_requested_at=now(), queue_deadline_at=now() WHERE id=$1`, id, state)
			require.NoError(t, err)
			row, err := repo.PollResponse(ctx, pool, id, org)
			require.NoError(t, err)
			require.Equal(t, state, row.State)
			require.NotNil(t, row.CancelRequestedAt)
			require.NotNil(t, row.QueueDeadlineAt)
			if state == executionrequests.StateSucceeded || state == executionrequests.StateFailed {
				require.Equal(t, []byte("request"), row.RequestCiphertext)
				require.Equal(t, []byte("result"), row.ResultCiphertext)
				_, err = pool.Exec(ctx, `UPDATE execution_requests SET kind='transform' WHERE id=$1`, id)
				require.NoError(t, err)
				row, err = repo.PollResponse(ctx, pool, id, org)
				require.NoError(t, err)
				require.Nil(t, row.RequestCiphertext)
				require.Equal(t, []byte("result"), row.ResultCiphertext)
			} else {
				require.Nil(t, row.RequestCiphertext)
				require.Nil(t, row.ResultCiphertext)
			}
		})
	}
}

func BenchmarkPendingExecutionPoll(b *testing.B) {
	ctx := b.Context()
	pool := e2edb.OpenIsolatedPostgresPool(b, "large execution poll")
	repo := executionrequests.New()
	const org = "poll-benchmark"
	id := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(slug,name) VALUES ($1,$1)`, org)
	require.NoError(b, err)
	require.NoError(b, repo.Create(ctx, pool, executionrequests.Request{
		ID: id, OrganizationSlug: org, Kind: "transform", RequestCiphertext: make([]byte, 10<<20),
		DeadlineAt: time.Now().Add(time.Hour),
	}, time.Now().Add(time.Hour)))

	for _, full := range []bool{true, false} {
		read, name := repo.PollResponse, "PollResponse"
		if full {
			read, name = repo.Find, "Find"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			var row executionrequests.Request
			for b.Loop() {
				row, err = read(ctx, pool, id, org)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(row.RequestCiphertext)+len(row.ResultCiphertext)), "envelope-B/op")
		})
	}
}
