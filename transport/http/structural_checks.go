package http

import (
	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
)

func toAPIStructuralChecks(checks []pipelinerun.StructuralCheck) *[]api.PipelineRunStructuralCheck {
	if checks == nil {
		return nil
	}
	result := make([]api.PipelineRunStructuralCheck, 0, len(checks))
	for _, check := range checks {
		record := api.PipelineRunStructuralCheck{
			ContractFailure: toAPIPipelineRunContractFailureDiagnostic(check.ContractFailure),
			Id:              check.ID, Step: check.Step.String(), Origin: api.PipelineRunStructuralCheckOrigin(check.Origin), Phase: api.PipelineRunStructuralCheckPhase(check.Phase),
			Declaration: check.Declaration, ProjectionDigest: check.ProjectionDigest,
			CompiledPlanDigest: optionalCheckString(check.CompiledPlanDigest), DeploymentResolutionDigest: optionalCheckString(check.DeploymentResolutionDigest),
			AttemptNumber: check.AttemptNumber, Evaluator: check.Evaluator, Policy: check.Policy,
			Target: optionalCheckString(check.Target.String()), Status: api.PipelineRunStructuralCheckStatus(check.Status.String()), ReasonCode: api.PipelineRunStructuralCheckReasonCode(check.ReasonCode),
			StartedAt: check.StartedAt, CompletedAt: check.CompletedAt, Input: optionalCheckString(check.Input),
			EvidenceDigest: optionalCheckString(check.EvidenceDigest), ReusedFromRun: optionalCheckString(check.ReusedFromRun),
			Window: api.PipelineRunCheckWindow{Kind: api.PipelineRunCheckWindowKind(check.Window.Kind), HasLowerBound: check.Window.HasLowerBound, HasUpperBound: check.Window.HasUpperBound, BackfillSeriesSlug: optionalCheckString(check.Window.BackfillSeriesSlug)},
		}
		record.Window.SourceSteps = make([]string, len(check.Window.SourceSteps))
		for i, source := range check.Window.SourceSteps {
			record.Window.SourceSteps[i] = source.String()
		}
		if check.Window.BackfillSeriesSlug != "" {
			record.Window.BackfillChunkIndex = &check.Window.BackfillChunkIndex
		}
		if check.Evidence != nil {
			record.Evidence = &api.PipelineRunCheckEvidence{Rows: check.Evidence.Rows, DurationMs: check.Evidence.DurationMs, Empty: check.Evidence.Empty, BatchScope: api.PipelineRunCheckEvidenceBatchScope(check.Evidence.BatchScope)}
		}
		result = append(result, record)
	}
	return &result
}

func optionalCheckString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
