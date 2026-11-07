package pipelineruntime

import (
	"fmt"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func preflightEdgeDiagnostics(pipeline *pipelinespec.Pipeline) ([]pipelinevalidate.EdgeCompatibilityResult, error) {
	diagnostics, err := pipelinerun.PreflightEdgeDiagnostics(pipeline)
	if err != nil {
		return diagnostics, err
	}
	if err := pipelinevalidate.ValidateStepInputCompatibility(pipeline); err != nil {
		if !hasIncompatibleEdgeDiagnostic(diagnostics) {
			diagnostics = append(
				diagnostics,
				pipelinevalidate.AggregateStepInputCompatibilityDiagnostics(pipeline)...,
			)
		}
		return diagnostics, err
	}
	return diagnostics, nil
}

// hasIncompatibleEdgeDiagnostic reports whether preflight already found an
// edge-local failure, so aggregate diagnostics are only appended when needed.
func hasIncompatibleEdgeDiagnostic(
	diagnostics []pipelinevalidate.EdgeCompatibilityResult,
) bool {
	for _, diagnostic := range diagnostics {
		if !diagnostic.Compatible {
			return true
		}
	}
	return false
}

func finalizeRunEdgeDiagnostics(
	pipeline *pipelinespec.Pipeline,
	completedResults map[slug.Slug]*pipelinerun.StepResult,
	fallback []pipelinevalidate.EdgeCompatibilityResult,
) []pipelinevalidate.EdgeCompatibilityResult {
	runtimeDiagnostics := runtimeEdgeDiagnostics(pipeline, completedResults)
	if len(runtimeDiagnostics) == 0 {
		return fallback
	}

	return runtimeDiagnostics
}

func runtimeEdgeDiagnostics(
	pipeline *pipelinespec.Pipeline,
	completedResults map[slug.Slug]*pipelinerun.StepResult,
) []pipelinevalidate.EdgeCompatibilityResult {
	return pipelinerun.RuntimeEdgeDiagnostics(pipeline, completedResults)
}

func collectEdgeDiagnostics(pipeline *pipelinespec.Pipeline) []pipelinevalidate.EdgeCompatibilityResult {
	return pipelinerun.CollectEdgeDiagnostics(pipeline)
}

func markStepResultPayloadVerificationStatus(
	stepResult *pipelinerun.StepResult,
	status payload.VerificationStatus,
) {
	if stepResult == nil {
		return
	}

	runtimePayload, ok := stepResult.Payload()
	if !ok || runtimePayload == nil {
		return
	}

	runtimePayload.VerificationStatus = status
}

func annotateStepResultPayload(
	originalStep pipelinespec.Step,
	step pipelinespec.Step,
	stepResult *pipelinerun.StepResult,
	upstreamPayload *payload.Payload,
) {
	if stepResult == nil {
		return
	}

	runtimePayload, ok := stepResult.Payload()
	if !ok || runtimePayload == nil {
		return
	}

	var declaredContract *payload.Contract
	switch {
	case step.Query != nil:
		declaredContract = step.Query.OutputContract
	case step.Transform != nil:
		declaredContract = step.Transform.OutputContract
	default:
		return
	}

	runtimePayload.Contract = pipelinerun.EffectiveRuntimePayloadContract(
		fmt.Sprintf("%s-output", step.Slug),
		runtimePayload,
		declaredContract,
	)
	runtimePayload.VerificationStatus = runtimePayloadVerificationStatus(
		originalStep,
		step,
		runtimePayload,
		upstreamPayload,
	)
}

func runtimePayloadVerificationStatus(
	originalStep pipelinespec.Step,
	step pipelinespec.Step,
	runtimePayload *payload.Payload,
	upstreamPayload *payload.Payload,
) payload.VerificationStatus {
	if runtimePayload == nil || runtimePayload.Contract == nil {
		return payload.VerificationStatusUnknown
	}

	switch {
	case step.Query != nil:
		if originalStep.Query == nil {
			return payload.VerificationStatusUnknown
		}
		if contractHasFields(originalStep.Query.OutputContract) {
			return payload.VerificationStatusVerified
		}
		if contractHasFields(step.Query.OutputContract) {
			return payload.VerificationStatusStaleIntrospection
		}
		return payload.VerificationStatusUnknown
	case step.Transform != nil:
		if originalStep.Transform != nil &&
			contractHasFields(originalStep.Transform.OutputContract) {
			return payload.VerificationStatusVerified
		}
		if upstreamPayload != nil &&
			isTrustedRuntimePayloadVerificationStatus(upstreamPayload.VerificationStatus) {
			return payload.VerificationStatusTransitivelyVerified
		}
		return payload.VerificationStatusUnknown
	default:
		return payload.VerificationStatusUnknown
	}
}

func contractHasFields(contract *payload.Contract) bool {
	return contract != nil && len(contract.Fields) > 0
}

func isTrustedRuntimePayloadVerificationStatus(status payload.VerificationStatus) bool {
	return status == payload.VerificationStatusVerified ||
		status == payload.VerificationStatusTransitivelyVerified
}
