package wishlistregistrations

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// NewRepository creates the SQLC-backed wishlist registration repository.
func NewRepository() Repository {
	return &repository{}
}

type repository struct{}

// Upsert creates or refreshes a public wishlist registration keyed by normalized email.
func (r *repository) Upsert(
	ctx context.Context,
	tx postgres.DBTX,
	params UpsertParams,
) (*WishlistRegistration, error) {
	row, err := dbgen.New(tx).UpsertWishlistRegistration(ctx, dbgen.UpsertWishlistRegistrationParams{
		WorkEmail:                   params.WorkEmail,
		CareMostAbout:               params.CareMostAbout.String(),
		CommunicationRequestVersion: textParam(params.CommunicationRequestVersion),
		WorkflowToTestFirst:         textParam(params.WorkflowToTestFirst),
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return wishlistRegistrationFromRow(row), nil
}

// DeleteExpired removes registrations whose most recent update has reached the retention cutoff.
func (r *repository) DeleteExpired(
	ctx context.Context,
	tx postgres.DBTX,
	cutoff time.Time,
) (int64, error) {
	deleted, err := dbgen.New(tx).DeleteExpiredWishlistRegistrations(ctx, postgres.ToTimestamptz(cutoff))
	if err != nil {
		return 0, xerror.Unexpected().With(err)
	}
	return deleted, nil
}

// wishlistRegistrationFromRow maps the SQLC wishlist row into the repository model.
func wishlistRegistrationFromRow(row dbgen.WishlistRegistration) *WishlistRegistration {
	return &WishlistRegistration{
		WorkEmail:                   row.WorkEmail,
		CareMostAbout:               wishlistFocus(row.CareMostAbout),
		CommunicationRequestVersion: nullableTextValue(row.CommunicationRequestVersion),
		WorkflowToTestFirst:         nullableTextValue(row.WorkflowToTestFirst),
		CreatedAt:                   row.CreatedAt.Time,
		UpdatedAt:                   row.UpdatedAt.Time,
	}
}

// wishlistFocus parses the stored focus value into the shared domain enum.
func wishlistFocus(raw string) model.WishlistFocus {
	focus, err := model.WishlistFocusString(raw)
	if err != nil {
		slog.Warn("unrecognized stored wishlist focus value", "raw", raw, "error", err)
		return model.WishlistFocusUnknown
	}
	return focus
}

// textParam maps a service string into SQLC's pgx nullable text type.
func textParam(value string) pgtype.Text {
	return pgtype.Text{
		String: value,
		Valid:  true,
	}
}

// nullableTextValue maps nullable SQL text to the model's empty-string optional field convention.
func nullableTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
