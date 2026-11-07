package executionrequests

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCreateValidatesAdmissionConfiguration(t *testing.T) {
	t.Parallel()

	queueDeadline := time.Now().Add(time.Minute)
	tests := []struct {
		name             string
		concurrencyLimit int
		queueCapacity    int
		queueDeadline    *time.Time
		wantError        string
	}{
		{
			name:             "non-positive concurrency limit",
			concurrencyLimit: 0,
			queueCapacity:    1,
			queueDeadline:    &queueDeadline,
			wantError:        "execution admission limits must be positive",
		},
		{
			name:             "non-positive queue capacity",
			concurrencyLimit: 1,
			queueCapacity:    0,
			queueDeadline:    &queueDeadline,
			wantError:        "execution admission limits must be positive",
		},
		{
			name:             "missing queue deadline",
			concurrencyLimit: 1,
			queueCapacity:    1,
			wantError:        "execution admission queue deadline is required",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := (Repository{}).Create(
				context.Background(),
				nil,
				Request{
					DataSourceSlug:   "postgres-main",
					ConcurrencyLimit: testCase.concurrencyLimit,
					QueueCapacity:    testCase.queueCapacity,
					QueueDeadlineAt:  testCase.queueDeadline,
				},
				time.Now().Add(time.Hour),
			)

			require.EqualError(t, err, testCase.wantError)
		})
	}
}
