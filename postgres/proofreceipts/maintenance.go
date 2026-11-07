package proofreceipts

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type MaintenanceOrganization struct {
	ID   int32
	Slug slug.Slug
}

type DeletedReceipt struct {
	Slug       slug.Slug
	Key        string
	EvidenceID int64
}

type MaintenanceRepository interface {
	LockTarget(context.Context, postgres.DBTX, string) error
	ListMaintenanceOrganizations(context.Context, postgres.DBTX, int32) ([]MaintenanceOrganization, error)
	DeleteExpired(context.Context, postgres.DBTX, slug.Slug) ([]DeletedReceipt, error)
	DeleteUnusedEvidence(context.Context, postgres.DBTX, slug.Slug, []int64) error
}

func (*repository) ListMaintenanceOrganizations(ctx context.Context, db postgres.DBTX, after int32) ([]MaintenanceOrganization, error) {
	rows, err := dbgen.New(db).ListProofReceiptMaintenanceOrganizations(ctx, after)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]MaintenanceOrganization, 0, len(rows))
	for _, row := range rows {
		result = append(result, MaintenanceOrganization{ID: row.OrganizationID, Slug: row.Slug})
	}
	return result, nil
}

func (*repository) DeleteExpired(ctx context.Context, db postgres.DBTX, org slug.Slug) ([]DeletedReceipt, error) {
	rows, err := dbgen.New(db).DeleteExpiredProofReceipts(ctx, org)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	result := make([]DeletedReceipt, 0, len(rows))
	for _, row := range rows {
		result = append(result, DeletedReceipt{Slug: row.Slug, Key: row.ReceiptKey, EvidenceID: row.EvidenceID})
	}
	return result, nil
}

func (*repository) DeleteUnusedEvidence(ctx context.Context, db postgres.DBTX, org slug.Slug, ids []int64) error {
	if err := dbgen.New(db).DeleteUnusedProofEvidence(ctx, dbgen.DeleteUnusedProofEvidenceParams{OrgSlug: org, EvidenceIds: ids}); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}
