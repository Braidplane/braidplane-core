package views

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

// testAlertDestinationConfig builds the generated alert destination config union for view fixtures.
func testAlertDestinationConfig(t *testing.T, value map[string]any) api.AlertDestinationConfig {
	t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(t, err)

	var ret api.AlertDestinationConfig
	require.NoError(t, ret.UnmarshalJSON(raw))
	return ret
}

func TestAlertDestinationRecipientValuesSupportedShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		destination api.AlertDestination
		want        []string
	}{
		{
			name: "UsesRecipientsArray",
			destination: api.AlertDestination{
				NonSecretConfig: testAlertDestinationConfig(t, map[string]any{
					"recipients": []any{"ops@example.com", "oncall@example.com"},
				}),
			},
			want: []string{"ops@example.com", "oncall@example.com"},
		},
		{
			name: "UsesRecipientObjectEmail",
			destination: api.AlertDestination{
				NonSecretConfig: testAlertDestinationConfig(t, map[string]any{
					"recipients": []any{map[string]any{"email": "ops@example.com"}},
				}),
			},
			want: []string{"ops@example.com"},
		},
		{
			name: "FallsBackToLegacyTo",
			destination: api.AlertDestination{
				NonSecretConfig: testAlertDestinationConfig(t, map[string]any{"to": "legacy@example.com"}),
			},
			want: []string{"legacy@example.com"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, AlertDestinationRecipientValues(tc.destination))
		})
	}
}

func TestAlertDestinationRecipientValues(t *testing.T) {
	t.Parallel()

	destination := api.AlertDestination{
		NonSecretConfig: testAlertDestinationConfig(t, map[string]any{"recipients": []any{
			"ops@example.com",
			map[string]any{"email": "oncall@example.com"},
			"",
		}}),
	}

	require.Equal(t, []string{"ops@example.com", "oncall@example.com"}, AlertDestinationRecipientValues(destination))
	require.Equal(t, "ops@example.com, oncall@example.com", AlertDestinationRecipientLabel(destination))
}

func TestAlertDestinationFormDataFromDestinationPreservesRecipients(t *testing.T) {
	t.Parallel()

	form := AlertDestinationFormDataFromDestination(api.AlertDestination{
		Name: "Operations Email",
		Slug: "ops-email",
		Type: api.AlertDestinationTypeEmail,
		NonSecretConfig: testAlertDestinationConfig(t, map[string]any{
			"recipients": []any{"ops@example.com", "oncall@example.com"},
		}),
		Enabled: true,
	})

	require.Equal(t, "Operations Email", form.Name)
	require.Equal(t, "ops-email", form.Slug)
	require.Equal(t, "ops@example.com, oncall@example.com", form.To)
	require.True(t, form.Enabled)
}

func TestAlertTabsNormalizeAndBuildRouteBackedLinks(t *testing.T) {
	t.Parallel()

	require.Equal(t, AlertTabDestinations, AlertTabFromString(""))
	require.Equal(t, AlertTabDestinations, AlertTabFromString("unknown"))
	require.Equal(t, AlertTabPolicies, AlertTabFromString("policies"))
	require.Equal(t, "/orgs/acme-hq/alerts?tab=destinations", AlertTabHref("acme-hq", AlertTabDestinations))
	require.Equal(t, "/orgs/acme-hq/alerts?tab=policies", AlertTabHref("acme-hq", AlertTabPolicies))
	require.Equal(t, "tab tab-active", AlertTabClass(AlertTabPolicies, AlertTabPolicies))
	require.Equal(t, "tab", AlertTabClass(AlertTabDestinations, AlertTabPolicies))
	require.Equal(t, "true", AlertTabAriaSelected(AlertTabDestinations, AlertTabDestinations))
	require.Equal(t, "false", AlertTabAriaSelected(AlertTabDestinations, AlertTabPolicies))
}

func TestSlackDestinationPreservesConnectionAndBlocksDisconnectedTests(t *testing.T) {
	destination := api.AlertDestination{Type: api.AlertDestinationTypeSlack, Enabled: true, NonSecretConfig: testAlertDestinationConfig(t, map[string]any{"connection_slug": "ops"})}
	require.Equal(t, "ops", AlertDestinationFormDataFromDestination(destination).ConnectionSlug)
	require.False(t, AlertDestinationCanTest(destination, nil))
	require.Equal(t, "ops (unavailable)", AlertDestinationTargetLabel(destination, nil))
	connections := []api.NotificationConnection{{Slug: "ops", Name: "Operations", Provider: api.NotificationProviderSlack, Status: api.CredentialStatusActive}}
	require.True(t, AlertDestinationCanTest(destination, connections))
	connections[0].Status = api.CredentialStatusRevoked
	require.False(t, AlertDestinationCanTest(destination, connections))
}

func TestAlertPolicyNotificationsLinkUsesSupportedConditionFilters(t *testing.T) {
	t.Parallel()
	project, environment := api.NullableSlug("orders"), api.NullableSlug("staging")
	condition := api.NotificationTypePipelineRetryExhausted
	state := api.NotificationLifecycleStateResolved
	href := AlertPolicyNotificationsHref("acme", api.AlertPolicy{ProjectSlug: &project, EnvironmentSlug: &environment, NotificationType: &condition, LifecycleState: &state})
	require.NotContains(t, href, "project_slug=")
	require.NotContains(t, href, "environment_slug=")
	require.Contains(t, href, "state=resolved")
	require.Contains(t, href, "notification_type=pipeline.retry_exhausted")
}

func TestAlertDeleteFormRetainsNotificationReturn(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	require.NoError(t, AlertDeleteConfirmModal("delete-original", "Delete policy", "Original", "original", "policy_confirmation", "/orgs/acme/alerts/policies/original/delete", "/orgs/acme/notifications#notification-original").Render(context.Background(), &out))
	require.Contains(t, out.String(), `name="notification_return" value="/orgs/acme/notifications#notification-original"`)
}

func TestAlertPolicyEditRetainsTypeAndSeverity(t *testing.T) {
	t.Parallel()

	form := AlertPolicyFormDataFromPolicy(api.AlertPolicy{
		NotificationType: new(api.NotificationTypePipelineRetryExhausted),
		Severity:         new(api.NotificationSeverityWarning),
		Enabled:          true,
	})
	form.Enabled = false
	form = AlertPolicyFormDataForRender(form)

	require.Equal(t, string(api.NotificationTypePipelineRetryExhausted), form.NotificationType)
	require.Equal(t, string(api.NotificationSeverityWarning), form.Severity)
	require.True(t, AlertPolicyShowsSeverity(form))
}

func TestFailedAlertDestinationEditIsExpanded(t *testing.T) {
	t.Parallel()
	destination := api.AlertDestination{Slug: "ops", Name: "Operations", Type: api.AlertDestinationTypeEmail}
	var out bytes.Buffer
	require.NoError(t, AlertDestinationList("acme", []api.AlertDestination{destination}, nil, AlertEditFormOverrides{
		DestinationSlug:     "ops",
		DestinationFormData: AlertDestinationFormData{Name: "Draft", Type: "email", ErrorMessage: "Save failed."},
	}, NewOrganizationPermissions("alert_destinations:update")).Render(context.Background(), &out))
	require.Contains(t, out.String(), `aria-controls="`+AlertDestinationEditPanelID(destination)+`" aria-expanded="true"`)
	require.Contains(t, out.String(), "Save failed.")
}
