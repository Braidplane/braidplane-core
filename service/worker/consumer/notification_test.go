package consumer

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestNotificationDeliveryWorker(t *testing.T) {
	t.Parallel()

	args := jobs.NotificationDeliveryArgs{
		OrganizationSlug: must.Slug("acme"),
		RecordSlug:       must.Slug("notif-pipeline-run-failed-001"),
		DestinationSlug:  must.Slug("ops-email"),
		AlertPolicySlug:  must.Slug("critical-pipeline-email"),
	}
	expectedParams := notificationsvc.DeliverNotificationParams{
		OrgSlug:         args.OrganizationSlug,
		RecordSlug:      args.RecordSlug,
		DestinationSlug: args.DestinationSlug,
		AlertPolicySlug: args.AlertPolicySlug,
	}
	deliveryErr := xerror.Unexpected().Detail("smtp unavailable")

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "DeliversAttempt",
			run: func(t *testing.T) {
				service := &recordingNotificationDeliveryService{}
				worker := NewNotificationDeliveryWorker(service, slog.New(slog.DiscardHandler))

				err := worker.Work(context.Background(), &river.Job[jobs.NotificationDeliveryArgs]{Args: args})

				require.NoError(t, err)
				require.Equal(t, 1, service.calls)
				require.Equal(t, expectedParams, service.lastParams)
			},
		},
		{
			name: "ReturnsDeliveryErrorForRetry",
			run: func(t *testing.T) {
				service := &recordingNotificationDeliveryService{err: deliveryErr}
				worker := NewNotificationDeliveryWorker(service, slog.New(slog.DiscardHandler))

				err := worker.Work(context.Background(), &river.Job[jobs.NotificationDeliveryArgs]{Args: args})

				require.ErrorIs(t, err, deliveryErr)
				require.Equal(t, 1, service.calls)
			},
		},
		{
			name: "PanicsForNilService",
			run: func(t *testing.T) {
				require.PanicsWithValue(
					t,
					"nil NotificationDeliveryService passed to NewNotificationDeliveryWorker",
					func() {
						NewNotificationDeliveryWorker(nil, slog.New(slog.DiscardHandler))
					},
				)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.run(t)
		})
	}
}

type recordingNotificationDeliveryService struct {
	calls      int
	lastParams notificationsvc.DeliverNotificationParams
	err        error
}

func (*recordingNotificationDeliveryService) CreateCandidate(context.Context, notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error) {
	return &notificationsvc.CreateCandidateResult{}, nil
}

func (s *recordingNotificationDeliveryService) DeliverNotification(
	_ context.Context,
	params notificationsvc.DeliverNotificationParams,
) error {
	s.calls++
	s.lastParams = params
	return s.err
}

func TestNotificationWorkerHonorsProviderRetryAndPermanentFailure(t *testing.T) {
	service := &recordingNotificationDeliveryService{err: &notificationsvc.DeliveryError{Message: "limited", Retryable: true, RetryAfter: 42 * time.Second}}
	worker := NewNotificationDeliveryWorker(service, slog.New(slog.DiscardHandler))
	err := worker.Work(t.Context(), &river.Job[jobs.NotificationDeliveryArgs]{})
	var snooze *river.JobSnoozeError
	require.ErrorAs(t, err, &snooze)
	require.Equal(t, 42*time.Second, snooze.Duration)
	service.err = &notificationsvc.DeliveryError{Message: "disconnected"}
	err = worker.Work(t.Context(), &river.Job[jobs.NotificationDeliveryArgs]{})
	var cancel *rivertype.JobCancelError
	require.ErrorAs(t, err, &cancel)
}
