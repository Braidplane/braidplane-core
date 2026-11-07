package notifications

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
)

func TestRepositoryRejectsInvalidDispatchSummaryStatusBeforeSQL(t *testing.T) {
	t.Parallel()

	repo := New()
	now := time.Date(2026, time.June, 29, 18, 0, 0, 0, time.UTC)

	statuses := []struct {
		name   string
		status model.NotificationDispatchStatus
	}{
		{name: "UnknownStatus", status: model.NotificationDispatchStatusUnknown},
		{name: "OutOfRangeStatus", status: model.NotificationDispatchStatus(99)},
	}

	for _, tc := range statuses {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := repo.UpdateRecordDispatchSummary(context.Background(), nil, UpdateRecordDispatchSummaryParams{
				OrgSlug:         must.Slug("acme"),
				RecordSlug:      must.Slug("pipeline-run-failed"),
				DestinationSlug: must.Slug("ops-email"),
				Status:          tc.status,
				QueuedAt:        now,
				JobKind:         "notification_delivery",
				JobQueue:        "notification_delivery",
				JobState:        "queued",
			})

			require.ErrorIs(t, err, ErrDeliveryInvalidInput)
		})
	}
}
