package pipelinesvc

import (
	"context"
	"encoding/json"

	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

// Validate saved operations with the same role and replay rules as inline ones,
// without replacing the authored references or executing a connector.
func (s *service) resolveSavedWorkspaceOperations(ctx context.Context, org, environment slug.Slug, pipeline *pipelinespec.Pipeline) (*pipelinespec.Pipeline, error) {
	resolved := *pipeline
	resolved.Steps = append([]pipelinespec.Step(nil), pipeline.Steps...)
	for i, step := range pipeline.Steps {
		var operationType string
		var dataSource, reference slug.Slug
		if step.Query != nil {
			operationType, dataSource, reference = step.Query.OperationType, step.Query.DataSourceSlug, step.Query.OperationRef
		} else if step.Sink != nil {
			operationType, dataSource, reference = step.Sink.OperationType, step.Sink.DataSourceSlug, step.Sink.OperationRef
		}
		if operationType != pipelinespec.OperationTypeGoogleWorkspace || reference == "" {
			continue
		}
		if environment == "" {
			return nil, xerror.InvalidInput(xerror.Query).Detailf("step[%d] %q: an environment is required to resolve a saved Workspace operation", i, step.Slug)
		}
		query, err := s.queryExecutor.FindBySlug(ctx, org, dataSource, reference)
		if err != nil {
			return nil, xerror.Wrapf("step[%d] %q resolve saved Workspace operation: %w", i, step.Slug, err)
		}
		statement, err := queriessvc.ExtractQueryStatement(query.Data)
		if err != nil {
			return nil, xerror.Wrapf("step[%d] %q decode saved Workspace operation: %w", i, step.Slug, err)
		}
		var inline map[string]any
		if err := json.Unmarshal(statement, &inline); err != nil {
			return nil, xerror.InvalidInput(xerror.Query).With(err)
		}
		if step.Query != nil {
			queryRef := *step.Query
			queryRef.OperationRef, queryRef.OperationInline = "", inline
			resolved.Steps[i].Query = &queryRef
		} else {
			sink := *step.Sink
			sink.OperationRef, sink.OperationInline = "", inline
			resolved.Steps[i].Sink = &sink
		}
	}
	return &resolved, nil
}
