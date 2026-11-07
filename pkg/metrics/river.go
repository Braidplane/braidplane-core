package metrics

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var (
	riverQueueDepthStates = map[string]struct{}{
		"available": {},
		"pending":   {},
		"retryable": {},
		"running":   {},
		"scheduled": {},
	}
	riverOldestAgeStates = map[string]struct{}{
		"available": {},
		"scheduled": {},
	}
	riverFinalizedStates = map[string]struct{}{
		"cancelled": {},
		"completed": {},
		"discarded": {},
	}
)

const riverQueueHeartbeatFreshness = 15 * time.Minute

type riverQueueAllowlist map[string]struct{}

type riverQueueHealthCollector struct {
	allowedQueues      []string
	allowlist          riverQueueAllowlist
	provider           RiverQueueHealthProvider
	queueDepth         *prometheus.Desc
	oldestJobAge       *prometheus.Desc
	retryTotal         *prometheus.Desc
	discardedTotal     *prometheus.Desc
	finalizedTotal     *prometheus.Desc
	workerLiveness     *prometheus.Desc
	workerHeartbeatAge *prometheus.Desc
	periodicJobLag     *prometheus.Desc
}

type riverPostgresHealthProvider struct {
	pool             *pgxpool.Pool
	allowedQueues    []string
	periodicJobKinds []string
	now              func() time.Time
}

// newRiverQueueHealthCollector builds the Prometheus collector for River queue aggregates.
func newRiverQueueHealthCollector(
	serviceName string,
	environment string,
	config RiverQueueHealthConfig,
) *riverQueueHealthCollector {
	allowlist := newRiverQueueAllowlist(config.AllowedQueues)
	if len(allowlist) == 0 {
		return nil
	}

	constLabels := prometheus.Labels{
		"service_name":           serviceName,
		"deployment_environment": environment,
	}

	return &riverQueueHealthCollector{
		allowedQueues: allowlist.sortedValues(),
		allowlist:     allowlist,
		provider:      config.Provider,
		queueDepth: prometheus.NewDesc(
			"braidplane_river_queue_depth",
			"Current River job depth grouped by queue and state.",
			[]string{"queue_name", "state"},
			constLabels,
		),
		oldestJobAge: prometheus.NewDesc(
			"braidplane_river_job_oldest_age_seconds",
			"Age of the oldest River job grouped by queue and state.",
			[]string{"queue_name", "state"},
			constLabels,
		),
		retryTotal: prometheus.NewDesc(
			"braidplane_river_job_retry_total",
			"Cumulative River job retry observations grouped by queue.",
			[]string{"queue_name"},
			constLabels,
		),
		discardedTotal: prometheus.NewDesc(
			"braidplane_river_job_discarded_total",
			"Cumulative River discarded job observations grouped by queue.",
			[]string{"queue_name"},
			constLabels,
		),
		finalizedTotal: prometheus.NewDesc(
			"braidplane_river_job_finalized_total",
			"Cumulative finalized River job observations grouped by queue and final state.",
			[]string{"queue_name", "state"},
			constLabels,
		),
		workerLiveness: prometheus.NewDesc(
			"braidplane_river_worker_liveness",
			"River worker queue liveness by queue; 1 means at least one worker is registered.",
			[]string{"queue_name"},
			constLabels,
		),
		workerHeartbeatAge: prometheus.NewDesc(
			"braidplane_river_worker_last_heartbeat_age_seconds",
			"Age of the most recent River worker heartbeat grouped by queue.",
			[]string{"queue_name"},
			constLabels,
		),
		periodicJobLag: prometheus.NewDesc(
			"braidplane_river_periodic_job_lag_seconds",
			"Lag for periodic River jobs grouped by queue when periodic work can be identified.",
			[]string{"queue_name"},
			constLabels,
		),
	}
}

