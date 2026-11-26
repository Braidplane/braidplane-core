package e2e

import (
	"context"

	"github.com/Braidplane/braidplane-core/service/authorization"
)

type allowAuthorizationService struct {
	authorization.Service
}

func (allowAuthorizationService) Can(context.Context, authorization.Input) (bool, error) {
	return true, nil
}
