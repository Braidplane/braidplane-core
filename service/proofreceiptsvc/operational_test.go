package proofreceiptsvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/stretchr/testify/require"
)

type operationalRunStore struct {
	run   *proofreceipts.OperationalRun
	err   error
	calls int
}

func (r *operationalRunStore) FindOperationalRun(_ context.Context, _ postgres.DBTX, org, project, environment, run slug.Slug) (*proofreceipts.OperationalRun, error) {
	r.calls++
	return r.run, r.err
}

func newOperationalFixture(t *testing.T) (*OperationalCompiler, *fakeReceiptRepository, *operationalRunStore, OperationalSelection, time.Time) {
	t.Helper()
	selection, observation, now := operationalPolicyFixture(t)
	target := testCandidateTarget(t)
	raw, compiledDigest, err := contractplan.Compile(contractplan.Input{
		Source: contractplan.Source{Commit: target.HeadSHA, Path: target.PipelinePath}, Authored: target.CanonicalSpec,
		Operations: []contractplan.Operation{{Step: "extract", Role: "query", DataSource: "source", Slug: "select-orders", Revision: "v1", Content: json.RawMessage(`{"data":{"statement":"{\"sql\":\"SELECT 1\"}"},"parameters":[]}`)}},
	})
	require.NoError(t, err)
	compiled, err := contractplan.Read(raw, compiledDigest)
	require.NoError(t, err)
	target.SpecHash = "sha256:" + compiled.AuthoredSpecHash
	executable, err := compiled.RuntimePipeline()
	require.NoError(t, err)
	executable.Steps[0].Query.DataSourceSlug = "postgres"
	resolution, resolutionDigest, err := deploymentplan.Encode(deploymentplan.Plan{
		Organization: target.OrganizationSlug, Project: target.ProjectSlug, Environment: target.EnvironmentSlug,
		TemplateDigest: compiledDigest, CompiledDigest: compiledDigest, CompiledPlan: raw, Pipeline: executable,
		Targets: []deploymentplan.Target{{Step: "extract", Role: "query", Reference: "source", DataSource: "postgres", ConfigDigest: compiledDigest, BindingRevision: "revision-1"}},
	})
	require.NoError(t, err)
	repo := &fakeReceiptRepository{target: target}
	base, err := New(testutil.FakeTransactionManager{}, repo, validPipelineValidator{}, bindingResolver{}, schemaReader{metadata: &datasources.SchemaMetadata{Slug: "schema-1", UpdatedAt: now}}, deploymentReader{}, emptySecretResolver{}, noContractValidator{}, fixedClock{now})
	require.NoError(t, err)
	static, err := base.Compile(t.Context(), target.OrganizationSlug, target.CandidateSlug)
	require.NoError(t, err)
	repo.stored, repo.created = static, nil
	selection.Candidate = static.Value.Candidate
	selection.CompiledPlanDigest, selection.DeploymentResolutionDigest = compiledDigest, resolutionDigest
	selection.Target = "postgres"
	check := pipelinerun.StructuralCheck{
		ID: selection.CheckID, Step: selection.Step, Origin: selection.Origin, Phase: selection.Phase, Declaration: selection.Declaration, ProjectionDigest: selection.ProjectionDigest,
		CompiledPlanDigest: compiledDigest, DeploymentResolutionDigest: resolutionDigest, AttemptNumber: selection.Attempt, Evaluator: selection.Evaluator, Policy: selection.EvaluatorPolicy, Target: selection.Target,
		Status: observation.Status, ReasonCode: observation.ReasonCode, StartedAt: selection.StartedAt, CompletedAt: selection.CompletedAt, Evidence: observation.Evidence, EvidenceDigest: selection.EvidenceDigest, Window: selection.Window,
	}
	timeline, err := json.Marshal(pipelinerun.RunTimeline{
		RunSlug: selection.Run.String(), PipelineSlug: selection.Pipeline, ProjectSlug: target.ProjectSlug, EnvironmentSlug: target.EnvironmentSlug,
		DeploymentResolutionDigest: resolutionDigest, Status: "failed", StructuralChecks: []pipelinerun.StructuralCheck{check},
	})
	require.NoError(t, err)
	ended := now.Add(-time.Second)
	runs := &operationalRunStore{run: &proofreceipts.OperationalRun{
		Pipeline: selection.Pipeline, Run: selection.Run, Attempt: 1, Status: "failed", Timeline: timeline, EndedAt: &ended,
		SpecHash: compiled.AuthoredSpecHash, VersionCommit: target.HeadSHA, ResolutionDigest: resolutionDigest, Resolution: resolution,
	}}
	audit := eventrecorder.New(observationAuditStore{create: func(_ context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
		return &model.Event{Slug: params.Slug}, nil
	}})
	service, err := NewOperationalCompiler(base, base, runs, audit)
	require.NoError(t, err)
	return service, repo, runs, selection, now
}