// Describe sends River queue health metric descriptors to Prometheus.
func (c *riverQueueHealthCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.queueDepth
	ch <- c.oldestJobAge
	ch <- c.retryTotal
	ch <- c.discardedTotal
	ch <- c.finalizedTotal
	ch <- c.workerLiveness
	ch <- c.workerHeartbeatAge
	ch <- c.periodicJobLag
}

// Collect emits one sanitized River queue health snapshot to Prometheus.
func (c *riverQueueHealthCollector) Collect(ch chan<- prometheus.Metric) {
	snapshot := c.provider(context.Background())

	for _, metric := range snapshot.QueueDepth {
		queue, ok := c.allowlist.queue(metric.Queue)
		state, stateOK := riverState(metric.State, riverQueueDepthStates)
		if !ok || !stateOK || metric.Depth < 0 {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.queueDepth,
			prometheus.GaugeValue,
			float64(metric.Depth),
			queue,
			state,
		)
	}

	for _, metric := range snapshot.OldestJobAge {
		queue, ok := c.allowlist.queue(metric.Queue)
		state, stateOK := riverState(metric.State, riverOldestAgeStates)
		if !ok || !stateOK {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.oldestJobAge,
			prometheus.GaugeValue,
			durationSeconds(metric.Age),
			queue,
			state,
		)
	}

	for _, metric := range snapshot.RetryTotal {
		queue, ok := c.allowlist.queue(metric.Queue)
		if !ok || metric.Count < 0 {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.retryTotal,
			prometheus.CounterValue,
			float64(metric.Count),
			queue,
		)
	}

	for _, metric := range snapshot.DiscardedTotal {
		queue, ok := c.allowlist.queue(metric.Queue)
		if !ok || metric.Count < 0 {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.discardedTotal,
			prometheus.CounterValue,
			float64(metric.Count),
			queue,
		)
	}

	for _, metric := range snapshot.FinalizedTotal {
		queue, ok := c.allowlist.queue(metric.Queue)
		state, stateOK := riverState(metric.State, riverFinalizedStates)
		if !ok || !stateOK || metric.Count < 0 {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.finalizedTotal,
			prometheus.CounterValue,
			float64(metric.Count),
			queue,
			state,
		)
	}

	seenWorkers := make(map[string]struct{}, len(snapshot.Workers))
	for _, metric := range snapshot.Workers {
		queue, ok := c.allowlist.queue(metric.Queue)
		if !ok {
			continue
		}
		seenWorkers[queue] = struct{}{}
		ch <- prometheus.MustNewConstMetric(
			c.workerLiveness,
			prometheus.GaugeValue,
			riverWorkerLiveness(metric),
			queue,
		)
		ch <- prometheus.MustNewConstMetric(
			c.workerHeartbeatAge,
			prometheus.GaugeValue,
			durationSeconds(metric.LastHeartbeatAge),
			queue,
		)
	}
	for _, queue := range c.allowedQueues {
		if _, ok := seenWorkers[queue]; ok {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.workerLiveness,
			prometheus.GaugeValue,
			0,
			queue,
		)
	}

	for _, metric := range snapshot.PeriodicJobLag {
		queue, ok := c.allowlist.queue(metric.Queue)
		if !ok {
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.periodicJobLag,
			prometheus.GaugeValue,
			durationSeconds(metric.Lag),
			queue,
		)
	}
}

// snapshot collects all River health aggregates, returning partial data if one query fails.
func (p riverPostgresHealthProvider) snapshot(ctx context.Context) RiverQueueHealthSnapshot {
	snapshot := RiverQueueHealthSnapshot{}
	if p.pool == nil || len(p.allowedQueues) == 0 {
		return snapshot
	}

	if values, err := p.queryQueueDepth(ctx); err == nil {
		snapshot.QueueDepth = values
	}
	if values, err := p.queryOldestJobAge(ctx); err == nil {
		snapshot.OldestJobAge = values
	}
	if values, err := p.queryRetryTotal(ctx); err == nil {
		snapshot.RetryTotal = values
	}
	if values, err := p.queryDiscardedTotal(ctx); err == nil {
		snapshot.DiscardedTotal = values
	}
	if values, err := p.queryFinalizedTotal(ctx); err == nil {
		snapshot.FinalizedTotal = values
	}
	if values, err := p.queryWorkers(ctx); err == nil {
		snapshot.Workers = values
	}
	if len(p.periodicJobKinds) > 0 {
		if values, err := p.queryPeriodicJobLag(ctx); err == nil {
			snapshot.PeriodicJobLag = values
		}
	}

	return snapshot
}

