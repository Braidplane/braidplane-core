package pipelinesvc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
)

type noopProofLinks struct{}

func newTestProofLinks() proofreceipts.LifecycleRepository { return noopProofLinks{} }

func (noopProofLinks) FindBySlug(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*proofreceipts.Receipt, error) {
	return nil, proofreceipts.ErrReceiptNotFound
}

func (noopProofLinks) LinkDeployment(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, proofreceipts.LinkDeploymentParams) (*proofreceipts.LifecycleLink, error) {
	return nil, proofreceipts.ErrLifecycleLinkNotFound
}

func (noopProofLinks) LinkRun(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, string, time.Time) (*proofreceipts.LifecycleLink, error) {
	return nil, proofreceipts.ErrLifecycleLinkNotFound
}

func (noopProofLinks) FindDeploymentLink(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*proofreceipts.LifecycleLink, error) {
	return nil, proofreceipts.ErrLifecycleLinkNotFound
}

func (noopProofLinks) FindRunLink(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*proofreceipts.LifecycleLink, error) {
	return nil, proofreceipts.ErrLifecycleLinkNotFound
}

type failingProofRunLinks struct {
	noopProofLinks
	deployment *proofreceipts.LifecycleLink
	err        error
}

func (f failingProofRunLinks) FindDeploymentLink(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*proofreceipts.LifecycleLink, error) {
	return f.deployment, nil
}

func (f failingProofRunLinks) LinkRun(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, string, time.Time) (*proofreceipts.LifecycleLink, error) {
	return nil, f.err
}

func TestSynchronousProofLinkFailureStopsBeforeExecution(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	deployment := &pipelinesrepo.PipelineDeployment{
		ID: 1, PipelineSlug: "sync", EnvironmentSlug: "prod",
		CurrentVersionID: 42, CurrentVersionSpecHash: "hash", DeployedAt: at,
	}
	failure := xerror.Unexpected().Detail("proof link write failed")
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "sync", Name: "Sync"}, Steps: []pipelinespec.Step{
		{Slug: "fetch", Kind: "query", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: "sql", OperationRef: "query"}},
	}}
	runtime := &fakePipelineRuntime{}
	created := false
	svc := &service{
		runtime: runtime, admission: testAdmissionService(), executionClock: fixedClock{at: at},
		pipelineStore: &runtimeBoundaryPipelineStore{deployment: deployment, version: &pipelinesrepo.PipelineVersion{
			ID: 42, PipelineSlug: "sync", SpecHash: "hash", SpecJSONB: mustJSON(t, pipeline),
		}},
		txm: &recordingPipelineGitTxManager{},
		proofLinks: failingProofRunLinks{deployment: &proofreceipts.LifecycleLink{
			SpecHash:   "hash",
			Identifier: deploymentLinkIdentifier("sync", deployment), RetainUntil: at.Add(time.Hour),
		}, err: failure},
		runRepo: &fakeRunRepository{createRunFn: func(_ context.Context, _ postgres.DBTX, org, project, env slug.Slug, params *pipelineruns.CreateRunParams) (*pipelineruns.PipelineRun, error) {
			require.Equal(t, slug.Slug("acme"), org)
			require.Equal(t, defaultProjectSlug, project)
			require.Equal(t, slug.Slug("prod"), env)
			require.Equal(t, int64(42), params.PipelineVersionID)
			require.Equal(t, "queued", params.Status)
			created = true
			return &pipelineruns.PipelineRun{}, nil
		}},
	}
	actor := model.UserEventActor("alice")
	_, err := svc.Execute(context.Background(), "acme", defaultProjectSlug, "prod", "sync", nil, nil, actor,
		pipelinerun.RunSourceSummary{
			Type: model.PipelineRunSourceTypeManual, Summary: "Manual", Entrypoint: model.PipelineRunSourceEntrypointUI,
			ActorType: actor.Type, ActorSlug: actor.Slug,
		}, nil)
	require.ErrorIs(t, err, failure)
	require.True(t, created)
	require.Zero(t, runtime.executeCalls)

	// Trigger intake already owns a running row. The synchronous Proof check
	// must link that row without trying to create a second attempt.
	runSlug := slug.Slug("scheduled")
	source := pipelinerun.NormalizeAttemptSource(pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeSchedule}, runSlug)
	jobID := int64(123)
	timeline := []byte(`{"status":"running"}`)
	pinned := false
	svc.runRepo = &fakeRunRepository{
		findRunBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) (*pipelineruns.PipelineRun, error) {
			return &pipelineruns.PipelineRun{WorkerJobID: &jobID, PipelineSlug: "sync", RunSlug: runSlug.String(), Source: source, Status: "running", StartedAt: at, Timeline: timeline}, nil
		},
		completeRunFn: func(_ context.Context, _ postgres.DBTX, _, _, _ slug.Slug, run string, params *pipelineruns.CompleteRunParams) (*pipelineruns.PipelineRun, error) {
			require.Equal(t, runSlug.String(), run)
			require.Equal(t, "running", params.Status)
			require.EqualValues(t, 42, params.PipelineVersionID)
			require.JSONEq(t, string(timeline), string(params.Timeline))
			pinned = true
			return &pipelineruns.PipelineRun{}, nil
		},
	}
	_, err = svc.executeResolvedVersion(t.Context(), "acme", defaultProjectSlug, "prod", &pipelinesrepo.PipelineVersion{
		ID: 42, PipelineSlug: "sync", SpecHash: "hash", SpecJSONB: mustJSON(t, pipeline),
	}, deployment, &runSlug, model.SystemEventActor(), source, nil, false, nil, pipelinerun.IncrementalRunOptions{}, nil)
	require.ErrorIs(t, err, failure)
	require.True(t, pinned)
	require.Zero(t, runtime.executeCalls)

	links := svc.proofLinks.(failingProofRunLinks)
	links.err = nil
	svc.proofLinks = links
	runtime.executeFn = func(context.Context, pipelineruntime.RunRequest) (*pipelineruntime.RunOutcome, error) {
		return nil, ErrExecutionControlRevision
	}
	_, err = svc.executeResolvedVersion(t.Context(), "acme", defaultProjectSlug, "prod", &pipelinesrepo.PipelineVersion{
		ID: 42, PipelineSlug: "sync", SpecHash: "hash", SpecJSONB: mustJSON(t, pipeline),
	}, deployment, &runSlug, model.SystemEventActor(), source, nil, false, nil, pipelinerun.IncrementalRunOptions{}, nil)
	require.ErrorIs(t, err, ErrExecutionControlRevision)
	require.ErrorIs(t, err, ErrPipelineExecutionNotStarted)
	require.Equal(t, 1, runtime.executeCalls)
}
