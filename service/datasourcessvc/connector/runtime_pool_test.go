package connector

import (
	"context"
	stdsql "database/sql"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type fakeRuntimeConnector struct {
	closeCount atomic.Int32
	statsCalls atomic.Int32
	stats      stdsql.DBStats
}

func (*fakeRuntimeConnector) Execute(
	context.Context,
	json.RawMessage,
	map[string]types.TypedParam,
	...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	return &result.ExecutionResult{}, nil
}

func (*fakeRuntimeConnector) TestConnection(context.Context) error { return nil }

func (c *fakeRuntimeConnector) Close() error {
	c.closeCount.Add(1)
	return nil
}

func (c *fakeRuntimeConnector) SQLPoolStats() stdsql.DBStats {
	c.statsCalls.Add(1)
	return c.stats
}

func TestManagerReusesExactSQLRuntimeIdentity(t *testing.T) {
	t.Parallel()

	var constructions atomic.Int32
	physical := &fakeRuntimeConnector{}
	manager := newTestManager(t, 4, func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		constructions.Add(1)
		return physical, nil
	})

	first, releaseFirst, err := manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-1", "credential-1"),
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, releaseFirst())

	second, releaseSecond, err := manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-1", "credential-1"),
		nil,
		nil,
	)
	require.NoError(t, err)
	require.Same(t, first, second)
	require.Equal(t, int32(1), constructions.Load())
	require.NoError(t, releaseSecond())
	require.Zero(t, physical.closeCount.Load())
}

func TestManagerPassesOrganizationSlugToPooledConnectorFactory(t *testing.T) {
	t.Parallel()

	var received slug.Slug
	manager := newTestManager(t, 1, func(
		ctx context.Context,
		_ model.DataSourceType,
		_, _ []byte,
	) (Connector, error) {
		received, _ = ctx.Value(organizationSlugContextKey{}).(slug.Slug)
		return &fakeRuntimeConnector{}, nil
	})

	_, release, err := manager.Acquire(t.Context(), testRuntimeIdentity("config-1", "credential-1"), nil, nil)
	require.NoError(t, err)
	require.Equal(t, slug.Slug("org-1"), received)
	require.NoError(t, release())
}

func TestManagerDeduplicatesConcurrentColdAcquire(t *testing.T) {
	t.Parallel()

	var constructions atomic.Int32
	started := make(chan struct{})
	proceed := make(chan struct{})
	physical := &fakeRuntimeConnector{}
	manager := newTestManager(t, 4, func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		if constructions.Add(1) == 1 {
			close(started)
		}
		<-proceed
		return physical, nil
	})

	type acquireResult struct {
		conn    Connector
		release ReleaseFunc
		err     error
	}
	results := make(chan acquireResult, 2)
	acquire := func() {
		conn, release, err := manager.Acquire(
			t.Context(),
			testRuntimeIdentity("config-1", "credential-1"),
			nil,
			nil,
		)
		results <- acquireResult{conn: conn, release: release, err: err}
	}
	go acquire()
	<-started
	go acquire()
	close(proceed)

	first := <-results
	second := <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.Same(t, first.conn, second.conn)
	require.Equal(t, int32(1), constructions.Load())
	require.NoError(t, first.release())
	require.NoError(t, second.release())
}

func TestManagerRetiresChangedRevisionAfterFinalRelease(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var physical []*fakeRuntimeConnector
	manager := newTestManager(t, 2, func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		conn := &fakeRuntimeConnector{}
		mu.Lock()
		physical = append(physical, conn)
		mu.Unlock()
		return conn, nil
	})

	first, releaseFirst, err := manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-1", "credential-1"),
		nil,
		nil,
	)
	require.NoError(t, err)
	second, releaseSecond, err := manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-2", "credential-1"),
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NotSame(t, first, second)
	require.Equal(t, 2, manager.Snapshot().Entries)
	require.Zero(t, physical[0].closeCount.Load())

	require.NoError(t, releaseFirst())
	require.Equal(t, int32(1), physical[0].closeCount.Load())
	require.Equal(t, 1, manager.Snapshot().Entries)
	require.NoError(t, releaseSecond())
}

