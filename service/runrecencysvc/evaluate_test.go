package runrecencysvc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWindow(t *testing.T) {
	at := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	oldSuccess, newSuccess := at.Add(-time.Hour), at.Add(30*time.Minute)
	for _, tc := range []struct {
		name              string
		enabled, deployed time.Time
		success           *time.Time
		deadline          time.Time
	}{
		{"initial grace without success", at, at.Add(-time.Hour), nil, at.Add(time.Hour)},
		{"prior success does not shorten initial grace", at, at.Add(-time.Hour), &oldSuccess, at.Add(time.Hour)},
		{"qualifying success extends window", at, at, &newSuccess, at.Add(90 * time.Minute)},
		{"deployment change starts full window", at.Add(-time.Hour), at, nil, at.Add(time.Hour)},
		{"reenablement starts full window", at, at.Add(-time.Hour), &oldSuccess, at.Add(time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := Window{EnabledAt: tc.enabled, DeployedAt: tc.deployed, MaximumAge: time.Hour}
			require.Equal(t, tc.deadline, w.Deadline(tc.success))
			require.False(t, w.Breached(tc.deadline.Add(-time.Nanosecond), tc.success))
			require.True(t, w.Breached(tc.deadline, tc.success))
			require.True(t, w.Breached(tc.deadline.Add(time.Hour), tc.success))
		})
	}
}
