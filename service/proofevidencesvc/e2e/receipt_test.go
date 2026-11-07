package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/proofidentity"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	bigquery "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery/client"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/Braidplane/braidplane-core/service/worker/producer"
	"github.com/stretchr/testify/require"
)

type receiptFixtureServices struct {
	observationCalls int
	publications     []model.ProofReceipt
	publication      proofreceiptsvc.ReceiptPublicationProducer
	publicationErr   error
	resolutions      []model.ResolvedPipelineDataSourceRef
}

func (s *receiptFixtureServices) EnqueueProofReceipt(ctx context.Context, tx postgres.DBTX, receipt model.ProofReceipt) error {
	if s.publicationErr != nil {
		return s.publicationErr
	}
	if err := s.publication.EnqueueProofReceipt(ctx, tx, receipt); err != nil {
		return err
	}
	s.publications = append(s.publications, receipt)
	return nil
}

func (s receiptFixtureServices) Validate(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*pipelinesvc.PipelineValidationResult, error) {
	return &pipelinesvc.PipelineValidationResult{Valid: true}, nil
}

func (s receiptFixtureServices) ResolvePipelineDataSourceRefs(_ context.Context, _, _, _ slug.Slug, pipeline *pipelinespec.Pipeline) (*datasourcerefssvc.ResolvedPipeline, error) {
	return &datasourcerefssvc.ResolvedPipeline{Pipeline: pipeline, Resolutions: s.resolutions}, nil
}

func (s receiptFixtureServices) ResolveRuntimeSecrets(context.Context, slug.Slug, slug.Slug) ([]model.RuntimeSecretValue, error) {
	return nil, nil
}

