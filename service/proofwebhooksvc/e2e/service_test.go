package e2e

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
	proofrepo "github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/proofcandidatesvc"
	"github.com/Braidplane/braidplane-core/service/proofwebhooksvc"
	backgroundworker "github.com/Braidplane/braidplane-core/service/worker"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

const pipelineSource = `metadata:
  slug: proof-intake-e2e
  name: Proof intake E2E
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-active-users
      output_contract:
        representation: rows
        fields:
          - name: email
            type: string
            required: true
  - kind: sink
    slug: proof-intake-e2e
    sink:
      idempotency_class: idempotent_with_key
      input: fetch-users
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: upsert-users
`

func TestDurableProofIntakeForGitHubAndGitLab(t *testing.T) { // Shared real database and River runtime.
	if testing.Short() {
		t.Skip("skipping real-Postgres Proof intake E2E test in short mode")
	}

	for _, provider := range []repostore.Provider{repostore.ProviderGitHub, repostore.ProviderGitLab} {
		t.Run(string(provider), func(t *testing.T) {
			fixture := newFixture(t, provider)
			first := fixture.event("create", strings.Repeat("a", 40), gitprovider.ChangeActionUpsert)

			queued, err := fixture.service.Accept(t.Context(), first)
			require.NoError(t, err)
			require.Positive(t, queued.ID)
			require.Equal(t, queue.QueuedJob{ID: queued.ID, Kind: "proof_webhook", Queue: fixture.queue, State: "queued"}, queued)
			fixture.waitForJob(t, first.DeliveryID, "completed")
			firstCandidate := fixture.latestCandidate(t)
			require.Equal(t, first.HeadSHA, firstCandidate.headSHA)
			require.False(t, firstCandidate.closed)
			require.Equal(t, 1, fixture.deliveryCount(t))
			require.Equal(t, 1, fixture.proofJobCount(t))
			fixture.requireSecretFreeJob(t, first.DeliveryID)

			queued, err = fixture.service.Accept(t.Context(), first)
			require.NoError(t, err)
			require.Empty(t, queued)
			require.Equal(t, 1, fixture.deliveryCount(t))
			require.Equal(t, 1, fixture.proofJobCount(t))
			mismatched := first
			mismatched.BodySHA256 = strings.Repeat("f", 64)
			_, err = fixture.service.Accept(t.Context(), mismatched)
			require.ErrorIs(t, err, proofwebhooksvc.ErrDeliveryRejected)
			require.Equal(t, 1, fixture.proofJobCount(t))

			updated := fixture.event("update", strings.Repeat("b", 40), gitprovider.ChangeActionUpsert)
			_, err = fixture.service.Accept(t.Context(), updated)
			require.NoError(t, err)
			fixture.waitForJob(t, updated.DeliveryID, "completed")
			updatedCandidate := fixture.latestCandidate(t)
			require.Equal(t, updated.HeadSHA, updatedCandidate.headSHA)
			require.Equal(t, firstCandidate.id, updatedCandidate.supersedesID)

			closed := fixture.event("close", updated.HeadSHA, gitprovider.ChangeActionClose)
			_, err = fixture.service.Accept(t.Context(), closed)
			require.NoError(t, err)
			fixture.waitForJob(t, closed.DeliveryID, "completed")
			require.True(t, fixture.latestCandidate(t).closed)

			reopened := fixture.event("reopen", updated.HeadSHA, gitprovider.ChangeActionUpsert)
			_, err = fixture.service.Accept(t.Context(), reopened)
			require.NoError(t, err)
			fixture.waitForJob(t, reopened.DeliveryID, "completed")
			require.False(t, fixture.latestCandidate(t).closed)
			require.Equal(t, 2, fixture.candidateCount(t))
			require.Equal(t, 3, fixture.sources.calls())
		})
	}
}

