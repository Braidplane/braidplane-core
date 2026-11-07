package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/Braidplane/braidplane-core/service/worker/producer"
	"github.com/stretchr/testify/require"
)

func TestHTTPCheckpointAdmission(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL pipeline storage")
	}
	pool := openE2EPool(t)
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprintf("inline=%t", inline), func(t *testing.T) {
			ctx := context.Background()
			org := must.Slug(fmt.Sprintf("http-admission-%d", time.Now().UnixNano()))
			member := createIncrementalCheckpointServiceOrgProject(t, pool, org)
			actor := model.UserEventActor(member)
			executor := &incrementalCheckpointServiceQueryExecutor{sourceObservations: []json.RawMessage{json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`)}}
			client, err := queue.NewEnqueueOnly(pool)
			require.NoError(t, err)
			runProducer, err := producer.NewPipelineRunProducer(queue.NewEnqueuer(client), pipelineruns.New())
			require.NoError(t, err)
			events := &bootstrapEventRecorder{}
			svc, store := newIncrementalCheckpointService(t, pool, executor, eventtest.NewProducer(events), runProducer)
			pipeline := incrementalCheckpointServicePipeline("http-admission")
			query := pipeline.Steps[0].Query
			query.OperationType, query.DataSourceSlug = "http", "source-http"
			if inline {
				query.OperationRef = ""
				query.OperationInline = map[string]any{"statement": `{"method":"GET","path":"/items"}`}
			}
			createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, org, member, pipeline)
			initialEvents := len(events.calls)
			validLower := json.RawMessage(`{"value":"2026-05-24T10:00:00.123456789+02:00"}`)
			validUpper := json.RawMessage(`{"value":"2026-05-24T11:00:00.123456789Z"}`)
			assertNoJobsOrRuns := func() {
				t.Helper()
				var count int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'organization_slug'=$1`, org).Scan(&count))
				require.Zero(t, count)
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_runs r JOIN project_environments pe ON pe.id=r.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1`, org).Scan(&count))
				require.Zero(t, count)
			}
			for _, raw := range []string{
				`{"value":"private-invalid-date"}`, `{"value":123}`, `{"value":"123"}`,
				`{"value":"2026-05-24T10:00:00.1234567891Z"}`, `{"value":"2026-05-24T10:00:00,123Z"}`,
				`{"value":"2026-05-24T1:00:00.1234567891Z"}`,
				`{"value":"2026-05-24T10:00:00+24:00"}`, `{"value":"2026-05-24T10:00:00+00:60"}`,
				`{"value":"2026-05-24T10:00:00"}`, `null`, `{"value":null}`, ``,
			} {
				err := svc.BootstrapCheckpoints(ctx, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, "invalid-bootstrap", nil, actor, nil, map[slug.Slug]json.RawMessage{"fetch-users": json.RawMessage(raw)})
				require.ErrorIs(t, err, xerror.InvalidInput(xerror.PipelineRun))
				require.NotContains(t, err.Error(), "private-invalid-date")
				require.Contains(t, xerror.PublicDetail(err), "RFC3339")
				for _, lowerInvalid := range []bool{false, true} {
					lower, upper := validLower, json.RawMessage(raw)
					if lowerInvalid {
						lower, upper = json.RawMessage(raw), validUpper
					}
					_, err = svc.StartBackfill(ctx, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("invalid-backfill"), nil, actor, e2eRunSource(actor), nil, map[slug.Slug]pipelinerun.IncrementalSourceWindow{"fetch-users": {LowerBound: lower, UpperBound: upper}})
					require.ErrorIs(t, err, xerror.InvalidInput(xerror.PipelineRun))
					assertNoJobsOrRuns()
				}
				require.Zero(t, countIncrementalCheckpointsByStatus(t, pool, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, "committed"))
				require.Empty(t, executor.checkpointInputs)
				require.Zero(t, executor.sourceCalls)
				require.Zero(t, executor.sinkCalls)
				require.Len(t, events.calls, initialEvents)
			}
			require.NoError(t, svc.BootstrapCheckpoints(ctx, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, "valid-bootstrap", nil, actor, nil, map[slug.Slug]json.RawMessage{"fetch-users": validLower}))
			bound, status := currentIncrementalCheckpoint(t, pool, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug)
			require.Equal(t, "committed", status)
			require.JSONEq(t, string(validLower), string(bound))
			require.Empty(t, executor.checkpointInputs)
			windows := map[slug.Slug]pipelinerun.IncrementalSourceWindow{"fetch-users": {LowerBound: validLower, UpperBound: validUpper}}
			_, err = svc.StartBackfill(ctx, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("valid-backfill"), nil, actor, e2eRunSource(actor), nil, windows)
			require.NoError(t, err)
			var rawArgs []byte
			require.NoError(t, pool.QueryRow(ctx, `SELECT args FROM river_job WHERE kind='pipeline_run_execution' AND args->>'organization_slug'=$1 AND args->>'run_slug'='valid-backfill'`, org).Scan(&rawArgs))
			var args jobs.PipelineRunExecutionArgs
			require.NoError(t, json.Unmarshal(rawArgs, &args))
			require.JSONEq(t, string(validLower), string(args.Incremental.SourceWindows["fetch-users"].LowerBound))
			require.JSONEq(t, string(validUpper), string(args.Incremental.SourceWindows["fetch-users"].UpperBound))
			require.NoError(t, svc.ExecuteQueuedPipelineRun(ctx, args))
			var runStatus string
			var runError string
			require.NoError(t, pool.QueryRow(ctx, `SELECT r.status,COALESCE(r.error_summary,'') FROM pipeline_runs r JOIN project_environments pe ON pe.id=r.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND r.run_slug='valid-backfill'`, org).Scan(&runStatus, &runError))
			require.Equal(t, "succeeded", runStatus, "run error: %v", runError)
			require.Len(t, executor.checkpointInputs, 1)
			require.JSONEq(t, string(validLower), string(executor.checkpointInputs[0].LowerBound))
			require.JSONEq(t, string(validUpper), string(executor.checkpointInputs[0].UpperBound))
			bound, status = currentIncrementalCheckpoint(t, pool, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug)
			require.Equal(t, "committed", status)
			require.JSONEq(t, string(validLower), string(bound), "backfill preserves normal progress")
		})
	}
}
