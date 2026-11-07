package pipelinesvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinecreations"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/stretchr/testify/require"
)

type creationMemoryRepo struct {
	row  *pipelinecreations.Row
	list func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, int) ([]*pipelinecreations.Row, error)
}

func (r *creationMemoryRepo) List(ctx context.Context, db postgres.DBTX, org, project, after slug.Slug, limit int) ([]*pipelinecreations.Row, error) {
	return r.list(ctx, db, org, project, after, limit)
}

func (r *creationMemoryRepo) Create(_ context.Context, _ postgres.DBTX, _, _ slug.Slug, row *pipelinecreations.Row) (bool, error) {
	if r.row != nil {
		return false, nil
	}
	saved := *row
	saved.Status = "publish_queued"
	r.row = &saved
	return true, nil
}

func (r *creationMemoryRepo) Get(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*pipelinecreations.Row, error) {
	if r.row == nil {
		return nil, pipelinecreations.ErrNotFound
	}
	saved := *r.row
	return &saved, nil
}

func (r *creationMemoryRepo) Find(ctx context.Context, db postgres.DBTX, o, p, _ slug.Slug) (*pipelinecreations.Row, error) {
	return r.Get(ctx, db, o, p, "")
}

func (r *creationMemoryRepo) Update(_ context.Context, _ postgres.DBTX, _, _ slug.Slug, row *pipelinecreations.Row) (bool, error) {
	if row.PublishAttemptID != r.row.PublishAttemptID || (r.row.Status == "proposal_open" || r.row.Status == "branch_pushed") {
		return false, nil
	}
	r.row.Status, r.row.Result, r.row.FailureCode = row.Status, row.Result, row.FailureCode
	return true, nil
}

func creationFixture(t *testing.T, auth authorization.Service) (*CreationService, *creationMemoryRepo, *fakeServicePipelineStore, *recordingPipelineGitProducer, *model.Project) {
	t.Helper()
	repo, store, producer := &creationMemoryRepo{}, &fakeServicePipelineStore{}, &recordingPipelineGitProducer{}
	project := &model.Project{Slug: "project", PipelineBackend: &model.ProjectPipelineBackend{DataSourceSlug: "git-source", TrackedBranch: "main", PipelineDir: "pipelines"}}
	svc, err := NewCreationService(testutil.FakeTransactionManager{}, repo, fakeProjectBackendService{project: project}, store, auth, producer, eventtest.NewProducer(&fakePipelineEventRecorder{}))
	require.NoError(t, err)
	return svc, repo, store, producer, project
}

func TestCreationListBoundsAndVerifiesRecordedDefinitions(t *testing.T) {
	svc, repo, _, _, _ := creationFixture(t, allowAuthorizationService{})
	created, err := svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), testPipelineSpec())
	require.NoError(t, err)
	repo.list = func(_ context.Context, _ postgres.DBTX, org, project, after slug.Slug, limit int) ([]*pipelinecreations.Row, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("project"), project)
		require.Equal(t, slug.Slug("creation-before"), after)
		require.Equal(t, 2, limit)
		return []*pipelinecreations.Row{repo.row, nil}, nil
	}
	page, err := svc.List(t.Context(), "acme", "project", "creation-before", 1)
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.Len(t, page.Items, 1)
	require.Equal(t, created.Slug, page.Items[0].Slug)
	require.Equal(t, created.SpecHash, page.Items[0].SpecHash)
	for _, limit := range []int{0, -1, 101} {
		_, err := svc.List(t.Context(), "acme", "project", "", limit)
		require.Error(t, err)
	}
	repo.row.Request = []byte(`{}`)
	_, err = svc.List(t.Context(), "acme", "project", "creation-before", 1)
	require.ErrorContains(t, err, "identity is inconsistent")
}

