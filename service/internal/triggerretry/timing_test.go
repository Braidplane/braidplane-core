package triggerretry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

func TestDelayJitterRangeUsesCappedBackoff(t *testing.T) {
	t.Parallel()
	policy := model.PipelineTriggerRetryPolicy{
		BackoffStrategy:     model.PipelineTriggerBackoffStrategyExponential,
		InitialDelaySeconds: 10, MaxDelaySeconds: 15, Jitter: true,
	}
	lowest := func(int64) int64 { return 0 }
	highest := func(n int64) int64 { return n - 1 }
	require.Equal(t, 5*time.Second, Delay(policy, 2, lowest))
	require.Equal(t, 10*time.Second, Delay(policy, 2, highest))
	require.Equal(t, 7500*time.Millisecond, Delay(policy, 3, lowest))
	require.Equal(t, 15*time.Second, Delay(policy, 3, highest))

	noDraw := func(int64) int64 { panic("unexpected random draw") }
	policy.Jitter = false
	require.Equal(t, 15*time.Second, Delay(policy, 3, noDraw))
	require.Zero(t, Delay(model.PipelineTriggerRetryPolicy{
		BackoffStrategy: model.PipelineTriggerBackoffStrategyNone, Jitter: true,
	}, 2, noDraw))
}
