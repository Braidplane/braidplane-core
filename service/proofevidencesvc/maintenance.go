package proofevidencesvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

// Maintenance never needs the evidence or credential encryption keys. It runs
// independently of feature exposure so disabling observations cannot stop erasure.
type Maintenance struct {
	txm      postgres.TransactionManager
	repo     *proofobservations.Repository
	audit    eventrecorder.Recorder
	receipts proofreceipts.MaintenanceRepository
}

func NewMaintenance(txm postgres.TransactionManager, repo *proofobservations.Repository, receipts proofreceipts.MaintenanceRepository, audit eventrecorder.Recorder) (*Maintenance, error) {
	if txm == nil || repo == nil || receipts == nil || audit == nil {
		return nil, xerror.Unexpected().Detail("Proof maintenance dependencies are required")
	}
	return &Maintenance{txm: txm, repo: repo, receipts: receipts, audit: audit}, nil
}

func (s *Maintenance) Run(ctx context.Context) error {
	var after proofobservations.MaintenanceScope
	var failures []error
	if err := s.cleanupReceipts(ctx); err != nil {
		failures = append(failures, err)
	}
	for {
		scopes, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]proofobservations.MaintenanceScope, error) {
			return s.repo.ListMaintenanceScopes(ctx, db, after)
		})
		if err != nil {
			return xerror.Join(append(failures, err)...)
		}
		if len(scopes) == 0 {
			return xerror.Join(failures...)
		}
		for _, scope := range scopes {
			if err := ctx.Err(); err != nil {
				return xerror.Join(append(failures, err)...)
			}
			if err := s.Cleanup(ctx, scope.Scope); err != nil && !xerror.Is(err, proofobservations.ErrNotFound) {
				failures = append(failures, err)
			}
			after = scope
		}
	}
}

// Cleanup commits each bounded mutation batch with its audit records. A busy
// environment or interrupted sweep must not repeatedly roll back prior erasure.
func (s *Maintenance) Cleanup(ctx context.Context, scope proofobservations.Scope) error {
	for _, phase := range []struct {
		action model.EventAction
		apply  func(context.Context, postgres.DBTX, int32, int64) ([]proofobservations.JobReference, error)
	}{
		{model.EventActionRevoke, s.repo.CancelObsolete},
		{model.EventActionExpire, s.repo.Expire},
		{model.EventActionPurge, s.repo.PurgeSnapshots},
		{model.EventActionDelete, s.repo.DeleteExpired},
	} {
		for {
			changed, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (bool, error) {
				current, err := s.repo.LockMaintenanceScope(ctx, db, scope)
				if err != nil {
					return false, err
				}
				if current.Disabled {
					if err := s.repo.DisablePolicy(ctx, db, current.OrganizationID, current.ProjectEnvironmentID); err != nil {
						return false, err
					}
				}
				references, err := phase.apply(ctx, db, current.OrganizationID, current.ProjectEnvironmentID)
				if err != nil {
					return false, err
				}
				for _, reference := range references {
					if _, err := s.audit.Record(ctx, scope.Organization, model.SystemEventActor(), JobAudit{Scope: scope, Reference: reference, Action: phase.action}); err != nil {
						return false, xerror.Unavailable(xerror.Event).Detail("Proof retention audit unavailable")
					}
				}
				return len(references) != 0, nil
			})
			if err != nil {
				return err
			}
			if !changed {
				break
			}
		}
	}
	return nil
}

// Health reads only deployment aggregates and bounds the work of each scrape.
func (s *Maintenance) Health(ctx context.Context) (model.ProofMaintenanceHealth, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	health, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (model.ProofMaintenanceHealth, error) {
		return s.repo.MaintenanceHealth(ctx, db)
	})
	if err != nil {
		return model.ProofMaintenanceHealth{}, xerror.Unavailable(xerror.Pipeline).Detail("Proof maintenance health unavailable")
	}
	return health, nil
}

// Activity reads deployment aggregates through the backend, without executor database access.
func (s *Maintenance) Activity(ctx context.Context) (proofevidence.ActivitySnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	activity, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (proofevidence.ActivitySnapshot, error) {
		return s.repo.Activity(ctx, db)
	})
	if err != nil {
		return proofevidence.ActivitySnapshot{}, xerror.Unavailable(xerror.Pipeline).Detail("Proof activity unavailable")
	}
	return activity, nil
}
