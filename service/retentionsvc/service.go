package retentionsvc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/retention"
)

const (
	retentionSettingsKey      = "retention"
	auditHistoryRetentionDays = 30
	wishlistRetentionMonths   = 12
	abuseControlRetention     = 24 * time.Hour
)

// DailyCronSchedule is the system-owned retention maintenance cadence.
const DailyCronSchedule = "CRON_TZ=UTC 0 0 * * *"

type Result struct {
	WishlistRegistrations int64
	AbuseControlBuckets   int64
	QueryExecutions       int64
	ReplayCheckpoints     int64
	PipelineRuns          int64
	Events                int64
	SkippedOrgs           []slug.Slug
}

// MaintenanceExecution identifies one private worker job attempt. SourceJobID
// exists only to correlate retries and is never part of tenant-safe models.
type MaintenanceExecution struct {
	SourceJobID int64
	Attempt     int
	StartedAt   time.Time
}

type Service interface {
	PruneExpiredCustomerData(context.Context, MaintenanceExecution) (Result, error)
	ListMaintenanceRuns(context.Context, slug.Slug) ([]model.OrganizationRetentionRun, error)
}

type wishlistRetentionRepository interface {
	DeleteExpired(context.Context, postgres.DBTX, time.Time) (int64, error)
}

type service struct {
	txm          postgres.TransactionManager
	repo         retention.Repository
	wishlistRepo wishlistRetentionRepository
	clk          clock.Clock
}

func New(
	txm postgres.TransactionManager,
	repo retention.Repository,
	wishlistRepo wishlistRetentionRepository,
	clk clock.Clock,
) Service {
	return &service{
		txm:          txm,
		repo:         repo,
		wishlistRepo: wishlistRepo,
		clk:          clk,
	}
}

func PolicyFromSettings(raw []byte) (model.OrganizationRetentionPolicy, error) {
	policy := model.DefaultOrganizationRetentionPolicy()
	if len(raw) == 0 {
		return policy, nil
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(raw, &settings); err != nil {
		return model.OrganizationRetentionPolicy{}, xerror.Unexpected().With(err).Detail("organization settings are invalid")
	}
	value, ok := settings[retentionSettingsKey]
	if !ok {
		return policy, nil
	}
	if err := json.Unmarshal(value, &policy); err != nil {
		return model.OrganizationRetentionPolicy{}, xerror.Unexpected().With(err).Detail("organization retention settings are invalid")
	}
	if err := ValidatePolicy(policy); err != nil {
		return model.OrganizationRetentionPolicy{}, err
	}
	return policy, nil
}

func ValidatePolicy(policy model.OrganizationRetentionPolicy) error {
	for name, days := range map[string]int{
		"query_execution_retention_days":   policy.QueryExecutionRetentionDays,
		"replay_checkpoint_retention_days": policy.ReplayCheckpointRetentionDays,
		"run_history_retention_days":       policy.RunHistoryRetentionDays,
		"event_log_retention_days":         policy.EventLogRetentionDays,
	} {
		if days < 1 || days > 30 {
			return xerror.ValidationFailed().Detail(name + " must be between 1 and 30")
		}
	}
	return nil
}

// NextRunAt returns the next platform-owned daily retention boundary.
func NextRunAt(now time.Time) time.Time {
	utc := now.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
}

func (s *service) ListMaintenanceRuns(
	ctx context.Context,
	org slug.Slug,
) ([]model.OrganizationRetentionRun, error) {
	var runs []model.OrganizationRetentionRun
	if err := s.txm.RunRead(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		runs, err = s.repo.ListRuns(ctx, tx, org)
		return err
	}); err != nil {
		return nil, xerror.Wrapf("list organization retention maintenance runs: %w", err)
	}
	return runs, nil
}

func (s *service) PruneExpiredCustomerData(ctx context.Context, execution MaintenanceExecution) (Result, error) {
	now := s.clk.Now().UTC()
	var result Result
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		deleted, err := s.wishlistRepo.DeleteExpired(
			ctx,
			tx,
			now.AddDate(0, -wishlistRetentionMonths, 0),
		)
		if err != nil {
			return xerror.Wrapf("delete expired wishlist registrations: %w", err)
		}
		result.WishlistRegistrations = deleted
		deleted, err = s.repo.DeleteAbuseControlBucketsBefore(ctx, tx, now.Add(-abuseControlRetention))
		if err != nil {
			return xerror.Wrapf("delete expired abuse-control buckets: %w", err)
		}
		result.AbuseControlBuckets = deleted
		return nil
	}); err != nil {
		return Result{}, err
	}

	var organizations []retention.OrganizationSettings
	if err := s.txm.RunRead(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		organizations, err = s.repo.ListOrganizationSettings(ctx, tx)
		return err
	}); err != nil {
		return Result{}, xerror.Wrapf("list organization retention policies: %w", err)
	}

	var transientErrors []error
	for _, organization := range organizations {
		succeeded, err := s.hasSucceededRunForJob(ctx, organization.Slug, execution.SourceJobID)
		if err != nil {
			transientErrors = append(
				transientErrors,
				xerror.Wrapf("check retention run for organization %s: %w", organization.Slug, err),
			)
			continue
		}
		if succeeded {
			continue
		}

		policy, err := PolicyFromSettings(organization.Settings)
		if err != nil {
			result.SkippedOrgs = append(result.SkippedOrgs, organization.Slug)
			if recordErr := s.recordFailedRun(
				ctx,
				organization.Slug,
				execution,
				nil,
				model.OrganizationRetentionRunFailureCodeInvalidPolicy,
				now,
			); recordErr != nil && !xerror.Is(recordErr, retention.ErrOrganizationNotFound) {
				transientErrors = append(
					transientErrors,
					xerror.Wrapf("record invalid retention policy for organization %s: %w", organization.Slug, recordErr),
				)
			}
			continue
		}

		organizationResult, err := s.pruneOrganization(ctx, organization.Slug, policy, execution, now)
		// An organization can be deleted after the policy list was read.
		if xerror.Is(err, retention.ErrOrganizationNotFound) {
			result.SkippedOrgs = append(result.SkippedOrgs, organization.Slug)
			continue
		}
		if err != nil {
			pruneErr := xerror.Wrapf("prune retention for organization %s: %w", organization.Slug, err)
			if recordErr := s.recordFailedRun(
				ctx,
				organization.Slug,
				execution,
				&policy,
				model.OrganizationRetentionRunFailureCodeMaintenanceFailed,
				now,
			); recordErr != nil && !xerror.Is(recordErr, retention.ErrOrganizationNotFound) {
				pruneErr = xerror.Join(
					pruneErr,
					xerror.Wrapf("record failed retention run for organization %s: %w", organization.Slug, recordErr),
				)
			}
			transientErrors = append(transientErrors, pruneErr)
			continue
		}

		result.QueryExecutions += organizationResult.QueryExecutions
		result.ReplayCheckpoints += organizationResult.ReplayCheckpoints
		result.PipelineRuns += organizationResult.PipelineRuns
		result.Events += organizationResult.Events
	}
	return result, xerror.Join(transientErrors...)
}

