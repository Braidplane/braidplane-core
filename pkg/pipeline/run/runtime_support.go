package run

import (
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func NewStepResult(executionResult *result.ExecutionResult) *StepResult {
	return newStepResult(executionResult)
}

func NewTransformStepResult(
	transform *pipepayload.Output,
	durationMs int64,
	err error,
) *StepResult {
	return newTransformStepResult(transform, durationMs, err)
}

func NewRunInputStepResult(input *pipepayload.Payload) *StepResult {
	return newRunInputStepResult(input)
}

func ResolveParams(
	params map[string]any,
	completed map[slug.Slug]*StepResult,
) (map[string]any, error) {
	return resolveParams(params, completed)
}

func ValidateQueryOutputContractAtRuntime(
	executionResult *result.ExecutionResult,
	contract *pipepayload.Contract,
) error {
	return validateQueryOutputContractAtRuntime(executionResult, contract)
}

func ValidateSinkInputContract(source *StepResult, contract *pipepayload.Contract) error {
	return validateSinkInputContract(source, contract)
}

func EffectiveRuntimePayloadContract(
	name string,
	payload *pipepayload.Payload,
	declared *pipepayload.Contract,
) *pipepayload.Contract {
	return pipepayload.EffectiveContract(name, payload, declared)
}

func PreflightEdgeDiagnostics(pipeline *pipelinespec.Pipeline) ([]pipelinevalidate.EdgeCompatibilityResult, error) {
	contexts, err := pipelinevalidate.StepEdgeContexts(pipeline)
	if err != nil {
		return nil, err
	}

	results := make([]pipelinevalidate.EdgeCompatibilityResult, 0, len(contexts))
	for _, ctx := range contexts {
		diagnostic := pipelinevalidate.EvaluateEdgeDiagnosticsForSteps(ctx.FromStep, ctx.ToStep)
		if diagnostic != nil {
			results = append(results, *diagnostic)
			if !diagnostic.Compatible {
				detail := pipelinevalidate.EdgeCompatibilityDetail(diagnostic)
				if detail != "" {
					return results, ErrContractMismatch.Detailf(
						"pre-execution edge %q incompatible contracts (%s): %s",
						diagnostic.EdgeSlug,
						diagnostic.ReasonCode,
						detail,
					)
				}
				return results, ErrContractMismatch.Detailf(
					"pre-execution edge %q incompatible contracts (%s)",
					diagnostic.EdgeSlug,
					diagnostic.ReasonCode,
				)
			}
			continue
		}

		return results, ErrContractMismatch.Detailf(
			"pre-execution edge %q missing compatibility contracts",
			ctx.FromStep.Slug.String()+"->"+ctx.ToStep.Slug.String(),
		)
	}

	return results, nil
}

func RuntimeEdgeDiagnostics(
	pipeline *pipelinespec.Pipeline,
	completedResults map[slug.Slug]*StepResult,
) []pipelinevalidate.EdgeCompatibilityResult {
	if pipeline == nil {
		return nil
	}

	contexts, err := pipelinevalidate.StepEdgeContexts(pipeline)
	if err != nil {
		return nil
	}

	diagnostics := make([]pipelinevalidate.EdgeCompatibilityResult, 0, len(contexts))
	for _, ctx := range contexts {
		diagnostics = append(
			diagnostics,
			runtimeEdgeDiagnostic(ctx.FromStep, ctx.ToStep, completedResults),
		)
	}

	return diagnostics
}

func CollectEdgeDiagnostics(pipeline *pipelinespec.Pipeline) []pipelinevalidate.EdgeCompatibilityResult {
	if pipeline == nil {
		return nil
	}

	diagnostics, err := PreflightEdgeDiagnostics(pipeline)
	if err != nil && len(diagnostics) > 0 {
		return diagnostics
	}
	return diagnostics
}

func runtimeEdgeDiagnostic(
	fromStep pipelinespec.Step,
	toStep pipelinespec.Step,
	completedResults map[slug.Slug]*StepResult,
) pipelinevalidate.EdgeCompatibilityResult {
	diagnostic := pipelinevalidate.EvaluateEdgeDiagnosticsForSteps(fromStep, toStep)
	if diagnostic == nil {
		return pipelinevalidate.EdgeCompatibilityResult{
			EdgeSlug:     fromStep.Slug.String() + "->" + toStep.Slug.String(),
			FromStepSlug: fromStep.Slug,
			ToStepSlug:   toStep.Slug,
			Compatible:   false,
			ReasonCode:   pipelinevalidate.ContractReasonMissingContract,
		}
	}

	if result := completedResults[fromStep.Slug]; result != nil {
		payload, ok := result.Payload()
		if ok && payload != nil && payload.Contract != nil {
			consumer := pipelinevalidate.StepInputContract(toStep)
			if consumer != nil {
				producer := payload.Contract
				if payload.VerificationStatus == pipepayload.VerificationStatusIncompatible {
					producer = pipepayload.EffectiveContract(
						fromStep.Slug.String()+"-output",
						payload,
						nil,
					)
				}
				runtimeDiagnostic := pipelinevalidate.EvaluateEdgeCompatibility(
					pipelinevalidate.EdgeLink{FromStep: fromStep.Slug, ToStep: toStep.Slug},
					producer,
					consumer,
				)
				runtimeDiagnostic.EdgeSlug = diagnostic.EdgeSlug
				return runtimeDiagnostic
			}
		}
	}

	return *diagnostic
}