func TestLiveViewEvidenceCreatesNewReceiptWithoutRewritingTableHistory(t *testing.T) {
	f := newFixture(t)
	compiler, services := newReceiptCompiler(t, f)
	repo := proofreceipts.New()
	completeReceiptObservation(t, f, bigquery.TableTypeTable, "table-observation")
	first, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictUnverified, appendClaim(t, first.Value).Verdict)
	completeReceiptObservation(t, f, bigquery.TableTypeView, "view-observation")
	second, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictFailed, appendClaim(t, second.Value).Verdict)
	require.NotEqual(t, first.Value.ID, second.Value.ID)
	require.Equal(t, &first.Value.ID, second.Value.SupersedesReceiptID)
	unchanged, err := repo.FindBySlug(t.Context(), f.pool, f.scope.Organization, f.scope.Project, f.scope.Environment, first.Value.ID)
	require.NoError(t, err)
	require.True(t, unchanged.Superseded)
	require.Equal(t, first.Value, unchanged.Value)
	raw, err := json.Marshal(second.Value)
	require.NoError(t, err)
	for _, private := range []string{"private_dataset", "private_table", "private_email"} {
		require.NotContains(t, string(raw), private)
	}
	completeReceiptObservation(t, f, bigquery.TableTypeView, "refresh-observation")
	refreshed, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
	require.NoError(t, err)
	require.NotEqual(t, second.Value.ID, refreshed.Value.ID, "identical refreshed metadata needs a new immutable receipt")
	detail, err := compiler.Get(t.Context(), f.scope.Organization, f.scope.Project, f.scope.Environment, refreshed.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictFailed, appendClaim(t, detail.Receipt).Verdict)
	old, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{"table-observation"})
	require.NoError(t, err)
	require.Equal(t, first.Value, old.Value)
	current, err := repo.FindBySlug(t.Context(), f.pool, f.scope.Organization, f.scope.Project, f.scope.Environment, refreshed.Value.ID)
	require.NoError(t, err)
	require.False(t, current.Superseded)
	var queued int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM river_job WHERE kind='proof_publication' AND args->>'organization'=$1`, f.scope.Organization).Scan(&queued))
	require.Equal(t, 3, queued, "old evidence replay must coalesce with its existing publication job")
	require.Len(t, services.publications, 4)
	replayed, err := compiler.Compile(t.Context(), f.scope.Organization, f.request.CandidateSlug)
	require.NoError(t, err)
	require.Equal(t, refreshed.Value, replayed.Value, "provider re-request preserves the newest admitted live receipt")
	require.Zero(t, services.observationCalls, "compilation and provider re-request never dispatch observations")
	_, err = repo.FindLatestForCandidate(t.Context(), f.pool, "another-tenant", f.request.CandidateSlug)
	require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound)
	// A failure record must never validate a previously observed live clause.
	successfulJob, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET result='{"outcome":"worker_lost"}'::jsonb,snapshot_ciphertext=NULL WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug=$2`, f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	detail, err = compiler.Get(t.Context(), f.scope.Organization, f.scope.Project, f.scope.Environment, refreshed.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictStale, appendClaim(t, detail.Receipt).Verdict)
	originalResult, err := json.Marshal(successfulJob.Result)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET result=$3,snapshot_ciphertext=$4 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug=$2`, f.scope.Organization, f.request.JobID, originalResult, successfulJob.SnapshotCiphertext)
	require.NoError(t, err)
	originalPolicy := f.policy
	f.policy.Enabled = false
	f.policy.Version = "disabled-receipt-policy"
	require.NoError(t, f.ledger.SetPolicy(t.Context(), f.scope, f.policy, f.request.Worker.Identity))
	detail, err = compiler.Get(t.Context(), f.scope.Organization, f.scope.Project, f.scope.Environment, refreshed.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictStale, appendClaim(t, detail.Receipt).Verdict)
	f.policy = originalPolicy
	require.NoError(t, f.ledger.SetPolicy(t.Context(), f.scope, f.policy, f.request.Worker.Identity))
	_, err = f.pool.Exec(t.Context(), `UPDATE data_sources SET config='{"project_id":"changed-project"}' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='warehouse'`, f.scope.Organization)
	require.NoError(t, err)
	detail, err = compiler.Get(t.Context(), f.scope.Organization, f.scope.Project, f.scope.Environment, refreshed.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictStale, appendClaim(t, detail.Receipt).Verdict)
	_, err = compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	config, err := json.Marshal(bigquery.Config{ProjectID: f.target.ProjectID})
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE data_sources SET config=$2 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='warehouse'`, f.scope.Organization, config)
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE credentials SET updated_at=updated_at+interval '1 second' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='warehouse-credential'`, f.scope.Organization)
	require.NoError(t, err)
	_, err = compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	stored, err := repo.FindBySlug(t.Context(), f.pool, f.scope.Organization, f.scope.Project, f.scope.Environment, refreshed.Value.ID)
	require.NoError(t, err)
	require.Equal(t, refreshed.Value, stored.Value)
	detail, err = compiler.Get(t.Context(), f.scope.Organization, f.scope.Project, f.scope.Environment, refreshed.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictStale, appendClaim(t, detail.Receipt).Verdict)
	require.Equal(t, model.ProofVerdictFailed, appendClaim(t, stored.Value).Verdict)
}

func completeReceiptObservation(t *testing.T, f *fixture, kind bigquery.TableType, job slug.Slug) {
	t.Helper()
	f.request.JobID = job
	auth := decision(t, f.request)
	_, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
	require.NoError(t, err)
	finishAdmittedReceiptObservation(t, f, kind)
}

// Exercise protected completion and receipt evaluation without provider calls.
func finishAdmittedReceiptObservation(t *testing.T, f *fixture, kind bigquery.TableType) {
	t.Helper()
	dispatch, err := f.ledger.BeginExecution(t.Context(), f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	grant, err := dispatch.Policy.Resume(dispatch.Request, dispatch.Authorization, dispatch.Deadline)
	require.NoError(t, err)
	metadata, err := json.Marshal(struct {
		TableType bigquery.TableType `json:"table_type"`
		Fields    []bigquery.Field   `json:"fields"`
	}{kind, []bigquery.Field{{Name: "private_email", Type: "STRING", Mode: "NULLABLE"}}})
	require.NoError(t, err)
	snapshot := &proofevidencesvc.Snapshot{TargetFingerprint: f.request.TargetFingerprint, Metadata: metadata}
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	result := proofevidence.Result{RequestDigest: dispatch.Authorization.RequestDigest, Outcome: proofevidence.OutcomeComplete, EvidenceLevel: f.request.EvidenceLevel, EvidenceDigest: hash(raw), ObservedAt: time.Now().UTC(), ExpiresAt: f.request.EvidenceRetainUntil, Usage: proofevidence.Usage{Duration: time.Millisecond, Requests: 2, Rows: 1, Bytes: int64(len(raw)), MaxConcurrency: 1}}
	require.NoError(t, f.ledger.Record(t.Context(), grant, proofevidencesvc.Observation{Result: result, Snapshot: snapshot}))
}

func appendClaim(t *testing.T, receipt model.ProofReceipt) model.ProofClaim {
	t.Helper()
	for _, claim := range receipt.Claims {
		if claim.Identity.SemanticPath == "/steps/sink/destination/append_capability" {
			return claim
		}
	}
	t.Fatal("append clause missing")
	return model.ProofClaim{}
}

func newReceiptCompiler(t *testing.T, f *fixture) (*proofreceiptsvc.LiveCompiler, *receiptFixtureServices) {
	t.Helper()
	return newReceiptCompilerWithPublishing(t, f, true)
}

func newReceiptCompilerWithPublishing(t *testing.T, f *fixture, enabled bool) (*proofreceiptsvc.LiveCompiler, *receiptFixtureServices) {
	t.Helper()
	services := &receiptFixtureServices{resolutions: []model.ResolvedPipelineDataSourceRef{
		{PipelineSlug: "contacts", RefSlug: "source", DataSourceSlug: "crm", DataSourceType: model.DataSourceTypeHubSpot},
		{PipelineSlug: "contacts", RefSlug: "destination", DataSourceSlug: "warehouse", DataSourceType: model.DataSourceTypeBigQuery},
	}}
	contract := &payload.Contract{Name: "contacts", Representation: model.ContractRepresentationRows, RootShape: model.ContractRootShapeArray, Fields: []payload.ContractField{{Name: "email", Type: model.ContractFieldTypeValueString}}}
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "contacts", Name: "Contacts"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "source", Query: &pipelinespec.QueryRef{OutputContract: contract, DataSourceSlug: "source", OperationType: "hubspot", OperationInline: map[string]any{"type": "hubspot", "object_type": "contacts", "operation": "list"}}},
		{Kind: "sink", Slug: "sink", Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "source"}, DataSourceSlug: "destination", OperationType: "bigquery", OperationInline: map[string]any{"type": "bigquery", "operation": "insert_rows", "dataset": f.target.DatasetID, "table": f.target.TableID, "rows": "{{rows}}"}, IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger}},
	}}
	raw, err := json.Marshal(pipeline)
	require.NoError(t, err)
	repo := proofreceipts.New()
	original, err := repo.FindCandidateTarget(t.Context(), f.pool, f.scope.Organization, f.request.CandidateSlug)
	require.NoError(t, err)
	_, err = proofcandidates.New().Create(t.Context(), f.pool, f.scope.Organization, proofcandidates.CreateCandidateParams{MappingID: original.MappingID, Slug: "candidate-live", CandidateKey: f.scope.Organization.String() + "-live", ChangeRequestID: "42", SourceRef: "feature", HeadSHA: f.request.Candidate.HeadSHA, SourceSHA256: hash(raw), SpecHash: hash(raw), CanonicalSpec: raw})
	require.NoError(t, err)
	config, err := json.Marshal(bigquery.Config{ProjectID: f.target.ProjectID})
	require.NoError(t, err)
	_, err = f.pool.Exec(t.Context(), `UPDATE data_sources SET config=$2 WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='warehouse'`, f.scope.Organization, config)
	require.NoError(t, err)
	f.request.CandidateSlug = "candidate-live"
	f.request.Candidate.SpecHash = hash(raw)
	f.request.Candidate.CompilerVersion = proofreceiptsvc.LiveCompilerVersion
	f.request.Candidate.PolicyVersion = proofreceiptsvc.LivePolicyVersion
	f.request.Candidate.BindingFingerprint, err = proofidentity.BindingFingerprint(services.resolutions)
	require.NoError(t, err)
	f.request.Candidate.CapabilityFingerprint, err = proofidentity.CapabilityFingerprint(pipeline, services.resolutions)
	require.NoError(t, err)
	f.request.Claim.PipelineVersionHash = f.request.Candidate.SpecHash
	f.request.Claim.SemanticPath = "/steps/sink/destination/append_capability"
	base, err := proofreceiptsvc.New(postgres.NewTransactionManager(f.pool), repo, services, services, datasources.NewSchemaMetadataRepository(), pipelines.New(), services, odcscontractsvc.New(postgres.NewTransactionManager(f.pool), odcscontracts.NewRepository(), projects.NewRepository(), compiledplans.NewRepository()), clock.New())
	require.NoError(t, err)
	coordinator, err := proofevidencesvc.NewCoordinator(f.ledger, &executionCredentials{}, proofExecutor(func(_ context.Context, request proofevidencesvc.ExecutionRequest) (proofevidencesvc.Observation, error) {
		services.observationCalls++
		return completeObservation(t, request), nil
	}))
	require.NoError(t, err)
	queueClient, err := queue.NewEnqueueOnly(f.pool)
	require.NoError(t, err)
	services.publication, err = producer.NewProofPublicationProducer(queue.NewEnqueuer(queueClient))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := f.pool.Exec(context.Background(), `DELETE FROM river_job WHERE kind='proof_publication' AND args->>'organization'=$1`, f.scope.Organization)
		require.NoError(t, err)
	})
	compiler, err := proofreceiptsvc.NewLiveCompiler(base, f.ledger, datasources.New(), queries.New(), coordinator, f.retentionAudit, services, enabled)
	require.NoError(t, err)
	return compiler, services
}

func TestLivePublicationEnqueueFailureRollsBackNewReceipt(t *testing.T) {
	f := newFixture(t)
	compiler, services := newReceiptCompiler(t, f)
	completeReceiptObservation(t, f, bigquery.TableTypeTable, "queued-observation")
	services.publicationErr = xerror.Unavailable(xerror.Pipeline).Detail("publication queue unavailable")
	_, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
	require.ErrorIs(t, err, services.publicationErr)
	_, err = proofreceipts.New().FindLatestForCandidate(t.Context(), f.pool, f.scope.Organization, f.request.CandidateSlug)
	require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound)
	services.publicationErr = nil
	receipt, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
	require.NoError(t, err)
	require.Len(t, services.publications, 1)
	require.Equal(t, receipt.Value.ID, services.publications[0].ID)
	require.Zero(t, services.observationCalls)
}

func TestDisabledPublicationStillCompilesLiveReceipt(t *testing.T) {
	f := newFixture(t)
	compiler, services := newReceiptCompilerWithPublishing(t, f, false)
	completeReceiptObservation(t, f, bigquery.TableTypeTable, "disabled-publication")
	receipt, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
	require.NoError(t, err)
	require.Equal(t, model.ProofEvidenceLevelLiveReadOnly, *appendClaim(t, receipt.Value).ActualEvidenceLevel)
	require.Empty(t, services.publications)
	var queued int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM river_job WHERE kind='proof_publication' AND args->>'organization'=$1`, f.scope.Organization).Scan(&queued))
	require.Zero(t, queued)
}