func TestProofIntakeFailsClosedAndRedactsWorkerFailures(t *testing.T) { // Shared real database and River runtime.
	if testing.Short() {
		t.Skip("skipping real-Postgres Proof intake E2E test in short mode")
	}

	fixture := newFixture(t, repostore.ProviderGitHub)
	secret := "provider-response-secret"
	fixture.sources.setError(gitprovider.ErrChangeUnavailable.Detail(secret))
	event := fixture.event("provider-failure", strings.Repeat("c", 40), gitprovider.ChangeActionUpsert)
	_, err := fixture.service.Accept(t.Context(), event)
	require.NoError(t, err)
	job := fixture.waitForJob(t, event.DeliveryID, "retryable")
	require.NotContains(t, job.errors, secret)
	require.NotContains(t, fixture.logs.String(), secret)
	require.Contains(t, job.errors, proofwebhooksvc.ErrProcessingUnavailable.Error())

	fixture.sources.setError(nil)
	_, err = fixture.pool.Exec(t.Context(), `
		UPDATE credentials SET status='revoked', revoked_at=NOW()
		WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)
	`, fixture.organization)
	require.NoError(t, err)
	err = fixture.service.Process(t.Context(), fixture.event("revoked", strings.Repeat("d", 40), gitprovider.ChangeActionUpsert))
	require.ErrorIs(t, err, proofwebhooksvc.ErrDeliveryRejected)
	require.Equal(t, 0, fixture.candidateCount(t))

	_, err = fixture.pool.Exec(t.Context(), `
		UPDATE credentials SET status='active', revoked_at=NULL
		WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)
	`, fixture.organization)
	require.NoError(t, err)
	_, err = fixture.pool.Exec(t.Context(), `
		UPDATE data_sources SET is_active=false
		WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)
	`, fixture.organization)
	require.NoError(t, err)
	err = fixture.service.Process(t.Context(), fixture.event("inactive", strings.Repeat("e", 40), gitprovider.ChangeActionUpsert))
	require.ErrorIs(t, err, proofwebhooksvc.ErrDeliveryRejected)
	require.Equal(t, 0, fixture.candidateCount(t))

	_, err = fixture.pool.Exec(t.Context(), `
		UPDATE data_sources SET is_active=true, deleted_at=NOW()
		WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)
	`, fixture.organization)
	require.NoError(t, err)
	err = fixture.service.Process(t.Context(), fixture.event("deleted", strings.Repeat("f", 40), gitprovider.ChangeActionUpsert))
	require.ErrorIs(t, err, proofwebhooksvc.ErrDeliveryRejected)
	require.Equal(t, 0, fixture.candidateCount(t))

	_, err = fixture.pool.Exec(t.Context(), `
		UPDATE data_sources SET deleted_at=NULL
		WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)
	`, fixture.organization)
	require.NoError(t, err)
	missing := fixture.event("missing", strings.Repeat("f", 40), gitprovider.ChangeActionUpsert)
	missing.RepositoryID = "999999999"
	err = fixture.service.Process(t.Context(), missing)
	require.ErrorIs(t, err, proofwebhooksvc.ErrDeliveryRejected)
	require.Equal(t, 0, fixture.candidateCount(t))

	other := fixture.event("cross-tenant", strings.Repeat("f", 40), gitprovider.ChangeActionUpsert)
	fixture.bindings.organization = "other-tenant"
	err = fixture.service.Process(t.Context(), other)
	require.ErrorIs(t, err, proofwebhooksvc.ErrDeliveryRejected)
	require.Equal(t, 0, fixture.candidateCount(t))
}

type fixture struct {
	pool         *pgxpool.Pool
	service      *proofwebhooksvc.Service
	sources      *sourceVerifier
	bindings     *githubBindingResolver
	logs         *safeBuffer
	organization slug.Slug
	provider     repostore.Provider
	connectionID string
	repositoryID string
	deliveryKey  string
	queue        string
}

func newFixture(t *testing.T, provider repostore.Provider) *fixture {
	t.Helper()
	unique := time.Now().UnixNano()
	connectionID := fmt.Sprintf("%d", unique%8_000_000_000+1_000_000_000)
	repositoryID := fmt.Sprintf("%d", unique%7_000_000_000+2_000_000_000)
	return newFixtureWithIdentity(t, provider, connectionID, repositoryID, "refs/heads/main", "pipelines/proof.yaml")
}

