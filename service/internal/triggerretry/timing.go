package triggerretry

import (
	"math/rand/v2"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

// Delay returns the backoff for the next attempt (2 is the first retry).
// Jitter samples uniformly from the upper half of the capped delay.
func Delay(policy model.PipelineTriggerRetryPolicy, attempt int, randomInt64N func(int64) int64) time.Duration {
	seconds := int64(policy.InitialDelaySeconds)
	switch policy.BackoffStrategy {
	case model.PipelineTriggerBackoffStrategyFixed:
	case model.PipelineTriggerBackoffStrategyExponential:
		for i := 2; i < attempt; i++ {
			seconds *= 2
		}
		if policy.MaxDelaySeconds > 0 {
			seconds = min(seconds, int64(policy.MaxDelaySeconds))
		}
	default:
		return 0
	}
	backoff := time.Duration(seconds) * time.Second
	if !policy.Jitter || backoff <= 0 {
		return backoff
	}
	if randomInt64N == nil {
		randomInt64N = rand.Int64N
	}
	// Match the timestamp precision of the retry records and queue.
	half := backoff / 2
	return half + time.Duration(randomInt64N(int64(half/time.Microsecond)+1))*time.Microsecond
}