func TestCreationProposalPublishesWithoutCanonicalPipelineAndResumesExactContent(t *testing.T) {
	t.Parallel()
	svc, repo, store, producer, _ := creationFixture(t, allowAuthorizationService{})
	spec := testPipelineSpec()
	created, err := svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), spec)
	require.NoError(t, err)
	require.Equal(t, "publish_queued", created.Status)
	require.Equal(t, created.Slug, producer.publishArgs.CreationSlug)
	require.Equal(t, "pipelines/acme-users-sync.pipeline.yaml", producer.publishArgs.Path)
	require.Zero(t, store.findSpecCalls)
	args := producer.publishArgs
	require.NoError(t, svc.AuthorizeProposalPublish(t.Context(), args))
	require.NoError(t, svc.FailProposal(t.Context(), pipelinegit.ProposalFailure{CreationSlug: created.Slug, OrganizationSlug: "acme", ProjectSlug: "project", PipelineSlug: spec.Metadata.Slug, MemberSlug: "alice", PublishAttemptID: args.PublishAttemptID, FailureCode: "provider_unavailable"}))
	require.Equal(t, "publish_failed", repo.row.Status)
	retry, err := svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), spec)
	require.NoError(t, err)
	require.Equal(t, created.Slug, retry.Slug)
	require.Equal(t, args, producer.publishArgs)
	require.Empty(t, retry.FailureCode)
	changed := testPipelineSpec()
	changed.Metadata.Name = "Different definition"
	_, err = svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), changed)
	require.ErrorContains(t, err, "another creation proposal")
	_, err = svc.Create(t.Context(), "acme", "project", "bob", model.UserEventActor("bob"), spec)
	require.ErrorContains(t, err, "another creation proposal")
	completion := pipelinegit.ProposalCompletion{CreationSlug: created.Slug, OrganizationSlug: "acme", ProjectSlug: "project", PipelineSlug: spec.Metadata.Slug, MemberSlug: "alice", PublishAttemptID: args.PublishAttemptID, DraftSpecHash: args.DraftSpecHash, Authority: args.Authority, BranchName: args.BranchName, BranchHeadSHA: "synthetic-head", Provider: "github", ProposalID: "42", ProposalURL: "https://github.com/customer/repo/pull/42", ProposalState: "open"}
	wrong := completion
	wrong.DraftSpecHash = "other"
	require.ErrorIs(t, svc.CompleteProposal(t.Context(), wrong), ErrPipelineProposalAuthorityDenied)
	require.NoError(t, svc.CompleteProposal(t.Context(), completion))
	require.NoError(t, svc.CompleteProposal(t.Context(), completion))
	conflicting := completion
	conflicting.ProposalID = "other-proposal"
	require.ErrorIs(t, svc.CompleteProposal(t.Context(), conflicting), ErrPipelineProposalAuthorityDenied)
	require.NoError(t, svc.FailProposal(t.Context(), pipelinegit.ProposalFailure{CreationSlug: created.Slug, OrganizationSlug: "acme", ProjectSlug: "project", PipelineSlug: spec.Metadata.Slug, MemberSlug: "alice", PublishAttemptID: args.PublishAttemptID, FailureCode: "late-failure"}))
	require.Equal(t, "proposal_open", repo.row.Status)
	producer.publishArgs = jobs.PipelineGitPublishProposalArgs{}
	again, err := svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), spec)
	require.NoError(t, err)
	require.Equal(t, "proposal_open", again.Status)
	require.Equal(t, completion.ProposalURL, again.Proposal.ProposalURL)
	require.Empty(t, producer.publishArgs.CreationSlug)
}

func TestCreationPreservesWideIntegerThroughProposalInspectionAndRetry(t *testing.T) {
	svc, _, _, producer, _ := creationFixture(t, allowAuthorizationService{})
	raw, err := json.Marshal(testPipelineSpec())
	require.NoError(t, err)
	spec, err := pipelinespec.Load("first.pipeline.json", raw)
	require.NoError(t, err)
	spec.Steps[0].Query.Parameters = map[string]any{"id": int64(9007199254740993)}
	created, err := svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), spec)
	require.NoError(t, err)
	inspected, err := svc.Get(t.Context(), "acme", "project", created.Slug)
	require.NoError(t, err)
	require.Equal(t, int64(9007199254740993), inspected.Spec.Steps[0].Query.Parameters["id"])
	storedJSON, err := json.Marshal(inspected.Spec)
	require.NoError(t, err)
	reloaded, err := pipelineFromJSON(storedJSON)
	require.NoError(t, err)
	require.Equal(t, int64(9007199254740993), reloaded.Steps[0].Query.Parameters["id"])
	args := producer.publishArgs
	retry, err := svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), reloaded)
	require.NoError(t, err)
	require.Equal(t, created.Slug, retry.Slug)
	require.Equal(t, args, producer.publishArgs)
}

func TestCreationInspectionReturnsImmutableProposedDefinitionAndRejectsHashMismatch(t *testing.T) {
	t.Parallel()
	svc, repo, _, _, _ := creationFixture(t, allowAuthorizationService{})
	created, err := svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), testPipelineSpec())
	require.NoError(t, err)
	originalName := created.Spec.Metadata.Name
	created.Spec.Metadata.Name = "Caller mutation"
	inspected, err := svc.Get(t.Context(), "acme", "project", created.Slug)
	require.NoError(t, err)
	require.Equal(t, originalName, inspected.Spec.Metadata.Name)
	var args jobs.PipelineGitPublishProposalArgs
	require.NoError(t, json.Unmarshal(repo.row.Request, &args))
	args.DraftSpecHash = "substituted-hash"
	repo.row.Request, err = json.Marshal(args)
	require.NoError(t, err)
	_, err = svc.Get(t.Context(), "acme", "project", created.Slug)
	require.ErrorContains(t, err, "does not match its recorded identity")
}

