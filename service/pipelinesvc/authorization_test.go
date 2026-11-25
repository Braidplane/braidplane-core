package pipelinesvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/service/authorization"
)

type allowAuthorizationService struct {
	authorization.Service

	CanFn func(context.Context, authorization.Input) (bool, error)
}

func (s allowAuthorizationService) Can(ctx context.Context, input authorization.Input) (bool, error) {
	if s.CanFn != nil {
		return s.CanFn(ctx, input)
	}
	return true, nil
}
