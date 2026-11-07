package pipelinetriggersvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
)

func (*fakeTriggerRepository) ListCompletionRunFinalizationsForSystem(context.Context, postgres.DBTX) ([]pipelinetriggers.CompletionDispatchRef, error) {
	panic("unexpected completion run finalization")
}

func (*fakeTriggerRepository) FenceCompletionRetryDecision(context.Context, postgres.DBTX, slug.Slug, int) (time.Time, error) {
	panic("unexpected completion retry decision")
}

func (*fakeTriggerRepository) SaveCompletionRetryExecution(context.Context, postgres.DBTX, slug.Slug, int, slug.Slug, []byte) error {
	panic("unexpected completion retry execution")
}

func (*fakeTriggerRepository) DeferCompletionRunFinalization(context.Context, postgres.DBTX, slug.Slug, int, slug.Slug, string) error {
	panic("unexpected completion run finalization deferral")
}

func (*fakeTriggerRepository) ListPendingCompletionsForSystem(context.Context, postgres.DBTX) ([]pipelinetriggers.CompletionDispatchRef, error) {
	panic("unexpected completion dispatch")
}

func (*fakeTriggerRepository) ListCompletionRepairsForSystem(context.Context, postgres.DBTX) ([]pipelinetriggers.CompletionDispatchRef, error) {
	panic("unexpected completion repair")
}

func (*fakeTriggerRepository) AcceptCompletionSnapshot(context.Context, postgres.DBTX, slug.Slug, int, slug.Slug, []byte) error {
	panic("unexpected completion acceptance")
}

func (*fakeTriggerRepository) DeferCompletionSnapshot(context.Context, postgres.DBTX, slug.Slug, int, string) error {
	panic("unexpected completion deferral")
}

func (*fakeTriggerRepository) CompleteCompletionSnapshotsForSystem(context.Context, postgres.DBTX) error {
	panic("unexpected completion finalization")
}

func TestCompletionGraph(t *testing.T) {
	edge := func(id, from, to string, enabled bool) pipelinetriggers.CompletionEdge {
		return pipelinetriggers.CompletionEdge{TriggerSlug: slug.Slug(id), PipelineSlug: slug.Slug(to), Enabled: enabled, Config: model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: slug.Slug(from)}}
	}
	config := model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "a", Condition: model.PipelineCompletionConditionSuccess, ExecutionGrantEnabled: true}
	tests := []struct {
		name            string
		edges           []pipelinetriggers.CompletionEdge
		trigger, target slug.Slug
		enabled         bool
		wantError       bool
	}{
		{name: "success edge", target: "b", enabled: true},
		{name: "self edge", target: "a", wantError: true},
		{name: "disabled cycle", edges: []pipelinetriggers.CompletionEdge{edge("reverse", "b", "a", false)}, target: "b", enabled: true, wantError: true},
		{name: "transitive cycle", edges: []pipelinetriggers.CompletionEdge{edge("bc", "b", "c", true), edge("ca", "c", "a", true)}, target: "b", wantError: true},
		{name: "replace own edge", edges: []pipelinetriggers.CompletionEdge{edge("edit", "b", "a", true)}, trigger: "edit", target: "b", enabled: true},
		{name: "converging branches", edges: []pipelinetriggers.CompletionEdge{edge("bc", "b", "c", true), edge("bd", "b", "d", true), edge("dc", "d", "c", true)}, target: "b", enabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCompletionGraph(tt.edges, tt.trigger, tt.target, tt.enabled, config)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	edges := make([]pipelinetriggers.CompletionEdge, 10)
	for i := range edges {
		edges[i] = edge("outgoing", "a", "b", true)
	}
	require.Error(t, validateCompletionGraph(edges, "new", "c", true, config))
	require.NoError(t, validateCompletionGraph(edges, "new", "c", false, config))
	edges[0].Enabled = false
	require.NoError(t, validateCompletionGraph(edges, "new", "c", true, config))
}

func TestCompletionTargetRefusesDisabledControlProtectedAndMissingUpstream(t *testing.T) {
	for _, tt := range []struct {
		name                       string
		control, protected, exists bool
	}{
		{name: "control disabled", exists: true},
		{name: "protected", control: true, protected: true, exists: true},
		{name: "missing upstream", control: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeTriggerRepository{protected: tt.protected, pipelineExists: tt.exists}
			svc := &service{repo: repo, completionTriggersEnabled: tt.control}
			err := svc.validateCompletionTarget(context.Background(), postgres.DBTX(nil), "org", "project", "env", "trigger", "b", true, &model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "a"})
			require.Error(t, err)
		})
	}
}

