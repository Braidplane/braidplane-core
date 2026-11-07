package clock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewClockReturnsCurrentTime(t *testing.T) {
	t.Parallel()

	before := time.Now()
	got := New().Now()
	after := time.Now()

	require.False(t, got.Before(before))
	require.False(t, got.After(after))
}
