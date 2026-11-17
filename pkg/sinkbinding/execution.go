package sinkbinding

import "context"

type sinkExecutionKey struct{}

// WithSinkExecution marks an operation as a pipeline sink execution.
func WithSinkExecution(ctx context.Context) context.Context {
	return context.WithValue(ctx, sinkExecutionKey{}, true)
}

// IsSinkExecution reports whether the operation was marked as a sink execution.
func IsSinkExecution(ctx context.Context) bool {
	value, _ := ctx.Value(sinkExecutionKey{}).(bool)
	return value
}
