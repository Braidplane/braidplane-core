package queriessvc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type localAdmissionTestSlot struct {
	name    string
	release func()
	err     error
}

func TestNewLocalConnectorExecutorRequiresRuntimePool(t *testing.T) {
	t.Parallel()

	executor, err := NewLocalConnectorExecutor(nil)
	require.Nil(t, executor)
	require.ErrorContains(t, err, "connector runtime pool is required")
}

func TestLocalConnectorAdmissionEnforcesLimitQueueCapacityAndFIFO(t *testing.T) {
	t.Parallel()

	admission := newLocalConnectorAdmission()
	key := localConnectorAdmissionKey{
		organizationSlug: must.Slug("org-1"),
		dataSourceSlug:   must.Slug("postgres-main"),
	}

	releaseFirst, err := admission.acquire(context.Background(), key, 1, 2, time.Second)
	require.NoError(t, err)

	acquired := make(chan localAdmissionTestSlot, 2)
	acquire := func(name string) {
		release, acquireErr := admission.acquire(context.Background(), key, 1, 2, time.Second)
		acquired <- localAdmissionTestSlot{name: name, release: release, err: acquireErr}
	}

	go acquire("second")
	requireLocalAdmissionState(t, admission, key, 1, 1)
	go acquire("third")
	requireLocalAdmissionState(t, admission, key, 1, 2)

	_, err = admission.acquire(context.Background(), key, 1, 2, time.Second)
	require.Error(t, err)
	require.True(t, xerror.Is(err, xerror.ExecutionConcurrencyLimitExceeded()), "got %v", err)

	releaseFirst()
	second := requireAcquiredSlot(t, acquired)
	require.Equal(t, "second", second.name)
	require.NoError(t, second.err)

	second.release()
	third := requireAcquiredSlot(t, acquired)
	require.Equal(t, "third", third.name)
	require.NoError(t, third.err)
	third.release()

	requireLocalAdmissionState(t, admission, key, 0, 0)
}

func TestLocalConnectorAdmissionTimesOutQueuedRequest(t *testing.T) {
	t.Parallel()

	admission := newLocalConnectorAdmission()
	key := localConnectorAdmissionKey{
		organizationSlug: must.Slug("org-1"),
		dataSourceSlug:   must.Slug("postgres-main"),
	}

	release, err := admission.acquire(context.Background(), key, 1, 1, time.Second)
	require.NoError(t, err)
	defer release()

	_, err = admission.acquire(context.Background(), key, 1, 1, 10*time.Millisecond)
	require.Error(t, err)
	require.True(t, xerror.Is(err, xerror.ExecutionConcurrencyLimitExceeded()), "got %v", err)
	requireLocalAdmissionState(t, admission, key, 1, 0)
}

func TestLocalConnectorAdmissionRemovesCancelledQueuedRequest(t *testing.T) {
	t.Parallel()

	admission := newLocalConnectorAdmission()
	key := localConnectorAdmissionKey{
		organizationSlug: must.Slug("org-1"),
		dataSourceSlug:   must.Slug("postgres-main"),
	}

	release, err := admission.acquire(context.Background(), key, 1, 1, time.Second)
	require.NoError(t, err)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, acquireErr := admission.acquire(ctx, key, 1, 1, time.Second)
		errCh <- acquireErr
	}()
	requireLocalAdmissionState(t, admission, key, 1, 1)

	cancel()
	require.ErrorIs(t, <-errCh, context.Canceled)
	requireLocalAdmissionState(t, admission, key, 1, 0)
}

func TestLocalConnectorAdmissionGrantCancelRaceReleasesSlot(t *testing.T) {
	t.Parallel()

	key := localConnectorAdmissionKey{
		organizationSlug: must.Slug("org-1"),
		dataSourceSlug:   must.Slug("postgres-main"),
	}
	for range 100 {
		admission := newLocalConnectorAdmission()
		releaseFirst, err := admission.acquire(context.Background(), key, 1, 1, time.Second)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		acquired := make(chan localAdmissionTestSlot, 1)
		go func() {
			release, acquireErr := admission.acquire(ctx, key, 1, 1, time.Second)
			acquired <- localAdmissionTestSlot{release: release, err: acquireErr}
		}()
		requireLocalAdmissionState(t, admission, key, 1, 1)

		start := make(chan struct{})
		var racers sync.WaitGroup
		racers.Add(2)
		go func() {
			defer racers.Done()
			<-start
			cancel()
		}()
		go func() {
			defer racers.Done()
			<-start
			releaseFirst()
		}()
		close(start)
		racers.Wait()

		result := requireAcquiredSlot(t, acquired)
		if result.err == nil {
			result.release()
		} else {
			require.ErrorIs(t, result.err, context.Canceled)
		}
		requireLocalAdmissionState(t, admission, key, 0, 0)
	}
}

func requireAcquiredSlot(
	t *testing.T,
	acquired <-chan localAdmissionTestSlot,
) localAdmissionTestSlot {
	t.Helper()

	select {
	case slot := <-acquired:
		return slot
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for connector admission")
		return localAdmissionTestSlot{}
	}
}

func requireLocalAdmissionState(
	t *testing.T,
	admission *localConnectorAdmission,
	key localConnectorAdmissionKey,
	active int,
	queued int,
) {
	t.Helper()

	require.Eventually(t, func() bool {
		admission.mu.Lock()
		defer admission.mu.Unlock()
		queue := admission.queues[key]
		if queue == nil {
			return active == 0 &&
				queued == 0 &&
				admission.active == int64(active) &&
				admission.queued == int64(queued)
		}
		return queue.active == active &&
			len(queue.waiters) == queued &&
			admission.active == int64(active) &&
			admission.queued == int64(queued)
	}, time.Second, time.Millisecond)
}
