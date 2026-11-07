package connector

import (
	"context"
	stdsql "database/sql"
	"sync"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	// DefaultRuntimePoolMaxEntries bounds SQL pools retained by one process.
	DefaultRuntimePoolMaxEntries = 128
	// DefaultRuntimePoolIdleTimeout retires a pool unused by connector work.
	DefaultRuntimePoolIdleTimeout = 10 * time.Minute
	// DefaultRuntimePoolMaxLifetime bounds reuse of one connector pool revision.
	DefaultRuntimePoolMaxLifetime = time.Hour
	// DefaultRuntimePoolCleanupInterval controls background idle and lifetime eviction.
	DefaultRuntimePoolCleanupInterval = time.Minute
)

var (
	// ErrRuntimePoolClosed reports acquisition after manager shutdown.
	ErrRuntimePoolClosed = xerror.ExecutionUnavailable().Detail("connector runtime pool is closed")
	// ErrRuntimePoolExhausted reports that every bounded entry is still active.
	ErrRuntimePoolExhausted = xerror.ExecutionUnavailable().Detail("connector runtime pool is full")
	// ErrRuntimePoolRevisionRetired reports an older revision that is still draining.
	ErrRuntimePoolRevisionRetired = xerror.ExecutionUnavailable().Detail(
		"connector runtime pool revision is retired",
	)
)

// RuntimeIdentity identifies one reusable connector configuration without
// carrying credential material. Version values are opaque to the manager.
type RuntimeIdentity struct {
	OrganizationSlug  slug.Slug
	DataSourceSlug    slug.Slug
	DataSourceType    model.DataSourceType
	ConfigVersion     string
	CredentialVersion string
}

// RuntimePoolSettings controls the process-local cache of reusable SQL pools.
type RuntimePoolSettings struct {
	MaxEntries      int
	IdleTimeout     time.Duration
	MaxLifetime     time.Duration
	CleanupInterval time.Duration
}

// RuntimePoolSnapshot contains aggregate, tenant-safe process-local pool state.
type RuntimePoolSnapshot struct {
	Entries            int
	Creating           int
	Retired            int
	ActiveLeases       int
	MaxEntries         int
	OpenConnections    int
	InUseConnections   int
	IdleConnections    int
	MaxOpenConnections int
	WaitCount          int64
	WaitDuration       time.Duration
	MaxIdleClosed      int64
	MaxIdleTimeClosed  int64
	MaxLifetimeClosed  int64
}

// ReleaseFunc releases one acquired connector lease.
type ReleaseFunc func() error

type runtimePoolKey struct {
	organizationSlug  slug.Slug
	dataSourceSlug    slug.Slug
	dataSourceType    model.DataSourceType
	configVersion     string
	credentialVersion string
}

type runtimePoolBaseKey struct {
	organizationSlug slug.Slug
	dataSourceSlug   slug.Slug
	dataSourceType   model.DataSourceType
}

type runtimePoolEntry struct {
	key       runtimePoolKey
	connector Connector
	ready     chan struct{}
	err       error
	createdAt time.Time
	lastUsed  time.Time
	leases    int
	retired   bool
	closed    bool
}

// Manager owns reusable PostgreSQL and MySQL connector pools for one process.
// Other connector types pass through the factory and are closed by their
// release function.
type Manager struct {
	factory  NewConnectorFunc
	settings RuntimePoolSettings

	mu      sync.Mutex
	entries map[runtimePoolKey]*runtimePoolEntry
	closed  bool
	stop    chan struct{}
	wg      sync.WaitGroup
}

// NewManager creates and starts a process-local connector runtime pool manager.
func NewManager(factory NewConnectorFunc, settings RuntimePoolSettings) (*Manager, error) {
	if factory == nil {
		return nil, xerror.Unexpected().Detail("connector factory is required")
	}

	resolved, err := resolveRuntimePoolSettings(settings)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		factory:  factory,
		settings: resolved,
		entries:  make(map[runtimePoolKey]*runtimePoolEntry),
		stop:     make(chan struct{}),
	}
	manager.wg.Add(1)
	go manager.cleanup()
	return manager, nil
}

