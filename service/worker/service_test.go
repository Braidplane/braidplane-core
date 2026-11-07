package worker

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineschedulesvc"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestService_RegisterAddsWorkers(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)
	require.NoError(t, err)

	workers := river.NewWorkers()
	require.NoError(t, svc.Register(workers))
	require.ErrorContains(t, svc.Register(workers), "already registered")
}

func TestService_RegisterAddsNotificationDeliveryWorker(t *testing.T) {
	t.Parallel()

	notificationDelivery := &recordingNotificationDeliveryService{}
	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		notificationDelivery,
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)
	require.NoError(t, err)

	workers := river.NewWorkers()
	require.NoError(t, svc.Register(workers))
	require.ErrorContains(
		t,
		river.AddWorkerSafely(
			workers,
			consumer.NewNotificationDeliveryWorker(notificationDelivery, slog.New(slog.DiscardHandler)),
		),
		"already registered",
	)
}

func TestService_QueuesIncludesWorkerQueues(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)
	require.NoError(t, err)

	queues := svc.Queues()

	require.Contains(t, queues, jobs.PipelineGitQueue)
	require.Equal(t, 1, queues[jobs.PipelineGitQueue].MaxWorkers)
	require.Contains(t, queues, jobs.EmailQueue)
	require.Equal(t, 1, queues[jobs.EmailQueue].MaxWorkers)
	require.Contains(t, queues, jobs.NotificationDeliveryQueue)
	require.Equal(t, 1, queues[jobs.NotificationDeliveryQueue].MaxWorkers)
	require.Contains(t, queues, jobs.EventsQueue)
	require.Equal(t, 1, queues[jobs.EventsQueue].MaxWorkers)
	require.Contains(t, queues, jobs.EventMaintenanceQueue)
	require.Equal(t, 1, queues[jobs.EventMaintenanceQueue].MaxWorkers)
	require.Contains(t, queues, jobs.PipelineScheduleQueue)
	require.Equal(t, 4, queues[jobs.PipelineScheduleQueue].MaxWorkers)
	require.Contains(t, queues, jobs.PipelineRetryQueue)
	require.Equal(t, 4, queues[jobs.PipelineRetryQueue].MaxWorkers)
	require.Contains(t, queues, jobs.PipelineRunQueue)
	require.Equal(t, 4, queues[jobs.PipelineRunQueue].MaxWorkers)
	require.Contains(t, queues, jobs.DataSourceSchemaImportQueue)
	require.Equal(t, 2, queues[jobs.DataSourceSchemaImportQueue].MaxWorkers)
}

// TestServiceQueuesUsesConfiguredConcurrency verifies each queue uses its explicit per-process capacity.
func TestServiceQueuesUsesConfiguredConcurrency(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		QueueConcurrency{
			PipelineGit:            2,
			Email:                  3,
			NotificationDelivery:   4,
			Events:                 5,
			EventMaintenance:       6,
			PipelineSchedule:       7,
			PipelineRetry:          8,
			PipelineRun:            9,
			DataSourceSchemaImport: 10,
		},
		&recordingPipelineWebhookExecutor{},
	)
	require.NoError(t, err)

	queues := svc.Queues()
	require.Equal(t, 2, queues[jobs.PipelineGitQueue].MaxWorkers)
	require.Equal(t, 3, queues[jobs.EmailQueue].MaxWorkers)
	require.Equal(t, 4, queues[jobs.NotificationDeliveryQueue].MaxWorkers)
	require.Equal(t, 5, queues[jobs.EventsQueue].MaxWorkers)
	require.Equal(t, 6, queues[jobs.EventMaintenanceQueue].MaxWorkers)
	require.Equal(t, 7, queues[jobs.PipelineScheduleQueue].MaxWorkers)
	require.Equal(t, 8, queues[jobs.PipelineRetryQueue].MaxWorkers)
	require.Equal(t, 9, queues[jobs.PipelineRunQueue].MaxWorkers)
	require.Equal(t, 10, queues[jobs.DataSourceSchemaImportQueue].MaxWorkers)
}

