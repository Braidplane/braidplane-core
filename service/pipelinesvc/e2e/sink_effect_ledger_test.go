package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

// TestSinkEffectLedgerMarkSucceededDoesNotOverwriteCompletedEffect preserves the
// first provider-confirmed terminal diagnostic when a duplicate delivery arrives.
func TestSinkEffectLedgerMarkSucceededDoesNotOverwriteCompletedEffect(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres sink effect ledger integration test in short mode")
	}

	pool := openIntegrationPool(t)
	ctx := context.Background()
	unique := time.Now().UnixNano()
	orgSlug := must.Slug(fmt.Sprintf("sink-effect-ledger-org-%d", unique))
	projectSlug := defaultProjectSlug
	environmentSlug := must.Slug("prod")
	seedOrganization(t, pool, orgSlug, "Sink Effect Ledger Org")
	seedPipelineDeploymentEnvironment(t, pool, orgSlug, projectSlug, environmentSlug)
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, err)
	})

	ledger, err := pipelinesvc.NewSinkEffectLedger(
		postgres.NewTransactionManager(pool),
		pipelinesinkeffects.New(),
	)
	require.NoError(t, err)

	attempt := pipelinerun.SinkEffectAttempt{
		ReplayIdentity:    fmt.Sprintf("sink-effect-ledger-run-%d", unique),
		SinkStepSlug:      must.Slug("write-users"),
		IdempotencyKey:    fmt.Sprintf("sink-effect-ledger-key-%d", unique),
		IdempotencyClass:  "deduplicated_by_ledger",
		ReplayMethod:      "durable_effect_ledger",
		ReplayDecision:    "claim_effect_attempt",
		DiagnosticCode:    "SINK_REPLAY_DECISION",
		DiagnosticSummary: "claim_effect_attempt",
	}
	effect, err := ledger.BeginOrGet(ctx, orgSlug, projectSlug, environmentSlug, attempt)
	require.NoError(t, err)
	require.True(t, effect.Claimed)

	attempt.ReplayDecision = "execute_first_attempt"
	attempt.DiagnosticSummary = attempt.ReplayDecision
	require.NoError(t, ledger.MarkSucceeded(ctx, orgSlug, projectSlug, environmentSlug, attempt))

	attempt.ReplayDecision = "blocked_ledger_outcome_unknown"
	attempt.DiagnosticSummary = attempt.ReplayDecision
	err = ledger.MarkSucceeded(ctx, orgSlug, projectSlug, environmentSlug, attempt)
	require.ErrorIs(t, err, pipelinesinkeffects.ErrPipelineSinkEffectNotFound)

	var replayDecision, diagnosticCode, diagnosticSummary string
	var succeededAt time.Time
	err = pool.QueryRow(
		ctx,
		`SELECT pse.replay_decision, pse.diagnostic_code, pse.diagnostic_summary, pse.succeeded_at
		 FROM pipeline_sink_effects pse
		 JOIN project_environments pe ON pe.id = pse.project_environment_id
		 JOIN projects p ON p.id = pe.project_id
		 JOIN organizations o ON o.id = p.organization_id
		 WHERE o.slug = $1 AND p.slug = $2 AND pse.run_slug = $3 AND pse.sink_step_slug = $4 AND pse.idempotency_key = $5`,
		orgSlug,
		projectSlug,
		attempt.ReplayIdentity,
		attempt.SinkStepSlug,
		attempt.IdempotencyKey,
	).Scan(&replayDecision, &diagnosticCode, &diagnosticSummary, &succeededAt)
	require.NoError(t, err)
	require.Equal(t, "execute_first_attempt", replayDecision)
	require.Equal(t, "SINK_REPLAY_DECISION", diagnosticCode)
	require.Equal(t, "execute_first_attempt", diagnosticSummary)
	require.False(t, succeededAt.IsZero())
}

type lostAcknowledgementLedger struct {
	pipelinerun.SinkEffectLedger
	loseAcknowledgement bool
}

func (l *lostAcknowledgementLedger) MarkSucceeded(ctx context.Context, org, project, environment slug.Slug, attempt pipelinerun.SinkEffectAttempt) error {
	if l.loseAcknowledgement && attempt.SinkStepSlug == "write-b" {
		l.loseAcknowledgement = false
		return xerror.Unexpected().Detail("simulated crash after provider write")
	}
	return l.SinkEffectLedger.MarkSucceeded(ctx, org, project, environment, attempt)
}

