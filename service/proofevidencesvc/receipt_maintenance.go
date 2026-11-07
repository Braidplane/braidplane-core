package proofevidencesvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

func (s *Maintenance) cleanupReceipts(ctx context.Context) error {
	var after int32
	var failures []error
	for {
		organizations, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) ([]proofreceipts.MaintenanceOrganization, error) {
			return s.receipts.ListMaintenanceOrganizations(ctx, db, after)
		})
		if err != nil {
			return xerror.Join(append(failures, err)...)
		}
		if len(organizations) == 0 {
			return xerror.Join(failures...)
		}
		for _, org := range organizations {
			if err := ctx.Err(); err != nil {
				return xerror.Join(append(failures, err)...)
			}
			if err := s.CleanupReceipts(ctx, org.Slug); err != nil {
				failures = append(failures, err)
			}
			after = org.ID
		}
	}
}

// CleanupReceipts shares the compiler's tenant lock. Each batch either erases
// content and records replay keys together, or leaves both unchanged.
func (s *Maintenance) CleanupReceipts(ctx context.Context, org slug.Slug) error {
	for {
		changed, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (bool, error) {
			if err := s.receipts.LockTarget(ctx, db, "proof-retention:"+org.String()); err != nil {
				return false, err
			}
			receipts, err := s.receipts.DeleteExpired(ctx, db, org)
			if err != nil {
				return false, err
			}
			ids := make([]int64, 0, len(receipts))
			for _, receipt := range receipts {
				ids = append(ids, receipt.EvidenceID)
				if _, err := s.audit.Record(ctx, org, model.SystemEventActor(), receiptDeletionAudit{org: org, slug: receipt.Slug, key: receipt.Key}); err != nil {
					return false, xerror.Unavailable(xerror.Event).Detail("Proof receipt retention audit unavailable")
				}
			}
			if len(ids) != 0 {
				if err := s.receipts.DeleteUnusedEvidence(ctx, db, org, ids); err != nil {
					return false, err
				}
			}
			return len(receipts) != 0, nil
		})
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
	}
}

type receiptDeletionAudit struct {
	org  slug.Slug
	slug slug.Slug
	key  string
}

func (e receiptDeletionAudit) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{SlugPrefix: "evt-proof-retention", EventClass: model.EventClassSecurityAudit, EntityType: model.EventEntityTypeOrganization, EntitySlug: e.org, Action: model.EventActionDelete, SchemaVersion: model.CurrentEventSchemaVersion, Metadata: map[string]any{"proof_receipt_slug": e.slug.String(), "proof_receipt_key": e.key}}, nil
}
