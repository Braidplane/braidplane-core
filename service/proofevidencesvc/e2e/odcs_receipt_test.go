package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/stretchr/testify/require"
)

func TestODCSReceiptsPreserveRevisionHistoryAndTenantBoundaries(t *testing.T) {
	f := newFixture(t)
	other := newFixture(t)
	ctx := t.Context()
	txm := postgres.NewTransactionManager(f.pool)
	catalog := odcscontractsvc.New(txm, odcscontracts.NewRepository(), projects.NewRepository(), compiledplans.NewRepository())
	_, err := catalog.CreateContract(ctx, f.scope.Organization, f.scope.Project, model.NewODCSContract{Slug: "orders", Name: "Orders", ODCSID: "orders"})
	require.NoError(t, err)
	document := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","status":"active","schema":[{"id":"order","name":"Orders","properties":[{"id":"order-id","name":"id","logicalType":"string"}]}]}`
	revisions := map[slug.Slug]*model.ODCSRevision{}
	for _, revision := range []slug.Slug{"v1", "v2"} {
		revisions[revision], err = catalog.CreateRevision(ctx, f.scope.Organization, f.scope.Project, "orders", revision, json.RawMessage(strings.Replace(document, `"version":"1"`, `"version":"`+revision.String()+`"`, 1)))
		require.NoError(t, err)
	}
	_, err = catalog.ValidateRevision(ctx, other.scope.Organization, other.scope.Project, "orders", "v1", "order")
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	_, err = catalog.ValidateRevision(ctx, f.scope.Organization, "absent-project", "orders", "v1", "order")
	require.ErrorIs(t, err, odcscontracts.ErrNotFound)
	repo := proofreceipts.New()
	original, err := repo.FindCandidateTarget(ctx, f.pool, f.scope.Organization, f.request.CandidateSlug)
	require.NoError(t, err)
	services := &receiptFixtureServices{}
	compiler, err := proofreceiptsvc.New(txm, repo, services, services, datasources.NewSchemaMetadataRepository(), pipelines.New(), services, catalog, clock.New())
	require.NoError(t, err)
	var first *proofreceipts.Receipt
	var previous model.ProofReceipt
	var previousCandidateID *int64
	for _, revision := range []slug.Slug{"v1", "v2"} {
		pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "contacts", Name: "Contacts"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: "source", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: "hubspot", OperationInline: map[string]any{"type": "hubspot", "object_type": "contacts", "operation": "list"}, OutputContractRef: &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: revision, SchemaObjectID: "order", Representation: model.ContractRepresentationRows}}}}}
		raw, err := json.Marshal(pipeline)
		require.NoError(t, err)
		candidate, err := proofcandidates.New().Create(ctx, f.pool, f.scope.Organization, proofcandidates.CreateCandidateParams{MappingID: original.MappingID, SupersedesCandidateID: previousCandidateID, Slug: slug.Slug("candidate-" + revision.String()), CandidateKey: f.scope.Organization.String() + revision.String(), ChangeRequestID: "42", SourceRef: "feature", HeadSHA: strings.Repeat(string(revision[len(revision)-1]), 40), SourceSHA256: hash(raw), SpecHash: hash(raw), CanonicalSpec: raw})
		require.NoError(t, err)
		previousCandidateID = &candidate.ID
		receipt, err := compiler.Compile(ctx, f.scope.Organization, candidate.Slug)
		require.NoError(t, err)
		claim := receiptODCSClaim(t, receipt.Value, "/revision_validity")
		require.Equal(t, model.ProofVerdictPassed, claim.Verdict)
		require.Contains(t, claim.Identity.SemanticPath, revisions[revision].Digest)
		require.Equal(t, model.ProofVerdictUnverified, receiptODCSClaim(t, receipt.Value, "/runtime_payload_conformance").Verdict)
		if first == nil {
			first = receipt
			stored, err := repo.FindBySlug(ctx, f.pool, f.scope.Organization, f.scope.Project, f.scope.Environment, first.Value.ID)
			require.NoError(t, err)
			previous = stored.Value
		} else {
			require.NotEqual(t, first.Value.ID, receipt.Value.ID)
			require.NotEqual(t, first.Value.Candidate.SpecHash, receipt.Value.Candidate.SpecHash)
			require.Equal(t, &first.Value.ID, receipt.Value.SupersedesReceiptID)
			stored, err := repo.FindBySlug(ctx, f.pool, f.scope.Organization, f.scope.Project, f.scope.Environment, first.Value.ID)
			require.NoError(t, err)
			require.Equal(t, previous, stored.Value, "stored historical receipt is immutable")
			detail, err := compiler.Get(ctx, f.scope.Organization, f.scope.Project, f.scope.Environment, first.Value.ID)
			require.NoError(t, err)
			require.Equal(t, model.ProofVerdictSuperseded, detail.Receipt.AggregateVerdict)
		}
	}
	_, err = compiler.Get(ctx, other.scope.Organization, other.scope.Project, other.scope.Environment, first.Value.ID)
	require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound)
}

func receiptODCSClaim(t *testing.T, receipt model.ProofReceipt, suffix string) model.ProofClaim {
	t.Helper()
	for _, claim := range receipt.Claims {
		if strings.HasSuffix(claim.Identity.SemanticPath, suffix) {
			return claim
		}
	}
	t.Fatalf("missing ODCS claim %s", suffix)
	return model.ProofClaim{}
}
