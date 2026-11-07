package eventssvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
)

type Service interface {
	CreateEvent(ctx context.Context, params CreateEventParams) (*model.Event, error)
	GetEvent(ctx context.Context, orgSlug, eventSlug slug.Slug) (*model.Event, error)
	ListEvents(
		ctx context.Context,
		orgSlug slug.Slug,
		filters ListEventsFilters,
		req model.ListRequest,
	) (model.ListResponse[*model.Event], error)
	ListSourceMetadata(
		ctx context.Context,
		orgSlug slug.Slug,
		filters SourceMetadataFilters,
	) (model.EventSourceMetadataResponse, error)
	PruneExpiredEvents(ctx context.Context, retention time.Duration) (int64, error)
}

type service struct {
	txm  postgres.TransactionManager
	repo repoevents.Repository
	clk  clock.Clock
}

func New(
	txm postgres.TransactionManager,
	repo repoevents.Repository,
	clk clock.Clock,
) Service {
	if clk == nil {
		clk = clock.New()
	}

	return &service{
		txm:  txm,
		repo: repo,
		clk:  clk,
	}
}
