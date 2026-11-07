package proofwebhooksvc

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	proofrepo "github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/proofcandidatesvc"
)

func TestProcessResolvesTrustedTargetsAndDispatchesLifecycle(t *testing.T) {
	t.Parallel()
	resolver := &fakeMappingResolver{mappings: []proofrepo.Mapping{
		webhookMapping("acme", "pipelines/z.yaml"),
		webhookMapping("acme", "pipelines/a.yaml"),
		webhookMapping("acme", "pipelines/z.yaml"),
	}}
	resolver.mappings[2].ID = 2
	resolver.mappings[2].EnvironmentSlug = "production"
	candidates := &fakeCandidates{}
	compiler := &fakeCompiler{}
	svc, err := New(testutil.FakeTransactionManager{}, resolver, resolver, candidates, compiler, &fakeDeliveryProducer{}, githubBinding("acme"), fakeStatusPublisher{}, false)
	require.NoError(t, err)

	event := webhookEvent(gitprovider.ChangeActionUpsert)
	require.NoError(t, svc.Process(t.Context(), event))
	require.Len(t, candidates.ingested, 3)
	require.Equal(t, must.Slug("acme"), candidates.ingested[0].organization)
	require.Equal(t, "pipelines/a.yaml", candidates.ingested[0].change.PipelinePath)
	require.Equal(t, "pipelines/z.yaml", candidates.ingested[1].change.PipelinePath)
	require.Equal(t, []slug.Slug{"candidate-1", "candidate-2", "candidate-3"}, compiler.candidates)

	event.Action = gitprovider.ChangeActionClose
	event.DeliveryID = "delivery-2"
	require.NoError(t, svc.Process(t.Context(), event))
	require.Len(t, candidates.closed, 3)
	require.Equal(t, event.HeadSHA, candidates.closed[0].change.HeadSHA)
	require.Equal(t, "pipelines/a.yaml", candidates.closed[0].change.PipelinePath)
	require.Equal(t, "pipelines/z.yaml", candidates.closed[1].change.PipelinePath)
}

func TestProcessIngestsAndAttemptsEveryCandidateBeforeReturningCompileFailures(t *testing.T) {
	t.Parallel()
	firstErr := xerror.Unexpected().Detail("first compile failed")
	secondErr := xerror.Unexpected().Detail("second compile failed")
	resolver := &fakeMappingResolver{mappings: []proofrepo.Mapping{
		webhookMapping("acme", "pipelines/a.yaml"), webhookMapping("acme", "pipelines/b.yaml"),
	}}
	candidates := &fakeCandidates{}
	compiler := &fakeCompiler{errors: map[slug.Slug]error{"candidate-1": firstErr, "candidate-2": secondErr}}
	svc, err := New(testutil.FakeTransactionManager{}, resolver, resolver, candidates, compiler, &fakeDeliveryProducer{}, githubBinding("acme"), fakeStatusPublisher{}, false)
	require.NoError(t, err)

	err = svc.Process(t.Context(), webhookEvent(gitprovider.ChangeActionUpsert))
	require.ErrorIs(t, err, firstErr)
	require.ErrorIs(t, err, secondErr)
	require.Len(t, candidates.ingested, 2)
	require.Equal(t, []slug.Slug{"candidate-1", "candidate-2"}, compiler.candidates)
}

func TestProcessPublishesCompiledReceiptOnlyWhenEnabled(t *testing.T) {
	t.Parallel()
	resolver := &fakeMappingResolver{mappings: []proofrepo.Mapping{webhookMapping("acme", "pipelines/proof.yaml")}}
	publisher := &recordingStatusPublisher{}
	svc, err := New(testutil.FakeTransactionManager{}, resolver, resolver, &fakeCandidates{}, &fakeCompiler{}, &fakeDeliveryProducer{}, githubBinding("acme"), publisher, true)
	require.NoError(t, err)
	require.NoError(t, svc.Process(t.Context(), webhookEvent(gitprovider.ChangeActionUpsert)))
	require.Len(t, publisher.receipts, 1)
}

func TestProcessAttemptsEveryPublicationBeforeReturningFailures(t *testing.T) {
	t.Parallel()
	publishErr := xerror.Unexpected().Detail("publish failed")
	resolver := &fakeMappingResolver{mappings: []proofrepo.Mapping{
		webhookMapping("acme", "pipelines/a.yaml"), webhookMapping("acme", "pipelines/b.yaml"),
	}}
	publisher := &recordingStatusPublisher{err: publishErr}
	svc, err := New(testutil.FakeTransactionManager{}, resolver, resolver, &fakeCandidates{}, &fakeCompiler{}, &fakeDeliveryProducer{}, githubBinding("acme"), publisher, true)
	require.NoError(t, err)
	err = svc.Process(t.Context(), webhookEvent(gitprovider.ChangeActionUpsert))
	require.ErrorIs(t, err, publishErr)
	require.Len(t, publisher.receipts, 2)
}

