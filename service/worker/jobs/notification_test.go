package jobs

import (
	"encoding/json"
	"testing"

	"github.com/riverqueue/river/rivershared/structtag"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/must"
)

func TestNotificationDeliveryArgs(t *testing.T) {
	t.Parallel()

	args := NotificationDeliveryArgs{
		OrganizationSlug: must.Slug("acme"),
		RecordSlug:       must.Slug("notif-pipeline-run-failed-001"),
		DestinationSlug:  must.Slug("ops-email"),
		AlertPolicySlug:  must.Slug("critical-pipeline-email"),
	}

	tests := []struct {
		name   string
		assert func(t *testing.T)
	}{
		{
			name: "UsesDeliveryQueueAndSafeUniqueIdentity",
			assert: func(t *testing.T) {
				opts := args.InsertOpts()

				require.Equal(t, "notification_delivery", NotificationDeliveryQueue)
				require.Equal(t, "notification_delivery", args.Kind())
				require.Equal(t, NotificationDeliveryQueue, opts.Queue)
				require.True(t, opts.UniqueOpts.ByArgs)
				require.ElementsMatch(t, rivertype.UniqueOptsByStateDefault(), opts.UniqueOpts.ByState)
			},
		},
		{
			name: "DeduplicatesByDeliveryTargetNotAlertPolicy",
			assert: func(t *testing.T) {
				sameTargetDifferentPolicy := args
				sameTargetDifferentPolicy.AlertPolicySlug = must.Slug("backup-pipeline-email")

				differentDestination := args
				differentDestination.DestinationSlug = must.Slug("security-email")

				require.Equal(
					t,
					notificationDeliveryUniqueArgs(t, args),
					notificationDeliveryUniqueArgs(t, sameTargetDifferentPolicy),
				)
				require.NotEqual(
					t,
					notificationDeliveryUniqueArgs(t, args),
					notificationDeliveryUniqueArgs(t, differentDestination),
				)
			},
		},
		{
			name: "CarriesOnlySafeSlugs",
			assert: func(t *testing.T) {
				payload, err := json.Marshal(args)
				require.NoError(t, err)

				var got map[string]string
				require.NoError(t, json.Unmarshal(payload, &got))
				require.Equal(t, map[string]string{
					"organization_slug": "acme",
					"record_slug":       "notif-pipeline-run-failed-001",
					"destination_slug":  "ops-email",
					"alert_policy_slug": "critical-pipeline-email",
				}, got)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t)
		})
	}
}

func notificationDeliveryUniqueArgs(t *testing.T, args NotificationDeliveryArgs) map[string]string {
	t.Helper()

	payload, err := json.Marshal(args)
	require.NoError(t, err)

	uniqueFields, err := structtag.SortedFieldsWithTag(args, "unique")
	require.NoError(t, err)

	uniqueValues := structtag.ExtractValues(payload, uniqueFields)
	uniqueArgs := make(map[string]string, len(uniqueFields))
	for i, field := range uniqueFields {
		if uniqueValues[i] == "undefined" {
			continue
		}
		uniqueArgs[field] = uniqueValues[i]
	}

	return uniqueArgs
}
