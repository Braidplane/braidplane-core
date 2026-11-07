package memberinvitations

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	Create(ctx context.Context, tx postgres.DBTX, params CreateParams) (*Invitation, error)
	Find(ctx context.Context, tx postgres.DBTX, orgSlug, invitationSlug slug.Slug) (*Invitation, error)
	FindByTokenHash(ctx context.Context, tx postgres.DBTX, tokenHash string) (*Invitation, error)
	FindPendingByEmail(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, email string) (*Invitation, error)
	ListByOrganization(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		req model.ListRequest,
	) ([]*Invitation, error)
	ExpirePendingByOrganization(ctx context.Context, tx postgres.DBTX, orgSlug slug.Slug, expiredAt time.Time) ([]*Invitation, error)
	ListPendingByEmail(ctx context.Context, tx postgres.DBTX, email string, asOf time.Time) ([]*Invitation, error)
	RefreshToken(ctx context.Context, tx postgres.DBTX, params RefreshTokenParams) (*Invitation, error)
	MarkAccepted(ctx context.Context, tx postgres.DBTX, params MarkAcceptedParams) (*Invitation, error)
	MarkDeclined(ctx context.Context, tx postgres.DBTX, params MarkDeclinedParams) (*Invitation, error)
	MarkRevoked(ctx context.Context, tx postgres.DBTX, params MarkRevokedParams) (*Invitation, error)
	MarkExpired(ctx context.Context, tx postgres.DBTX, params MarkExpiredParams) (*Invitation, error)
}
