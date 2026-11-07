package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/email"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/pipelineschedulesvc"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/Braidplane/braidplane-core/service/worker/scheduler"
)

// Service registers all background workers for the backend runtime.
type Service struct {
	pipelineGit                  consumer.PipelineGitExecutor
	proposalRecorder             consumer.PipelineGitProposalRecorder
	proposalAuthorizer           consumer.PipelineGitProposalAuthorizer
	emailSvc                     email.Service
	notificationDelivery         consumer.NotificationDeliveryService
	alertDeliveryAttemptRecorder consumer.AlertDeliveryAttemptRecorder
	events                       eventService
	eventPruner                  pruneExpiredCustomerDataService
	scheduleDispatcher           scheduleDispatchService
	pipelineRunExecutor          pipelineRunExecutionService
	schemaImportExecutor         dataSourceSchemaImportService
	pipelineWebhookExecutor      pipelineWebhookExecutionService
	tokenCipher                  crypto.AES256
	logger                       *slog.Logger
	queueConcurrency             QueueConcurrency
}

// QueueConcurrency defines the maximum number of jobs each River queue can run concurrently in one process.
type QueueConcurrency struct {
	PipelineGit            int
	Email                  int
	NotificationDelivery   int
	Events                 int
	EventMaintenance       int
	PipelineSchedule       int
	PipelineRetry          int
	PipelineRun            int
	DataSourceSchemaImport int
}

type eventService interface {
	CreateEvent(ctx context.Context, params eventssvc.CreateEventParams) (*model.Event, error)
}

type pruneExpiredCustomerDataService interface {
	PruneExpiredCustomerData(ctx context.Context, execution retentionsvc.MaintenanceExecution) (retentionsvc.Result, error)
}

type scheduleDispatchService interface {
	DispatchDueSchedules(ctx context.Context, now time.Time) (pipelineschedulesvc.DispatchResult, error)
	ExecuteClaimedScheduleFire(ctx context.Context, args jobs.PipelineScheduleFireExecutionArgs) error
	ExecuteTriggerRetryAttempt(ctx context.Context, args jobs.PipelineTriggerRetryExecutionArgs) error
}

type pipelineRunExecutionService interface {
	ExecuteQueuedPipelineRun(ctx context.Context, args jobs.PipelineRunExecutionArgs) error
}

type dataSourceSchemaImportService interface {
	ExecuteQueuedSchemaImport(ctx context.Context, args jobs.DataSourceSchemaImportArgs) (*datasourcessvc.SchemaImportResult, error)
}

type pipelineWebhookExecutionService interface {
	ExecuteDelivery(context.Context, jobs.PipelineWebhookDeliveryArgs) error
}

