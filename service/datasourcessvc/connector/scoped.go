package connector

import (
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type scopedExecutor interface {
	ExecuteScoped(context.Context, json.RawMessage, map[string]types.TypedParam, resourcescope.Scope, ...types.ExecutionLimits) (*result.ExecutionResult, error)
}

// Execute preserves ordinary execution for historical requests. A scoped
// request cannot fall back to a connector that does not implement its policy.
func Execute(ctx context.Context, conn Connector, statement json.RawMessage, params map[string]types.TypedParam, scope *resourcescope.Scope, limits types.ExecutionLimits) (*result.ExecutionResult, error) {
	if scope == nil {
		return conn.Execute(ctx, statement, params, limits)
	}
	if scope.Validate() != nil {
		return nil, resourcescope.ErrInvalid
	}
	scoped, ok := conn.(scopedExecutor)
	if !ok {
		return nil, resourcescope.ErrInvalid
	}
	return scoped.ExecuteScoped(ctx, statement, params, *scope, limits)
}
