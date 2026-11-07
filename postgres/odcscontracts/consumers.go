package odcscontracts

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func (r *repository) ListConsumerSnapshots(ctx context.Context, db postgres.DBTX, org, project slug.Slug) ([]ConsumerSnapshot, error) {
	rows, err := dbgen.New(db).ListODCSConsumerSnapshots(ctx, dbgen.ListODCSConsumerSnapshotsParams{OrgSlug: org, ProjectSlug: project})
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]ConsumerSnapshot, 0, len(rows))
	for _, row := range rows {
		items = append(items, ConsumerSnapshot{Origin: row.Origin, Environment: row.EnvironmentSlug, SpecHash: row.SpecHash, Spec: row.SpecJsonb, Pins: row.ResolvedContracts})
	}
	return items, nil
}
