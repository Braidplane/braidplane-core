package notificationsvc

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
	"github.com/stretchr/testify/require"
)

func TestSuccessSlackMessageLinksExactRunAndRetriesIndependently(t *testing.T) {
	work := &reponotifications.DeliveryWork{
		OrgSlug: "acme", RecordSlug: "notice", DestinationSlug: "slack", AlertPolicySlug: "success",
		DestinationType: model.AlertDestinationTypeSlack, NotificationType: model.NotificationTypePipelineRunSucceeded,
		NonSecretConfig: []byte(`{"connection_slug":"finance"}`),
		SourceRefs:      []byte(`{"project_slug":"ops","environment_slug":"prod","run_slug":"run-success"}`),
		SafeMetadata:    []byte(`{"summary":"Pipeline run completed successfully. All required sink writes completed."}`),
	}
	for _, fails := range []bool{true, false} {
		t.Run(map[bool]string{true: "failure", false: "success"}[fails], func(t *testing.T) {
			finished := false
			repo := &fakeNotificationsRepo{
				claimConnectionDeliveryFn: func(context.Context, postgres.DBTX, reponotifications.ClaimConnectionDeliveryParams) (reponotifications.ConnectionDeliveryClaim, error) {
					return reponotifications.ConnectionDeliveryClaim{Slug: "attempt", Acquired: true, ProviderAttempt: 1}, nil
				},
				finishConnectionDeliveryFn: func(_ context.Context, _ postgres.DBTX, p reponotifications.FinishConnectionDeliveryParams) error {
					finished = true
					if fails {
						require.Equal(t, model.AlertDeliveryAttemptStatusFailed, p.Status)
						require.NotNil(t, p.RetryAfterAt)
					} else {
						require.Equal(t, model.AlertDeliveryAttemptStatusSent, p.Status)
					}
					return nil
				},
			}
			svc := newTestServiceWithClock(repo, testutil.FakeClock{NowFn: time.Now}).(*service)
			svc.connectionProviders = ConnectionProviders{model.AlertDestinationTypeSlack: connectionProviderFunc(func(_ context.Context, org, connection slug.Slug, text string) error {
				require.Equal(t, work.OrgSlug, org)
				require.Equal(t, slug.Slug("finance"), connection)
				require.Contains(t, text, "View run: https://app.example/orgs/acme/projects/ops/runs/run-success?environment_slug=prod")
				require.Contains(t, text, "All required sink writes completed")
				require.NotContains(t, text, "rows")
				if fails {
					return &DeliveryError{Category: "provider_unavailable", Message: "Unavailable", Retryable: true}
				}
				return nil
			})}
			err := svc.deliverConnectionNotification(t.Context(), work)
			if fails {
				var delivery *DeliveryError
				require.ErrorAs(t, err, &delivery)
				require.True(t, delivery.Retryable)
			} else {
				require.NoError(t, err)
			}
			require.True(t, finished)
		})
	}
}
