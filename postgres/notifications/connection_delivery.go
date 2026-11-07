package notifications

import (
	"context"
	"database/sql"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func (*repository) ClaimConnectionDelivery(ctx context.Context, db postgres.DBTX, params ClaimConnectionDeliveryParams) (ConnectionDeliveryClaim, error) {
	row, err := dbgen.New(db).ClaimConnectionDelivery(ctx, dbgen.ClaimConnectionDeliveryParams{
		OrgSlug: params.OrgSlug, RecordSlug: params.RecordSlug, DestinationSlug: params.DestinationSlug,
		AlertPolicySlug: params.AlertPolicySlug, AttemptSlug: params.AttemptSlug,
		Now: postgres.ToTimestamptz(params.Now), LeaseUntil: postgres.ToTimestamptz(params.LeaseUntil),
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return ConnectionDeliveryClaim{}, ErrDeliveryNotFound.With(err)
		}
		return ConnectionDeliveryClaim{}, xerror.Wrapf("claim notification delivery: %w", err)
	}
	status, err := model.AlertDeliveryAttemptStatusString(row.Status)
	if err != nil {
		return ConnectionDeliveryClaim{}, ErrDeliveryInvalidInput.With(err)
	}
	return ConnectionDeliveryClaim{Slug: row.Slug, ProviderAttempt: int(row.ProviderAttempt), Status: status, JobState: row.JobState, LeaseUntil: postgres.FromNullableTimestamptz(row.DeliveryLeaseUntil), RetryAfterAt: postgres.FromNullableTimestamptz(row.RetryAfterAt), Acquired: row.Acquired}, nil
}

func (*repository) FinishConnectionDelivery(ctx context.Context, db postgres.DBTX, params FinishConnectionDeliveryParams) error {
	attempt, err := providerAttemptInt32(params.ProviderAttempt)
	if err != nil {
		return err
	}
	count, err := dbgen.New(db).FinishConnectionDelivery(ctx, dbgen.FinishConnectionDeliveryParams{
		OrgSlug: params.OrgSlug, AttemptSlug: params.AttemptSlug, ProviderAttempt: attempt,
		Status: params.Status.String(), JobState: params.JobState, Now: postgres.ToTimestamptz(params.Now),
		RetryAfterAt:  postgres.ToNullableTimestamptz(params.RetryAfterAt),
		ErrorCategory: postgres.ToNullableText(params.ErrorCategory), ErrorMessage: postgres.ToNullableText(params.ErrorMessage),
		ProviderResponseMetadata: normalizedRawJSON(params.ProviderResponseMetadata),
	})
	if err != nil {
		return xerror.Wrapf("finish notification delivery: %w", err)
	}
	if count != 1 {
		return ErrDeliveryNotFound
	}
	return nil
}
