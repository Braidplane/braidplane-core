package runrecencysvc

import (
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/postgres/runrecency"
	"github.com/stretchr/testify/require"
)

func TestInspectionOutcomesUseExactDeploymentWindow(t *testing.T) {
	now := testNow
	success := now.Add(-time.Minute)
	old := now.Add(-2 * time.Hour)
	for _, tc := range []struct {
		name                 string
		rule                 bool
		revision, deployment int64
		since                time.Time
		success              *time.Time
		outcome, reason      string
	}{
		{"unconfigured", false, 0, 1, old, nil, "unavailable", "not_configured"},
		{"disabled rule", false, 2, 1, old, &success, "unavailable", "expectation_disabled"},
		{"removed deployment", true, 1, 0, old, &success, "unavailable", "deployment_unavailable"},
		{"grace without success", true, 1, 1, now, nil, "grace", ""},
		{"reenabled with old success", true, 2, 1, now, &old, "grace", ""},
		{"qualifying success", true, 1, 1, old, &success, "healthy", ""},
		{"breach at exact boundary", true, 1, 1, now.Add(-time.Hour), nil, "breached", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := &Inspection{EvaluatedAt: now, Expectation: runrecency.Expectation{Revision: tc.revision, Enabled: tc.rule, MaximumAge: time.Hour, EnabledAt: tc.since, DeployedAt: old, DeploymentID: tc.deployment, SucceededAt: tc.success}}
			i.calculate()
			require.Equal(t, tc.outcome, i.Outcome.String())
			if tc.reason != "" {
				require.NotNil(t, i.UnavailableReason)
				require.Equal(t, tc.reason, *i.UnavailableReason)
				require.Nil(t, i.Deadline)
			} else {
				require.Nil(t, i.UnavailableReason)
				require.NotNil(t, i.Deadline)
			}
		})
	}
}

func TestInspectRetainsEvidenceWithoutProducing(t *testing.T) {
	e := testExpectation(testNow)
	repo, notifications := &evaluatorRepository{expectations: []runrecency.Expectation{e}}, &evaluatorNotifications{}
	result, err := New(&evaluatorTransactions{}, repo, notifications).Inspect(t.Context(), e.Scope, testNow)
	require.NoError(t, err)
	require.Equal(t, e.Revision, result.Expectation.Revision)
	require.Nil(t, result.UnavailableReason)
	require.Empty(t, notifications.created)
	require.Empty(t, notifications.resolved)
}