// Acquire returns a connector and a release function. PostgreSQL and MySQL
// connectors are reused only for an exact runtime identity.
func (m *Manager) Acquire(
	ctx context.Context,
	identity RuntimeIdentity,
	configJSON, credentialsJSON []byte,
) (Connector, ReleaseFunc, error) {
	if identity.DataSourceType == model.DataSourceTypeUnknown {
		return nil, nil, xerror.InvalidInput(xerror.DataSource).Detail(
			"data source type is required",
		)
	}
	ctx = WithDataSourceIdentity(ctx, identity.OrganizationSlug, identity.DataSourceSlug)
	if !isPooledSQLType(identity.DataSourceType) {
		return m.acquireDirect(ctx, identity.DataSourceType, configJSON, credentialsJSON)
	}
	if identity.OrganizationSlug == "" ||
		identity.DataSourceSlug == "" ||
		identity.ConfigVersion == "" ||
		identity.CredentialVersion == "" {
		return nil, nil, xerror.InvalidInput(xerror.DataSource).Detail(
			"complete SQL connector runtime identity is required",
		)
	}

	key := runtimePoolKey{
		organizationSlug:  identity.OrganizationSlug,
		dataSourceSlug:    identity.DataSourceSlug,
		dataSourceType:    identity.DataSourceType,
		configVersion:     identity.ConfigVersion,
		credentialVersion: identity.CredentialVersion,
	}
	baseKey := runtimePoolBaseKey{
		organizationSlug: identity.OrganizationSlug,
		dataSourceSlug:   identity.DataSourceSlug,
		dataSourceType:   identity.DataSourceType,
	}

	for {
		now := time.Now()
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, nil, ErrRuntimePoolClosed
		}

		toClose := m.evictExpiredLocked(now)
		if entry, ok := m.entries[key]; ok {
			if entry.retired {
				m.mu.Unlock()
				closeConnectors(toClose)
				return nil, nil, ErrRuntimePoolRevisionRetired
			}
			if entry.ready != nil {
				ready := entry.ready
				m.mu.Unlock()
				closeConnectors(toClose)
				select {
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				case <-ready:
					if entry.err != nil {
						return nil, nil, entry.err
					}
					continue
				}
			}

			entry.leases++
			entry.lastUsed = now
			conn := entry.connector
			m.mu.Unlock()
			closeConnectors(toClose)
			return conn, m.releaseFunc(entry), nil
		}

		toClose = append(toClose, m.retireOtherRevisionsLocked(baseKey)...)
		if len(m.entries) >= m.settings.MaxEntries {
			if idle := m.evictLeastRecentlyUsedLocked(baseKey.organizationSlug); idle != nil {
				toClose = append(toClose, idle)
			} else {
				m.mu.Unlock()
				closeConnectors(toClose)
				return nil, nil, ErrRuntimePoolExhausted
			}
		}

		entry := &runtimePoolEntry{
			key:       key,
			ready:     make(chan struct{}),
			createdAt: now,
			lastUsed:  now,
		}
		m.entries[key] = entry
		m.mu.Unlock()
		closeConnectors(toClose)

		conn, err := m.factory(
			ctx,
			identity.DataSourceType,
			configJSON,
			credentialsJSON,
		)

		m.mu.Lock()
		switch {
		case err != nil:
			entry.err = err
			delete(m.entries, key)
			close(entry.ready)
			entry.ready = nil
			m.mu.Unlock()
			return nil, nil, err
		case m.closed:
			entry.err = ErrRuntimePoolClosed
		case entry.retired:
			entry.err = ErrRuntimePoolRevisionRetired
		default:
			entry.connector = conn
			entry.leases = 1
			entry.lastUsed = time.Now()
			close(entry.ready)
			entry.ready = nil
			m.mu.Unlock()
			return conn, m.releaseFunc(entry), nil
		}

		delete(m.entries, key)
		close(entry.ready)
		entry.ready = nil
		entry.closed = true
		acquireErr := entry.err
		m.mu.Unlock()
		_ = conn.Close()
		return nil, nil, acquireErr
	}
}

