package pipelineruntime

import (
	"testing"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestPrevalidationFailureSanitizesCauses(t *testing.T) {
	t.Parallel()
	for _, cause := range []*xerror.Error{transformsql.ErrContractType, transformsql.ErrContractMissing, transformsql.ErrContractInvalid, pipelinevalidate.ErrContractMismatch} {
		t.Run(cause.Code(), func(t *testing.T) {
			err := &prevalidationStepError{step: pipelinespec.Step{Slug: "project-orders", Transform: &pipelinespec.TransformSpec{}}, cause: cause.Detail("private-input-secret SQL literal")}
			failure := prevalidationFailure(err)
			require.ErrorIs(t, err, cause)
			require.Equal(t, pipelinerun.FailureReasonCodeTransformContractMismatch, failure.ReasonCode)
			require.Equal(t, cause.Code()+": "+cause.Message(), failure.Error)
			require.NotContains(t, failure.Error+failure.ActionHint, "private-input-secret")
		})
	}
	err := &prevalidationStepError{step: pipelinespec.Step{Slug: "project-orders", Transform: &pipelinespec.TransformSpec{}}, cause: xerror.Unexpected().Detail("private-provider-secret")}
	failure := prevalidationFailure(err)
	require.Equal(t, pipelinerun.FailureReasonCodeTransformRuntimeFailed, failure.ReasonCode)
	require.Equal(t, "Transform pre-execution validation failed.", failure.Error)
	require.NotContains(t, failure.Error+failure.ActionHint, "private-provider-secret")
	failure = prevalidationFailure(&prevalidationStepError{step: err.step, cause: transformsql.ErrTypeInput.Detail("private SQL reference")})
	require.Equal(t, pipelinerun.FailureReasonCodeTransformRuntimeFailed, failure.ReasonCode)
	require.Equal(t, transformsql.ErrTypeInput.Code()+": "+transformsql.ErrTypeInput.Message(), failure.Error)
}