func TestFailedObservationsCreateImmutableNonPassingReceipts(t *testing.T) {
	f := newFixture(t)
	compiler, _ := newReceiptCompiler(t, f)
	f.policy.Version = "failure-policy"
	f.request.PolicyVersion = f.policy.Version
	f.policy.Operations[0].Limits.MaxRequests *= 3
	f.policy.Operations[0].Limits.MaxRows *= 3
	f.policy.Operations[0].Limits.MaxBytes *= 3
	require.NoError(t, f.ledger.SetPolicy(t.Context(), f.scope, f.policy, f.request.Worker.Identity))
	completeReceiptObservation(t, f, bigquery.TableTypeView, "before-failure")
	first, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
	require.NoError(t, err)
	previous := first
	var earliestFailure *proofreceipts.Receipt
	var earliestJob slug.Slug
	for _, outcome := range proofevidence.OutcomeValues() {
		if outcome == proofevidence.OutcomeUnknown || outcome == proofevidence.OutcomeComplete {
			continue
		}
		t.Run(outcome.String(), func(t *testing.T) {
			f.request.JobID = slug.Slug("failure-" + strings.ReplaceAll(outcome.String(), "_", "-"))
			auth := decision(t, f.request)
			_, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
			require.NoError(t, err)
			_, err = f.ledger.BeginExecution(t.Context(), f.scope.Organization, f.request.JobID)
			require.NoError(t, err)
			grant, err := f.policy.Authorize(f.request, auth)
			require.NoError(t, err)
			// Bogus evidence fields must not persist or turn a failed attempt into a fact.
			result := proofevidence.Result{RequestDigest: auth.RequestDigest, Outcome: outcome, EvidenceLevel: f.request.EvidenceLevel, EvidenceDigest: "private-provider-diagnostic", ObservedAt: time.Now().Add(time.Hour), ExpiresAt: time.Now().Add(2 * time.Hour), Usage: proofevidence.Usage{Requests: 1}}
			require.NoError(t, f.ledger.Record(t.Context(), grant, proofevidencesvc.Observation{Result: result}))
			stored, err := f.repo.Find(t.Context(), f.pool, f.scope.Organization, f.request.JobID)
			require.NoError(t, err)
			require.Empty(t, stored.Result.EvidenceDigest)
			require.True(t, stored.Result.ObservedAt.IsZero())
			require.True(t, stored.Result.ExpiresAt.IsZero())
			require.Empty(t, stored.SnapshotCiphertext)
			require.Equal(t, int64(1), stored.Result.Usage.Requests)
			receipt, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
			require.NoError(t, err)
			claim := appendClaim(t, receipt.Value)
			verdict := model.ProofVerdictInconclusive
			if outcome == proofevidence.OutcomeUnsupported {
				verdict = model.ProofVerdictUnverified
			}
			require.Equal(t, verdict, claim.Verdict)
			require.Nil(t, claim.ActualEvidenceLevel)
			require.Nil(t, claim.ObservedAt)
			require.Nil(t, claim.ExpiresAt)
			require.Equal(t, &previous.Value.ID, receipt.Value.SupersedesReceiptID)
			raw, err := json.Marshal(receipt.Value)
			require.NoError(t, err)
			for _, private := range []string{"private_dataset", "private_table", "private_email", "private-provider-diagnostic"} {
				require.NotContains(t, string(raw), private)
			}
			previous = receipt
			if earliestFailure == nil {
				earliestFailure = receipt
				earliestJob = f.request.JobID
			}
		})
	}
	// Changing policy and credentials cannot make historical failure classification
	// unavailable or silently upgrade it into live evidence.
	f.policy.Enabled = false
	f.policy.Version = "failure-disabled"
	require.NoError(t, f.ledger.SetPolicy(t.Context(), f.scope, f.policy, f.request.Worker.Identity))
	_, err = f.pool.Exec(t.Context(), `UPDATE credentials SET status='revoked',updated_at=clock_timestamp() WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug='warehouse-credential'`, f.scope.Organization)
	require.NoError(t, err)
	// Failure compilation does not need protected selectors, even after payload purge.
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET target_ciphertext=NULL WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1) AND slug=$2`, f.scope.Organization, earliestJob)
	require.NoError(t, err)
	again, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{earliestJob})
	require.NoError(t, err)
	require.Equal(t, earliestFailure.Value, again.Value)
	detail, err := compiler.Get(t.Context(), f.scope.Organization, f.scope.Project, f.scope.Environment, earliestFailure.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictSuperseded, detail.Receipt.AggregateVerdict)
	require.Nil(t, appendClaim(t, detail.Receipt).ActualEvidenceLevel)
	current, err := proofreceipts.New().FindBySlug(t.Context(), f.pool, f.scope.Organization, f.scope.Project, f.scope.Environment, previous.Value.ID)
	require.NoError(t, err)
	require.False(t, current.Superseded)
	unchanged, err := proofreceipts.New().FindBySlug(t.Context(), f.pool, f.scope.Organization, f.scope.Project, f.scope.Environment, first.Value.ID)
	require.NoError(t, err)
	require.Equal(t, first.Value, unchanged.Value)
}

func TestFailureReceiptsRequireRetainedBoundTerminalJobs(t *testing.T) {
	f := newFixture(t)
	compiler, services := newReceiptCompiler(t, f)
	auth := decision(t, f.request)
	_, err := f.ledger.Reserve(t.Context(), f.request, auth, f.target)
	require.NoError(t, err)
	compile := func() error {
		_, err := compiler.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
		return err
	}
	require.Error(t, compile(), "pending jobs cannot become receipts")
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET deadline_at=clock_timestamp()-interval '1 second' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.NoError(t, f.maintenance.Cleanup(t.Context(), f.scope))
	job, err := f.ledger.Job(t.Context(), f.scope, f.request.JobID)
	require.NoError(t, err)
	require.Equal(t, proofevidence.OutcomeWorkerLost, job.Result.Outcome)
	require.Empty(t, job.Result.RequestDigest, "cleanup classifies worker loss without fabricating a worker response")
	require.NoError(t, compile())
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET snapshot_ciphertext='invalid'::bytea WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.Error(t, compile(), "failed outcomes cannot carry protected snapshots")
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET snapshot_ciphertext=NULL,result='{"outcome":"denied","request_digest":"wrong"}'::jsonb WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.Error(t, compile(), "mismatched worker result digest cannot be normalized into an accepted failure")
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET result='{"outcome":"unknown"}'::jsonb WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.Error(t, compile(), "unknown outcome cannot establish a terminal failure")
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET result='{"outcome":"worker_lost"}'::jsonb WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	original := services.resolutions[1].DataSourceSlug
	services.resolutions[1].DataSourceSlug = "changed-warehouse"
	require.Error(t, compile(), "failed attempts remain bound to the original candidate mapping")
	services.resolutions[1].DataSourceSlug = original
	require.NoError(t, compile())
	_, err = f.pool.Exec(t.Context(), `UPDATE proof_observation_jobs SET deadline_at=clock_timestamp()-interval '2 hours', budget_expires_at=clock_timestamp()-interval '1 hour', evidence_retain_until=clock_timestamp()-interval '1 hour', metadata_retain_until=clock_timestamp()-interval '1 minute' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, f.scope.Organization)
	require.NoError(t, err)
	require.Error(t, compile(), "expired metadata cannot be recompiled before its scheduled deletion")
	f.request.JobID = "unbound-failure"
	f.request.Claim.SemanticPath = "/steps/missing/destination/append_capability"
	_, err = f.ledger.Reserve(t.Context(), f.request, decision(t, f.request), f.target)
	require.NoError(t, err)
	err = f.ledger.Cancel(t.Context(), f.scope.Organization, f.request.JobID)
	require.NoError(t, err)
	require.ErrorIs(t, compile(), proofevidence.ErrAuthorizationDenied, "a terminal failure must still identify a canonical candidate step")
}