// queryQueueDepth counts non-finalized River jobs by queue and state.
func (p riverPostgresHealthProvider) queryQueueDepth(ctx context.Context) ([]RiverQueueDepthMetric, error) {
	rows, err := p.pool.Query(ctx, `
SELECT queue, state::text, count(*)::bigint
FROM river_job
WHERE queue = ANY($1::text[])
  AND state::text = ANY($2::text[])
GROUP BY queue, state
`, p.allowedQueues, sortedMapKeys(riverQueueDepthStates))
	if err != nil {
		return nil, xerror.Wrapf("query River queue depth metrics: %w", err)
	}
	defer rows.Close()

	metrics := make([]RiverQueueDepthMetric, 0)
	for rows.Next() {
		var metric RiverQueueDepthMetric
		if err := rows.Scan(&metric.Queue, &metric.State, &metric.Depth); err != nil {
			return nil, xerror.Wrapf("scan River queue depth metric: %w", err)
		}
		metrics = append(metrics, metric)
	}
	if err := rows.Err(); err != nil {
		return nil, xerror.Wrapf("iterate River queue depth metrics: %w", err)
	}

	return metrics, nil
}

// queryOldestJobAge measures the oldest available or scheduled River job age by queue.
func (p riverPostgresHealthProvider) queryOldestJobAge(ctx context.Context) ([]RiverJobAgeMetric, error) {
	rows, err := p.pool.Query(ctx, `
SELECT queue,
       state::text,
       EXTRACT(EPOCH FROM ($2::timestamptz - MIN(scheduled_at)))::double precision
FROM river_job
WHERE queue = ANY($1::text[])
  AND state::text = ANY($3::text[])
GROUP BY queue, state
`, p.allowedQueues, p.now(), sortedMapKeys(riverOldestAgeStates))
	if err != nil {
		return nil, xerror.Wrapf("query River oldest job age metrics: %w", err)
	}
	defer rows.Close()

	metrics := make([]RiverJobAgeMetric, 0)
	for rows.Next() {
		var seconds float64
		var metric RiverJobAgeMetric
		if err := rows.Scan(&metric.Queue, &metric.State, &seconds); err != nil {
			return nil, xerror.Wrapf("scan River oldest job age metric: %w", err)
		}
		metric.Age = secondsToDuration(seconds)
		metrics = append(metrics, metric)
	}
	if err := rows.Err(); err != nil {
		return nil, xerror.Wrapf("iterate River oldest job age metrics: %w", err)
	}

	return metrics, nil
}

// queryRetryTotal counts River job error entries, which represent retry observations.
func (p riverPostgresHealthProvider) queryRetryTotal(ctx context.Context) ([]RiverQueueCountMetric, error) {
	rows, err := p.pool.Query(ctx, `
SELECT queue, COALESCE(SUM(COALESCE(cardinality(errors), 0)), 0)::bigint
FROM river_job
WHERE queue = ANY($1::text[])
GROUP BY queue
`, p.allowedQueues)
	if err != nil {
		return nil, xerror.Wrapf("query River retry metrics: %w", err)
	}
	defer rows.Close()

	metrics := make([]RiverQueueCountMetric, 0)
	for rows.Next() {
		var metric RiverQueueCountMetric
		if err := rows.Scan(&metric.Queue, &metric.Count); err != nil {
			return nil, xerror.Wrapf("scan River retry metric: %w", err)
		}
		metrics = append(metrics, metric)
	}
	if err := rows.Err(); err != nil {
		return nil, xerror.Wrapf("iterate River retry metrics: %w", err)
	}

	return metrics, nil
}

