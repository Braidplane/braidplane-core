package pipelineruntime

import (
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// Keep component identity independent of error text, which can contain SQL or provider details.
type prevalidationStepError struct {
	step  pipelinespec.Step
	cause error
}

func (e *prevalidationStepError) Error() string { return e.cause.Error() }
func (e *prevalidationStepError) Unwrap() error { return e.cause }

func prevalidationFailure(err error) *pipelinerun.FailureContext {
	failure := &pipelinerun.FailureContext{
		Kind:       failureKindPlatform,
		Error:      "Pipeline pre-execution validation failed.",
		ActionHint: "Run server-backed pipeline validation and review the reported configuration before retrying.",
	}
	var stepErr *prevalidationStepError
	if !xerror.As(err, &stepErr) {
		return failure
	}
	failure.Step = stepErr.step.Slug
	if stepErr.step.Query != nil {
		failure.Side = failureSideSource
		failure.ReasonCode = sourceFailureReasonCode(err)
		failure.DataSourceSlug = stepErr.step.Query.DataSourceSlug.String()
		failure.Error = "Source schema pre-execution validation failed."
		failure.ActionHint = "Check the source binding and cached schema, then run server-backed pipeline validation before retrying."
		return failure
	}
	failure.Side = failureSideTransform
	failure.ReasonCode = pipelinerun.FailureReasonCodeTransformRuntimeFailed
	failure.Error = "Transform pre-execution validation failed."
	failure.ActionHint = "Review the transform SQL and named input contracts, then run server-backed pipeline validation before retrying."
	// Expose only known static templates; dependency errors and details may contain private values.
	for _, cause := range []*xerror.Error{
		transformsql.ErrContractType, transformsql.ErrContractMissing, transformsql.ErrContractInvalid,
		pipelinevalidate.ErrContractMismatch, transformsql.ErrTypeInput,
	} {
		if xerror.Is(err, cause) {
			if cause != transformsql.ErrTypeInput {
				failure.ReasonCode = pipelinerun.FailureReasonCodeTransformContractMismatch
			}
			failure.Error = cause.Code() + ": " + cause.Message()
			failure.ActionHint = "Make the transform SQL projection and output contract agree with the named input contracts and cached source types. Run server-backed pipeline validation before retrying."
			break
		}
	}
	return failure
}