func newFixtureWithIdentity(t *testing.T, provider repostore.Provider, connectionID, repositoryID, targetRef, pipelinePath string) *fixture {
	t.Helper()
	pool := e2edb.OpenPostgresPool(t, "Proof webhook service integration")
	unique := time.Now().UnixNano()
	org := slug.Slug(fmt.Sprintf("proof-intake-%s-%d", provider, unique))
	deliveryKey := fmt.Sprintf("webhook-fixture-%s-%d", provider, unique)
	if provider == repostore.ProviderGitLab {
		connectionID = "gitlab"
	}
	seedTarget(t, pool, org, provider, connectionID, repositoryID)

	repo := proofrepo.New()
	_, err := repo.CreateMapping(t.Context(), pool, proofrepo.CreateMappingParams{
		OrganizationSlug: org, ProjectSlug: "data", EnvironmentSlug: "staging", ProviderConnectionSlug: slug.Slug(provider),
		Provider: string(provider), ProviderConnectionID: connectionID, RepositoryID: repositoryID,
		TargetRef: targetRef, PipelinePath: pipelinePath,
	})
	require.NoError(t, err)

	txm := postgres.NewTransactionManager(pool)
	logs := &safeBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	bindings := &githubBindingResolver{txm: txm, repo: githubapps.New(), organization: org}
	sources := &sourceVerifier{}

	enqueueClient, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	enqueuer := queue.NewEnqueuer(enqueueClient)
	eventProducer, err := workerproducer.NewEventProducer(enqueuer, clock.New())
	require.NoError(t, err)
	candidates, err := proofcandidatesvc.New(txm, repo, sources, eventProducer)
	require.NoError(t, err)
	proofQueue := "proof_webhook_" + strconv.FormatInt(unique, 10)
	proofProducer, err := workerproducer.NewProofWebhookProducer(queueOverrideEnqueuer{delegate: enqueuer, queue: proofQueue})
	require.NoError(t, err)
	service, err := proofwebhooksvc.New(txm, repo, proofrepo.NewDeliveryClaimer(), candidates, noopCompiler{}, proofProducer, bindings, noopPublisher{}, false)
	require.NoError(t, err)
	registrar, err := backgroundworker.NewProofWebhookRegistrar(service, logger)
	require.NoError(t, err)
	queueClient, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Registrars: []queue.WorkerRegistrar{registrar},
		Queues:     map[string]river.QueueConfig{proofQueue: {MaxWorkers: 1}},
		Logger:     logger,
	})
	require.NoError(t, err)
	require.NoError(t, queueClient.Start(context.Background()))

	f := &fixture{pool: pool, service: service, sources: sources, bindings: bindings, logs: logs, organization: org, provider: provider, connectionID: connectionID, repositoryID: repositoryID, deliveryKey: deliveryKey, queue: proofQueue}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, queueClient.Stop(stopCtx))
		f.cleanup(t)
	})
	return f
}

func (f *fixture) event(suffix, head string, action gitprovider.ChangeAction) gitprovider.VerifiedChangeEvent {
	event := gitprovider.VerifiedChangeEvent{
		DeliveryID: f.deliveryKey + "-" + suffix, BodySHA256: strings.Repeat("1", 64), Action: action, Provider: f.provider,
		ProviderConnectionID: f.connectionID, RepositoryID: f.repositoryID, ChangeRequestID: "7",
		HeadSHA: head, SourceRef: "refs/heads/feature", TargetRef: "refs/heads/main",
	}
	if f.provider == repostore.ProviderGitLab {
		event.OrganizationSlug = f.organization
	}
	return event
}

type jobState struct{ errors string }

type queueOverrideEnqueuer struct {
	delegate queue.Enqueuer
	queue    string
}

func (e queueOverrideEnqueuer) EnqueueTx(ctx context.Context, tx postgres.DBTX, req queue.EnqueueRequest) (queue.QueuedJob, error) {
	req.Queue = e.queue
	return e.delegate.EnqueueTx(ctx, tx, req)
}