func New(
	pipelineGit consumer.PipelineGitExecutor,
	proposalRecorder consumer.PipelineGitProposalRecorder,
	proposalAuthorizer consumer.PipelineGitProposalAuthorizer,
	emailSvc email.Service,
	events eventService,
	eventPruner pruneExpiredCustomerDataService,
	scheduleDispatcher scheduleDispatchService,
	pipelineRunExecutor pipelineRunExecutionService,
	schemaImportExecutor dataSourceSchemaImportService,
	notificationDelivery consumer.NotificationDeliveryService,
	tokenCipher crypto.AES256,
	logger *slog.Logger,
	queueConcurrency QueueConcurrency,
	pipelineWebhookExecutor pipelineWebhookExecutionService,
) (*Service, error) {
	if pipelineGit == nil {
		return nil, xerror.Unexpected().Detail("pipeline git executor is required")
	}
	if proposalRecorder == nil {
		return nil, xerror.Unexpected().Detail("pipeline git proposal recorder is required")
	}
	if proposalAuthorizer == nil {
		return nil, xerror.Unexpected().Detail("pipeline git proposal authorizer is required")
	}
	if emailSvc == nil {
		return nil, xerror.Unexpected().Detail("email service is required")
	}
	if events == nil {
		return nil, xerror.Unexpected().Detail("durable event service is required")
	}
	if eventPruner == nil {
		return nil, xerror.Unexpected().Detail("event pruner is required")
	}
	if scheduleDispatcher == nil {
		return nil, xerror.Unexpected().Detail("pipeline schedule dispatcher is required")
	}
	if pipelineRunExecutor == nil {
		return nil, xerror.Unexpected().Detail("pipeline run executor is required")
	}
	if schemaImportExecutor == nil {
		return nil, xerror.Unexpected().Detail("data source schema import executor is required")
	}
	if notificationDelivery == nil {
		return nil, xerror.Unexpected().Detail("notification delivery service is required")
	}
	if pipelineWebhookExecutor == nil {
		return nil, xerror.Unexpected().Detail("pipeline webhook executor is required")
	}
	if tokenCipher == nil {
		return nil, xerror.Unexpected().Detail("email token cipher is required")
	}
	if err := queueConcurrency.validate(); err != nil {
		return nil, err
	}

	if logger == nil {
		logger = slog.Default()
	}

	svc := &Service{
		pipelineGit:             pipelineGit,
		proposalRecorder:        proposalRecorder,
		proposalAuthorizer:      proposalAuthorizer,
		emailSvc:                emailSvc,
		events:                  events,
		eventPruner:             eventPruner,
		scheduleDispatcher:      scheduleDispatcher,
		pipelineRunExecutor:     pipelineRunExecutor,
		schemaImportExecutor:    schemaImportExecutor,
		notificationDelivery:    notificationDelivery,
		pipelineWebhookExecutor: pipelineWebhookExecutor,
		tokenCipher:             tokenCipher,
		logger:                  logger,
		queueConcurrency:        queueConcurrency,
	}
	if recorder, ok := notificationDelivery.(consumer.AlertDeliveryAttemptRecorder); ok {
		svc.alertDeliveryAttemptRecorder = recorder
	}
	return svc, nil
}

// validate rejects queue configurations that would register workers without runnable capacity.
func (c QueueConcurrency) validate() error {
	limits := []struct {
		name  string
		value int
	}{
		{name: jobs.PipelineGitQueue, value: c.PipelineGit},
		{name: jobs.EmailQueue, value: c.Email},
		{name: jobs.NotificationDeliveryQueue, value: c.NotificationDelivery},
		{name: jobs.EventsQueue, value: c.Events},
		{name: jobs.EventMaintenanceQueue, value: c.EventMaintenance},
		{name: jobs.PipelineScheduleQueue, value: c.PipelineSchedule},
		{name: jobs.PipelineRetryQueue, value: c.PipelineRetry},
		{name: jobs.PipelineRunQueue, value: c.PipelineRun},
		{name: jobs.DataSourceSchemaImportQueue, value: c.DataSourceSchemaImport},
	}
	for _, limit := range limits {
		if limit.value <= 0 {
			return xerror.Unexpected().Detailf("River queue %s max workers must be greater than 0", limit.name)
		}
	}

	return nil
}

