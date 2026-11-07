package pipelinesvc

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/stretchr/testify/require"
)

type recoveryProtectedStore struct {
	*runtimeBoundaryPipelineStore
	protected bool
}

func (s recoveryProtectedStore) ProjectEnvironmentProtection(_ context.Context, _, _, environment slug.Slug) (*model.ProjectEnvironmentProtection, error) {
	return &model.ProjectEnvironmentProtection{EnvironmentSlug: environment, Protected: s.protected}, nil
}

func TestRecoverRunPinsVersionScopeAndRequiresAuditBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "worker-loss", "worker-loss-later-attempt", "unfenced-worker-loss", "platform-failure", "running-worker-loss", "audit-unavailable", "terminal-audit-unavailable", "protected", "version-mismatch", "wrong-scope"} {
		t.Run(mode, func(t *testing.T) {
			at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
			actor := model.UserEventActor(must.Slug("alice"))
			events := &fakePipelineEventRecorder{}
			if mode == "audit-unavailable" {
				events.err = xerror.Unexpected().Detail("audit unavailable")
			}
			versionID := int64(42)
			if mode == "version-mismatch" {
				versionID++
			}
			called := false
			effectIdentity := "original"
			if mode == "worker-loss-later-attempt" {
				effectIdentity = "logical-root"
			}
			fromStep := "read"
			store := recoveryProtectedStore{runtimeBoundaryPipelineStore: &runtimeBoundaryPipelineStore{version: &pipelinesrepo.PipelineVersion{ID: versionID, SpecHash: "pinned-hash", SpecJSONB: []byte(`{"metadata":{"slug":"export","name":"Export"},"steps":[]}`)}}, protected: mode == "protected"}
			svc := &service{
				proofLinks: newTestProofLinks(), admission: testAdmissionService(), pipelineStore: store,
				txm: &recordingPipelineGitTxManager{}, executionClock: fixedClock{at: at}, eventProducer: eventtest.NewProducer(events),
				runRepo: &fakeRunRepository{lockRecoveryFenceFn: func(_ context.Context, _ postgres.DBTX, org, project, environment slug.Slug, run string) error {
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("ops"), project)
					require.Equal(t, slug.Slug("dev"), environment)
					require.Equal(t, "original", run)
					require.Empty(t, events.calls, "fencing must precede recovery intent")
					if mode == "unfenced-worker-loss" {
						return pipelineruns.ErrRunNotFound
					}
					return nil
				}, findRunBySlugFn: func(_ context.Context, _ postgres.DBTX, org, project, environment slug.Slug, run string) (*pipelineruns.PipelineRun, error) {
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("ops"), project)
					require.Equal(t, slug.Slug("dev"), environment)
					if mode == "wrong-scope" {
						return nil, pipelineruns.ErrRunNotFound
					}
					failure := &pipelinerun.FailureContext{Step: "write", Side: "sink"}
					status := "failed"
					if mode == "worker-loss" || mode == "worker-loss-later-attempt" || mode == "unfenced-worker-loss" || mode == "running-worker-loss" {
						failure.Side = "transform"
						failure.ReasonCode = pipelinerun.FailureReasonCodeWorkerOwnershipLost
					}
					if mode == "platform-failure" {
						failure.Side = "transform"
					}
					if mode == "running-worker-loss" {
						status = "running"
					}
					row := &pipelineruns.PipelineRun{PipelineSlug: "export", PipelineVersionID: 42, PipelineVersionSpecHash: "pinned-hash", EnvironmentSlug: "dev", RunSlug: run, Status: status}
					timeline := pipelinerun.RunTimeline{Failure: failure}
					if mode == "worker-loss-later-attempt" {
						previous := "previous-attempt"
						row.ReplayOfRunSlug = &previous
						timeline.SinkReplayIdentity = effectIdentity
					}
					row.Timeline = mustJSON(t, timeline)
					return row, nil
				}},
				runtime: &fakePipelineRuntime{recoverFn: func(_ context.Context, req pipelineruntime.ReplayRequest) (*pipelineruntime.RunOutcome, error) {
					called = true
					require.Len(t, events.calls, 1, "audit intent must precede dispatch")
					require.Equal(t, slug.Slug("acme"), req.OrgSlug)
					require.Equal(t, slug.Slug("ops"), req.ProjectSlug)
					require.Equal(t, slug.Slug("dev"), req.EnvironmentSlug)
					require.Equal(t, int64(42), req.PipelineVersionID)
					require.Equal(t, "original", req.SourceRunSlug)
					if mode == "terminal-audit-unavailable" {
						events.err = xerror.Unexpected().Detail("terminal audit unavailable")
						return &pipelineruntime.RunOutcome{Metadata: pipelineruntime.RunOutcomeMetadata{PipelineVersionID: 42, PipelineSlug: "export", ProjectSlug: "ops", EnvironmentSlug: "dev", RunSlug: req.ReplayRunSlug, Status: "failed", Source: req.Source, ReplayOfRunSlug: &req.SourceRunSlug, ReplayFromStep: &fromStep}}, xerror.Unexpected().Detail("provider unavailable")
					}
					return &pipelineruntime.RunOutcome{Metadata: pipelineruntime.RunOutcomeMetadata{PipelineVersionID: 42, PipelineSlug: "export", ProjectSlug: "ops", EnvironmentSlug: "dev", RunSlug: req.ReplayRunSlug, Status: "succeeded", Source: req.Source, ReplayOfRunSlug: &effectIdentity, ReplayFromStep: &fromStep}}, nil
				}},
			}
			run, err := svc.RecoverRun(t.Context(), "acme", "ops", "dev", "original", nil, actor, testRunSource(actor))
			if mode == "terminal-audit-unavailable" {
				require.ErrorContains(t, err, "terminal audit unavailable")
				require.True(t, called)
				require.Nil(t, run, "audit failure must not be accepted as a normal failed recovery")
				return
			}
			if mode != "success" && mode != "worker-loss" && mode != "worker-loss-later-attempt" {
				require.Error(t, err)
				if mode == "unfenced-worker-loss" {
					require.ErrorIs(t, err, pipelineruns.ErrRunNotFound)
					require.Contains(t, xerror.PublicDetail(err), "retained queue evidence")
				}
				require.False(t, called)
				require.Nil(t, run)
				return
			}
			require.NoError(t, err)
			require.True(t, called)
			require.Len(t, events.calls, 2)
			for _, event := range events.calls {
				require.Equal(t, model.EventClassSecurityAudit, event.EventClass)
				require.Equal(t, actor.Slug, event.ActorSlug)
				require.Equal(t, effectIdentity, event.Metadata["replay_of_run_slug"])
				require.Equal(t, "pinned-hash", event.Metadata["pipeline_version_spec_hash"])
			}
		})
	}
}
