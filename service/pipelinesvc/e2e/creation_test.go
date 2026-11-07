package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelinecreations"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/require"
)

type creationE2EProjectService struct {
	backend *model.ProjectPipelineBackend
}

func (s creationE2EProjectService) GetProject(_ context.Context, _, project slug.Slug) (*model.Project, error) {
	return &model.Project{Slug: project, PipelineBackend: s.backend}, nil
}

type creationE2ELifecycleUpdates struct{}

func (creationE2ELifecycleUpdates) CompleteProposal(context.Context, pipelinegit.ProposalCompletion) error {
	return nil
}

func (creationE2ELifecycleUpdates) FailProposal(context.Context, pipelinegit.ProposalFailure) error {
	return nil
}

func (creationE2ELifecycleUpdates) CompleteProposalRefresh(context.Context, pipelinegit.ProposalRefreshCompletion) error {
	return nil
}

func (creationE2ELifecycleUpdates) FailProposalRefresh(context.Context, pipelinegit.ProposalRefreshFailure) error {
	return nil
}

func TestCreationServicePersistsExactRetryAndImmutableCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-Postgres pipeline creation e2e test in short mode")
	}

	ctx := t.Context()
	pool := openE2EPool(t)
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)

	unique := time.Now().UnixNano()
	org := must.Slug(fmt.Sprintf("creation-e2e-org-%d", unique))
	member := must.Slug(fmt.Sprintf("creation-e2e-member-%d", unique))
	otherMember := must.Slug(fmt.Sprintf("creation-e2e-other-member-%d", unique))
	source := must.Slug("creation-e2e-git")
	pipeline := must.Slug("creation-e2e-pipeline")
	seedE2EOrg(t, pool, org)
	seedE2EMember(t, pool, org, member)
	seedE2EDataSource(t, pool, org, source)
	seedE2EProposalAuthority(t, pool, org, source)

	producer := &recordingPublishPipelineGitProducer{}
	creations, err := pipelinesvc.NewCreationService(
		txm,
		pipelinecreations.New(),
		creationE2EProjectService{backend: &model.ProjectPipelineBackend{DataSourceSlug: source, TrackedBranch: "main", PipelineDir: "pipelines"}},
		store,
		allowAuthorizationService{},
		producer,
		eventtest.NewProducer(nil),
	)
	require.NoError(t, err)
	lifecycle, err := pipelinesvc.NewProposalLifecycle(creationE2ELifecycleUpdates{}, creations, creations)
	require.NoError(t, err)

	spec := ownershipPipeline(pipeline)
	created, err := creations.Create(ctx, org, defaultProjectSlug, member, model.UserEventActor(member), spec)
	require.NoError(t, err)
	require.Equal(t, "publish_queued", created.Status)
	require.Equal(t, 1, producer.publishCalls)
	queued := producer.publishArgs
	require.Equal(t, created.Slug, queued.CreationSlug)
	require.Equal(t, pipeline, queued.PipelineSlug)
	require.NoError(t, lifecycle.AuthorizeProposalPublish(ctx, queued))
	resumed, err := creations.Create(ctx, org, defaultProjectSlug, member, model.UserEventActor(member), spec)
	require.NoError(t, err)
	require.Equal(t, created.Slug, resumed.Slug)
	require.Equal(t, 1, producer.publishCalls, "resuming queued progress must not enqueue duplicate work")
	page, err := creations.List(ctx, org, defaultProjectSlug, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, created.Slug, page.Items[0].Slug)

	stored, err := pipelinecreations.New().Get(ctx, pool, org, defaultProjectSlug, created.Slug)
	require.NoError(t, err)
	require.Equal(t, "publish_queued", stored.Status)
	require.Equal(t, queued.PublishAttemptID, stored.PublishAttemptID)
	require.NotEmpty(t, stored.Request)
	exists, err := store.Exists(ctx, org, defaultProjectSlug, pipeline)
	require.NoError(t, err)
	require.False(t, exists)

	require.NoError(t, lifecycle.FailProposal(ctx, pipelinegit.ProposalFailure{
		CreationSlug: created.Slug, OrganizationSlug: org, ProjectSlug: defaultProjectSlug,
		PipelineSlug: pipeline, MemberSlug: member, PublishAttemptID: queued.PublishAttemptID,
		FailureCode: "provider_unavailable",
	}))
	failed, err := creations.Get(ctx, org, defaultProjectSlug, created.Slug)
	require.NoError(t, err)
	require.Equal(t, "publish_failed", failed.Status)
	require.Equal(t, "provider_unavailable", failed.FailureCode)

	retried, err := creations.Create(ctx, org, defaultProjectSlug, member, model.UserEventActor(member), spec)
	require.NoError(t, err)
	require.Equal(t, created.Slug, retried.Slug)
	require.Equal(t, "publish_queued", retried.Status)
	require.Equal(t, 2, producer.publishCalls)
	require.Equal(t, queued, producer.publishArgs)
	require.NoError(t, lifecycle.AuthorizeProposalPublish(ctx, producer.publishArgs))
	stale := queued
	stale.PublishAttemptID = "unrelated-attempt"
	require.ErrorIs(t, lifecycle.AuthorizeProposalPublish(ctx, stale), pipelinesvc.ErrPipelineProposalAuthorityDenied)
	_, err = pool.Exec(ctx, `UPDATE pipeline_proposal_authorities a SET revoked_at=now()
		FROM organizations o WHERE a.organization_id=o.id AND o.slug=$1`, org)
	require.NoError(t, err)
	require.ErrorIs(t, lifecycle.AuthorizeProposalPublish(ctx, queued), pipelinesvc.ErrPipelineProposalAuthorityDenied)
	_, err = pool.Exec(ctx, `UPDATE pipeline_proposal_authorities a SET revoked_at=NULL
		FROM organizations o WHERE a.organization_id=o.id AND o.slug=$1`, org)
	require.NoError(t, err)
	require.NoError(t, lifecycle.AuthorizeProposalPublish(ctx, queued))

	changed := ownershipPipeline(pipeline)
	changed.Metadata.Name = "Changed definition"
	_, err = creations.Create(ctx, org, defaultProjectSlug, member, model.UserEventActor(member), changed)
	require.ErrorContains(t, err, "another creation proposal")
	_, err = creations.Create(ctx, org, defaultProjectSlug, otherMember, model.UserEventActor(otherMember), spec)
	require.ErrorContains(t, err, "another creation proposal")

	completion := pipelinegit.ProposalCompletion{
		CreationSlug: created.Slug, OrganizationSlug: org, ProjectSlug: defaultProjectSlug,
		PipelineSlug: pipeline, MemberSlug: member, PublishAttemptID: queued.PublishAttemptID,
		DraftSpecHash: queued.DraftSpecHash, Authority: queued.Authority, BranchName: queued.BranchName,
		BranchHeadSHA: "e2e-head", Provider: "github", ProposalID: "42",
		ProposalURL: "https://github.com/Braidplane/acme-pipelines/pull/42", ProposalState: "open",
	}
	require.NoError(t, lifecycle.CompleteProposal(ctx, completion))
	require.NoError(t, lifecycle.CompleteProposal(ctx, completion))
	require.ErrorIs(t, lifecycle.AuthorizeProposalPublish(ctx, queued), pipelinesvc.ErrPipelineProposalAuthorityDenied)
	completed, err := creations.Get(ctx, org, defaultProjectSlug, created.Slug)
	require.NoError(t, err)
	require.Equal(t, "proposal_open", completed.Status)
	require.Equal(t, completion.ProposalURL, completed.Proposal.ProposalURL)
	require.Empty(t, completed.FailureCode)

	lateFailure := pipelinegit.ProposalFailure{
		CreationSlug: created.Slug, OrganizationSlug: org, ProjectSlug: defaultProjectSlug,
		PipelineSlug: pipeline, MemberSlug: member, PublishAttemptID: queued.PublishAttemptID,
		FailureCode: "late_failure",
	}
	require.NoError(t, lifecycle.FailProposal(ctx, lateFailure))
	conflicting := completion
	conflicting.ProposalID = "43"
	require.ErrorIs(t, lifecycle.CompleteProposal(ctx, conflicting), pipelinesvc.ErrPipelineProposalAuthorityDenied)
	immutable, err := creations.Get(ctx, org, defaultProjectSlug, created.Slug)
	require.NoError(t, err)
	require.Equal(t, "proposal_open", immutable.Status)
	require.Equal(t, completion.ProposalID, immutable.Proposal.ProposalID)
	require.Empty(t, immutable.FailureCode)

	otherOrg := must.Slug(fmt.Sprintf("creation-e2e-other-org-%d", unique))
	seedE2EOrg(t, pool, otherOrg)
	_, err = creations.Get(ctx, otherOrg, defaultProjectSlug, created.Slug)
	require.ErrorIs(t, err, pipelinecreations.ErrNotFound)
	otherPage, err := creations.List(ctx, otherOrg, defaultProjectSlug, "", 10)
	require.NoError(t, err)
	require.Empty(t, otherPage.Items)

	otherProject := must.Slug("other")
	_, err = pool.Exec(ctx, `INSERT INTO projects (organization_id, name, slug)
		SELECT id, 'Other project', $2 FROM organizations WHERE slug = $1`, org, otherProject)
	require.NoError(t, err)
	_, err = creations.Get(ctx, org, otherProject, created.Slug)
	require.ErrorIs(t, err, pipelinecreations.ErrNotFound)
	projectPage, err := creations.List(ctx, org, otherProject, "", 10)
	require.NoError(t, err)
	require.Empty(t, projectPage.Items)
}