func TestManagerRejectsRevisionWhenBoundIsFullyLeased(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, 1, func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		return &fakeRuntimeConnector{}, nil
	})
	_, release, err := manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-1", "credential-1"),
		nil,
		nil,
	)
	require.NoError(t, err)

	_, _, err = manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-2", "credential-1"),
		nil,
		nil,
	)
	require.ErrorIs(t, err, ErrRuntimePoolExhausted)
	require.NoError(t, release())
}

func TestManagerEvictsLeastRecentlyUsedIdlePoolAtCapacity(t *testing.T) {
	t.Parallel()

	var physical []*fakeRuntimeConnector
	var mu sync.Mutex
	manager := newTestManager(t, 1, func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		conn := &fakeRuntimeConnector{}
		mu.Lock()
		physical = append(physical, conn)
		mu.Unlock()
		return conn, nil
	})

	firstIdentity := testRuntimeIdentity("config-1", "credential-1")
	firstIdentity.OrganizationSlug = slug.Slug("org-2")
	_, releaseFirst, err := manager.Acquire(t.Context(), firstIdentity, nil, nil)
	require.NoError(t, err)
	require.NoError(t, releaseFirst())

	secondIdentity := testRuntimeIdentity("config-1", "credential-1")
	secondIdentity.DataSourceSlug = slug.Slug("postgres-secondary")
	_, releaseSecond, err := manager.Acquire(t.Context(), secondIdentity, nil, nil)
	require.NoError(t, err)
	require.Equal(t, int32(1), physical[0].closeCount.Load())
	require.Equal(t, 1, manager.Snapshot().Entries)
	require.NoError(t, releaseSecond())
}

func TestManagerPrefersSameOrganizationIdlePoolAtCapacity(t *testing.T) {
	t.Parallel()

	var physical []*fakeRuntimeConnector
	manager := newTestManager(t, 2, func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		conn := &fakeRuntimeConnector{}
		physical = append(physical, conn)
		return conn, nil
	})

	otherOrganization := testRuntimeIdentity("config-1", "credential-1")
	otherOrganization.OrganizationSlug = slug.Slug("org-2")
	_, releaseOther, err := manager.Acquire(t.Context(), otherOrganization, nil, nil)
	require.NoError(t, err)
	require.NoError(t, releaseOther())

	preferredOrganization := testRuntimeIdentity("config-1", "credential-1")
	preferredOrganization.DataSourceSlug = slug.Slug("postgres-preferred")
	_, releasePreferred, err := manager.Acquire(t.Context(), preferredOrganization, nil, nil)
	require.NoError(t, err)
	require.NoError(t, releasePreferred())

	newPreferred := testRuntimeIdentity("config-1", "credential-1")
	newPreferred.DataSourceSlug = slug.Slug("postgres-new")
	_, releaseNew, err := manager.Acquire(t.Context(), newPreferred, nil, nil)
	require.NoError(t, err)
	require.Zero(t, physical[0].closeCount.Load())
	require.Equal(t, int32(1), physical[1].closeCount.Load())
	require.NoError(t, releaseNew())
}

func TestManagerDoesNotPreferLeasedSameOrganizationPoolAtCapacity(t *testing.T) {
	t.Parallel()

	var physical []*fakeRuntimeConnector
	manager := newTestManager(t, 2, func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		conn := &fakeRuntimeConnector{}
		physical = append(physical, conn)
		return conn, nil
	})

	preferredLeased := testRuntimeIdentity("config-1", "credential-1")
	preferredLeased.DataSourceSlug = slug.Slug("postgres-leased")
	_, releasePreferred, err := manager.Acquire(t.Context(), preferredLeased, nil, nil)
	require.NoError(t, err)

	fallbackIdle := testRuntimeIdentity("config-1", "credential-1")
	fallbackIdle.OrganizationSlug = slug.Slug("org-2")
	_, releaseFallback, err := manager.Acquire(t.Context(), fallbackIdle, nil, nil)
	require.NoError(t, err)
	require.NoError(t, releaseFallback())

	newPreferred := testRuntimeIdentity("config-1", "credential-1")
	newPreferred.DataSourceSlug = slug.Slug("postgres-new")
	_, releaseNew, err := manager.Acquire(t.Context(), newPreferred, nil, nil)
	require.NoError(t, err)
	require.Zero(t, physical[0].closeCount.Load())
	require.Equal(t, int32(1), physical[1].closeCount.Load())
	require.NoError(t, releaseNew())
	require.NoError(t, releasePreferred())
}