func (s *service) hasSucceededRunForJob(ctx context.Context, org slug.Slug, sourceJobID int64) (bool, error) {
	var succeeded bool
	err := s.txm.RunRead(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		succeeded, err = s.repo.HasSucceededRunForJob(ctx, tx, org, sourceJobID)
		return err
	})
	return succeeded, err
}

func (s *service) pruneOrganization(
	ctx context.Context,
	org slug.Slug,
	policy model.OrganizationRetentionPolicy,
	execution MaintenanceExecution,
	now time.Time,
) (Result, error) {
	var result Result
	runSlug, err := newRunSlug()
	if err != nil {
		return Result{}, err
	}
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if _, err := s.repo.DeleteRunsBefore(ctx, tx, org, now.AddDate(0, 0, -auditHistoryRetentionDays)); err != nil {
			return err
		}

		var err error
		deleted, err := s.repo.DeleteQueryExecutionsBefore(ctx, tx, org, now.AddDate(0, 0, -policy.QueryExecutionRetentionDays))
		if err != nil {
			return err
		}
		result.QueryExecutions = deleted
		deleted, err = s.repo.DeleteReplayCheckpointsBefore(ctx, tx, org, now.AddDate(0, 0, -policy.ReplayCheckpointRetentionDays))
		if err != nil {
			return err
		}
		result.ReplayCheckpoints = deleted
		deleted, err = s.repo.DeletePipelineRunsBefore(ctx, tx, org, now.AddDate(0, 0, -policy.RunHistoryRetentionDays))
		if err != nil {
			return err
		}
		result.PipelineRuns = deleted
		if err := s.repo.PruneCompletionDetailsBefore(ctx, tx, org, now.AddDate(0, 0, -policy.RunHistoryRetentionDays)); err != nil {
			return err
		}
		deleted, err = s.repo.DeleteEventsBefore(ctx, tx, org, now.AddDate(0, 0, -policy.EventLogRetentionDays))
		if err != nil {
			return err
		}
		result.Events = deleted

		policySnapshot := policy
		return s.repo.CreateRun(ctx, tx, org, execution.SourceJobID, model.OrganizationRetentionRun{
			Slug:                     runSlug,
			Attempt:                  execution.Attempt,
			Status:                   model.OrganizationRetentionRunStatusSucceeded,
			PolicySnapshot:           &policySnapshot,
			QueryExecutionsDeleted:   result.QueryExecutions,
			ReplayCheckpointsDeleted: result.ReplayCheckpoints,
			PipelineRunsDeleted:      result.PipelineRuns,
			EventsDeleted:            result.Events,
			StartedAt:                execution.StartedAt.UTC(),
			CompletedAt:              now,
		})
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *service) recordFailedRun(
	ctx context.Context,
	org slug.Slug,
	execution MaintenanceExecution,
	policy *model.OrganizationRetentionPolicy,
	failureCode model.OrganizationRetentionRunFailureCode,
	now time.Time,
) error {
	runSlug, err := newRunSlug()
	if err != nil {
		return err
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		// Maintenance diagnostics use a fixed system-owned 30-day history. This
		// avoids making audit durability depend on the data being audited.
		if _, err := s.repo.DeleteRunsBefore(ctx, tx, org, now.AddDate(0, 0, -auditHistoryRetentionDays)); err != nil {
			return err
		}
		return s.repo.CreateRun(ctx, tx, org, execution.SourceJobID, model.OrganizationRetentionRun{
			Slug:           runSlug,
			Attempt:        execution.Attempt,
			Status:         model.OrganizationRetentionRunStatusFailed,
			PolicySnapshot: policy,
			FailureCode:    failureCode,
			StartedAt:      execution.StartedAt.UTC(),
			CompletedAt:    now,
		})
	})
}

func newRunSlug() (slug.Slug, error) {
	return slug.New("retention-run-" + uuid.NewString())
}