func TestOperationalReceiptIsSeparateAndSurvivesLaterRunFailure(t *testing.T) {
	t.Parallel()
	s, repo, _, selection, now := newOperationalFixture(t)
	before, err := json.Marshal(repo.stored.Value)
	require.NoError(t, err)
	scope := ObservationScope{Organization: selection.Candidate.OrganizationSlug, Project: selection.Candidate.ProjectSlug, Environment: selection.Candidate.EnvironmentSlug}
	stored, err := s.CompileOperational(t.Context(), scope, "reviewer", repo.stored.Value.ID, selection)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictPassed, stored.Value.AggregateVerdict)
	require.NotEqual(t, repo.stored.Value.ID, stored.Value.ID)
	require.Nil(t, stored.Value.SupersedesReceiptID)
	require.Equal(t, now.Add(7*24*time.Hour), stored.Value.RetainUntil)
	require.Equal(t, OperationalPolicyVersion, stored.Value.Operational.PolicyVersion)
	require.Equal(t, repo.stored.Value.ID, stored.Value.Operational.SourceReceiptID)
	require.Equal(t, model.ProofEvidenceLevelOperational, *stored.Value.Claims[0].ActualEvidenceLevel)
	require.Len(t, repo.created, 1)
	require.Nil(t, repo.created[0].Supersedes)
	after, err := json.Marshal(repo.stored.Value)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestOperationalPreservesHistoricalReadAndDisclosesMissingEvidence(t *testing.T) {
	t.Parallel()
	s, repo, runs, selection, _ := newOperationalFixture(t)
	candidate := selection.Candidate
	stored, err := s.CompileOperational(t.Context(), ObservationScope{Organization: candidate.OrganizationSlug, Project: candidate.ProjectSlug, Environment: candidate.EnvironmentSlug}, "reviewer", repo.stored.Value.ID, selection)
	require.NoError(t, err)
	repo.stored = stored
	detail, err := s.Get(t.Context(), candidate.OrganizationSlug, candidate.ProjectSlug, candidate.EnvironmentSlug, stored.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofOperationalEvidenceStatusAvailable, detail.OperationalEvidenceStatus)
	require.NotNil(t, detail.OperationalAdmissionEnabled)
	require.True(t, *detail.OperationalAdmissionEnabled)
	runs.err = proofreceipts.ErrReceiptNotFound
	detail, err = s.Get(t.Context(), candidate.OrganizationSlug, candidate.ProjectSlug, candidate.EnvironmentSlug, stored.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofOperationalEvidenceStatusMissing, detail.OperationalEvidenceStatus)
	require.Equal(t, model.ProofVerdictUnverified, detail.OperationalCurrentVerdict)
	require.Equal(t, model.ProofVerdictPassed, detail.Receipt.AggregateVerdict)
	require.Equal(t, stored.Value.Operational.RecordDigest, detail.Receipt.Operational.RecordDigest)
}