func TestManagerWaiterHonorsCancellation(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	proceed := make(chan struct{})
	manager := newTestManager(t, 2, func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		close(started)
		<-proceed
		return &fakeRuntimeConnector{}, nil
	})

	firstDone := make(chan ReleaseFunc, 1)
	go func() {
		_, release, _ := manager.Acquire(
			t.Context(),
			testRuntimeIdentity("config-1", "credential-1"),
			nil,
			nil,
		)
		firstDone <- release
	}()
	<-started

	waitCtx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := manager.Acquire(
		waitCtx,
		testRuntimeIdentity("config-1", "credential-1"),
		nil,
		nil,
	)
	require.ErrorIs(t, err, context.Canceled)

	close(proceed)
	require.NoError(t, (<-firstDone)())
}

func TestManagerEvictsIdleSQLPool(t *testing.T) {
	t.Parallel()

	physical := &fakeRuntimeConnector{}
	manager, err := NewManager(
		func(context.Context, model.DataSourceType, []byte, []byte) (Connector, error) {
			return physical, nil
		},
		RuntimePoolSettings{
			MaxEntries:      2,
			IdleTimeout:     10 * time.Millisecond,
			MaxLifetime:     time.Minute,
			CleanupInterval: 2 * time.Millisecond,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	_, release, err := manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-1", "credential-1"),
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, release())

	require.Eventually(t, func() bool {
		return physical.closeCount.Load() == 1 && manager.Snapshot().Entries == 0
	}, time.Second, 5*time.Millisecond)
}

func TestManagerEvictsSQLPoolAtMaximumLifetime(t *testing.T) {
	t.Parallel()

	physical := &fakeRuntimeConnector{}
	manager, err := NewManager(
		func(context.Context, model.DataSourceType, []byte, []byte) (Connector, error) {
			return physical, nil
		},
		RuntimePoolSettings{
			MaxEntries:      2,
			IdleTimeout:     time.Minute,
			MaxLifetime:     10 * time.Millisecond,
			CleanupInterval: 2 * time.Millisecond,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	_, release, err := manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-1", "credential-1"),
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, release())

	require.Eventually(t, func() bool {
		return physical.closeCount.Load() == 1 && manager.Snapshot().Entries == 0
	}, time.Second, 5*time.Millisecond)
}

func TestManagerBypassesCacheForNonSQLConnector(t *testing.T) {
	t.Parallel()

	var constructions atomic.Int32
	var physical []*fakeRuntimeConnector
	var mu sync.Mutex
	manager := newTestManager(t, 2, func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		conn := &fakeRuntimeConnector{}
		constructions.Add(1)
		mu.Lock()
		physical = append(physical, conn)
		mu.Unlock()
		return conn, nil
	})
	identity := RuntimeIdentity{DataSourceType: model.DataSourceTypeHTTP}

	_, releaseFirst, err := manager.Acquire(t.Context(), identity, nil, nil)
	require.NoError(t, err)
	require.NoError(t, releaseFirst())
	_, releaseSecond, err := manager.Acquire(t.Context(), identity, nil, nil)
	require.NoError(t, err)
	require.NoError(t, releaseSecond())

	require.Equal(t, int32(2), constructions.Load())
	require.Equal(t, int32(1), physical[0].closeCount.Load())
	require.Equal(t, int32(1), physical[1].closeCount.Load())
	require.Zero(t, manager.Snapshot().Entries)
}

func TestManagerSnapshotAggregatesSQLPoolStats(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, 4, func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (Connector, error) {
		return &fakeRuntimeConnector{stats: stdsql.DBStats{
			MaxOpenConnections: 5,
			OpenConnections:    3,
			InUse:              2,
			Idle:               1,
			WaitCount:          7,
			WaitDuration:       9 * time.Millisecond,
			MaxIdleClosed:      11,
			MaxIdleTimeClosed:  13,
			MaxLifetimeClosed:  17,
		}}, nil
	})

	_, release, err := manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-1", "credential-1"),
		nil,
		nil,
	)
	require.NoError(t, err)
	snapshot := manager.Snapshot()
	require.Equal(t, 1, snapshot.Entries)
	require.Equal(t, 1, snapshot.ActiveLeases)
	require.Equal(t, 3, snapshot.OpenConnections)
	require.Equal(t, 2, snapshot.InUseConnections)
	require.Equal(t, 1, snapshot.IdleConnections)
	require.Equal(t, 5, snapshot.MaxOpenConnections)
	require.Equal(t, int64(7), snapshot.WaitCount)
	require.Equal(t, 9*time.Millisecond, snapshot.WaitDuration)
	require.Equal(t, int64(11), snapshot.MaxIdleClosed)
	require.Equal(t, int64(13), snapshot.MaxIdleTimeClosed)
	require.Equal(t, int64(17), snapshot.MaxLifetimeClosed)
	require.NoError(t, release())
}

