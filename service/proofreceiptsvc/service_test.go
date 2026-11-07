package proofreceiptsvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

func TestCompileProducesDeterministicFreshAndStalePreviewClaims(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 31, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		observedAt  time.Time
		wantVerdict model.ProofVerdict
	}{
		{name: "fresh", observedAt: now.Add(-time.Hour), wantVerdict: model.ProofVerdictInconclusive},
		{name: "stale", observedAt: now.Add(-25 * time.Hour), wantVerdict: model.ProofVerdictStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := testCandidateTarget(t)
			repo := &fakeReceiptRepository{target: target}
			svc, err := New(
				testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{},
				schemaReader{metadata: &datasources.SchemaMetadata{Slug: "schema-1", SchemaData: json.RawMessage(`{"tables":[]}`), UpdatedAt: tc.observedAt}},
				deploymentReader{deployment: &pipelinesrepo.PipelineDeployment{CurrentVersionSpecHash: target.SpecHash}},
				emptySecretResolver{}, noContractValidator{},
				fixedClock{now},
			)
			require.NoError(t, err)

			got, err := svc.Compile(t.Context(), target.OrganizationSlug, target.CandidateSlug)
			require.NoError(t, err)
			require.Equal(t, tc.wantVerdict, got.Value.AggregateVerdict)
			require.Equal(t, target.SpecHash, got.Value.Candidate.SpecHash)
			require.Equal(t, CompilerVersion, got.Value.Candidate.CompilerVersion)
			require.NotEmpty(t, got.Value.Claims)
			require.Len(t, repo.created, 1)
			require.Equal(t, repo.created[0].Receipt.ID, got.Value.ID)
		})
	}
}

func TestSecretRedactorReplacesLongerRuntimeSecretsFirst(t *testing.T) {
	t.Parallel()
	redact := secretRedactor([]model.RuntimeSecretValue{{Value: "token"}, {Value: "token-suffix"}})
	require.Equal(t, "[REDACTED]", redact("token-suffix"))
}

func TestStaticFailuresStayCausal(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	contract := &payload.Contract{Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueString}}}
	pipeline := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{
		{Slug: "extract", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: "hubspot", OutputContract: contract}},
		{Slug: "shape", Transform: &pipelinespec.TransformSpec{InputContract: contract, OutputContract: contract}},
		{Slug: "load", Sink: &pipelinespec.SinkSpec{DataSourceSlug: "destination", OperationType: "bigquery", InputContract: contract}},
	}}
	bindings := []model.ResolvedPipelineDataSourceRef{
		{RefSlug: "source", DataSourceSlug: "crm", DataSourceType: model.DataSourceTypeHubSpot},
		{RefSlug: "destination", DataSourceSlug: "warehouse", DataSourceType: model.DataSourceTypeBigQuery},
	}
	for _, tc := range []struct {
		name, code, field, step, failedPath string
	}{
		{"transform", "PIPELINE_INVALID_INPUT", "steps[1].transform", "shape", ""},
		{"missing binding", "DATA_SOURCE_BINDING_NOT_FOUND", "steps[0].query", "extract", "/steps/extract/source"},
		{"incompatible capability", "DATA_SOURCE_REF_INCOMPATIBLE", "steps[2].sink", "load", "/steps/load/destination/capability"},
		{"missing location", "PIPELINE_INVALID_INPUT", "spec", "", ""},
		{"mismatched location", "DATA_SOURCE_BINDING_NOT_FOUND", "steps[0].query", "load", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validation := &pipelinesvc.PipelineValidationResult{Diagnostics: []pipelinesvc.PipelineValidationDiagnostic{{Code: tc.code, Field: tc.field, StepSlug: &tc.step, Message: "private-provider-token"}}}
			claims := compileClaims(pipeline, validation, bindings, nil, nil, "candidate", "evidence", now)
			for _, claim := range claims {
				switch claim.Identity.SemanticPath {
				case "/validation":
					require.Equal(t, model.ProofVerdictFailed, claim.Verdict)
					require.Contains(t, claim.Diagnostics[0].Summary, tc.field)
				case tc.failedPath:
					require.Equal(t, model.ProofVerdictFailed, claim.Verdict)
					require.Equal(t, tc.code, claim.Diagnostics[0].Code)
				default:
					require.Equal(t, model.ProofVerdictUnverified, claim.Verdict, claim.Identity.SemanticPath)
				}
			}
			require.Equal(t, model.ProofVerdictFailed, model.AggregateProofVerdict(claims))
			raw, err := json.Marshal(claims)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private-provider-token")
		})
	}
}

