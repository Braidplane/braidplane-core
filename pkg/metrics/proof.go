package metrics

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/prometheus/client_golang/prometheus"
)

type proofMaintenanceCollector struct {
	provider                          func(context.Context) (model.ProofMaintenanceHealth, error)
	available, success, failures, due *prometheus.Desc
}

// RegisterProofMaintenanceHealth exposes fixed-cardinality deployment health.
func (m *Meter) RegisterProofMaintenanceHealth(provider func(context.Context) (model.ProofMaintenanceHealth, error)) {
	if m == nil || !m.enabled || provider == nil {
		return
	}
	labels := prometheus.Labels{"service_name": m.serviceName, "deployment_environment": m.environment}
	mustRegisterCollector(m.registry, &proofMaintenanceCollector{
		provider:  provider,
		available: prometheus.NewDesc("braidplane_proof_maintenance_health_available", "Whether the current Proof maintenance database probe succeeded.", nil, labels),
		success:   prometheus.NewDesc("braidplane_proof_maintenance_last_success_timestamp_seconds", "Database timestamp of the most recent completed Proof sweep; zero if none is retained.", nil, labels),
		failures:  prometheus.NewDesc("braidplane_proof_maintenance_recent_failures", "Discarded Proof sweeps finalized in the last ten minutes.", nil, labels),
		due:       prometheus.NewDesc("braidplane_proof_maintenance_due_jobs", "Jobs currently eligible for each Proof cleanup phase.", []string{"phase"}, labels),
	})
}

func (c *proofMaintenanceCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.available
	ch <- c.success
	ch <- c.failures
	ch <- c.due
}

func (c *proofMaintenanceCollector) Collect(ch chan<- prometheus.Metric) {
	health, err := c.provider(context.Background())
	if err != nil {
		// A failed probe must not publish healthy zeros or retain stale measurements.
		ch <- prometheus.MustNewConstMetric(c.available, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.available, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.success, prometheus.GaugeValue, health.LastSuccessTimestamp)
	ch <- prometheus.MustNewConstMetric(c.failures, prometheus.GaugeValue, float64(health.RecentFailures))
	for _, phase := range []struct {
		name  string
		count int64
	}{
		{"cancellation", health.CancellationDue},
		{"expiry", health.ExpiryDue},
		{"evidence", health.EvidenceDue},
		{"metadata", health.MetadataDue},
	} {
		ch <- prometheus.MustNewConstMetric(c.due, prometheus.GaugeValue, float64(phase.count), phase.name)
	}
}

func RecordProofControl(action model.EventAction, outcome model.ProofControlOutcome) {
	currentProcessMeter().RecordProofControl(action, outcome)
}

func (m *Meter) RecordProofControl(action model.EventAction, outcome model.ProofControlOutcome) {
	if m == nil || !m.enabled {
		return
	}
	var label string
	switch action {
	case model.EventActionCreate:
		label = "preview"
	case model.EventActionAccept:
		label = "confirm"
	case model.EventActionUpdate:
		label = "policy"
	case model.EventActionRevoke:
		label = "cancel"
	case model.EventActionRead:
		label = "read"
	case model.EventActionExecute:
		label = "compile"
	case model.EventActionDecline:
		label = "access"
	default:
		label = "unknown"
	}
	if !outcome.IsAProofControlOutcome() {
		outcome = model.ProofControlOutcomeUnknown
	}
	m.proofControlTotal.WithLabelValues(label, outcome.String()).Inc()
}

type proofActivityCollector struct {
	provider                   func(context.Context) (proofevidence.ActivitySnapshot, error)
	available, starts, results *prometheus.Desc
}

func (m *Meter) RegisterProofActivity(provider func(context.Context) (proofevidence.ActivitySnapshot, error)) {
	if m == nil || !m.enabled || provider == nil {
		return
	}
	labels := prometheus.Labels{"service_name": m.serviceName, "deployment_environment": m.environment}
	mustRegisterCollector(m.registry, &proofActivityCollector{
		provider:  provider,
		available: prometheus.NewDesc("braidplane_proof_activity_available", "Whether the current Proof activity database probe succeeded.", nil, labels),
		starts:    prometheus.NewDesc("braidplane_proof_recent_starts", "Jobs started in the last ten minutes; execution admission does not prove provider dispatch.", nil, labels),
		results:   prometheus.NewDesc("braidplane_proof_recent_results", "Retained terminal job outcomes completed in the last ten minutes.", []string{"outcome"}, labels),
	})
}

func (c *proofActivityCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.available
	ch <- c.starts
	ch <- c.results
}

func (c *proofActivityCollector) Collect(ch chan<- prometheus.Metric) {
	activity, err := c.provider(context.Background())
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.available, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.available, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.starts, prometheus.GaugeValue, float64(activity.Starts))
	// Iterating the closed vocabulary prevents stored/provider labels from escaping.
	for _, outcome := range proofevidence.OutcomeValues() {
		ch <- prometheus.MustNewConstMetric(c.results, prometheus.GaugeValue, float64(activity.Results[outcome]), outcome.String())
	}
}