// Snapshot returns aggregate cache and database/sql pool utilization.
func (m *Manager) Snapshot() RuntimePoolSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	snapshot := RuntimePoolSnapshot{
		Entries:    len(m.entries),
		MaxEntries: m.settings.MaxEntries,
	}
	for _, entry := range m.entries {
		snapshot.ActiveLeases += entry.leases
		if entry.ready != nil {
			snapshot.Creating++
		}
		if entry.retired {
			snapshot.Retired++
		}
		if entry.connector == nil || entry.closed {
			continue
		}
		provider, ok := entry.connector.(interface {
			SQLPoolStats() stdsql.DBStats
		})
		if !ok {
			continue
		}
		stats := provider.SQLPoolStats()
		snapshot.OpenConnections += stats.OpenConnections
		snapshot.InUseConnections += stats.InUse
		snapshot.IdleConnections += stats.Idle
		snapshot.MaxOpenConnections += stats.MaxOpenConnections
		snapshot.WaitCount += stats.WaitCount
		snapshot.WaitDuration += stats.WaitDuration
		snapshot.MaxIdleClosed += stats.MaxIdleClosed
		snapshot.MaxIdleTimeClosed += stats.MaxIdleTimeClosed
		snapshot.MaxLifetimeClosed += stats.MaxLifetimeClosed
	}
	return snapshot
}

// Close prevents new acquisitions and closes every connector currently owned
// by the manager. It is safe to call more than once.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.stop)

	connectors := make([]Connector, 0, len(m.entries))
	for key, entry := range m.entries {
		entry.retired = true
		if entry.connector != nil && !entry.closed {
			entry.closed = true
			connectors = append(connectors, entry.connector)
		}
		delete(m.entries, key)
	}
	m.mu.Unlock()

	m.wg.Wait()
	var closeErr error
	for _, conn := range connectors {
		closeErr = xerror.Join(closeErr, conn.Close())
	}
	return closeErr
}

func resolveRuntimePoolSettings(settings RuntimePoolSettings) (RuntimePoolSettings, error) {
	if settings.MaxEntries == 0 {
		settings.MaxEntries = DefaultRuntimePoolMaxEntries
	}
	if settings.IdleTimeout == 0 {
		settings.IdleTimeout = DefaultRuntimePoolIdleTimeout
	}
	if settings.MaxLifetime == 0 {
		settings.MaxLifetime = DefaultRuntimePoolMaxLifetime
	}
	if settings.CleanupInterval == 0 {
		settings.CleanupInterval = DefaultRuntimePoolCleanupInterval
	}
	if settings.MaxEntries < 0 ||
		settings.IdleTimeout < 0 ||
		settings.MaxLifetime < 0 ||
		settings.CleanupInterval < 0 {
		return RuntimePoolSettings{}, xerror.InvalidInput(xerror.DataSource).Detail(
			"invalid connector runtime pool settings",
		)
	}
	return settings, nil
}

func isPooledSQLType(dataSourceType model.DataSourceType) bool {
	return dataSourceType == model.DataSourceTypePostgres ||
		dataSourceType == model.DataSourceTypeMySQL
}

func (m *Manager) acquireDirect(
	ctx context.Context,
	dataSourceType model.DataSourceType,
	configJSON, credentialsJSON []byte,
) (Connector, ReleaseFunc, error) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, nil, ErrRuntimePoolClosed
	}

	conn, err := m.factory(ctx, dataSourceType, configJSON, credentialsJSON)
	if err != nil {
		return nil, nil, err
	}

	m.mu.Lock()
	closed = m.closed
	m.mu.Unlock()
	if closed {
		_ = conn.Close()
		return nil, nil, ErrRuntimePoolClosed
	}

	var once sync.Once
	var closeErr error
	return conn, func() error {
		once.Do(func() {
			closeErr = conn.Close()
		})
		return closeErr
	}, nil
}