func TestInvalidPipelineDoesNotDisproveCachedFields(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	pipeline := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{{Slug: "extract", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OutputContract: &payload.Contract{Fields: []payload.ContractField{{Name: "id"}}}}}}}
	bindings := []model.ResolvedPipelineDataSourceRef{{RefSlug: "source", DataSourceSlug: "crm"}}
	for _, tc := range []struct {
		name         string
		observations []schemaObservation
		verdict      model.ProofVerdict
		code         string
	}{
		{"missing", nil, model.ProofVerdictUnverified, "proof.cached_observation_missing"},
		{"stale", []schemaObservation{{DataSourceSlug: "crm", ExpiresAt: now}}, model.ProofVerdictStale, "proof.cached_observation_stale"},
		{"unlinked", []schemaObservation{{DataSourceSlug: "crm", ExpiresAt: now.Add(time.Hour)}}, model.ProofVerdictInconclusive, "proof.cached_observation_unlinked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := compileClaims(pipeline, &pipelinesvc.PipelineValidationResult{}, bindings, tc.observations, nil, "candidate", "evidence", now)
			for _, claim := range claims {
				if claim.Identity.Family == model.ProofClaimFamilyFieldsTypes {
					require.Equal(t, tc.verdict, claim.Verdict)
					require.Equal(t, tc.code, claim.Diagnostics[0].Code)
				}
			}
		})
	}
}

func TestGetProjectsLifecycleMismatchWithoutChangingReceipt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 2, 8, 0, 0, 0, time.UTC)
	pipelineSlug := slug.Slug("orders")
	repo := &fakeReceiptRepository{
		stored: &proofreceipts.Receipt{Value: model.ProofReceipt{ID: "proof-123", Candidate: model.ProofCandidateIdentity{SpecHash: "candidate"}}},
		links: []proofreceipts.LifecycleLink{{
			Kind: model.ProofLifecycleLinkKindDeployment, Identifier: "orders:old", LinkedAt: now, RetainUntil: now.Add(time.Hour),
			PipelineSlug: &pipelineSlug, CurrentSpecHash: "selected", CurrentIdentifier: "orders:selected:new",
		}},
	}
	svc, err := New(testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{}, schemaReader{}, deploymentReader{}, emptySecretResolver{}, noContractValidator{}, fixedClock{now})
	require.NoError(t, err)

	detail, err := svc.Get(t.Context(), "acme", "data", "prod", "proof-123")
	require.NoError(t, err)
	require.Equal(t, model.ProofLifecycleLinkKindDeployment, detail.LifecycleLinks[0].Kind)
	require.Equal(t, "deployment_mismatch", detail.LifecycleLinks[0].Status)
	require.Equal(t, "selected", detail.LifecycleLinks[0].CurrentVersionSpecHash)
	require.Equal(t, "candidate", detail.Receipt.Candidate.SpecHash)
}

func TestGetReturnsLifecycleLinkRepositoryError(t *testing.T) {
	t.Parallel()
	wantErr := xerror.Unexpected().Detail("list lifecycle links")
	repo := &fakeReceiptRepository{
		stored:   &proofreceipts.Receipt{Value: model.ProofReceipt{ID: "proof-123"}},
		linksErr: wantErr,
	}
	svc, err := New(testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{}, schemaReader{}, deploymentReader{}, emptySecretResolver{}, noContractValidator{}, fixedClock{})
	require.NoError(t, err)

	_, err = svc.Get(t.Context(), "acme", "data", "prod", "proof-123")
	require.ErrorIs(t, err, wantErr)
}