// TestNewRejectsInvalidQueueConcurrency verifies the worker boundary rejects every disabled or negative queue capacity.
func TestNewRejectsInvalidQueueConcurrency(t *testing.T) {
	t.Parallel()

	fields := []struct {
		name string
		set  func(*QueueConcurrency, int)
	}{
		{name: jobs.PipelineGitQueue, set: func(c *QueueConcurrency, value int) { c.PipelineGit = value }},
		{name: jobs.EmailQueue, set: func(c *QueueConcurrency, value int) { c.Email = value }},
		{name: jobs.NotificationDeliveryQueue, set: func(c *QueueConcurrency, value int) { c.NotificationDelivery = value }},
		{name: jobs.EventsQueue, set: func(c *QueueConcurrency, value int) { c.Events = value }},
		{name: jobs.EventMaintenanceQueue, set: func(c *QueueConcurrency, value int) { c.EventMaintenance = value }},
		{name: jobs.PipelineScheduleQueue, set: func(c *QueueConcurrency, value int) { c.PipelineSchedule = value }},
		{name: jobs.PipelineRetryQueue, set: func(c *QueueConcurrency, value int) { c.PipelineRetry = value }},
		{name: jobs.PipelineRunQueue, set: func(c *QueueConcurrency, value int) { c.PipelineRun = value }},
		{name: jobs.DataSourceSchemaImportQueue, set: func(c *QueueConcurrency, value int) { c.DataSourceSchemaImport = value }},
	}

	for _, field := range fields {
		for _, value := range []int{0, -1} {
			t.Run(fmt.Sprintf("%s/%d", field.name, value), func(t *testing.T) {
				t.Parallel()

				concurrency := testQueueConcurrency()
				field.set(&concurrency, value)
				svc, err := New(
					&recordingPipelineGitExecutor{},
					&recordingPipelineGitProposalRecorder{},
					&recordingPipelineGitProposalAuthorizer{},
					&recordingEmailService{},
					&recordingWorkerEventService{},
					recordingEventPruner{},
					&recordingScheduleDispatcher{},
					&recordingPipelineRunExecutor{},
					&recordingDataSourceSchemaImportExecutor{},
					&recordingNotificationDeliveryService{},
					fakeEmailTokenCipher{},
					slog.New(slog.DiscardHandler),
					concurrency,
					&recordingPipelineWebhookExecutor{},
				)

				require.Nil(t, svc)
				require.ErrorContains(t, err, field.name)
			})
		}
	}
}

func TestService_PeriodicJobsIncludesEventPruneJob(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)
	require.NoError(t, err)

	periodicJobs, err := svc.PeriodicJobs()
	require.NoError(t, err)
	require.Len(t, periodicJobs, 2)
}

func TestNewRequiresPipelineGitExecutor(t *testing.T) {
	t.Parallel()

	svc, err := New(
		nil,
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "pipeline git executor is required")
}

func TestNewRequiresPipelineGitProposalRecorder(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		nil,
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "pipeline git proposal recorder is required")
}

func TestNewRequiresPipelineGitProposalAuthorizer(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		nil,
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "pipeline git proposal authorizer is required")
}

func TestNewRequiresEmailService(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		nil,
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "email service is required")
}

func TestNewRequiresEmailTokenCipher(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		nil,
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "email token cipher is required")
}

func TestNewRequiresNotificationDeliveryService(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		nil,
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "notification delivery service is required")
}

func TestNewRequiresDurableEventService(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		nil,
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "durable event service is required")
}

func TestNewRequiresEventPruner(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		nil,
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "event pruner is required")
}

func TestNewRequiresScheduleDispatcher(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		nil,
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "pipeline schedule dispatcher is required")
}

func TestNewRequiresPipelineRunExecutor(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		nil,
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "pipeline run executor is required")
}

func TestNewRequiresDataSourceSchemaImportExecutor(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		nil,
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		&recordingPipelineWebhookExecutor{},
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "data source schema import executor is required")
}

func TestNewRequiresPipelineWebhookExecutor(t *testing.T) {
	t.Parallel()

	svc, err := New(
		&recordingPipelineGitExecutor{},
		&recordingPipelineGitProposalRecorder{},
		&recordingPipelineGitProposalAuthorizer{},
		&recordingEmailService{},
		&recordingWorkerEventService{},
		recordingEventPruner{},
		&recordingScheduleDispatcher{},
		&recordingPipelineRunExecutor{},
		&recordingDataSourceSchemaImportExecutor{},
		&recordingNotificationDeliveryService{},
		fakeEmailTokenCipher{},
		slog.New(slog.DiscardHandler),
		testQueueConcurrency(),
		nil,
	)

	require.Nil(t, svc)
	require.ErrorContains(t, err, "pipeline webhook executor is required")
}

// testQueueConcurrency returns the preserved backend queue defaults for worker service tests.
func testQueueConcurrency() QueueConcurrency {
	return QueueConcurrency{
		PipelineGit:            1,
		Email:                  1,
		NotificationDelivery:   1,
		Events:                 1,
		EventMaintenance:       1,
		PipelineSchedule:       4,
		PipelineRetry:          4,
		PipelineRun:            4,
		DataSourceSchemaImport: 2,
	}
}

type recordingPipelineGitExecutor struct {
	write  jobs.PipelineGitWriteFileArgs
	delete jobs.PipelineGitDeleteFileArgs
}

func (e *recordingPipelineGitExecutor) WriteFile(
	_ context.Context,
	op jobs.PipelineGitWriteFileArgs,
) error {
	e.write = op
	return nil
}

func (e *recordingPipelineGitExecutor) DeleteFile(
	_ context.Context,
	op jobs.PipelineGitDeleteFileArgs,
) error {
	e.delete = op
	return nil
}

