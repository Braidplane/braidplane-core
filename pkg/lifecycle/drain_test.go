package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDrainLifecycleState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		wantInitially     bool
		wantBeginResults  []bool
		wantFinalDraining bool
	}{
		{
			name:              "BeginsNotDrainingAndTransitionsOnce",
			wantInitially:     false,
			wantBeginResults:  []bool{true, false},
			wantFinalDraining: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			drain := NewDrainLifecycle()

			require.Equal(t, tt.wantInitially, drain.IsDraining())
			for _, want := range tt.wantBeginResults {
				require.Equal(t, want, drain.BeginDrain())
			}
			require.Equal(t, tt.wantFinalDraining, drain.IsDraining())
		})
	}
}

func TestDrainLifecycleWaitDrainWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		duration       time.Duration
		cancelContext  bool
		wantErr        error
		wantMinElapsed time.Duration
	}{
		{
			name:           "WaitDrainWindowHonorsDuration",
			duration:       10 * time.Millisecond,
			wantMinElapsed: 10 * time.Millisecond,
		},
		{
			name:          "WaitDrainWindowHonorsContextCancellation",
			duration:      time.Hour,
			cancelContext: true,
			wantErr:       context.Canceled,
		},
		{
			name:     "WaitDrainWindowSkipsZeroDuration",
			duration: 0,
		},
		{
			name:     "WaitDrainWindowSkipsNegativeDuration",
			duration: -time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			drain := NewDrainLifecycle()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelContext {
				cancel()
			}
			startedAt := time.Now()

			err := drain.WaitDrainWindow(ctx, tt.duration)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			if tt.wantMinElapsed > 0 {
				require.GreaterOrEqual(t, time.Since(startedAt), tt.wantMinElapsed)
			}
		})
	}
}
