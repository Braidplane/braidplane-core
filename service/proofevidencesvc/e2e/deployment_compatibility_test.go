package e2e

import (
	"slices"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinediff "github.com/Braidplane/braidplane-core/pkg/pipeline/diff"
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

func TestDeployedCompatibilityUsesExactHeadAndImmutableBaseline(t *testing.T) {
	f := newFixture(t)
	ctx, scope := t.Context(), f.scope
	candidateSpec := []byte(`{"metadata":{"slug":"contacts","name":"Candidate private label"},"steps":[{"kind":"query","slug":"extract","query":{"data_source_ref":"source","operation_type":"sql","operation_ref":"select","output_contract":{"name":"contacts","representation":"rows","fields":[{"name":"email_address","type":"string","required":true}]}}}]}`)
	baselineSpec := []byte(strings.NewReplacer("email_address", "legacy_email", "Candidate private label", "Baseline private label").Replace(string(candidateSpec)))
	path := "pipelines/compatibility.yaml"
	receipts, candidates, deployment := proofreceipts.New(), proofcandidates.New(), pipelines.New()
	initial, err := receipts.FindCandidateTarget(ctx, f.pool, scope.Organization, f.request.CandidateSlug)
	require.NoError(t, err)
	enabledMapping, err := candidates.CreateMapping(ctx, f.pool, proofcandidates.CreateMappingParams{
		OrganizationSlug: scope.Organization, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment,
		ProviderConnectionSlug: initial.ProviderConnectionSlug, Provider: "github", ProviderConnectionID: f.connectionID,
		RepositoryID: initial.RepositoryID, TargetRef: initial.TargetRef, PipelinePath: path, CompareDeployed: true,
	})
	require.NoError(t, err)
	otherPathMapping, err := candidates.CreateMapping(ctx, f.pool, proofcandidates.CreateMappingParams{
		OrganizationSlug: scope.Organization, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment,
		ProviderConnectionSlug: initial.ProviderConnectionSlug, Provider: "github", ProviderConnectionID: f.connectionID,
		RepositoryID: initial.RepositoryID, TargetRef: initial.TargetRef, PipelinePath: "pipelines/other.yaml", CompareDeployed: true,
	})
	require.NoError(t, err)
	addCandidate := func(mapping int64, candidateSlug slug.Slug, head string) {
		t.Helper()
		_, err := candidates.Create(ctx, f.pool, scope.Organization, proofcandidates.CreateCandidateParams{
			MappingID: mapping, Slug: candidateSlug, CandidateKey: candidateSlug.String(), ChangeRequestID: "42",
			SourceRef: "refs/heads/feature", HeadSHA: head, SourceSHA256: hash(candidateSpec), SpecHash: hash(candidateSpec), CanonicalSpec: candidateSpec,
		})
		require.NoError(t, err)
	}
	head := strings.Repeat("a", 40)
	addCandidate(enabledMapping, "compatibility-first", head)
	addCandidate(initial.MappingID, "compatibility-disabled", head)
	addCandidate(otherPathMapping, "compatibility-other-path", head)
	txm := postgres.NewTransactionManager(f.pool)
	services := receiptFixtureServices{}
	compiler, err := proofreceiptsvc.New(txm, receipts, services, services, datasources.NewSchemaMetadataRepository(),
		deployment, services, odcscontractsvc.New(txm, odcscontracts.NewRepository(), projects.NewRepository(), compiledplans.NewRepository()), clock.New())
	require.NoError(t, err)
	compile := func(candidate slug.Slug) model.ProofReceipt {
		t.Helper()
		receipt, err := compiler.Compile(ctx, scope.Organization, candidate)
		require.NoError(t, err)
		return receipt.Value
	}
	get := func(id slug.Slug) model.ProofReceipt {
		t.Helper()
		detail, err := compiler.Get(ctx, scope.Organization, scope.Project, scope.Environment, id)
		require.NoError(t, err)
		return detail.Receipt
	}
	missing := compile("compatibility-first")
	require.Equal(t, model.ProofVerdictUnverified, missing.DeployedCompatibilityVerdict())
	require.Zero(t, missing.DeployedCompatibility.DeploymentRevision)
	require.Nil(t, missing.DeployedCompatibility.Impact)
	require.Nil(t, compile("compatibility-disabled").DeployedCompatibility)

	upstreamHead := strings.Repeat("c", 40)
	_, err = deployment.UpsertSpec(ctx, f.pool, scope.Organization, scope.Project, pipelines.UpsertSpecParams{
		Slug: "contacts", Name: "Contacts", SpecJSONB: candidateSpec, SpecHash: hash(candidateSpec),
		SourcePath: &path, SourceRef: &initial.TargetRef, LastAppliedCommit: &upstreamHead,
	})
	require.NoError(t, err)
	upstream, err := deployment.CreateVersion(ctx, f.pool, scope.Organization, scope.Project, pipelines.CreateVersionParams{
		PipelineSlug: "contacts", SpecJSONB: candidateSpec, SpecHash: hash(candidateSpec), SourcePath: &path, GitCommitSHA: &upstreamHead,
	})
	require.NoError(t, err)
	require.NoError(t, deployment.SetCurrentVersion(ctx, f.pool, scope.Organization, scope.Project, "contacts", upstream.ID))
	old, err := deployment.CreateVersion(ctx, f.pool, scope.Organization, scope.Project, pipelines.CreateVersionParams{
		PipelineSlug: "contacts", SpecJSONB: baselineSpec, SpecHash: hash(baselineSpec), SourcePath: &path, GitCommitSHA: new(strings.Repeat("d", 40)),
	})
	require.NoError(t, err)
	deploy := func(version int64) *pipelines.PipelineDeployment {
		t.Helper()
		result, err := deployment.Deploy(ctx, f.pool, scope.Organization, scope.Project, scope.Environment, pipelines.DeployParams{
			PipelineSlug: "contacts", VersionID: version, DeployedByActorType: "member",
		})
		require.NoError(t, err)
		return result
	}
	observed := deploy(old.ID)
	require.Equal(t, model.ProofVerdictStale, get(missing.ID).DeployedCompatibilityVerdict())
	require.Nil(t, compile("compatibility-disabled").DeployedCompatibility)
	_, err = deployment.UpsertSpec(ctx, f.pool, scope.Organization, scope.Project, pipelines.UpsertSpecParams{
		Slug: "contacts", Name: "Contacts", SpecJSONB: candidateSpec, SpecHash: hash(candidateSpec),
		SourcePath: new("pipelines/other.yaml"), SourceRef: &initial.TargetRef, LastAppliedCommit: &upstreamHead,
	})
	require.NoError(t, err)
	otherPath := compile("compatibility-other-path")
	require.Equal(t, model.ProofVerdictUnverified, get(otherPath.ID).DeployedCompatibilityVerdict())
	require.Empty(t, otherPath.DeployedCompatibility.BaselineSpecHash)

	first := compile("compatibility-first")
	require.Equal(t, head, first.Candidate.HeadSHA)
	require.Equal(t, path, first.Candidate.PipelinePath)
	require.Equal(t, model.ProofVerdictFailed, first.DeployedCompatibilityVerdict())
	require.Equal(t, old.SpecHash, first.DeployedCompatibility.BaselineSpecHash)
	require.Equal(t, observed.Revision, first.DeployedCompatibility.DeploymentRevision)
	require.Equal(t, pipelinediff.SeverityBreaking, first.DeployedCompatibility.Impact.Severity)
	require.Contains(t, first.DeployedCompatibility.Impact.Format(), "output_contract.legacy_email")
	require.NotContains(t, first.DeployedCompatibility.Impact.Format(), "private label")
	require.Equal(t, first.ID, compile("compatibility-first").ID)

	next := deploy(old.ID)
	require.Equal(t, observed.CurrentVersionSpecHash, next.CurrentVersionSpecHash)
	require.Greater(t, next.Revision, observed.Revision)
	stale := get(first.ID)
	require.Equal(t, model.ProofVerdictStale, stale.DeployedCompatibilityVerdict())
	require.Equal(t, first.DeployedCompatibility, stale.DeployedCompatibility)
	require.Equal(t, model.ProofVerdictFailed, first.DeployedCompatibilityVerdict())
	refreshed := compile("compatibility-first")
	require.NotEqual(t, first.ID, refreshed.ID)
	require.Equal(t, next.Revision, refreshed.DeployedCompatibility.DeploymentRevision)

	otherSpec := []byte(strings.Replace(string(candidateSpec), "Candidate private label", "Other private label", 1))
	otherVersion, err := deployment.CreateVersion(ctx, f.pool, scope.Organization, scope.Project, pipelines.CreateVersionParams{
		PipelineSlug: "contacts", SpecJSONB: otherSpec, SpecHash: hash(otherSpec), SourcePath: new("pipelines/other.yaml"),
	})
	require.NoError(t, err)
	deploy(otherVersion.ID)
	missingAgain := compile("compatibility-first")
	require.NotEqual(t, missing.ID, missingAgain.ID)
	require.Equal(t, &refreshed.ID, missingAgain.SupersedesReceiptID)
	require.Equal(t, model.ProofVerdictUnverified, get(missingAgain.ID).DeployedCompatibilityVerdict())
	require.Equal(t, missingAgain.ID, compile("compatibility-first").ID)

	newHead := strings.Repeat("b", 40)
	addCandidate(enabledMapping, "compatibility-next", newHead)
	require.Equal(t, model.ProofVerdictSuperseded, get(refreshed.ID).DeployedCompatibilityVerdict())
	_, err = compiler.Compile(ctx, scope.Organization, "compatibility-first")
	require.ErrorIs(t, err, proofreceipts.ErrCandidateNotFound)
	deploy(upstream.ID)
	second := compile("compatibility-next")
	require.Equal(t, newHead, second.Candidate.HeadSHA)
	require.Equal(t, model.ProofVerdictPassed, second.DeployedCompatibilityVerdict())
	require.Empty(t, second.DeployedCompatibility.Impact.Findings)

	_, err = candidates.CloseLatest(ctx, f.pool, scope.Organization, enabledMapping, second.Candidate.ChangeRequestID, newHead)
	require.NoError(t, err)
	_, err = compiler.Compile(ctx, scope.Organization, "compatibility-next")
	require.ErrorIs(t, err, proofreceipts.ErrCandidateNotFound)

	_, err = f.pool.Exec(ctx, `UPDATE proof_change_candidate_mappings SET disabled_at = NOW() WHERE id = $1`, otherPathMapping)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictSuperseded, get(otherPath.ID).DeployedCompatibilityVerdict())
	page, err := compiler.List(ctx, scope.Organization, scope.Project, scope.Environment, model.ListRequest{Limit: 20})
	require.NoError(t, err)
	require.True(t, slices.ContainsFunc(page.Items, func(detail *proofreceiptsvc.ReceiptDetail) bool {
		return detail.Receipt.ID == otherPath.ID && detail.Receipt.DeployedCompatibilityVerdict() == model.ProofVerdictSuperseded
	}))
}
