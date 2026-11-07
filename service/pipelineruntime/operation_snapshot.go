package pipelineruntime

import (
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

type snapshotQueryExecutor interface {
	ExecuteSnapshot(context.Context, slug.Slug, slug.Slug, contractplan.OperationContent, map[string]any, *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
}

func executeCapturedOperation(ctx context.Context, executor any, org, source slug.Slug, op contractplan.Operation, params map[string]any, checkpoint *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	captured, ok := executor.(snapshotQueryExecutor)
	if !ok {
		return nil, nil, xerror.Unsupported(xerror.QueryExecutor).Detail("immutable operation executor is required")
	}
	var content contractplan.OperationContent
	if err := json.Unmarshal(op.Content, &content); err != nil {
		return nil, nil, contractplan.ErrInvalid.With(err)
	}
	return captured.ExecuteSnapshot(ctx, org, source, content, params, checkpoint)
}

// All sink backends keep their existing payload bindings and effect ledger;
// only their mutable definition lookup is replaced by captured content.
type capturedSinkExecutor struct {
	executor snapshotQueryExecutor
	content  contractplan.OperationContent
}

func (e capturedSinkExecutor) Execute(ctx context.Context, org, source, _ slug.Slug, _ model.EventActor, params map[string]any, _ ...queriessvc.ExecuteOption) (*result.ExecutionResult, error) {
	output, _, err := e.executor.ExecuteSnapshot(ctx, org, source, e.content, params, nil)
	return output, err
}

func (e capturedSinkExecutor) ExecuteInline(ctx context.Context, org, source slug.Slug, _, params map[string]any) (*result.ExecutionResult, error) {
	output, _, err := e.executor.ExecuteSnapshot(ctx, org, source, e.content, params, nil)
	return output, err
}
