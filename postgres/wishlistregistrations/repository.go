package wishlistregistrations

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	Upsert(ctx context.Context, tx postgres.DBTX, params UpsertParams) (*WishlistRegistration, error)
	DeleteExpired(ctx context.Context, tx postgres.DBTX, cutoff time.Time) (int64, error)
}
