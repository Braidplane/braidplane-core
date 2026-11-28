package pipelinesinkeffects

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// CreateFTPArtifactIntent joins the already claimed effect in the caller's
// transaction. False means the claim, scope, or preallocated identity refused it.
func CreateFTPArtifactIntent(ctx context.Context, tx postgres.DBTX, intent dbgen.CreateFTPArtifactIntentParams) (bool, error) {
	rows, err := dbgen.New(tx).CreateFTPArtifactIntent(ctx, intent)
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return rows == 1, nil
}

func FindFTPArtifactIntent(ctx context.Context, db postgres.DBTX, org slug.Slug, environmentID int64, deliveryID uuid.UUID) (dbgen.PipelineArtifactDelivery, error) {
	return dbgen.New(db).FindFTPArtifactIntent(ctx, dbgen.FindFTPArtifactIntentParams{
		DeliveryID:           pgtype.UUID{Bytes: deliveryID.UUID, Valid: true},
		OrgSlug:              org.String(),
		ProjectEnvironmentID: environmentID,
	})
}

// ConfirmFTPArtifactDelivery updates the original effect and its receipt
// together. Call only after validating the successful private broker response.
func ConfirmFTPArtifactDelivery(ctx context.Context, tx postgres.DBTX, org slug.Slug, environmentID int64, deliveryID, publishID uuid.UUID) (bool, error) {
	ok, err := dbgen.New(tx).ConfirmFTPArtifactDelivery(ctx, dbgen.ConfirmFTPArtifactDeliveryParams{
		DeliveryID:           pgtype.UUID{Bytes: deliveryID.UUID, Valid: true},
		RequestID:            pgtype.UUID{Bytes: publishID.UUID, Valid: true},
		OrgSlug:              org.String(),
		ProjectEnvironmentID: environmentID,
	})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return ok, nil
}