func TestProcessFailsClosedForProviderAndTenantSubstitution(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		mappings []proofrepo.Mapping
		mutate   func(*gitprovider.VerifiedChangeEvent)
	}{
		{name: "Installation", mappings: []proofrepo.Mapping{webhookMapping("acme", "pipelines/proof.yaml")}, mutate: func(event *gitprovider.VerifiedChangeEvent) { event.ProviderConnectionID = "other" }},
		{name: "Repository", mappings: []proofrepo.Mapping{webhookMapping("acme", "pipelines/proof.yaml")}, mutate: func(event *gitprovider.VerifiedChangeEvent) { event.RepositoryID = "other" }},
		{name: "GitLabTenant", mappings: []proofrepo.Mapping{webhookMapping("acme", "pipelines/proof.yaml")}, mutate: func(event *gitprovider.VerifiedChangeEvent) {
			event.Provider = repostore.ProviderGitLab
			event.OrganizationSlug = must.Slug("other")
		}},
		{name: "CrossTenant", mappings: []proofrepo.Mapping{webhookMapping("acme", "pipelines/proof.yaml"), webhookMapping("other", "pipelines/proof.yaml")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			event := webhookEvent(gitprovider.ChangeActionUpsert)
			if tc.mutate != nil {
				tc.mutate(&event)
			}
			candidates := &fakeCandidates{}
			resolver := &fakeMappingResolver{mappings: tc.mappings}
			svc, err := New(testutil.FakeTransactionManager{}, resolver, resolver, candidates, &fakeCompiler{}, &fakeDeliveryProducer{}, githubBinding("acme"), fakeStatusPublisher{}, false)
			require.NoError(t, err)
			require.ErrorIs(t, svc.Process(t.Context(), event), ErrDeliveryRejected)
			require.Empty(t, candidates.ingested)
		})
	}
}

func TestProcessRejectsGitHubBindingResolutionFailures(t *testing.T) {
	t.Parallel()
	for _, resolver := range []fakeGitHubBindingResolver{
		{err: xerror.Unexpected().Detail("resolver unavailable")},
		{},
		{organization: must.Slug("other")},
	} {
		candidates := &fakeCandidates{}
		mappings := &fakeMappingResolver{mappings: []proofrepo.Mapping{webhookMapping("acme", "pipelines/proof.yaml")}}
		svc, err := New(testutil.FakeTransactionManager{}, mappings, mappings, candidates, &fakeCompiler{}, &fakeDeliveryProducer{}, resolver, fakeStatusPublisher{}, false)
		require.NoError(t, err)
		require.ErrorIs(t, svc.Process(t.Context(), webhookEvent(gitprovider.ChangeActionUpsert)), ErrDeliveryRejected)
		require.Empty(t, candidates.ingested)
		require.Empty(t, candidates.closed)
	}
}

func TestAcceptHandsOnlyVerifiedIdentityToDurableProducer(t *testing.T) {
	t.Parallel()
	producer := &fakeDeliveryProducer{}
	resolver := &fakeMappingResolver{}
	svc, err := New(testutil.FakeTransactionManager{}, resolver, resolver, &fakeCandidates{}, &fakeCompiler{}, producer, githubBinding("acme"), fakeStatusPublisher{}, false)
	require.NoError(t, err)
	event := webhookEvent(gitprovider.ChangeActionUpsert)
	queued, err := svc.Accept(t.Context(), event)
	require.NoError(t, err)
	require.Equal(t, "queued", queued.State)
	require.Equal(t, event, producer.event)
}

func TestAcceptRejectsDeliveryIdentityReusedWithDifferentBytes(t *testing.T) {
	t.Parallel()
	producer := &fakeDeliveryProducer{}
	resolver := &fakeMappingResolver{claimErr: proofrepo.ErrWebhookDeliveryDigestMismatch}
	svc, err := New(testutil.FakeTransactionManager{}, resolver, resolver, &fakeCandidates{}, &fakeCompiler{}, producer, githubBinding("acme"), fakeStatusPublisher{}, false)
	require.NoError(t, err)

	_, err = svc.Accept(t.Context(), webhookEvent(gitprovider.ChangeActionUpsert))
	require.ErrorIs(t, err, ErrDeliveryRejected)
	require.Empty(t, producer.event.DeliveryID)
}

func TestAcceptDoesNotEnqueueIdenticalReplay(t *testing.T) {
	t.Parallel()
	producer := &fakeDeliveryProducer{}
	resolver := &fakeMappingResolver{duplicateDelivery: true}
	svc, err := New(testutil.FakeTransactionManager{}, resolver, resolver, &fakeCandidates{}, &fakeCompiler{}, producer, githubBinding("acme"), fakeStatusPublisher{}, false)
	require.NoError(t, err)

	queued, err := svc.Accept(t.Context(), webhookEvent(gitprovider.ChangeActionUpsert))
	require.NoError(t, err)
	require.Empty(t, queued)
	require.Empty(t, producer.event.DeliveryID)
}

func TestProofWebhookRequiresGitHubBindingResolver(t *testing.T) {
	t.Parallel()
	resolver := &fakeMappingResolver{}
	_, err := New(testutil.FakeTransactionManager{}, resolver, resolver, &fakeCandidates{}, &fakeCompiler{}, &fakeDeliveryProducer{}, nil, fakeStatusPublisher{}, false)
	require.ErrorContains(t, err, "dependencies are required")
}