func TestListProjectsReceiptsWithSharedFreshnessAndPagination(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 19, 8, 0, 0, 0, time.UTC)
	live := model.ProofEvidenceLevelLiveReadOnly
	repo := &fakeReceiptRepository{listed: []*proofreceipts.Receipt{
		{Value: model.ProofReceipt{ID: "proof-2", CreatedAt: now, Claims: []model.ProofClaim{{Verdict: model.ProofVerdictPassed, ActualEvidenceLevel: &live}}}},
		{Superseded: true, Value: model.ProofReceipt{ID: "proof-1", CreatedAt: now.Add(-time.Minute)}},
	}}
	svc, err := New(testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{}, schemaReader{}, deploymentReader{}, emptySecretResolver{}, noContractValidator{}, fixedClock{now})
	require.NoError(t, err)

	page, err := svc.List(t.Context(), "acme", "data", "prod", model.ListRequest{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.True(t, page.HasMore)
	require.NotNil(t, page.NextCursor)
	require.Equal(t, slug.Slug("proof-2"), page.Items[0].Receipt.ID)
	require.Equal(t, model.ProofVerdictStale, page.Items[0].Receipt.Claims[0].Verdict)
	require.Equal(t, 2, repo.listReq.Limit, "repository receives limit plus one")
	require.Equal(t, model.ListOrderDesc, repo.listReq.Order)
}

func TestListPreviousPageReturnsNewerReceiptsAndForwardBoundary(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 19, 8, 0, 0, 0, time.UTC)
	cursor := *model.EncodeCursor(now.Add(-2*time.Minute).Format(time.RFC3339Nano), "proof-1")
	repo := &fakeReceiptRepository{listFn: func(req model.ListRequest) []*proofreceipts.Receipt {
		require.Equal(t, model.ListOrderAsc, req.Order)
		require.Equal(t, model.ListDirectionPrev, req.Direction)
		require.Equal(t, cursor, req.Cursor)
		require.Equal(t, 3, req.Limit)
		return []*proofreceipts.Receipt{
			{Value: model.ProofReceipt{ID: "proof-2", CreatedAt: now.Add(-time.Minute)}},
			{Value: model.ProofReceipt{ID: "proof-3", CreatedAt: now}},
		}
	}}
	svc, err := New(testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{}, schemaReader{}, deploymentReader{}, emptySecretResolver{}, noContractValidator{}, fixedClock{now})
	require.NoError(t, err)

	page, err := svc.List(t.Context(), "acme", "data", "prod", model.ListRequest{Limit: 2, Cursor: cursor, Direction: model.ListDirectionPrev})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.Equal(t, slug.Slug("proof-3"), page.Items[0].Receipt.ID)
	require.Equal(t, slug.Slug("proof-2"), page.Items[1].Receipt.ID)
	require.True(t, page.HasMore)
	require.Nil(t, page.PrevCursor)
	require.Equal(t, model.EncodeCursor(now.Add(-time.Minute).Format(time.RFC3339Nano), "proof-2"), page.NextCursor)
}

func testCandidateTarget(t *testing.T) *proofreceipts.CandidateTarget {
	t.Helper()
	pipeline := pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"},
		Steps: []pipelinespec.Step{{Kind: "query", Slug: "extract", Query: &pipelinespec.QueryRef{
			DataSourceSlug: "source", OperationType: "sql", OperationRef: "select-orders",
			OutputContract: &payload.Contract{Name: "orders", Representation: model.ContractRepresentationRows, RootShape: model.ContractRootShapeArray, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueString, Required: true}}},
		}}},
	}
	raw, err := json.Marshal(pipeline)
	require.NoError(t, err)
	return &proofreceipts.CandidateTarget{
		OrganizationID: 1, ProjectID: 2, ProjectEnvironmentID: 3, MappingID: 4, CandidateID: 5,
		CandidateSlug: "candidate-1", OrganizationSlug: "acme", ProjectSlug: "data", EnvironmentSlug: "prod",
		ProviderConnectionSlug: "github", RepositoryID: "42", ChangeRequestID: "7", HeadSHA: strings.Repeat("a", 40),
		TargetRef: "refs/heads/main", PipelinePath: "pipelines/orders.json", SpecHash: "sha256:" + strings.Repeat("b", 64),
		CanonicalSpec: raw,
	}
}

type validPipelineValidator struct{}

func (validPipelineValidator) Validate(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*pipelinesvc.PipelineValidationResult, error) {
	return &pipelinesvc.PipelineValidationResult{Valid: true, Diagnostics: []pipelinesvc.PipelineValidationDiagnostic{}}, nil
}

