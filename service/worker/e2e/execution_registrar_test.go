package e2e

import (
	"context"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/postgres"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	workerhostsvc "github.com/Braidplane/braidplane-core/service/worker"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type noopExecutionProcessor struct{}

func (noopExecutionProcessor) Process(
	context.Context,
	uuid.UUID,
) (executiondelivery.ProcessResult, error) {
	return executiondelivery.ProcessResult{}, nil
}

func TestExecutionRegistrarRejectsInvalidConfigurationAndRegistersItsQueue(t *testing.T) {
	t.Parallel()

	registrar, err := workerhostsvc.NewExecutionRegistrar(nil, 1)
	require.Nil(t, registrar)
	require.ErrorContains(t, err, "execution processor is required")

	registrar, err = workerhostsvc.NewExecutionRegistrar(noopExecutionProcessor{}, 0)
	require.Nil(t, registrar)
	require.ErrorContains(t, err, "max workers must be positive")

	registrar, err = workerhostsvc.NewExecutionRegistrar(noopExecutionProcessor{}, 3)
	require.NoError(t, err)
	require.Equal(t, 3, registrar.Queues()[jobs.ExecutionQueue].MaxWorkers)
	require.ErrorContains(t, registrar.Register(nil), "workers registry is required")

	workers := river.NewWorkers()
	require.NoError(t, registrar.Register(workers))
	require.ErrorContains(t, registrar.Register(workers), "already registered")
}

func TestWorkerServiceRejectsMissingRuntimeDependencies(t *testing.T) { // Real dependency setup is shared.
	if testing.Short() {
		t.Skip("skipping real worker dependency validation test in short mode")
	}

	pool := openWorkerE2EPool(t)
	validPipelineGit := noopPipelineGitExecutor{}
	validProposalRecorder := noopPipelineGitProposalRecorder{}
	validProposalAuthorizer := noopPipelineGitProposalAuthorizer{}
	validEmail := newReachableRealEmailService(t)
	validEvents := eventssvc.New(postgres.NewTransactionManager(pool), repoevents.New(), clock.New())
	validRetention := newWorkerE2ERetentionService(pool)

	assertInvalid := func(service *workerhostsvc.Service, err error, wantError string) {
		t.Helper()
		require.Nil(t, service)
		require.ErrorContains(t, err, wantError)
	}

	service, err := workerhostsvc.New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, workerhostsvc.QueueConcurrency{}, nil)
	assertInvalid(service, err, "pipeline git executor is required")
	service, err = workerhostsvc.New(validPipelineGit, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, workerhostsvc.QueueConcurrency{}, nil)
	assertInvalid(service, err, "pipeline git proposal recorder is required")
	service, err = workerhostsvc.New(validPipelineGit, validProposalRecorder, validProposalAuthorizer, nil, nil, nil, nil, nil, nil, nil, nil, nil, workerhostsvc.QueueConcurrency{}, nil)
	assertInvalid(service, err, "email service is required")
	service, err = workerhostsvc.New(validPipelineGit, validProposalRecorder, validProposalAuthorizer, validEmail, nil, nil, nil, nil, nil, nil, nil, nil, workerhostsvc.QueueConcurrency{}, nil)
	assertInvalid(service, err, "durable event service is required")
	service, err = workerhostsvc.New(validPipelineGit, validProposalRecorder, validProposalAuthorizer, validEmail, validEvents, nil, nil, nil, nil, nil, nil, nil, workerhostsvc.QueueConcurrency{}, nil)
	assertInvalid(service, err, "event pruner is required")
	service, err = workerhostsvc.New(validPipelineGit, validProposalRecorder, validProposalAuthorizer, validEmail, validEvents, validRetention, nil, nil, nil, nil, nil, nil, workerhostsvc.QueueConcurrency{}, nil)
	assertInvalid(service, err, "pipeline schedule dispatcher is required")
	service, err = workerhostsvc.New(validPipelineGit, validProposalRecorder, validProposalAuthorizer, validEmail, validEvents, validRetention, noopScheduleDispatcher{}, nil, nil, nil, nil, nil, workerhostsvc.QueueConcurrency{}, nil)
	assertInvalid(service, err, "pipeline run executor is required")
	service, err = workerhostsvc.New(validPipelineGit, validProposalRecorder, validProposalAuthorizer, validEmail, validEvents, validRetention, noopScheduleDispatcher{}, noopPipelineRunExecutor{}, nil, nil, nil, nil, workerhostsvc.QueueConcurrency{}, nil)
	assertInvalid(service, err, "data source schema import executor is required")
	service, err = workerhostsvc.New(validPipelineGit, validProposalRecorder, validProposalAuthorizer, validEmail, validEvents, validRetention, noopScheduleDispatcher{}, noopPipelineRunExecutor{}, noopSchemaImportExecutor{}, nil, nil, nil, workerhostsvc.QueueConcurrency{}, nil)
	assertInvalid(service, err, "notification delivery service is required")
	service, err = workerhostsvc.New(
		validPipelineGit, validProposalRecorder, validProposalAuthorizer, validEmail, validEvents, validRetention,
		noopScheduleDispatcher{}, noopPipelineRunExecutor{}, noopSchemaImportExecutor{}, noopNotificationDeliveryService{},
		workerE2ETokenCipher(t), slog.New(slog.DiscardHandler), workerE2EQueueConcurrency(), nil,
	)
	assertInvalid(service, err, "pipeline webhook executor is required")
}