// queryDiscardedTotal counts discarded River jobs by queue.
func (p riverPostgresHealthProvider) queryDiscardedTotal(ctx context.Context) ([]RiverQueueCountMetric, error) {
	rows, err := p.pool.Query(ctx, `
SELECT queue, count(*)::bigint
FROM river_job
WHERE queue = ANY($1::text[])
  AND state = 'discarded'
GROUP BY queue
`, p.allowedQueues)
	if err != nil {
		return nil, xerror.Wrapf("query River discarded job metrics: %w", err)
	}
	defer rows.Close()

	metrics := make([]RiverQueueCountMetric, 0)
	for rows.Next() {
		var metric RiverQueueCountMetric
		if err := rows.Scan(&metric.Queue, &metric.Count); err != nil {
			return nil, xerror.Wrapf("scan River discarded job metric: %w", err)
		}
		metrics = append(metrics, metric)
	}
	if err := rows.Err(); err != nil {
		return nil, xerror.Wrapf("iterate River discarded job metrics: %w", err)
	}

	return metrics, nil
}

// queryFinalizedTotal counts finalized River jobs by queue and final state.
func (p riverPostgresHealthProvider) queryFinalizedTotal(ctx context.Context) ([]RiverJobFinalizedMetric, error) {
	rows, err := p.pool.Query(ctx, `
SELECT queue, state::text, count(*)::bigint
FROM river_job
WHERE queue = ANY($1::text[])
  AND state::text = ANY($2::text[])
GROUP BY queue, state
`, p.allowedQueues, sortedMapKeys(riverFinalizedStates))
	if err != nil {
		return nil, xerror.Wrapf("query River finalized job metrics: %w", err)
	}
	defer rows.Close()

	metrics := make([]RiverJobFinalizedMetric, 0)
	for rows.Next() {
		var metric RiverJobFinalizedMetric
		if err := rows.Scan(&metric.Queue, &metric.State, &metric.Count); err != nil {
			return nil, xerror.Wrapf("scan River finalized job metric: %w", err)
		}
		metrics = append(metrics, metric)
	}
	if err := rows.Err(); err != nil {
		return nil, xerror.Wrapf("iterate River finalized job metrics: %w", err)
	}

	return metrics, nil
}

// queryWorkers reads River queue heartbeats as liveness signals. River OSS
// updates these rows every ten minutes but does not populate river_client_queue.
func (p riverPostgresHealthProvider) queryWorkers(ctx context.Context) ([]RiverWorkerMetric, error) {
	now := p.now()
	rows, err := p.pool.Query(ctx, `
SELECT name,
       CASE WHEN updated_at >= $3::timestamptz THEN 1 ELSE 0 END::bigint,
       1::bigint,
       EXTRACT(EPOCH FROM ($2::timestamptz - updated_at))::double precision
FROM river_queue
WHERE name = ANY($1::text[])
`, p.allowedQueues, now, now.Add(-riverQueueHeartbeatFreshness))
	if err != nil {
		return nil, xerror.Wrapf("query River worker liveness metrics: %w", err)
	}
	defer rows.Close()

	metrics := make([]RiverWorkerMetric, 0)
	for rows.Next() {
		var seconds float64
		var metric RiverWorkerMetric
		if err := rows.Scan(&metric.Queue, &metric.ActiveWorkers, &metric.MaxWorkers, &seconds); err != nil {
			return nil, xerror.Wrapf("scan River worker liveness metric: %w", err)
		}
		metric.LastHeartbeatAge = secondsToDuration(seconds)
		metrics = append(metrics, metric)
	}
	if err := rows.Err(); err != nil {
		return nil, xerror.Wrapf("iterate River worker liveness metrics: %w", err)
	}

	return metrics, nil
}