func TestCompletionConfigRejectsCancellationAndMixedTypes(t *testing.T) {
	valid := &model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "a", Condition: model.PipelineCompletionConditionSuccess}
	require.NoError(t, validateTriggerTypeConfig(model.PipelineTriggerTypeCompletion, nil, nil, valid))
	require.Error(t, validateTriggerTypeConfig(model.PipelineTriggerTypeCompletion, nil, nil, nil))
	require.Error(t, validateTriggerTypeConfig(model.PipelineTriggerTypeCompletion, &model.PipelineTriggerScheduleConfig{}, nil, valid))
	require.Error(t, validateTriggerTypeConfig(model.PipelineTriggerTypeWebhook, nil, &model.PipelineTriggerWebhookConfig{}, valid))
	invalid := *valid
	invalid.Condition = model.PipelineCompletionConditionUnknown
	require.Error(t, validateTriggerTypeConfig(model.PipelineTriggerTypeCompletion, nil, nil, &invalid))
}

func TestCompletionSnapshotChecksClosureAndCurrentAuthorityWithoutCancelingAcceptedChild(t *testing.T) {
	authority := model.CompletionAuthoritySnapshot{Valid: true, Permissions: []string{"*:*"}}
	for _, tt := range []struct {
		name, state, reason string
		closure, current    bool
		depth               int
		wantError, refused  bool
	}{
		{name: "admission", state: "pending", closure: true, current: true, depth: 1},
		{name: "capacity recovery", state: "pending", closure: true, current: true, depth: 1, reason: "capacity_waiting"},
		{name: "dispatch recovery", state: "pending", closure: true, current: true, depth: 1, reason: "dispatch_retry_pending"},
		{name: "execution", state: "accepted", closure: true, current: true, depth: 8},
		{name: "closure had no authority", state: "pending", current: true, depth: 1, wantError: true, refused: true},
		{name: "revoked before admission", state: "pending", closure: true, depth: 1, wantError: true, refused: true},
		{name: "revoked before execution", state: "accepted", closure: true, depth: 1, wantError: true},
		{name: "changed revision", state: "pending", closure: true, current: true, depth: 1, reason: "trigger_changed", wantError: true, refused: true},
		{name: "ninth edge", state: "pending", closure: true, current: true, depth: 9, wantError: true, refused: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := &model.PipelineCompletionSnapshot{ID: 1, State: tt.state, CausalDepth: tt.depth, DecisionReason: tt.reason}
			if tt.closure {
				snapshot.ClosureAuthority = authority
			}
			if tt.current {
				snapshot.CurrentAuthority = authority
			}
			repo := &fakeTriggerRepository{snapshot: snapshot}
			svc := &service{completionTriggersEnabled: true, txm: testutil.FakeTransactionManager{}, repo: repo, completionValidator: fakeCompletionValidator{}}
			got, err := svc.CheckCompletionSnapshot(t.Context(), "org", "project", "env", 1)
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, got)
			require.Equal(t, tt.refused, repo.refusalReason != "")
			if tt.state == "accepted" {
				require.Equal(t, "accepted", got.State)
			}
		})
	}
}

func TestCompletionSnapshotTransientValidationFailureRemainsPending(t *testing.T) {
	authority := model.CompletionAuthoritySnapshot{Valid: true, Permissions: []string{"*:*"}}
	transient := xerror.Unavailable(xerror.Pipeline)
	repo := &fakeTriggerRepository{snapshot: &model.PipelineCompletionSnapshot{State: "pending", CausalDepth: 1, ClosureAuthority: authority, CurrentAuthority: authority}}
	svc := &service{completionTriggersEnabled: true, txm: testutil.FakeTransactionManager{}, repo: repo, completionValidator: fakeCompletionValidator{err: transient}}
	_, err := svc.CheckCompletionSnapshot(t.Context(), "org", "project", "env", 1)
	require.ErrorIs(t, err, transient)
	require.Empty(t, repo.refusalReason)
	require.Equal(t, "pending", repo.snapshot.State)
}

func TestExistingCompletionTriggerCanBeDisabledAfterRollbackOrProtection(t *testing.T) {
	for _, protected := range []bool{false, true} {
		repo := &fakeTriggerRepository{protected: protected, pipelineExists: true}
		svc := &service{repo: repo, completionTriggersEnabled: false}
		require.NoError(t, svc.validateCompletionTarget(t.Context(), nil, "org", "project", "env", "existing", "b", false, &model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "a"}))
		err := svc.validateCompletionTarget(t.Context(), nil, "org", "project", "env", "existing", "b", true, &model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "a"})
		require.ErrorContains(t, err, "completion triggers are disabled")
	}
}