func webhookEvent(action gitprovider.ChangeAction) gitprovider.VerifiedChangeEvent {
	return gitprovider.VerifiedChangeEvent{
		DeliveryID: "delivery-1", BodySHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Action: action, Provider: repostore.ProviderGitHub,
		ProviderConnectionID: "9001", RepositoryID: "42", ChangeRequestID: "7",
		HeadSHA:   "a123456789012345678901234567890123456789",
		SourceRef: "refs/heads/feature", TargetRef: "refs/heads/main",
	}
}

func webhookMapping(organization, path string) proofrepo.Mapping {
	return proofrepo.Mapping{
		ID: 1, OrganizationSlug: must.Slug(organization), ProjectSlug: must.Slug("data"),
		EnvironmentSlug: must.Slug("staging"), ProviderConnectionSlug: must.Slug("github"),
		Provider: "github", ProviderConnectionID: "9001", RepositoryID: "42",
		TargetRef: "refs/heads/main", PipelinePath: path,
	}
}

type fakeMappingResolver struct {
	mappings          []proofrepo.Mapping
	duplicateDelivery bool
	claimErr          error
}

type fakeGitHubBindingResolver struct {
	organization slug.Slug
	err          error
}

func (r fakeGitHubBindingResolver) Resolve(context.Context, int64, int64) (slug.Slug, error) {
	return r.organization, r.err
}

func githubBinding(organization string) fakeGitHubBindingResolver {
	return fakeGitHubBindingResolver{organization: must.Slug(organization)}
}

func (r *fakeMappingResolver) ResolveWebhookMappings(_ context.Context, _ postgres.DBTX, lookup proofrepo.WebhookMappingLookup) ([]proofrepo.Mapping, error) {
	var matches []proofrepo.Mapping
	for _, mapping := range r.mappings {
		if mapping.Provider == lookup.Provider && mapping.ProviderConnectionID == lookup.ProviderConnectionID &&
			mapping.RepositoryID == lookup.RepositoryID && mapping.TargetRef == lookup.TargetRef {
			matches = append(matches, mapping)
		}
	}
	return matches, nil
}

func (r *fakeMappingResolver) ClaimWebhookDelivery(context.Context, postgres.DBTX, string, string, string) (bool, error) {
	return !r.duplicateDelivery, r.claimErr
}

type candidateCall struct {
	organization slug.Slug
	change       gitprovider.VerifiedChange
}

type fakeCandidates struct {
	ingested []candidateCall
	closed   []candidateCall
}

func (c *fakeCandidates) Ingest(_ context.Context, organization slug.Slug, _ model.EventActor, change gitprovider.VerifiedChange) (*proofcandidatesvc.Result, error) {
	c.ingested = append(c.ingested, candidateCall{organization: organization, change: change})
	return &proofcandidatesvc.Result{CandidateSlug: slug.Slug("candidate-" + strconv.Itoa(len(c.ingested)))}, nil
}

func (c *fakeCandidates) Close(_ context.Context, organization slug.Slug, _ model.EventActor, change gitprovider.VerifiedChange) error {
	c.closed = append(c.closed, candidateCall{organization: organization, change: change})
	return nil
}

type fakeCompiler struct {
	candidates []slug.Slug
	errors     map[slug.Slug]error
}

func (c *fakeCompiler) Compile(_ context.Context, _, candidate slug.Slug) (*proofreceipts.Receipt, error) {
	c.candidates = append(c.candidates, candidate)
	if err := c.errors[candidate]; err != nil {
		return nil, err
	}
	return &proofreceipts.Receipt{}, nil
}

type fakeDeliveryProducer struct {
	event gitprovider.VerifiedChangeEvent
}

type fakeStatusPublisher struct{}

func (fakeStatusPublisher) Publish(context.Context, model.ProofReceipt) error { return nil }

type recordingStatusPublisher struct {
	receipts []model.ProofReceipt
	err      error
}

func (p *recordingStatusPublisher) Publish(_ context.Context, receipt model.ProofReceipt) error {
	p.receipts = append(p.receipts, receipt)
	return p.err
}

func (p *fakeDeliveryProducer) EnqueueProofChange(_ context.Context, _ postgres.DBTX, event gitprovider.VerifiedChangeEvent) (queue.QueuedJob, error) {
	p.event = event
	return queue.QueuedJob{Kind: "proof_webhook", Queue: "pipeline_git", State: "queued"}, nil
}

func (c *fakeCandidates) IngestTarget(ctx context.Context, target proofrepo.Mapping, actor model.EventActor, change gitprovider.VerifiedChange) (*proofcandidatesvc.Result, error) {
	return c.Ingest(ctx, target.OrganizationSlug, actor, change)
}

func (c *fakeCandidates) CloseTarget(ctx context.Context, target proofrepo.Mapping, actor model.EventActor, change gitprovider.VerifiedChange) error {
	return c.Close(ctx, target.OrganizationSlug, actor, change)
}
