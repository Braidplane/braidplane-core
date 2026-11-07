package worker

import (
	"context"
	"testing"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type testExecutionProcessor struct{}

func (*testExecutionProcessor) Process(
	context.Context,
	uuid.UUID,
) (executiondelivery.ProcessResult, error) {
	return executiondelivery.ProcessResult{}, nil
}

func TestExecutionRegistrarOwnsDedicatedQueue(t *testing.T) {
	t.Parallel()
	registrar, err := NewExecutionRegistrar(&testExecutionProcessor{}, 4)
	require.NoError(t, err)
	require.Equal(t, 4, registrar.Queues()[jobs.ExecutionQueue].MaxWorkers)
	require.ErrorContains(t, registrar.Register(nil), "River workers registry is required")
	workers := river.NewWorkers()
	require.NoError(t, registrar.Register(workers))
	require.ErrorContains(t, registrar.Register(workers), "already registered")
}

func TestExecutionRegistrarRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	registrar, err := NewExecutionRegistrar(nil, 1)
	require.Nil(t, registrar)
	require.ErrorContains(t, err, "execution processor is required")

	for _, maxWorkers := range []int{0, -1} {
		registrar, err = NewExecutionRegistrar(&testExecutionProcessor{}, maxWorkers)
		require.Nil(t, registrar)
		require.ErrorContains(t, err, "execution River queue max workers must be positive")
	}
}
