package proofreceiptsvc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func operationalPolicyFixture(t *testing.T) (OperationalSelection, *OperationalObservation, time.Time) {
	t.Helper()
	now := time.Date(2026, time.October, 19, 10, 0, 0, 0, time.UTC)
	started, completed := now.Add(-time.Minute), now.Add(-time.Second)
	target := testCandidateTarget(t)
	selection := OperationalSelection{
		CandidateSlug: target.CandidateSlug,
		Candidate:     receiptCandidate(target, "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64), CompilerVersion, PolicyVersion),
		Pipeline:      "orders", VersionCommit: target.HeadSHA,
		CompiledPlanDigest: "sha256:" + strings.Repeat("1", 64), DeploymentResolutionDigest: "sha256:" + strings.Repeat("2", 64),
		Run: "run-1", Attempt: 1, CheckID: strings.Repeat("3", 64), Step: "extract",
		Origin: "step", Phase: "producer_output", Declaration: "step/producer_output/orders/revision-1/orders",
		ProjectionDigest: strings.Repeat("4", 64), Target: "source",
		Window:    pipelinerun.CheckWindow{Kind: "full", SourceSteps: []slug.Slug{"extract"}},
		Evaluator: "braidplane/structural-runtime/v1", EvaluatorPolicy: contractplan.StructuralPolicyID,
		StartedAt: &started, CompletedAt: &completed,
	}
	evidence := &pipelinerun.CheckEvidence{Rows: 2, DurationMs: 59000, BatchScope: "observed_batch_completeness_unknown"}
	raw, err := json.Marshal(evidence)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	selection.EvidenceDigest = hex.EncodeToString(sum[:])
	return selection, &OperationalObservation{Selection: selection, Status: pipelinerun.CheckStatusPassed, ReasonCode: "conformant", Evidence: evidence, ExpiresAt: now.Add(time.Hour)}, now
}

func TestOperationalAdmissionVerdicts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*OperationalObservation)
		want model.ProofVerdict
	}{
		{"decisive pass", func(*OperationalObservation) {}, model.ProofVerdictPassed},
		{"decisive failure", func(o *OperationalObservation) {
			o.Status, o.ReasonCode = pipelinerun.CheckStatusFailed, "contract_violation"
		}, model.ProofVerdictFailed},
		{"skipped", func(o *OperationalObservation) {
			o.Status, o.ReasonCode = pipelinerun.CheckStatusSkipped, "downstream_not_reached"
		}, model.ProofVerdictUnverified},
		{"unavailable", func(o *OperationalObservation) {
			o.Status, o.ReasonCode = pipelinerun.CheckStatusUnavailable, "evaluator_unavailable"
		}, model.ProofVerdictUnverified},
		{"error", func(o *OperationalObservation) {
			o.Status, o.ReasonCode = pipelinerun.CheckStatusError, "evaluation_error"
		}, model.ProofVerdictInconclusive},
		{"unknown status", func(o *OperationalObservation) { o.Status = pipelinerun.CheckStatus(99) }, model.ProofVerdictInconclusive},
		{"unknown pass reason", func(o *OperationalObservation) { o.ReasonCode = "successful_run" }, model.ProofVerdictInconclusive},
		{"unknown failure reason", func(o *OperationalObservation) {
			o.Status, o.ReasonCode = pipelinerun.CheckStatusFailed, "sink_failure"
		}, model.ProofVerdictInconclusive},
		{"expired", func(o *OperationalObservation) { o.ExpiresAt = *o.Selection.CompletedAt }, model.ProofVerdictStale},
		{"expired skipped", func(o *OperationalObservation) {
			o.Status = pipelinerun.CheckStatusSkipped
			o.ExpiresAt = *o.Selection.CompletedAt
		}, model.ProofVerdictUnverified},
		{"expired absent evaluation", func(o *OperationalObservation) {
			o.Selection.CompletedAt = nil
			o.ExpiresAt = o.Selection.StartedAt.Add(-time.Second)
		}, model.ProofVerdictInconclusive},
		{"missing evaluator", func(o *OperationalObservation) { o.Selection.Evaluator = "" }, model.ProofVerdictUnverified},
		{"unsupported evaluator", func(o *OperationalObservation) { o.Selection.Evaluator = "native-quality/v1" }, model.ProofVerdictUnverified},
		{"unsupported evaluator policy", func(o *OperationalObservation) { o.Selection.EvaluatorPolicy = "native-sla/v1" }, model.ProofVerdictUnverified},
		{"missing start", func(o *OperationalObservation) { o.Selection.StartedAt = nil }, model.ProofVerdictInconclusive},
		{"missing completion", func(o *OperationalObservation) { o.Selection.CompletedAt = nil }, model.ProofVerdictInconclusive},
		{"future completion", func(o *OperationalObservation) { at := o.ExpiresAt; o.Selection.CompletedAt = &at }, model.ProofVerdictInconclusive},
		{"completion before start", func(o *OperationalObservation) {
			at := o.Selection.StartedAt.Add(-time.Second)
			o.Selection.CompletedAt = &at
		}, model.ProofVerdictInconclusive},
		{"replayed acknowledgement", func(o *OperationalObservation) { o.ReusedFromRun = "original-run" }, model.ProofVerdictUnverified},
		{"missing aggregates", func(o *OperationalObservation) { o.Evidence, o.Selection.EvidenceDigest = nil, "" }, model.ProofVerdictInconclusive},
		{"truncated observed scope", func(o *OperationalObservation) {
			o.Evidence.BatchScope = "truncated_batch"
			refreshOperationalEvidenceDigest(t, o)
		}, model.ProofVerdictPassed},
		{"empty observed scope", func(o *OperationalObservation) {
			o.Evidence.Rows, o.Evidence.Empty, o.ReasonCode = 0, true, "empty_batch"
			refreshOperationalEvidenceDigest(t, o)
		}, model.ProofVerdictPassed},
		{"empty reason with rows", func(o *OperationalObservation) { o.ReasonCode = "empty_batch" }, model.ProofVerdictInconclusive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, observation, now := operationalPolicyFixture(t)
			tc.edit(observation)
			before, err := json.Marshal(observation)
			require.NoError(t, err)
			verdict, digest, err := admitOperationalCheck(observation.Selection, observation, now)
			require.NoError(t, err)
			require.Equal(t, tc.want, verdict)
			require.True(t, strings.HasPrefix(digest, "sha256:"))
			after, err := json.Marshal(observation)
			require.NoError(t, err)
			require.Equal(t, before, after, "admission must preserve captured evidence")
		})
	}
	selection, _, now := operationalPolicyFixture(t)
	verdict, digest, err := admitOperationalCheck(selection, nil, now)
	require.NoError(t, err)
	require.Equal(t, model.ProofVerdictUnverified, verdict)
	require.Empty(t, digest, "missing records have no invented evidence digest")
}

