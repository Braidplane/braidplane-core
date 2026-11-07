package producer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestNotificationDeliveryProducer(t *testing.T) {
	t.Parallel()

	params := notificationsvc.NotificationDeliveryJobParams{
		OrgSlug:         must.Slug("acme"),
		RecordSlug:      must.Slug("notif-pipeline-run-failed-001"),
		DestinationSlug: must.Slug("ops-email"),
		AlertPolicySlug: must.Slug("critical-pipeline-email"),
	}
	enqueueErr := producerTestError("queue unavailable")

	tests := []struct {
		name          string
		enqueuer      *recordingEnqueuer
		params        notificationsvc.NotificationDeliveryJobParams
		expectedQueue queue.QueuedJob
		errContains   string
		wantErr       error
	}{
		{
			name: "EnqueuesSlugOnlyDeliveryJob",
			enqueuer: &recordingEnqueuer{
				queued: queue.QueuedJob{
					Kind:  jobs.NotificationDeliveryArgs{}.Kind(),
					Queue: jobs.NotificationDeliveryQueue,
					State: "queued",
				},
			},
			params: params,
			expectedQueue: queue.QueuedJob{
				Kind:  jobs.NotificationDeliveryArgs{}.Kind(),
				Queue: jobs.NotificationDeliveryQueue,
				State: "queued",
			},
		},
		{
			name:     "ReturnsEnqueueError",
			enqueuer: &recordingEnqueuer{err: enqueueErr},
			params:   params,
			wantErr:  enqueueErr,
		},
		{
			name:        "RequiresEnqueuer",
			errContains: "queue enqueuer is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var enqueuer queue.Enqueuer
			if tc.enqueuer != nil {
				enqueuer = tc.enqueuer
			}
			producer, err := NewNotificationDeliveryProducer(enqueuer)
			if tc.errContains != "" {
				require.Nil(t, producer)
				require.ErrorContains(t, err, tc.errContains)
				return
			}
			require.NoError(t, err)

			queued, err := producer.EnqueueNotificationDelivery(context.Background(), nil, tc.params)
			if tc.wantErr != nil {
				require.Empty(t, queued)
				require.ErrorIs(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.expectedQueue, queued)
			require.Equal(t, 1, tc.enqueuer.calls)
			require.Nil(t, tc.enqueuer.tx)
			require.Equal(t, jobs.NotificationDeliveryQueue, tc.enqueuer.req.Queue)
			require.Equal(t, jobs.NotificationDeliveryArgs{
				OrganizationSlug: tc.params.OrgSlug,
				RecordSlug:       tc.params.RecordSlug,
				DestinationSlug:  tc.params.DestinationSlug,
				AlertPolicySlug:  tc.params.AlertPolicySlug,
			}, tc.enqueuer.req.Args)

			payload, err := json.Marshal(tc.enqueuer.req.Args)
			require.NoError(t, err)
			require.NotContains(t, string(payload), "ops@example.com")
			require.NotContains(t, string(payload), "raw-token")
			require.NotContains(t, string(payload), "message_body")
			require.NotContains(t, string(payload), "provider_response")
		})
	}
}

func TestNotificationDeliveryProducerPreservesTestFlag(t *testing.T) {
	enqueuer := &recordingEnqueuer{}
	producer, err := NewNotificationDeliveryProducer(enqueuer)
	require.NoError(t, err)
	_, err = producer.EnqueueNotificationDelivery(t.Context(), nil, notificationsvc.NotificationDeliveryJobParams{OrgSlug: must.Slug("acme"), DestinationSlug: must.Slug("ops"), RecordSlug: must.Slug("test-unique"), Test: true})
	require.NoError(t, err)
	require.True(t, enqueuer.req.Args.(jobs.NotificationDeliveryArgs).Test)
}