type recoverySinkExecutor struct{ calls map[string]int }

func (e recoverySinkExecutor) Execute(ctx context.Context, _, _, operation slug.Slug, _ model.EventActor, _ map[string]any, _ ...queriessvc.ExecuteOption) (*result.ExecutionResult, error) {
	if err := writegate.BeforeWrite(ctx); err != nil {
		return nil, err
	}
	e.calls[operation.String()]++
	return result.NewS3Result(&result.FileResult{Operation: "write"}, 0, nil), nil
}

// This uses real ledger transactions; provider transport faults stay deterministic.
func TestSinkEffectLedgerCrashRecoveryPreservesSucceededSibling(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real PostgreSQL sink-effect transactions")
	}
	pool := openIntegrationPool(t)
	ctx := t.Context()
	org := must.Slug(fmt.Sprintf("recovery-org-%d", time.Now().UnixNano()))
	project, environment := defaultProjectSlug, must.Slug("prod")
	seedOrganization(t, pool, org, "Recovery ledger integration")
	seedPipelineDeploymentEnvironment(t, pool, org, project, environment)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
		require.NoError(t, err)
	})
	durable := testSinkEffectLedger(t, postgres.NewTransactionManager(pool))
	ledger := &lostAcknowledgementLedger{SinkEffectLedger: durable, loseAcknowledgement: true}
	calls := map[string]int{}
	runtime := pipelineruntime.NewSinkRuntime(recoverySinkExecutor{calls: calls}, ledger)
	input := func(step string) *pipelineruntime.SinkRuntimeInput {
		return &pipelineruntime.SinkRuntimeInput{
			OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: "original", ReplayIdentity: "original",
			SinkStepSlug: must.Slug(step), SinkBackend: model.SinkBackendS3, SinkDataSourceSlug: "object-store", SinkOperationRef: must.Slug(step),
			IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent, IdempotencyKey: "sink:v1:" + step,
			SinkConfig:   map[string]any{"key": "recovery/{{runtime.sink.replay_identity}}/" + step + ".json", "format": "json", "overwrite": true},
			SourceResult: pipelinerun.NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil)),
		}
	}
	a, b := input("write-a"), input("write-b")
	require.NoError(t, runtime.Execute(ctx, a))
	require.ErrorContains(t, runtime.Execute(ctx, b), "simulated crash")
	var firstSuccess time.Time
	const succeededAtQuery = `SELECT pse.succeeded_at FROM pipeline_sink_effects pse
		JOIN project_environments pe ON pe.id=pse.project_environment_id
		JOIN environments e ON e.id=pe.environment_id
		JOIN projects p ON p.id=pe.project_id
		JOIN organizations o ON o.id=p.organization_id
		WHERE o.slug=$1 AND p.slug=$2 AND e.slug=$3
		AND pse.run_slug=$4 AND pse.sink_step_slug=$5 AND pse.idempotency_key=$6`
	require.NoError(t, pool.QueryRow(ctx, succeededAtQuery, org, project, environment, "original", a.SinkStepSlug, a.IdempotencyKey).Scan(&firstSuccess))
	for _, step := range []string{"write-a", "write-b"} {
		replay := input(step)
		replay.RunSlug = "recovery"
		require.NoError(t, runtime.Execute(ctx, replay))
		require.Equal(t, "confirmed", replay.DeliveryOutcome)
	}
	require.Equal(t, map[string]int{"write-a": 1, "write-b": 2}, calls)
	var afterSuccess time.Time
	require.NoError(t, pool.QueryRow(ctx, succeededAtQuery, org, project, environment, "original", a.SinkStepSlug, a.IdempotencyKey).Scan(&afterSuccess))
	require.Equal(t, firstSuccess, afterSuccess, "recovery must not rewrite the completed record")
	wrongEnvironment := input("write-a")
	wrongEnvironment.EnvironmentSlug = "other"
	_, err := durable.BeginOrGet(ctx, org, project, wrongEnvironment.EnvironmentSlug, pipelinerun.SinkEffectAttempt{ReplayIdentity: "original", SinkStepSlug: "write-a", IdempotencyKey: a.IdempotencyKey, IdempotencyClass: "naturally_idempotent"})
	require.ErrorIs(t, err, pipelinesinkeffects.ErrProjectEnvironmentNotFound)
}
