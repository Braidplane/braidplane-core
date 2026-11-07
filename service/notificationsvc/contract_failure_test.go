package notificationsvc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
	"github.com/stretchr/testify/require"
)

func testContractFailure() ContractFailureParams {
	return ContractFailureParams{OrgSlug: "acme", ProjectSlug: "ops", EnvironmentSlug: "dev", PipelineSlug: "report", RunSlug: "run-1", StepSlug: "supplier", CheckID: "check-1", ContractName: "supplier-product", ContractRevision: "v1", Field: "product_code", Rule: "required", ReasonCode: "contract_violation", ObservedAt: time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)}
}

func TestContractFailureCandidateScopeAndSafeEvidence(t *testing.T) {
	p := testContractFailure()
	candidate, err := ContractFailureCandidate(p)
	require.NoError(t, err)
	require.NoError(t, validateCreateCandidateInput(candidate))
	require.Equal(t, model.NotificationTypePipelineContractFailure, candidate.NotificationType)
	require.Equal(t, "run-1", candidate.SourceRefs["run_slug"])
	require.Equal(t, "supplier", candidate.SourceRefs["step_slug"])
	require.Equal(t, "product_code", candidate.SafeMetadata["field"])
	require.Equal(t, "v1", candidate.SafeMetadata["contract_revision"])
	refs, metadata, err := normalizeCreateCandidateInput(candidate)
	require.NoError(t, err)
	require.JSONEq(t, `{"project_slug":"ops","environment_slug":"dev","pipeline_slug":"report","step_slug":"supplier","check_id":"check-1","run_slug":"run-1"}`, string(refs))
	var values map[string]any
	require.NoError(t, json.Unmarshal(metadata, &values))
	require.Len(t, values, 7, "metadata has only the explicit diagnostic allowlist")

	for _, change := range []func(*ContractFailureParams){
		func(p *ContractFailureParams) { p.OrgSlug = "another" },
		func(p *ContractFailureParams) { p.ProjectSlug = "another" },
		func(p *ContractFailureParams) { p.EnvironmentSlug = "another" },
		func(p *ContractFailureParams) { p.RunSlug = "another" },
		func(p *ContractFailureParams) { p.CheckID = "another" },
	} {
		other := p
		change(&other)
		changed, err := ContractFailureCandidate(other)
		require.NoError(t, err)
		require.NotEqual(t, candidate.DedupeFingerprint, changed.DedupeFingerprint)
	}
	p.ObservedAt = p.ObservedAt.Add(time.Hour)
	replayed, err := ContractFailureCandidate(p)
	require.NoError(t, err)
	require.Equal(t, candidate.DedupeFingerprint, replayed.DedupeFingerprint)

	p.RunSlug, p.ValidationSlug = "", "validation-1"
	validation, err := ContractFailureCandidate(p)
	require.NoError(t, err)
	require.NotContains(t, validation.SourceRefs, "run_slug")
	require.Equal(t, "validation-1", validation.SourceRefs["validation_result_slug"])
	require.Equal(t, "report", validation.SourceRefs["source_ref"])
	require.NotEqual(t, candidate.DedupeFingerprint, validation.DedupeFingerprint)
}

func TestContractFailureRejectsIncompleteIdentity(t *testing.T) {
	for _, change := range []func(*ContractFailureParams){
		func(p *ContractFailureParams) { p.OrgSlug = "" },
		func(p *ContractFailureParams) { p.ProjectSlug = "" },
		func(p *ContractFailureParams) { p.EnvironmentSlug = "" },
		func(p *ContractFailureParams) { p.PipelineSlug = "" },
		func(p *ContractFailureParams) { p.RunSlug = "" },
		func(p *ContractFailureParams) { p.ValidationSlug = "validation-1" },
		func(p *ContractFailureParams) { p.CheckID = "" },
		func(p *ContractFailureParams) { p.ObservedAt = time.Time{} },
	} {
		p := testContractFailure()
		change(&p)
		_, err := ContractFailureCandidate(p)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
}

func TestContractFailureReplayPreservesResolvedRecord(t *testing.T) {
	p := testContractFailure()
	candidate, err := ContractFailureCandidate(p)
	require.NoError(t, err)
	repo := &fakeNotificationsRepo{findRecordBySlugFn: func(_ context.Context, _ postgres.DBTX, org, record slug.Slug) (*reponotifications.Record, error) {
		require.Equal(t, p.OrgSlug, org)
		require.Equal(t, *candidate.RecordSlug, record)
		return &reponotifications.Record{Slug: record, LifecycleState: model.NotificationLifecycleStateResolved}, nil
	}}
	svc := newTestService(repo).(*service)
	result, err := svc.CreateCandidate(t.Context(), candidate)
	require.NoError(t, err)
	require.Equal(t, model.NotificationLifecycleStateResolved, result.Record.LifecycleState)
}