func TestOperationalAdmissionRejectsSubstitution(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*OperationalSelection)
	}{
		{"tenant", func(s *OperationalSelection) { s.Candidate.OrganizationSlug = "other" }},
		{"project", func(s *OperationalSelection) { s.Candidate.ProjectSlug = "other" }},
		{"environment", func(s *OperationalSelection) { s.Candidate.EnvironmentSlug = "other" }},
		{"candidate", func(s *OperationalSelection) { s.CandidateSlug = "candidate-2" }},
		{"repository", func(s *OperationalSelection) { s.Candidate.RepositoryID = "another-repository" }},
		{"provider", func(s *OperationalSelection) { s.Candidate.ProviderConnectionSlug = "other" }},
		{"change request", func(s *OperationalSelection) { s.Candidate.ChangeRequestID = "8" }},
		{"candidate commit", func(s *OperationalSelection) { s.Candidate.HeadSHA = strings.Repeat("b", 40) }},
		{"version commit", func(s *OperationalSelection) { s.VersionCommit = strings.Repeat("b", 40) }},
		{"pipeline", func(s *OperationalSelection) { s.Pipeline = "other" }},
		{"spec", func(s *OperationalSelection) { s.Candidate.SpecHash = "sha256:" + strings.Repeat("f", 64) }},
		{"bindings", func(s *OperationalSelection) { s.Candidate.BindingFingerprint = "sha256:" + strings.Repeat("f", 64) }},
		{"capabilities", func(s *OperationalSelection) { s.Candidate.CapabilityFingerprint = "sha256:" + strings.Repeat("f", 64) }},
		{"compiler", func(s *OperationalSelection) { s.Candidate.CompilerVersion = "other" }},
		{"candidate policy", func(s *OperationalSelection) { s.Candidate.PolicyVersion = "other" }},
		{"compiled plan", func(s *OperationalSelection) { s.CompiledPlanDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"deployment", func(s *OperationalSelection) { s.DeploymentResolutionDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"run", func(s *OperationalSelection) { s.Run = "run-2" }},
		{"attempt", func(s *OperationalSelection) { s.Attempt++ }},
		{"check", func(s *OperationalSelection) { s.CheckID = strings.Repeat("f", 64) }},
		{"step", func(s *OperationalSelection) { s.Step = "other" }},
		{"named input", func(s *OperationalSelection) { s.Input = "accounts" }},
		{"origin", func(s *OperationalSelection) { s.Origin = "operation" }},
		{"phase", func(s *OperationalSelection) { s.Phase = "pre_sink" }},
		{"contract revision", func(s *OperationalSelection) { s.Declaration = "step/producer_output/orders/revision-2/orders" }},
		{"projection", func(s *OperationalSelection) { s.ProjectionDigest = strings.Repeat("f", 64) }},
		{"target", func(s *OperationalSelection) { s.Target = "other" }},
		{"window kind", func(s *OperationalSelection) { s.Window.Kind = "incremental" }},
		{"window lower bound", func(s *OperationalSelection) { s.Window.HasLowerBound = true }},
		{"window upper bound", func(s *OperationalSelection) { s.Window.HasUpperBound = true }},
		{"window source", func(s *OperationalSelection) { s.Window.SourceSteps = []slug.Slug{"other"} }},
		{"backfill", func(s *OperationalSelection) { s.Window.BackfillSeriesSlug = "backfill-1" }},
		{"backfill chunk", func(s *OperationalSelection) { s.Window.BackfillChunkIndex++ }},
		{"evaluator", func(s *OperationalSelection) { s.Evaluator = "other" }},
		{"evaluator policy", func(s *OperationalSelection) { s.EvaluatorPolicy = "other" }},
		{"observation start", func(s *OperationalSelection) { at := s.StartedAt.Add(-time.Second); s.StartedAt = &at }},
		{"observation completion", func(s *OperationalSelection) { at := s.CompletedAt.Add(-time.Second); s.CompletedAt = &at }},
		{"aggregate digest", func(s *OperationalSelection) { s.EvidenceDigest = strings.Repeat("f", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected, observation, now := operationalPolicyFixture(t)
			tc.edit(&observation.Selection)
			verdict, digest, err := admitOperationalCheck(expected, observation, now)
			require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
			require.Equal(t, model.ProofVerdictUnknown, verdict)
			require.Empty(t, digest)
		})
	}
}

func TestOperationalAdmissionRejectsCorruptAggregates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*OperationalObservation)
	}{
		{"changed counts", func(o *OperationalObservation) { o.Evidence.Rows++ }},
		{"negative count", func(o *OperationalObservation) { o.Evidence.Rows = -1 }},
		{"unbounded count", func(o *OperationalObservation) { o.Evidence.Rows = 1000000001 }},
		{"negative duration", func(o *OperationalObservation) { o.Evidence.DurationMs = -1 }},
		{"unbounded duration", func(o *OperationalObservation) { o.Evidence.DurationMs = 86400001 }},
		{"contradictory empty", func(o *OperationalObservation) { o.Evidence.Empty = true }},
		{"invented completeness", func(o *OperationalObservation) { o.Evidence.BatchScope = "whole_table" }},
		{"missing digest", func(o *OperationalObservation) { o.Selection.EvidenceDigest = "" }},
		{"missing evidence", func(o *OperationalObservation) { o.Evidence = nil }},
		{"missing expiry", func(o *OperationalObservation) { o.ExpiresAt = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, observation, now := operationalPolicyFixture(t)
			tc.edit(observation)
			_, _, err := admitOperationalCheck(observation.Selection, observation, now)
			require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
		})
	}
}

