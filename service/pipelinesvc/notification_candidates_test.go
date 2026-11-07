package pipelinesvc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/stretchr/testify/require"
)

func TestValidationCandidatesIncludeOnlyBlockingContractEvidence(t *testing.T) {
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "report"}}
	record := &PipelineValidationResultRecord{Slug: "validation-1", ProjectSlug: "ops", EnvironmentSlug: "dev", PipelineSlug: "report", CreatedAt: time.Now().UTC(), SpecHash: new("exact-spec"), Diagnostics: []PipelineValidationDiagnostic{
		{Severity: "warning", Code: "PIPELINE_CONTRACT_MISMATCH"},
		{Severity: "error", Code: "DATA_SOURCE_BINDING_NOT_FOUND"},
		{Severity: "error", Code: "PIPELINE_CONTRACT_MISMATCH", Field: "steps[0].query.output_contract", StepSlug: new("supplier"), Message: "private provider response must not be copied"},
	}}
	candidates, err := validationNotificationCandidates("acme", pipeline, record)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	candidate := candidates[0]
	require.Equal(t, model.NotificationTypePipelineContractFailure, candidate.NotificationType)
	require.NotContains(t, candidate.SourceRefs, "run_slug")
	require.Equal(t, "validation-1", candidate.SourceRefs["validation_result_slug"])
	require.Equal(t, "supplier", candidate.SourceRefs["step_slug"])
	require.Equal(t, "exact-spec", candidate.SafeMetadata["spec_hash"])
	require.Empty(t, candidate.SafeMetadata["contract_name"])
	require.Equal(t, "PIPELINE_CONTRACT_MISMATCH", candidate.SafeMetadata["rule"])
	encoded, err := json.Marshal(candidate)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private provider")
	record.Valid = true
	candidates, err = validationNotificationCandidates("acme", pipeline, record)
	require.NoError(t, err)
	require.Empty(t, candidates)
}

func TestValidationPersistenceEnqueuesGenericCandidateWithDurableIdentity(t *testing.T) {
	at := time.Now().UTC()
	repo := &fakeValidationResultsRepository{createdAt: at}
	var queued model.NotificationCandidate
	svc := &service{validationResults: repo, notificationProducer: testutil.FakeRunSucceededNotifier{CandidateFn: func(_ context.Context, _ postgres.DBTX, candidate model.NotificationCandidate) error {
		require.Equal(t, 1, repo.createCalls, "validation evidence precedes the durable handoff")
		queued = candidate
		return nil
	}}}
	record, err := svc.recordValidationResultTx(t.Context(), &fakeDBTX{}, "acme", "ops", "dev", PipelineValidationResultSourceAPI,
		&pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "report"}},
		&PipelineValidationResult{Diagnostics: []PipelineValidationDiagnostic{{Severity: "error", Code: "PIPELINE_CONTRACT_MISMATCH", Field: "steps[0].query.output_contract", StepSlug: new("supplier")}}})
	require.NoError(t, err)
	require.Equal(t, model.NotificationTypePipelineContractFailure, queued.NotificationType)
	require.Equal(t, record.Slug.String(), queued.SourceRefs["validation_result_slug"])
	require.Equal(t, "dev", queued.EnvironmentSlug.String())
	require.Equal(t, at, queued.ObservedAt)
}