func (f *fixture) waitForJob(t *testing.T, deliveryID, want string) jobState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var lastState string
	var lastErrors string
	var lastErr error
	for time.Now().Before(deadline) {
		var state, errors string
		err := f.pool.QueryRow(t.Context(), `
			SELECT state::text, COALESCE(array_to_json(errors)::text, '[]') FROM river_job
			WHERE kind='proof_webhook' AND args->>'delivery_id'=$1
			ORDER BY id DESC LIMIT 1
		`, deliveryID).Scan(&state, &errors)
		lastState, lastErrors, lastErr = state, errors, err
		if err == nil && (state == want || (want == "retryable" && state == "available" && errors != "[]")) {
			return jobState{errors: errors}
		}
		time.Sleep(25 * time.Millisecond)
	}
	require.FailNow(t, "Proof webhook job did not reach expected state", "delivery=%s want=%s last=%s errors=%s query_error=%v logs=%s", deliveryID, want, lastState, lastErrors, lastErr, f.logs.String())
	return jobState{}
}

type candidateState struct {
	id           int64
	headSHA      string
	supersedesID int64
	closed       bool
}

func (f *fixture) latestCandidate(t *testing.T) candidateState {
	t.Helper()
	var state candidateState
	require.NoError(t, f.pool.QueryRow(t.Context(), `
		SELECT id, head_sha, COALESCE(supersedes_candidate_id, 0), closed_at IS NOT NULL
		FROM proof_change_candidates
		WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)
		ORDER BY id DESC LIMIT 1
	`, f.organization).Scan(&state.id, &state.headSHA, &state.supersedesID, &state.closed))
	return state
}

func (f *fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), query, args...).Scan(&count))
	return count
}

func (f *fixture) deliveryCount(t *testing.T) int {
	return f.count(t, `SELECT count(*) FROM proof_webhook_deliveries WHERE delivery_id LIKE $1`, f.deliveryKey+"-%")
}

func (f *fixture) proofJobCount(t *testing.T) int {
	return f.count(t, `SELECT count(*) FROM river_job WHERE kind='proof_webhook' AND args->>'delivery_id' LIKE $1`, f.deliveryKey+"-%")
}

func (f *fixture) requireSecretFreeJob(t *testing.T, deliveryID string) {
	t.Helper()
	var args string
	require.NoError(t, f.pool.QueryRow(t.Context(), `
		SELECT args::text FROM river_job
		WHERE kind='proof_webhook' AND args->>'delivery_id'=$1
	`, deliveryID).Scan(&args))
	require.NotContains(t, args, pipelineSource)
	require.NotContains(t, args, "pipelines/proof.yaml")
	require.NotContains(t, args, "credential")
}

func (f *fixture) candidateCount(t *testing.T) int {
	return f.count(t, `SELECT count(*) FROM proof_change_candidates WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.organization)
}

func (f *fixture) cleanup(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM river_job WHERE (kind='proof_webhook' AND args->>'delivery_id' LIKE $1) OR args->>'org_slug'=$2`, []any{f.deliveryKey + "-%", f.organization.String()}},
		{`DELETE FROM proof_webhook_deliveries WHERE delivery_id LIKE $1`, []any{f.deliveryKey + "-%"}},
		{`DELETE FROM github_app_repositories WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, []any{f.organization}},
		{`DELETE FROM github_app_installations WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, []any{f.organization}},
		{`DELETE FROM proof_change_candidates WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, []any{f.organization}},
		{`DELETE FROM proof_change_candidate_mappings WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, []any{f.organization}},
		{`DELETE FROM projects WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, []any{f.organization}},
		{`DELETE FROM data_sources WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, []any{f.organization}},
		{`DELETE FROM organizations WHERE slug=$1`, []any{f.organization}},
	}
	for _, statement := range statements {
		_, err := f.pool.Exec(ctx, statement.query, statement.args...)
		require.NoError(t, err)
	}
}