func TestOperationalAdmissionRequiresExactRetainedRunAndCheck(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*proofreceipts.OperationalRun)
	}{
		{"attempt", func(r *proofreceipts.OperationalRun) { r.Attempt++ }},
		{"version commit", func(r *proofreceipts.OperationalRun) { r.VersionCommit = "other" }},
		{"same YAML different plan", func(r *proofreceipts.OperationalRun) { r.Resolution = append(r.Resolution, ' ') }},
		{"unfinished run", func(r *proofreceipts.OperationalRun) { r.EndedAt = nil }},
		{"nonterminal run", func(r *proofreceipts.OperationalRun) { r.Status = "running" }},
		{"corrupt timeline", func(r *proofreceipts.OperationalRun) { r.Timeline = json.RawMessage(`{}`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo, runs, selection, _ := newOperationalFixture(t)
			tc.edit(runs.run)
			c := selection.Candidate
			_, err := s.CompileOperational(t.Context(), ObservationScope{Organization: c.OrganizationSlug, Project: c.ProjectSlug, Environment: c.EnvironmentSlug}, "reviewer", repo.stored.Value.ID, selection)
			require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
			require.Empty(t, repo.created)
		})
	}
	s, repo, runs, selection, _ := newOperationalFixture(t)
	_, err := s.CompileOperational(t.Context(), ObservationScope{Organization: "other", Project: selection.Candidate.ProjectSlug, Environment: selection.Candidate.EnvironmentSlug}, "reviewer", repo.stored.Value.ID, selection)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	require.Zero(t, runs.calls)
}

func TestOperationalEvidenceExpiryAndRetentionChangePreserveCapture(t *testing.T) {
	t.Parallel()
	s, repo, runs, selection, now := newOperationalFixture(t)
	candidate := selection.Candidate
	scope := ObservationScope{Organization: candidate.OrganizationSlug, Project: candidate.ProjectSlug, Environment: candidate.EnvironmentSlug}
	// A source window shorter than Proof's seven days must remain independent.
	runs.run.Settings = json.RawMessage(`{"retention":{"run_history_retention_days":1}}`)
	stored, err := s.CompileOperational(t.Context(), scope, "reviewer", repo.stored.Value.ID, selection)
	require.NoError(t, err)
	require.Equal(t, runs.run.EndedAt.Add(24*time.Hour), stored.Value.Operational.ExpiresAt)
	require.Equal(t, now.Add(7*24*time.Hour), stored.Value.RetainUntil)
	repo.stored = stored
	s.base.clock = fixedClock{now.Add(25 * time.Hour)}
	detail, err := s.Get(t.Context(), candidate.OrganizationSlug, candidate.ProjectSlug, candidate.EnvironmentSlug, stored.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofOperationalEvidenceStatusExpired, detail.OperationalEvidenceStatus)
	require.Equal(t, model.ProofVerdictStale, detail.OperationalCurrentVerdict)
	require.Equal(t, model.ProofVerdictPassed, detail.Receipt.AggregateVerdict)
	// Increasing source retention cannot renew an existing capture's expiry.
	runs.run.Settings = json.RawMessage(`{"retention":{"run_history_retention_days":30}}`)
	detail, err = s.Get(t.Context(), candidate.OrganizationSlug, candidate.ProjectSlug, candidate.EnvironmentSlug, stored.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofOperationalEvidenceStatusExpired, detail.OperationalEvidenceStatus)
	require.Equal(t, stored.Value.Operational.ExpiresAt, detail.Receipt.Operational.ExpiresAt)
}

func TestOperationalCaptureDisclosesChangedEvidenceAndRejectsMissingAdmission(t *testing.T) {
	t.Parallel()
	s, repo, runs, selection, _ := newOperationalFixture(t)
	candidate := selection.Candidate
	scope := ObservationScope{Organization: candidate.OrganizationSlug, Project: candidate.ProjectSlug, Environment: candidate.EnvironmentSlug}
	stored, err := s.CompileOperational(t.Context(), scope, "reviewer", repo.stored.Value.ID, selection)
	require.NoError(t, err)
	repo.stored = stored
	var timeline pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal(runs.run.Timeline, &timeline))
	timeline.StructuralChecks[0].Status, timeline.StructuralChecks[0].ReasonCode = pipelinerun.CheckStatusFailed, "contract_violation"
	runs.run.Timeline, err = json.Marshal(timeline)
	require.NoError(t, err)
	detail, err := s.Get(t.Context(), candidate.OrganizationSlug, candidate.ProjectSlug, candidate.EnvironmentSlug, stored.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofOperationalEvidenceStatusChanged, detail.OperationalEvidenceStatus)
	require.Equal(t, model.ProofVerdictInconclusive, detail.OperationalCurrentVerdict)
	require.Equal(t, model.ProofVerdictPassed, detail.Receipt.AggregateVerdict)
	timeline.StructuralChecks = nil
	runs.run.Timeline, err = json.Marshal(timeline)
	require.NoError(t, err)
	_, _, _, err = s.loadOperational(t.Context(), nil, selection)
	require.ErrorIs(t, err, proofreceipts.ErrReceiptNotFound)
}

