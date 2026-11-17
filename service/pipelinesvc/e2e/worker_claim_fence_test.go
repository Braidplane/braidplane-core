package e2e

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
	s3connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

// The provider deliberately ignores cancellation while its write is held. A
// replacement must not write even when the original database session is gone.
func TestSinkClaimFencesSlowAndDisconnectedWriter(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable Postgres")
	}
	ctx := t.Context()
	pool := openIntegrationPool(t)
	org, project, environment := slug.Slug("claim-"+strings.ToLower(rand.Text())), defaultProjectSlug, slug.Slug("prod")
	seedOrganization(t, pool, org, "Claim fence fixture")
	seedPipelineDeploymentEnvironment(t, pool, org, project, environment)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM project_environments WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug=$1`, org)
		require.NoError(t, err)
	})
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	materializePipelines(t, ctx, store, org, project, nativeRepairPipeline())
	meta, err := store.FindSpec(ctx, org, project, "repair")
	require.NoError(t, err)
	for _, run := range []string{"original", "replacement"} {
		_, err := pipelineruns.New().CreateRun(ctx, pool, org, project, environment, &pipelineruns.CreateRunParams{PipelineVersionID: *meta.CurrentVersionID, PipelineSlug: "repair", RunSlug: run, Status: "running", StartedAt: time.Now().UTC(), Timeline: []byte(`{"events":[]}`)})
		require.NoError(t, err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var writes, active atomic.Int32
	var bodyMu sync.Mutex
	var stored []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			writes.Add(1)
			assert.EqualValues(t, 1, active.Add(1), "provider writers must remain exclusive")
			defer active.Add(-1)
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			close(entered)
			<-release
			bodyMu.Lock()
			stored = body
			bodyMu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			bodyMu.Lock()
			body := append([]byte(nil), stored...)
			bodyMu.Unlock()
			if body == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write(body)
			assert.NoError(t, err)
		default:
			t.Errorf("unexpected provider method %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	// Ensure a failed assertion releases the deliberately uncancellable provider before Close waits.
	defer releaseOnce.Do(func() { close(release) })
	connector, err := s3connector.New(transportsecurity.WithPlaintext(ctx), s3connector.Config{Endpoint: server.URL, Region: "test", Bucket: "fixtures", ForcePathStyle: true, AllowPrivateHosts: true, TimeoutSeconds: 30}, s3connector.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture-secret"})
	require.NoError(t, err)
	defer func() { require.NoError(t, connector.Close()) }()
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	require.NoError(t, err)
	defer func() { _ = conn.Close(context.Background()) }()
	originalRuntime := pipelineruntime.NewSinkRuntime(nativeRepairSink{connector}, connectionEffectLedger{conn})
	replacementRuntime := pipelineruntime.NewSinkRuntime(nativeRepairSink{connector}, testSinkEffectLedger(t, txm))
	input := func(run string) *pipelineruntime.SinkRuntimeInput {
		return &pipelineruntime.SinkRuntimeInput{
			ExclusiveEffectClaim: true, OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: run, ReplayIdentity: "original", SinkStepSlug: "write", SinkBackend: model.SinkBackendS3, SinkDataSourceSlug: "objects", SinkOperationRef: "write", IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent, IdempotencyKey: "fixture-effect",
			SinkConfig:   map[string]any{"key": "original.json", "format": "json", "overwrite": true},
			SourceResult: pipelinerun.NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil)),
		}
	}
	done := make(chan error, 1)
	go func() { done <- originalRuntime.Execute(ctx, input("original")) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("writer did not reach provider")
	}
	require.ErrorIs(t, replacementRuntime.Execute(ctx, input("replacement")), writegate.ErrOutcomeUnknown, "slow writer retains its durable claim")
	require.EqualValues(t, 1, writes.Load())
	var terminated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, conn.PgConn().PID()).Scan(&terminated))
	require.True(t, terminated)
	require.Error(t, conn.Ping(ctx), "original worker has lost its database session")
	_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET status='failed' WHERE run_slug='original' AND project_environment_id=(SELECT pe.id FROM project_environments pe JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND p.slug=$2)`, org, project)
	require.NoError(t, err)
	require.ErrorIs(t, replacementRuntime.Execute(ctx, input("replacement")), writegate.ErrOutcomeUnknown, "lost session cannot release a durable claim")
	require.EqualValues(t, 1, writes.Load())
	// A returning owner cannot obtain a fresh claim once its run has been fenced.
	late := input("original")
	late.SinkStepSlug = "late-write"
	late.IdempotencyKey = "late-effect"
	require.ErrorIs(t, replacementRuntime.Execute(ctx, late), writegate.ErrOutcomeUnknown)
	require.EqualValues(t, 1, writes.Load())
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		require.Error(t, err, "lost DB session cannot record acknowledgement")
	case <-time.After(10 * time.Second):
		t.Fatal("old writer did not return")
	}
	recovered := input("replacement")
	require.NoError(t, replacementRuntime.Execute(ctx, recovered), "readback confirms the old write without another PUT")
	require.Equal(t, "reconcile_provider_effect", recovered.ReplayDecision)
	repeated := input("replacement")
	require.NoError(t, replacementRuntime.Execute(ctx, repeated))
	require.Equal(t, "skip_ledger_succeeded", repeated.ReplayDecision)
	require.EqualValues(t, 1, writes.Load())
}

type connectionEffectLedger struct{ db postgres.DBTX }

func (l connectionEffectLedger) BeginOrGet(ctx context.Context, org, project, environment slug.Slug, attempt pipelinerun.SinkEffectAttempt) (*pipelinerun.SinkEffect, error) {
	return pipelinesinkeffects.New().BeginOrGet(ctx, l.db, org, project, environment, attempt)
}

func (l connectionEffectLedger) MarkSucceeded(ctx context.Context, org, project, environment slug.Slug, attempt pipelinerun.SinkEffectAttempt) error {
	return pipelinesinkeffects.New().MarkSucceeded(ctx, l.db, org, project, environment, attempt)
}
