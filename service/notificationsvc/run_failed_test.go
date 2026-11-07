package notificationsvc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunFailureIdentitySurvivesRepeatedFinalization(t *testing.T) {
	t.Parallel()
	at := time.Now().UTC()
	first := RunFailedCandidate("acme", "ops", "dev", "orders", "last-attempt", "root", at)
	repeat := RunFailedCandidate("acme", "ops", "dev", "orders", "last-attempt", "root", at.Add(time.Minute))
	require.Equal(t, first.RecordSlug, repeat.RecordSlug)
	require.Equal(t, first.DedupeFingerprint, repeat.DedupeFingerprint)
	other := RunFailedCandidate("other", "ops", "dev", "orders", "last-attempt", "root", at)
	require.NotEqual(t, first.RecordSlug, other.RecordSlug)
	manual := RunFailedCandidate("acme", "ops", "dev", "orders", "manual", "", at)
	require.Equal(t, "manual", manual.SourceRefs["root_run_slug"])
}