type changedOperationalBinding struct{}

func (changedOperationalBinding) ResolvePipelineDataSourceRefs(ctx context.Context, org, project, environment slug.Slug, pipeline *pipelinespec.Pipeline) (*datasourcerefssvc.ResolvedPipeline, error) {
	resolved, err := (bindingResolver{}).ResolvePipelineDataSourceRefs(ctx, org, project, environment, pipeline)
	if err == nil {
		resolved.Resolutions[0].DataSourceSlug = "replacement"
	}
	return resolved, err
}

func TestOperationalHistoryKeepsCapturedTargetAfterBindingEdit(t *testing.T) {
	t.Parallel()
	s, repo, _, selection, _ := newOperationalFixture(t)
	candidate := selection.Candidate
	scope := ObservationScope{Organization: candidate.OrganizationSlug, Project: candidate.ProjectSlug, Environment: candidate.EnvironmentSlug}
	source := repo.stored
	stored, err := s.CompileOperational(t.Context(), scope, "reviewer", source.Value.ID, selection)
	require.NoError(t, err)
	s.base.bindings = changedOperationalBinding{}
	repo.stored = stored
	detail, err := s.Get(t.Context(), candidate.OrganizationSlug, candidate.ProjectSlug, candidate.EnvironmentSlug, stored.Value.ID)
	require.NoError(t, err)
	require.Equal(t, model.ProofOperationalEvidenceStatusAvailable, detail.OperationalEvidenceStatus)
	require.Equal(t, selection.Target, detail.Receipt.Operational.Selection.Target)
	repo.stored = source
	_, err = s.CompileOperational(t.Context(), scope, "reviewer", source.Value.ID, selection)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied, "new admission must still match current authorized bindings")
}

func TestOperationalReceiptValidationRejectsLevelAndCaptureSubstitution(t *testing.T) {
	t.Parallel()
	s, repo, _, selection, _ := newOperationalFixture(t)
	c := selection.Candidate
	stored, err := s.CompileOperational(t.Context(), ObservationScope{Organization: c.OrganizationSlug, Project: c.ProjectSlug, Environment: c.EnvironmentSlug}, "reviewer", repo.stored.Value.ID, selection)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		edit func(*model.ProofReceipt)
	}{
		{"longer receipt retention", func(r *model.ProofReceipt) { r.RetainUntil = r.RetainUntil.Add(time.Second) }},
		{"static supersession", func(r *model.ProofReceipt) { id := slug.Slug("static-receipt"); r.SupersedesReceiptID = &id }},
		{"another candidate", func(r *model.ProofReceipt) { r.Operational.Selection.Candidate.EnvironmentSlug = "other" }},
		{"bounded execution substitution", func(r *model.ProofReceipt) {
			level := model.ProofEvidenceLevelBoundedExecution
			r.Claims[0].ActualEvidenceLevel = &level
		}},
		{"different check", func(r *model.ProofReceipt) { r.Operational.Selection.CheckID = strings.Repeat("f", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(stored.Value)
			require.NoError(t, err)
			var receipt model.ProofReceipt
			require.NoError(t, json.Unmarshal(raw, &receipt))
			tc.edit(&receipt)
			require.ErrorIs(t, receipt.Validate(), xerror.InvalidInput(xerror.Pipeline))
		})
	}
}
