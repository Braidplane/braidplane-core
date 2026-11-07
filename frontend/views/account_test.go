package views

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFormatAccountPATDateTimeLocalUsesUTC(t *testing.T) {
	t.Parallel()

	localTime := time.Date(2026, time.May, 14, 12, 30, 0, 0, time.FixedZone("ReviewLocal", 3*60*60))

	require.Equal(t, "2026-05-14T09:30", FormatAccountPATDateTimeLocal(&localTime))
}