func seedTarget(t *testing.T, pool *pgxpool.Pool, org slug.Slug, provider repostore.Provider, connectionID, repositoryID string) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `
		WITH organization AS (
			INSERT INTO organizations (slug, name) VALUES ($1, $1) RETURNING id
		), credential AS (
			INSERT INTO credentials (organization_id, slug, kind, status, encrypted_payload)
			SELECT id, $2 || '-credential', CASE WHEN $2='gitlab' THEN 'oauth2_grant' ELSE 'static_secret' END, 'active', decode('01', 'hex') FROM organization RETURNING id, organization_id
		), data_source AS (
			INSERT INTO data_sources (organization_id, credential_id, name, slug, type, config, is_active, proof_enabled)
			SELECT organization_id, id, $2, $2, $2, jsonb_build_object('repository',jsonb_build_object('id',$3::text)), true, true FROM credential RETURNING id, organization_id
		), project AS (
			INSERT INTO projects (organization_id, slug, name, pipeline_backend_data_source_id, pipeline_backend_repo, pipeline_backend_tracked_branch, pipeline_backend_pipeline_dir)
			SELECT organization_id, 'data', 'Data', id, 'https://example.invalid/pipelines.git', 'main', 'pipelines' FROM data_source RETURNING id, organization_id
		), environment AS (
			INSERT INTO environments (organization_id, slug, name) SELECT id, 'staging', 'Staging' FROM organization RETURNING id
		)
		INSERT INTO project_environments (organization_id, project_id, environment_id, is_default)
		SELECT project.organization_id, project.id, environment.id, true FROM project, environment
	`, org, string(provider), repositoryID)
	require.NoError(t, err)
	if provider != repostore.ProviderGitHub {
		return
	}
	installationID, err := strconv.ParseInt(connectionID, 10, 64)
	require.NoError(t, err)
	repoID, err := strconv.ParseInt(repositoryID, 10, 64)
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `
		WITH installation AS (
			INSERT INTO github_app_installations (organization_id, installation_id, account_id, account_login, account_type, repository_selection)
			SELECT id, $2, $2, 'proof-e2e', 'Organization', 'selected' FROM organizations WHERE slug=$1
			RETURNING id, organization_id
		)
		INSERT INTO github_app_repositories (organization_id, installation_id, repository_id, full_name, owner_login, name)
		SELECT organization_id, id, $3, 'proof-e2e/pipelines', 'proof-e2e', 'pipelines' FROM installation
	`, org, installationID, repoID)
	require.NoError(t, err)
}

type sourceVerifier struct {
	mu       sync.Mutex
	n        int
	err      error
	delegate proofcandidatesvc.SourceVerifier
}

func (s *sourceVerifier) VerifySource(ctx context.Context, mapping proofrepo.Mapping, change gitprovider.VerifiedChange) ([]byte, error) {
	s.mu.Lock()
	s.n++
	err, delegate := s.err, s.delegate
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if delegate != nil {
		return delegate.VerifySource(ctx, mapping, change)
	}
	return []byte(pipelineSource), nil
}

func (s *sourceVerifier) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *sourceVerifier) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

type githubBindingResolver struct {
	txm          postgres.TransactionManager
	repo         githubapps.Repository
	organization slug.Slug
}

func (r *githubBindingResolver) Resolve(ctx context.Context, installationID, repositoryID int64) (slug.Slug, error) {
	if r.organization != "" && r.organization != "other-tenant" {
		return postgres.InRead(ctx, r.txm, func(ctx context.Context, db postgres.DBTX) (slug.Slug, error) {
			return r.repo.Resolve(ctx, db, installationID, repositoryID)
		})
	}
	return r.organization, nil
}

type noopCompiler struct{}

func (noopCompiler) Compile(context.Context, slug.Slug, slug.Slug) (*proofreceipts.Receipt, error) {
	return &proofreceipts.Receipt{}, nil
}

type noopPublisher struct{}

func (noopPublisher) Publish(context.Context, model.ProofReceipt) error { return nil }

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
