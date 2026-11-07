package pipelineruns

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func TestToPipelineRunPreservesVersionSpecHashAndOptionalMetadata(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(2 * time.Second)
	run := toPipelineRun(dbgen.PipelineRun{
		PipelineVersionID:       pgtype.Int8{Int64: 42, Valid: true},
		PipelineVersionSpecHash: pgtype.Text{String: "spec-hash-42", Valid: true},
		ApiVersion:              "v1",
		PipelineSlug:            "customer-sync",
		RunSlug:                 "run-123",
		RequestID:               pgtype.Text{String: "request-123", Valid: true},
		SourceType:              "api",
		SourceSummary:           "API key run",
		SourceEntrypoint:        "api",
		SourceActorType:         "api_key",
		SourceActorSlug:         pgtype.Text{String: "deploy-key", Valid: true},
		SourceRequestID:         pgtype.Text{String: "request-123", Valid: true},
		SourceTraceID:           pgtype.Text{String: "4bf92f3577b34da6a3ce929d0e0e4736", Valid: true},
		SourceParameterKeys:     []string{"run_slug", "protected_environment_confirmation"},
		Status:                  "failed",
		DurationMs:              2000,
		StepCount:               3,
		StepSlug:                "load-customers",
		ErrorSummary:            pgtype.Text{String: "boom", Valid: true},
		FailureKind:             pgtype.Text{String: "connector", Valid: true},
		FailureSide:             pgtype.Text{String: "source", Valid: true},
		StartedAt:               pgtype.Timestamptz{Time: startedAt, Valid: true},
		EndedAt:                 pgtype.Timestamptz{Time: endedAt, Valid: true},
		ReplayOfRunSlug:         pgtype.Text{String: "run-original", Valid: true},
		ReplayFromStep:          pgtype.Text{String: "load-customers", Valid: true},
		SourceEventSlug:         pgtype.Text{String: "evt-pipeline-run-execute-001", Valid: true},
		RunMode:                 "backfill",
		CheckpointStatus:        "blocked",
		IncrementalSourceStepSlug: pgtype.Text{
			String: "fetch-customers",
			Valid:  true,
		},
		IncrementalBlockedReason: pgtype.Text{
			String: "sink write failed",
			Valid:  true,
		},
		ReplaySafetyStatus: pgtype.Text{
			String: "blocked",
			Valid:  true,
		},
		ReplayBlockedReason: pgtype.Text{
			String: "append-only sink append-audit is not replay safe",
			Valid:  true,
		},
		BackfillSeriesSlug: pgtype.Text{
			String: "series-001",
			Valid:  true,
		},
		BackfillChunkIndex:     3,
		BackfillChunkCount:     10,
		IncrementalDiagnostics: []byte(`{"sources":[{"source_step_slug":"fetch-customers","commit_result":{"status":"blocked"}}]}`),
		Timeline:               []byte(`[{"step_slug":"load-customers"}]`),
		CreatedAt:              pgtype.Timestamptz{Time: startedAt, Valid: true},
	}, "prod", nil)

	require.Equal(t, int64(42), run.PipelineVersionID)
	require.Equal(t, "spec-hash-42", run.PipelineVersionSpecHash)
	require.Equal(t, "prod", run.EnvironmentSlug)
	require.Equal(t, "request-123", *run.RequestID)
	require.Equal(t, model.PipelineRunSourceTypeAPI, run.Source.Type)
	require.Equal(t, "API key run", run.Source.Summary)
	require.Equal(t, model.PipelineRunSourceEntrypointAPI, run.Source.Entrypoint)
	require.Equal(t, model.EventActorTypeAPIKey, run.Source.ActorType)
	require.Equal(t, "deploy-key", run.Source.ActorSlug.String())
	require.Equal(t, "request-123", *run.Source.RequestID)
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", *run.Source.TraceID)
	require.Equal(t, []string{"run_slug", "protected_environment_confirmation"}, run.Source.ParameterKeys)
	require.Equal(t, "evt-pipeline-run-execute-001", run.Source.SourceEventSlug.String())
	require.Equal(t, "boom", *run.ErrorSummary)
	require.Equal(t, "connector", *run.FailureKind)
	require.Equal(t, "source", *run.FailureSide)
	require.Equal(t, &endedAt, run.EndedAt)
	require.Equal(t, "run-original", *run.ReplayOfRunSlug)
	require.Equal(t, "load-customers", *run.ReplayFromStep)
	require.Equal(t, "backfill", run.Incremental.Mode)
	require.Equal(t, "blocked", run.Incremental.CheckpointStatus)
	require.Equal(t, "fetch-customers", run.Incremental.SourceStepSlug)
	require.Equal(t, "sink write failed", run.Incremental.BlockedReason)
	require.Equal(t, "blocked", run.Incremental.ReplaySafetyStatus)
	require.Equal(t, "append-only sink append-audit is not replay safe", run.Incremental.ReplayBlockedReason)
	require.Equal(t, "series-001", run.Incremental.BackfillSeriesSlug)
	require.Equal(t, 3, run.Incremental.BackfillChunkIndex)
	require.Equal(t, 10, run.Incremental.BackfillChunkCount)
	require.Equal(t, pipelinerun.IncrementalRunDiagnostics{
		Sources: []pipelinerun.IncrementalSourceDiagnostic{{
			SourceStepSlug: "fetch-customers",
			CommitResult: pipelinerun.IncrementalCheckpointCommitResult{
				Status: "blocked",
			},
		}},
	}, run.IncrementalDiagnostics)
	require.JSONEq(t, `[{"step_slug":"load-customers"}]`, string(run.Timeline))
}

func TestToPipelineRunHydratesWebhookSourceFromDelivery(t *testing.T) {
	receivedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	run := toPipelineRun(dbgen.PipelineRun{
		SourceWebhookDeliveryID: pgtype.Int8{Int64: 42, Valid: true},
	}, "prod", []byte(`{
		"trigger_slug":"customer-hook",
		"trigger_revision":3,
		"delivery_id":"delivery-1",
		"payload_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"payload_size_bytes":17,
		"received_at":"2026-08-21T12:00:00Z",
		"verification_status":"verified"
	}`))

	require.Equal(t, &pipelinerun.RunSourceWebhookSummary{
		DeliveryRecordID: 42,
		TriggerSlug:      slug.FromString("customer-hook"), TriggerRevision: 3,
		DeliveryID: "delivery-1", PayloadDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PayloadSizeBytes: 17, ReceivedAt: receivedAt, VerificationStatus: "verified",
	}, run.Source.Webhook)
}