func (s *Service) Register(workers *river.Workers) error {
	if workers == nil {
		return xerror.Unexpected().Detail("River workers registry is required")
	}

	if err := river.AddWorkerSafely(
		workers,
		consumer.NewPipelineGitWriteFileWorker(s.pipelineGit, s.logger),
	); err != nil {
		return xerror.Wrapf("register pipeline git write worker: %w", err)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewPipelineGitDeleteFileWorker(s.pipelineGit, s.logger),
	); err != nil {
		return xerror.Wrapf("register pipeline git delete worker: %w", err)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewPipelineGitPublishProposalWorker(
			s.pipelineGit, s.proposalAuthorizer, s.proposalRecorder, s.logger,
		),
	); err != nil {
		return xerror.Wrapf("register pipeline git proposal worker: %w", err)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewPipelineGitRefreshProposalWorker(s.pipelineGit, s.proposalRecorder, s.logger),
	); err != nil {
		return xerror.Wrapf("register pipeline git proposal refresh worker: %w", err)
	}
	emailWorkerOptions := make([]consumer.EmailSendWorkerOption, 0, 1)
	if s.alertDeliveryAttemptRecorder != nil {
		emailWorkerOptions = append(
			emailWorkerOptions,
			consumer.WithAlertDeliveryAttemptRecorder(s.alertDeliveryAttemptRecorder),
		)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewEmailSendWorker(s.emailSvc, s.tokenCipher, s.logger, emailWorkerOptions...),
	); err != nil {
		return xerror.Wrapf("register email send worker: %w", err)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewNotificationDeliveryWorker(s.notificationDelivery, s.logger),
	); err != nil {
		return xerror.Wrapf("register notification delivery worker: %w", err)
	}
	if err := river.AddWorkerSafely(workers, consumer.NewNotificationCandidateWorker(s.notificationDelivery)); err != nil {
		return xerror.Wrapf("register notification candidate worker: %w", err)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewEventRecordWorker(s.events, s.logger),
	); err != nil {
		return xerror.Wrapf("register event record worker: %w", err)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewEventPruneWorker(s.eventPruner, s.logger),
	); err != nil {
		return xerror.Wrapf("register event prune worker: %w", err)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewPipelineScheduleDispatchWorker(s.scheduleDispatcher, clock.New(), s.logger),
	); err != nil {
		return xerror.Wrapf("register pipeline schedule dispatch worker: %w", err)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewPipelineScheduleFireExecutionWorker(s.scheduleDispatcher, s.logger),
	); err != nil {
		return xerror.Wrapf("register pipeline schedule fire execution worker: %w", err)
	}
	if err := river.AddWorkerSafely(workers, consumer.NewPipelineWebhookDeliveryWorker(s.pipelineWebhookExecutor, s.logger)); err != nil {
		return xerror.Wrapf("register pipeline webhook delivery worker: %w", err)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewPipelineTriggerRetryExecutionWorker(s.scheduleDispatcher, s.logger),
	); err != nil {
		return xerror.Wrapf("register pipeline trigger retry execution worker: %w", err)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewPipelineRunExecutionWorker(s.pipelineRunExecutor, s.logger),
	); err != nil {
		return xerror.Wrapf("register pipeline run execution worker: %w", err)
	}
	if err := river.AddWorkerSafely(
		workers,
		consumer.NewDataSourceSchemaImportWorker(s.schemaImportExecutor, s.logger),
	); err != nil {
		return xerror.Wrapf("register data source schema import worker: %w", err)
	}

	return nil
}

func (s *Service) Queues() map[string]river.QueueConfig {
	return map[string]river.QueueConfig{
		jobs.PipelineGitQueue:            {MaxWorkers: s.queueConcurrency.PipelineGit},
		jobs.EmailQueue:                  {MaxWorkers: s.queueConcurrency.Email},
		jobs.NotificationDeliveryQueue:   {MaxWorkers: s.queueConcurrency.NotificationDelivery},
		jobs.EventsQueue:                 {MaxWorkers: s.queueConcurrency.Events},
		jobs.EventMaintenanceQueue:       {MaxWorkers: s.queueConcurrency.EventMaintenance},
		jobs.PipelineScheduleQueue:       {MaxWorkers: s.queueConcurrency.PipelineSchedule},
		jobs.PipelineRetryQueue:          {MaxWorkers: s.queueConcurrency.PipelineRetry},
		jobs.PipelineRunQueue:            {MaxWorkers: s.queueConcurrency.PipelineRun},
		jobs.DataSourceSchemaImportQueue: {MaxWorkers: s.queueConcurrency.DataSourceSchemaImport},
	}
}

func (s *Service) PeriodicJobs() ([]*river.PeriodicJob, error) {
	return scheduler.PeriodicJobs()
}