func TestDisabledLiveReaderCannotReuseFreshEvidence(t *testing.T) {
	f := newFixture(t)
	live, services := newReceiptCompiler(t, f)
	completeReceiptObservation(t, f, bigquery.TableTypeView, "rollback-observation")
	compiled, err := live.CompileObserved(t.Context(), f.scope, "reviewer", f.request.CandidateSlug, []slug.Slug{f.request.JobID})
	require.NoError(t, err)
	disabled, err := proofreceiptsvc.New(postgres.NewTransactionManager(f.pool), proofreceipts.New(), services, services, datasources.NewSchemaMetadataRepository(), pipelines.New(), services, odcscontractsvc.New(postgres.NewTransactionManager(f.pool), odcscontracts.NewRepository(), projects.NewRepository(), compiledplans.NewRepository()), clock.New())
	require.NoError(t, err)
	projected, err := disabled.Get(t.Context(), f.scope.Organization, f.scope.Project, f.scope.Environment, compiled.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictStale, appendClaim(t, projected.Receipt).Verdict)
	require.Equal(t, "proof.live_observation_unavailable", appendClaim(t, projected.Receipt).Diagnostics[0].Code)
	stored, err := proofreceipts.New().FindBySlug(t.Context(), f.pool, f.scope.Organization, f.scope.Project, f.scope.Environment, compiled.Value.ID)
	require.NoError(t, err)
	require.Equal(t, compiled.Value, stored.Value, "rollback changes the read projection, never stored history")
	enabled, err := live.Get(t.Context(), f.scope.Organization, f.scope.Project, f.scope.Environment, compiled.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictFailed, appendClaim(t, enabled.Receipt).Verdict, "the enabled path still validates and uses current evidence")
}