type bindingResolver struct{}

func (bindingResolver) ResolvePipelineDataSourceRefs(_ context.Context, _, _, _ slug.Slug, pipeline *pipelinespec.Pipeline) (*datasourcerefssvc.ResolvedPipeline, error) {
	return &datasourcerefssvc.ResolvedPipeline{Pipeline: pipeline, Resolutions: []model.ResolvedPipelineDataSourceRef{{PipelineSlug: "orders", RefSlug: "source", DataSourceSlug: "postgres", DataSourceType: model.DataSourceTypePostgres}}}, nil
}

type schemaReader struct{ metadata *datasources.SchemaMetadata }

func (r schemaReader) FindByDataSourceSlug(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*datasources.SchemaMetadata, error) {
	return r.metadata, nil
}

type deploymentReader struct {
	deployment *pipelinesrepo.PipelineDeployment
}

func (r deploymentReader) FindDeployment(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*pipelinesrepo.PipelineDeployment, error) {
	return r.deployment, nil
}

type fakeReceiptRepository struct {
	target   *proofreceipts.CandidateTarget
	created  []proofreceipts.CreateReceiptParams
	stored   *proofreceipts.Receipt
	listed   []*proofreceipts.Receipt
	listFn   func(model.ListRequest) []*proofreceipts.Receipt
	listReq  model.ListRequest
	links    []proofreceipts.LifecycleLink
	linksErr error
}

func (r *fakeReceiptRepository) List(_ context.Context, _ postgres.DBTX, _, _, _ slug.Slug, req model.ListRequest) ([]*proofreceipts.Receipt, error) {
	r.listReq = req
	if r.listFn != nil {
		return r.listFn(req), nil
	}
	return r.listed, nil
}

func (r *fakeReceiptRepository) FindLatestForCandidate(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*proofreceipts.Receipt, error) {
	if r.stored != nil {
		return r.stored, nil
	}
	return nil, proofreceipts.ErrReceiptNotFound
}

func (r *fakeReceiptRepository) FindCandidateTarget(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*proofreceipts.CandidateTarget, error) {
	return r.target, nil
}
func (r *fakeReceiptRepository) LockTarget(context.Context, postgres.DBTX, string) error { return nil }
func (r *fakeReceiptRepository) CreateEvidence(_ context.Context, _ postgres.DBTX, _ *proofreceipts.CandidateTarget, params proofreceipts.EvidenceParams) (*proofreceipts.Evidence, error) {
	return &proofreceipts.Evidence{ID: 6, Digest: params.Digest, ObservedAt: params.ObservedAt, ExpiresAt: params.ExpiresAt}, nil
}

func (r *fakeReceiptRepository) FindLatest(context.Context, postgres.DBTX, *proofreceipts.CandidateTarget, string) (*proofreceipts.LatestReceipt, error) {
	return nil, proofreceipts.ErrReceiptNotFound
}

func (r *fakeReceiptRepository) Create(_ context.Context, _ postgres.DBTX, _ *proofreceipts.CandidateTarget, params proofreceipts.CreateReceiptParams) (*proofreceipts.Receipt, error) {
	r.created = append(r.created, params)
	return &proofreceipts.Receipt{Value: params.Receipt}, nil
}

func (r *fakeReceiptRepository) FindBySlug(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*proofreceipts.Receipt, error) {
	if r.stored != nil {
		return r.stored, nil
	}
	return nil, proofreceipts.ErrReceiptNotFound
}

func (r *fakeReceiptRepository) ListLifecycleLinksByReceipt(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) ([]proofreceipts.LifecycleLink, error) {
	return r.links, r.linksErr
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type emptySecretResolver struct{}

func (emptySecretResolver) ResolveRuntimeSecrets(context.Context, slug.Slug, slug.Slug) ([]model.RuntimeSecretValue, error) {
	return nil, nil
}

func (*fakeReceiptRepository) PublicationEnabled(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (bool, error) {
	return true, nil
}

func (*fakeReceiptRepository) LockSource(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error {
	return nil
}

type noContractValidator struct{}

func (noContractValidator) ValidateRevision(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (*odcs.ConversionResult, error) {
	panic("unexpected contract read")
}
