package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// OrganizationRetentionPolicy defines the customer-data windows owned by an
// organization. Project and pipeline overrides are deliberately unsupported.
type OrganizationRetentionPolicy struct {
	QueryExecutionRetentionDays   int `json:"query_execution_retention_days"`
	ReplayCheckpointRetentionDays int `json:"replay_checkpoint_retention_days"`
	RunHistoryRetentionDays       int `json:"run_history_retention_days"`
	EventLogRetentionDays         int `json:"event_log_retention_days"`
}

func DefaultOrganizationRetentionPolicy() OrganizationRetentionPolicy {
	return OrganizationRetentionPolicy{
		QueryExecutionRetentionDays:   1,
		ReplayCheckpointRetentionDays: 7,
		RunHistoryRetentionDays:       30,
		EventLogRetentionDays:         30,
	}
}

type OrganizationRetentionRunStatus string

const (
	OrganizationRetentionRunStatusSucceeded OrganizationRetentionRunStatus = "succeeded"
	OrganizationRetentionRunStatusFailed    OrganizationRetentionRunStatus = "failed"
)

type OrganizationRetentionRunFailureCode string

const (
	OrganizationRetentionRunFailureCodeInvalidPolicy     OrganizationRetentionRunFailureCode = "invalid_policy"
	OrganizationRetentionRunFailureCodeMaintenanceFailed OrganizationRetentionRunFailureCode = "maintenance_failed"
)

// OrganizationRetentionRun is the sanitized tenant-safe record of one terminal
// maintenance attempt. Worker job identifiers and dependency errors deliberately
// stay outside this model.
type OrganizationRetentionRun struct {
	Slug                     slug.Slug
	Attempt                  int
	Status                   OrganizationRetentionRunStatus
	PolicySnapshot           *OrganizationRetentionPolicy
	QueryExecutionsDeleted   int64
	ReplayCheckpointsDeleted int64
	PipelineRunsDeleted      int64
	EventsDeleted            int64
	FailureCode              OrganizationRetentionRunFailureCode
	StartedAt                time.Time
	CompletedAt              time.Time
}