func TestCreationProposalRechecksMemberRepositoryContentAndAuthority(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"permission", "content", "member", "repository", "authority", "canonical-exists", "already-published"} {
		t.Run(scenario, func(t *testing.T) {
			allowed := true
			auth := allowAuthorizationService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, model.Create, input.Action)
				return allowed, nil
			}}
			svc, repo, store, producer, project := creationFixture(t, auth)
			_, err := svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), testPipelineSpec())
			require.NoError(t, err)
			args := producer.publishArgs
			switch scenario {
			case "permission":
				allowed = false
			case "content":
				args.Content = []byte("different")
			case "member":
				args.MemberSlug = "bob"
			case "repository":
				project.PipelineBackend.DataSourceSlug = "another-source"
			case "authority":
				store.resolveProposalAuthorityFn = func(context.Context, slug.Slug, slug.Slug) (model.PipelineProposalAuthority, error) {
					changed := testProposalAuthority()
					changed.ID++
					return changed, nil
				}
			case "canonical-exists":
				store.existsFn = func(context.Context, slug.Slug, slug.Slug, slug.Slug) (bool, error) { return true, nil }
			case "already-published":
				repo.row.Status = "proposal_open"
			}
			require.ErrorIs(t, svc.AuthorizeProposalPublish(t.Context(), args), ErrPipelineProposalAuthorityDenied)
		})
	}
}

func TestCreationRejectsMachinePrincipalAndExistingCanonicalPipeline(t *testing.T) {
	t.Parallel()
	svc, repo, store, producer, _ := creationFixture(t, allowAuthorizationService{})
	_, err := svc.Create(t.Context(), "acme", "project", "", model.APIKeyEventActor("machine"), testPipelineSpec())
	require.Error(t, err)
	require.Nil(t, repo.row)
	store.existsFn = func(context.Context, slug.Slug, slug.Slug, slug.Slug) (bool, error) { return true, nil }
	_, err = svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), testPipelineSpec())
	require.ErrorContains(t, err, "pipeline already exists")
	require.Nil(t, repo.row)
	require.Empty(t, producer.publishArgs.CreationSlug)
}

func TestPlainGitCreationRecordsBranchAndKeepsRetryImmutable(t *testing.T) {
	svc, repo, store, producer, _ := creationFixture(t, allowAuthorizationService{})
	authority := model.PipelineProposalAuthority{ID: 7, PublicationMode: model.PipelineProposalModeRepositoryCredential, ExecutingPrincipalDisplay: "Configured Git connection", AuthoritySource: "git_data_source_credential", GitConfigurationHash: "configuration-one", GitRepository: "git.example.test/acme/pipelines", GitBaseRef: "refs/heads/main", GitDataSourceSlug: "git-source", GitCredentialSlug: "ssh-key", GitCredentialRevision: "revision-one"}
	store.resolveProposalAuthorityFn = func(context.Context, slug.Slug, slug.Slug) (model.PipelineProposalAuthority, error) {
		return authority, nil
	}
	spec := testPipelineSpec()
	created, err := svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), spec)
	require.NoError(t, err)
	args := producer.publishArgs
	require.NoError(t, svc.AuthorizeProposalPublish(t.Context(), args))
	authority.GitCredentialRevision = "revision-two"
	require.ErrorIs(t, svc.AuthorizeProposalPublish(t.Context(), args), ErrPipelineProposalAuthorityDenied)
	authority.GitCredentialRevision = "revision-one"
	completion := pipelinegit.ProposalCompletion{CreationSlug: created.Slug, OrganizationSlug: "acme", ProjectSlug: "project", PipelineSlug: spec.Metadata.Slug, MemberSlug: "alice", PublishAttemptID: args.PublishAttemptID, DraftSpecHash: args.DraftSpecHash, Authority: args.Authority, BranchName: args.BranchName, BranchHeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Provider: "git"}
	require.NoError(t, svc.CompleteProposal(t.Context(), completion))
	require.NoError(t, svc.CompleteProposal(t.Context(), completion))
	require.Equal(t, "branch_pushed", repo.row.Status)
	require.NoError(t, svc.FailProposal(t.Context(), pipelinegit.ProposalFailure{CreationSlug: created.Slug, OrganizationSlug: "acme", ProjectSlug: "project", PipelineSlug: spec.Metadata.Slug, MemberSlug: "alice", PublishAttemptID: args.PublishAttemptID, FailureCode: "late-failure"}))
	require.Equal(t, "branch_pushed", repo.row.Status)
	producer.publishArgs = jobs.PipelineGitPublishProposalArgs{}
	again, err := svc.Create(t.Context(), "acme", "project", "alice", model.UserEventActor("alice"), spec)
	require.NoError(t, err)
	require.Equal(t, "branch_pushed", again.Status)
	require.Empty(t, producer.publishArgs.CreationSlug)
	require.Empty(t, again.Proposal.ProposalID)
	require.Empty(t, again.Proposal.ProposalURL)
}