func (m *Manager) releaseFunc(entry *runtimePoolEntry) ReleaseFunc {
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() {
			var conn Connector
			m.mu.Lock()
			if entry.leases > 0 {
				entry.leases--
			}
			entry.lastUsed = time.Now()
			if entry.retired && !entry.closed && entry.connector != nil {
				entry.closed = true
				delete(m.entries, entry.key)
				conn = entry.connector
			}
			m.mu.Unlock()
			if conn != nil {
				closeErr = conn.Close()
			}
		})
		return closeErr
	}
}

func (m *Manager) retireOtherRevisionsLocked(base runtimePoolBaseKey) []Connector {
	var connectors []Connector
	for key, entry := range m.entries {
		if key.organizationSlug != base.organizationSlug ||
			key.dataSourceSlug != base.dataSourceSlug ||
			key.dataSourceType != base.dataSourceType {
			continue
		}
		entry.retired = true
		if entry.leases == 0 && entry.ready == nil && !entry.closed {
			entry.closed = true
			delete(m.entries, key)
			connectors = append(connectors, entry.connector)
		}
	}
	return connectors
}

func (m *Manager) evictExpiredLocked(now time.Time) []Connector {
	var connectors []Connector
	for key, entry := range m.entries {
		if entry.ready != nil || entry.leases != 0 || entry.closed {
			continue
		}
		idleExpired := now.Sub(entry.lastUsed) >= m.settings.IdleTimeout
		lifetimeExpired := now.Sub(entry.createdAt) >= m.settings.MaxLifetime
		if !idleExpired && !lifetimeExpired {
			continue
		}
		entry.retired = true
		entry.closed = true
		delete(m.entries, key)
		connectors = append(connectors, entry.connector)
	}
	return connectors
}

func (m *Manager) evictLeastRecentlyUsedLocked(
	preferredOrganizationSlug slug.Slug,
) Connector {
	var preferred, global *runtimePoolEntry
	for _, entry := range m.entries {
		if entry.ready != nil || entry.leases != 0 || entry.closed {
			continue
		}
		if global == nil || entry.lastUsed.Before(global.lastUsed) {
			global = entry
		}
		if entry.key.organizationSlug == preferredOrganizationSlug &&
			(preferred == nil || entry.lastUsed.Before(preferred.lastUsed)) {
			preferred = entry
		}
	}
	oldest := preferred
	if oldest == nil {
		oldest = global
	}
	if oldest == nil {
		return nil
	}
	oldest.retired = true
	oldest.closed = true
	delete(m.entries, oldest.key)
	return oldest.connector
}

func (m *Manager) cleanup() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.settings.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-ticker.C:
			m.mu.Lock()
			connectors := m.evictExpiredLocked(now)
			m.mu.Unlock()
			closeConnectors(connectors)
			m.recordSnapshot()
		}
	}
}

func (m *Manager) recordSnapshot() {
	snapshot := m.Snapshot()
	metrics.RecordSQLConnectorPoolSnapshot(metrics.SQLConnectorPoolSnapshot{
		Entries:            snapshot.Entries,
		Retired:            snapshot.Retired,
		ActiveLeases:       snapshot.ActiveLeases,
		OpenConnections:    snapshot.OpenConnections,
		InUseConnections:   snapshot.InUseConnections,
		IdleConnections:    snapshot.IdleConnections,
		MaxOpenConnections: snapshot.MaxOpenConnections,
		WaitCount:          snapshot.WaitCount,
		WaitDuration:       snapshot.WaitDuration,
		MaxIdleClosed:      snapshot.MaxIdleClosed,
		MaxIdleTimeClosed:  snapshot.MaxIdleTimeClosed,
		MaxLifetimeClosed:  snapshot.MaxLifetimeClosed,
	})
}

func closeConnectors(connectors []Connector) {
	for _, conn := range connectors {
		if conn != nil {
			_ = conn.Close()
		}
	}
}
