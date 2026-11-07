package executionbroker

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/postgres"
)

type pollerDispatcher struct{}

func NewPollerDispatcher() Dispatcher { return pollerDispatcher{} }

func (pollerDispatcher) Dispatch(context.Context, postgres.DBTX, uuid.UUID) error { return nil }
