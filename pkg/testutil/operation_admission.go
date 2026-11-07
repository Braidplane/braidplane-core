package testutil

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type FakeOperationAdmission struct {
	WithOperationFn        func(context.Context, slug.Slug, slug.Slug, func(context.Context) error) error
	EncodeOperationErrorFn func(error) (string, bool)
	DecodeOperationErrorFn func(string) (error, bool)
}

func (f FakeOperationAdmission) EncodeOperationError(err error) (string, bool) {
	if f.EncodeOperationErrorFn != nil {
		return f.EncodeOperationErrorFn(err)
	}
	return "", false
}

func (f FakeOperationAdmission) DecodeOperationError(code string) (error, bool) {
	if f.DecodeOperationErrorFn != nil {
		return f.DecodeOperationErrorFn(code)
	}
	return nil, false
}

func (f FakeOperationAdmission) WithOperation(ctx context.Context, org, source slug.Slug, execute func(context.Context) error) error {
	if f.WithOperationFn != nil {
		return f.WithOperationFn(ctx, org, source, execute)
	}
	return execute(ctx)
}