// queryPeriodicJobLag measures lag for the configured periodic job kinds without exposing kind labels.
func (p riverPostgresHealthProvider) queryPeriodicJobLag(ctx context.Context) ([]RiverPeriodicJobLagMetric, error) {
	rows, err := p.pool.Query(ctx, `
SELECT queue,
       EXTRACT(EPOCH FROM ($3::timestamptz - MIN(scheduled_at)))::double precision
FROM river_job
WHERE queue = ANY($1::text[])
  AND kind = ANY($2::text[])
  AND state::text = ANY($4::text[])
GROUP BY queue
`, p.allowedQueues, p.periodicJobKinds, p.now(), sortedMapKeys(riverQueueDepthStates))
	if err != nil {
		return nil, xerror.Wrapf("query River periodic job lag metrics: %w", err)
	}
	defer rows.Close()

	metrics := make([]RiverPeriodicJobLagMetric, 0)
	for rows.Next() {
		var seconds float64
		var metric RiverPeriodicJobLagMetric
		if err := rows.Scan(&metric.Queue, &seconds); err != nil {
			return nil, xerror.Wrapf("scan River periodic job lag metric: %w", err)
		}
		metric.Lag = secondsToDuration(seconds)
		metrics = append(metrics, metric)
	}
	if err := rows.Err(); err != nil {
		return nil, xerror.Wrapf("iterate River periodic job lag metrics: %w", err)
	}

	return metrics, nil
}

// newRiverQueueAllowlist creates a safe exact-match queue allowlist for River labels.
func newRiverQueueAllowlist(values []string) riverQueueAllowlist {
	allowlist := make(riverQueueAllowlist, len(values))
	for _, value := range values {
		token, ok := safeRiverLabelToken(value)
		if !ok {
			continue
		}
		allowlist[token] = struct{}{}
	}
	return allowlist
}

// queue returns an allowed queue label token and reports whether it is permitted.
func (a riverQueueAllowlist) queue(value string) (string, bool) {
	token, ok := safeRiverLabelToken(value)
	if !ok {
		return "", false
	}
	if _, ok := a[token]; !ok {
		return "", false
	}
	return token, true
}

// sortedValues returns allowlisted queue names in deterministic order.
func (a riverQueueAllowlist) sortedValues() []string {
	values := make([]string, 0, len(a))
	for value := range a {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

// riverSafeTokens returns safe query tokens without using them as labels directly.
func riverSafeTokens(values []string) []string {
	tokens := make([]string, 0, len(values))
	for _, value := range values {
		token, ok := safeRiverLabelToken(value)
		if !ok {
			continue
		}
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	return tokens
}

// riverState returns a safe River state token if it belongs to the expected state set.
func riverState(value string, allowed map[string]struct{}) (string, bool) {
	token, ok := safeRiverLabelToken(value)
	if !ok {
		return "", false
	}
	if _, ok := allowed[token]; !ok {
		return "", false
	}
	return token, true
}

// safeRiverLabelToken normalizes River queue, kind, and state values to bounded tokens.
func safeRiverLabelToken(value string) (string, bool) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "", false
	}
	for _, r := range value {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return "", false
	}
	return value, true
}

// sortedMapKeys returns deterministic string keys for SQL array filters.
func sortedMapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// riverWorkerLiveness converts worker counts to a 0-or-1 liveness signal.
func riverWorkerLiveness(metric RiverWorkerMetric) float64 {
	if metric.ActiveWorkers <= 0 || metric.MaxWorkers <= 0 {
		return 0
	}
	return 1
}

// durationSeconds converts a duration to a non-negative second value.
func durationSeconds(value time.Duration) float64 {
	if value <= 0 {
		return 0
	}
	return value.Seconds()
}

// secondsToDuration converts database seconds to a non-negative duration.
func secondsToDuration(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}