func TestManagerCleanupSamplesPoolStatsAndStopsOnClose(t *testing.T) {
	t.Parallel()

	physical := &fakeRuntimeConnector{}
	manager, err := NewManager(
		func(context.Context, model.DataSourceType, []byte, []byte) (Connector, error) {
			return physical, nil
		},
		RuntimePoolSettings{
			MaxEntries:      2,
			IdleTimeout:     time.Minute,
			MaxLifetime:     time.Hour,
			CleanupInterval: 2 * time.Millisecond,
		},
	)
	require.NoError(t, err)

	_, release, err := manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-1", "credential-1"),
		nil,
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, release())
	require.Eventually(t, func() bool {
		return physical.statsCalls.Load() > 0
	}, time.Second, 5*time.Millisecond)

	require.NoError(t, manager.Close())
	callsAfterClose := physical.statsCalls.Load()
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, callsAfterClose, physical.statsCalls.Load())
}

func TestManagerCloseClosesLeasedPoolsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	physical := &fakeRuntimeConnector{}
	manager, err := NewManager(
		func(context.Context, model.DataSourceType, []byte, []byte) (Connector, error) {
			return physical, nil
		},
		RuntimePoolSettings{},
	)
	require.NoError(t, err)
	_, release, err := manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-1", "credential-1"),
		nil,
		nil,
	)
	require.NoError(t, err)

	require.NoError(t, manager.Close())
	require.NoError(t, manager.Close())
	require.Equal(t, int32(1), physical.closeCount.Load())
	require.NoError(t, release())
	require.Equal(t, int32(1), physical.closeCount.Load())
	_, _, err = manager.Acquire(
		t.Context(),
		testRuntimeIdentity("config-1", "credential-1"),
		nil,
		nil,
	)
	require.ErrorIs(t, err, ErrRuntimePoolClosed)
}

func newTestManager(t *testing.T, maxEntries int, factory NewConnectorFunc) *Manager {
	t.Helper()

	manager, err := NewManager(factory, RuntimePoolSettings{
		MaxEntries:      maxEntries,
		IdleTimeout:     time.Minute,
		MaxLifetime:     time.Hour,
		CleanupInterval: time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	return manager
}

func testRuntimeIdentity(configVersion, credentialVersion string) RuntimeIdentity {
	return RuntimeIdentity{
		OrganizationSlug:  slug.Slug("org-1"),
		DataSourceSlug:    slug.Slug("postgres-main"),
		DataSourceType:    model.DataSourceTypePostgres,
		ConfigVersion:     configVersion,
		CredentialVersion: credentialVersion,
	}
}
