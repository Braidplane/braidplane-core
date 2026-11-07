package frontend

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSafeReturnTo_AllowsInternalPaths(t *testing.T) {
	t.Parallel()

	require.Equal(t, "/dashboard", safeReturnTo("/dashboard"))
	require.Equal(t, "/dashboard?cursor=next-123", safeReturnTo("/dashboard?cursor=next-123"))
	require.Equal(t, "/organizations/acme-hq", safeReturnTo("/organizations/acme-hq"))
}

func TestSafeReturnTo_RejectsUnsafeTargets(t *testing.T) {
	t.Parallel()

	require.Empty(t, safeReturnTo(""))
	require.Empty(t, safeReturnTo("dashboard"))
	require.Empty(t, safeReturnTo("//evil.example/path"))
	require.Empty(t, safeReturnTo("https://evil.example/path"))
	require.Empty(t, safeReturnTo("http://evil.example/path"))
	require.Empty(t, safeReturnTo("/\\evil.example/path"))
}

func TestNotificationJourneyReturnIsOrganizationScoped(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"https://evil.example/orgs/acme/notifications", "//evil.example/path", "/orgs/other/notifications", "/orgs/acme/notifications/../alerts", "/orgs/acme/alerts", "/orgs/acme/notifications/extra"} {
		require.Empty(t, notificationReturnTo(raw, "acme"), raw)
	}
	const inbox = "/orgs/acme/notifications?state=resolved&severity=warning&cursor=older#notification-retry"
	require.Equal(t, inbox, notificationReturnTo(inbox, "acme"))
	require.Empty(t, alertSetupReturnTo("/orgs/other/alerts?tab=policies", "acme"))
	require.Equal(t, "/orgs/acme/alerts?tab=policies", alertSetupReturnTo("/orgs/acme/alerts?tab=policies", "acme"))
}