func TestOperationalAdmissionDigestSurvivesPersistence(t *testing.T) {
	t.Parallel()
	expected, observation, now := operationalPolicyFixture(t)
	wantVerdict, wantDigest, err := admitOperationalCheck(expected, observation, now)
	require.NoError(t, err)
	raw, err := json.Marshal(observation)
	require.NoError(t, err)
	var restored OperationalObservation
	require.NoError(t, json.Unmarshal(raw, &restored))
	gotVerdict, gotDigest, err := admitOperationalCheck(expected, &restored, now)
	require.NoError(t, err)
	require.Equal(t, wantVerdict, gotVerdict)
	require.Equal(t, wantDigest, gotDigest)
	restored.ReasonCode = "unknown"
	_, changedDigest, err := admitOperationalCheck(expected, &restored, now)
	require.NoError(t, err)
	require.NotEqual(t, wantDigest, changedDigest, "the receipt digest must bind the result as well as the aggregate")
}

func TestOperationalAdmissionRejectsIncompleteSelections(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*OperationalSelection)
	}{
		{"candidate identity", func(s *OperationalSelection) { s.Candidate.EnvironmentSlug = "" }},
		{"unbound version", func(s *OperationalSelection) { s.VersionCommit = strings.Repeat("f", 40) }},
		{"attempt", func(s *OperationalSelection) { s.Attempt = 0 }},
		{"missing run", func(s *OperationalSelection) { s.Run = "" }},
		{"invalid target", func(s *OperationalSelection) { s.Target = "invalid target" }},
		{"digest format", func(s *OperationalSelection) { s.CompiledPlanDigest = strings.Repeat("1", 64) }},
		{"invalid digest", func(s *OperationalSelection) { s.CheckID = strings.Repeat("z", 64) }},
		{"noncanonical digest", func(s *OperationalSelection) { s.ProjectionDigest = strings.Repeat("A", 64) }},
		{"missing declaration", func(s *OperationalSelection) { s.Declaration = "" }},
		{"unknown origin", func(s *OperationalSelection) { s.Origin = "inferred" }},
		{"unknown phase", func(s *OperationalSelection) { s.Phase = "quality" }},
		{"missing named input", func(s *OperationalSelection) { s.Phase = "normalized_input" }},
		{"unexpected named input", func(s *OperationalSelection) { s.Input = "orders" }},
		{"unknown window", func(s *OperationalSelection) { s.Window.Kind = "latest" }},
		{"missing backfill identity", func(s *OperationalSelection) { s.Window.Kind = "backfill" }},
		{"unexpected backfill chunk", func(s *OperationalSelection) { s.Window.BackfillChunkIndex = 1 }},
		{"negative backfill chunk", func(s *OperationalSelection) { s.Window.BackfillChunkIndex = -1 }},
		{"invalid window source", func(s *OperationalSelection) { s.Window.SourceSteps = []slug.Slug{"invalid source"} }},
		{"duplicate window source", func(s *OperationalSelection) { s.Window.SourceSteps = []slug.Slug{"extract", "extract"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection, _, now := operationalPolicyFixture(t)
			tc.edit(&selection)
			_, _, err := admitOperationalCheck(selection, nil, now)
			require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
		})
	}
	selection, observation, _ := operationalPolicyFixture(t)
	_, _, err := admitOperationalCheck(selection, observation, time.Time{})
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
}

func refreshOperationalEvidenceDigest(t *testing.T, observation *OperationalObservation) {
	t.Helper()
	raw, err := json.Marshal(observation.Evidence)
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	observation.Selection.EvidenceDigest = hex.EncodeToString(sum[:])
}
