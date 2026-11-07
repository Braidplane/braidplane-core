package testutil

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var ErrFakeQueryExecutorNotConfigured = xerror.Unexpected().Detail(
	"fake query executor not configured",
)

// FakeQueryExecutor is a test double for components that execute saved queries.
type FakeQueryExecutor struct {
	ExecuteFn func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error)
	ExecuteInlineFn func(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		operationInline map[string]any,
		paramValues map[string]any,
	) (*result.ExecutionResult, error)
}

func (f *FakeQueryExecutor) Execute(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	if f.ExecuteFn != nil {
		return f.ExecuteFn(ctx, orgSlug, dsSlug, querySlug, paramValues)
	}

	return nil, ErrFakeQueryExecutorNotConfigured
}

func (f *FakeQueryExecutor) ExecuteInline(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	if f.ExecuteInlineFn != nil {
		return f.ExecuteInlineFn(ctx, orgSlug, dsSlug, operationInline, paramValues)
	}

	return nil, ErrFakeQueryExecutorNotConfigured
}
