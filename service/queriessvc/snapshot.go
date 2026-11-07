package queriessvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// ExecuteSnapshot dispatches captured content through the ordinary credential,
// parameter and budget boundaries, without looking up a mutable saved query.
func (s *service) ExecuteSnapshot(ctx context.Context, org, source slug.Slug, content contractplan.OperationContent, values map[string]any, checkpoint *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	if scope := content.ResourceScope; scope != nil && (scope.Validate() != nil || scope.DataSource != source) {
		return nil, nil, resourcescope.ErrInvalid
	}
	statement, err := ExtractQueryStatement(content.Data)
	if err != nil {
		return nil, nil, ErrInvalidInput.With(err)
	}
	prepared := &preparedExecution{statementJSON: statement, rowLimit: parseRowLimit(content.Data), timeoutSeconds: parseTimeoutSeconds(content.Data)}
	prepared.resourceScope = content.ResourceScope
	for _, param := range content.Parameters {
		dataType := ParameterDataTypeString(param.DataType)
		if dataType == ParameterDataTypeUnknown {
			return nil, nil, ErrInvalidInput.Detail("invalid captured parameter type")
		}
		prepared.queryParams = append(prepared.queryParams, &QueryParameter{Slug: param.Slug, Name: param.Name, DataType: dataType, IsRequired: param.Required, DefaultValue: param.Default})
	}
	if checkpoint != nil {
		ctx = pipelineruntime.WithIncrementalExecution(ctx)
		values, err = withIncrementalParamValues(values, checkpoint)
		if err != nil {
			return nil, nil, err
		}
	}
	output, err := s.executePrepared(ctx, org, source, values, prepared, model.SystemEventActor())
	if err != nil || checkpoint == nil {
		return output, nil, err
	}
	observation, err := incrementalObservationFromResult(output, checkpoint, s.clock.Now())
	return output, observation, err
}