func (e *recordingPipelineGitExecutor) PublishProposal(
	_ context.Context,
	_ jobs.PipelineGitPublishProposalArgs,
) (*pipelinegit.ProposalResult, error) {
	return &pipelinegit.ProposalResult{}, nil
}

func (e *recordingPipelineGitExecutor) RefreshProposal(
	context.Context, jobs.PipelineGitRefreshProposalArgs,
) (*pipelinegit.ProposalResult, error) {
	return &pipelinegit.ProposalResult{}, nil
}

type recordingPipelineGitProposalRecorder struct{}

type recordingPipelineGitProposalAuthorizer struct{}

func (*recordingPipelineGitProposalAuthorizer) AuthorizeProposalPublish(
	context.Context,
	jobs.PipelineGitPublishProposalArgs,
) error {
	return nil
}

func (r *recordingPipelineGitProposalRecorder) CompleteProposal(
	context.Context,
	pipelinegit.ProposalCompletion,
) error {
	return nil
}

func (r *recordingPipelineGitProposalRecorder) FailProposal(
	context.Context,
	pipelinegit.ProposalFailure,
) error {
	return nil
}

func (r *recordingPipelineGitProposalRecorder) CompleteProposalRefresh(
	context.Context, pipelinegit.ProposalRefreshCompletion,
) error {
	return nil
}

func (r *recordingPipelineGitProposalRecorder) FailProposalRefresh(
	context.Context, pipelinegit.ProposalRefreshFailure,
) error {
	return nil
}

type recordingPipelineRunExecutor struct{}

func (*recordingPipelineRunExecutor) ExecuteQueuedPipelineRun(
	context.Context,
	jobs.PipelineRunExecutionArgs,
) error {
	return nil
}

type recordingPipelineWebhookExecutor struct{}

func (*recordingPipelineWebhookExecutor) ExecuteDelivery(
	context.Context,
	jobs.PipelineWebhookDeliveryArgs,
) error {
	return nil
}

type recordingDataSourceSchemaImportExecutor struct{}

func (*recordingDataSourceSchemaImportExecutor) ExecuteQueuedSchemaImport(
	context.Context,
	jobs.DataSourceSchemaImportArgs,
) (*datasourcessvc.SchemaImportResult, error) {
	return &datasourcessvc.SchemaImportResult{Skipped: true}, nil
}

type recordingEmailService struct{}

func (s *recordingEmailService) Message(context.Context, *model.User, string, string) error {
	return nil
}

func (s *recordingEmailService) EmailVerification(
	context.Context,
	*model.User,
	string,
) error {
	return nil
}

func (s *recordingEmailService) PasswordReset(
	context.Context,
	*model.User,
	string,
) error {
	return nil
}

func (s *recordingEmailService) SessionMFA(
	context.Context,
	*model.User,
	string,
) error {
	return nil
}

func (s *recordingEmailService) MemberInvitation(
	context.Context,
	model.MemberInvitationEmail,
	string,
) error {
	return nil
}

func (s *recordingEmailService) Notification(
	context.Context,
	model.NotificationEmailRecipient,
	model.NotificationEmail,
) error {
	return nil
}

type fakeEmailTokenCipher struct{}

func (fakeEmailTokenCipher) Encrypt(plaintext []byte) ([]byte, error) {
	return plaintext, nil
}

func (fakeEmailTokenCipher) Decrypt(ciphertext []byte) ([]byte, error) {
	return ciphertext, nil
}

type recordingWorkerEventService struct{}

func (*recordingWorkerEventService) CreateEvent(
	context.Context,
	eventssvc.CreateEventParams,
) (*model.Event, error) {
	return &model.Event{}, nil
}

type recordingEventPruner struct{}

func (recordingEventPruner) PruneExpiredCustomerData(
	context.Context,
	retentionsvc.MaintenanceExecution,
) (retentionsvc.Result, error) {
	return retentionsvc.Result{}, nil
}

type recordingNotificationDeliveryService struct{}

func (*recordingNotificationDeliveryService) CreateCandidate(context.Context, notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error) {
	return &notificationsvc.CreateCandidateResult{}, nil
}

func (*recordingNotificationDeliveryService) DeliverNotification(
	context.Context,
	notificationsvc.DeliverNotificationParams,
) error {
	return nil
}

type recordingScheduleDispatcher struct{}

func (*recordingScheduleDispatcher) DispatchDueSchedules(
	context.Context,
	time.Time,
) (pipelineschedulesvc.DispatchResult, error) {
	return pipelineschedulesvc.DispatchResult{}, nil
}

func (*recordingScheduleDispatcher) ExecuteClaimedScheduleFire(
	context.Context,
	jobs.PipelineScheduleFireExecutionArgs,
) error {
	return nil
}

func (*recordingScheduleDispatcher) ExecuteTriggerRetryAttempt(
	context.Context,
	jobs.PipelineTriggerRetryExecutionArgs,
) error {
	return nil
}
