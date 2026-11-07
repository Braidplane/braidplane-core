package events

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type Repository interface {
	Create(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		params *CreateParams,
	) (*Event, error)
	FindBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, eventSlug slug.Slug,
	) (*Event, error)
	ListByOrganization(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		filters *ListFilters,
		req model.ListRequest,
	) ([]*Event, error)
	ListSourceRefsByOrganization(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		filters SourceMetadataFilters,
		limit int,
	) ([]model.EventSourceRefOption, error)
	PruneOlderThan(
		ctx context.Context,
		tx postgres.DBTX,
		cutoff time.Time,
	) (int64, error)
}

type repository struct{}

func New() Repository {
	return &repository{}
}

func (r *repository) Create(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	params *CreateParams,
) (*Event, error) {
	queries := dbgen.New(tx)
	row, err := queries.CreateEvent(ctx, dbgen.CreateEventParams{
		OrgSlug:       postgres.SlugToText(orgSlug),
		Slug:          postgres.SlugToText(params.Slug),
		EventClass:    postgres.ToText(params.EventClass.String()),
		ActorType:     postgres.ToText(params.ActorType.String()),
		ActorSlug:     postgres.OptionalSlugToText(params.ActorSlug),
		EntityType:    postgres.ToText(params.EntityType.String()),
		EntitySlug:    postgres.OptionalSlugToText(params.EntitySlug),
		Action:        postgres.ToText(params.Action.String()),
		OccurredAt:    postgres.ToTimestamptz(params.OccurredAt),
		RequestID:     postgres.ToNullableText(params.RequestID),
		SchemaVersion: pgtype.Int4{Int32: params.SchemaVersion, Valid: true},
		Metadata:      params.Metadata,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound.With(err)
		}
		if postgres.IsUniqueViolation(err, "uq_events_org_slug") {
			return nil, ErrSlugTaken.With(err)
		}
		return nil, err
	}

	return toEvent(row), nil
}

func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, eventSlug slug.Slug,
) (*Event, error) {
	queries := dbgen.New(tx)
	row, err := queries.FindEventBySlug(ctx, dbgen.FindEventBySlugParams{
		OrgSlug: orgSlug,
		Slug:    eventSlug,
	})
	if err != nil {
		if xerror.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound.With(err)
		}
		return nil, err
	}

	return toEvent(row), nil
}

func (r *repository) ListByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	filters *ListFilters,
	req model.ListRequest,
) ([]*Event, error) {
	queries := dbgen.New(tx)

	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(req.Cursor, model.ListSortOccurredAt)
	if err != nil {
		return nil, err
	}

	var actorType *string
	var eventClass *string
	var requestID *string
	var occurredFrom *time.Time
	var occurredBefore *time.Time
	var actorSlug *slug.Slug
	var entityType *string
	var entitySlug *slug.Slug
	var action *string
	if filters != nil {
		requestID = filters.RequestID
		occurredFrom = filters.OccurredFrom
		occurredBefore = filters.OccurredBefore
		if filters.EventClass != nil {
			value := filters.EventClass.String()
			eventClass = &value
		}
		if filters.ActorType != nil {
			value := filters.ActorType.String()
			actorType = &value
		}
		actorSlug = filters.ActorSlug
		if filters.EntityType != nil {
			value := filters.EntityType.String()
			entityType = &value
		}
		entitySlug = filters.EntitySlug
		if filters.Action != nil {
			value := filters.Action.String()
			action = &value
		}
	}

	rows, err := queries.ListEventsByOrganization(ctx, dbgen.ListEventsByOrganizationParams{
		OrgSlug:         orgSlug,
		EventClass:      postgres.ToNullableText(eventClass),
		RequestID:       postgres.ToNullableText(requestID),
		OccurredFrom:    postgres.ToNullableTimestamptz(occurredFrom),
		OccurredBefore:  postgres.ToNullableTimestamptz(occurredBefore),
		ActorType:       postgres.ToNullableText(actorType),
		ActorSlug:       postgres.OptionalSlugToText(actorSlug),
		EntityType:      postgres.ToNullableText(entityType),
		EntitySlug:      postgres.OptionalSlugToText(entitySlug),
		Action:          postgres.ToNullableText(action),
		CursorSlug:      cursorSlug,
		CursorTimestamp: cursorTimestamp,
		OrderDir:        postgres.ToText(req.Order),
		LimitCount:      int64(req.Limit),
	})
	if err != nil {
		return nil, err
	}

	events := make([]*Event, len(rows))
	for i, row := range rows {
		events[i] = toEvent(row)
	}

	return events, nil
}

func (r *repository) ListSourceRefsByOrganization(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	filters SourceMetadataFilters,
	limit int,
) ([]model.EventSourceRefOption, error) {
	var sourceKind *string
	if filters.SourceKind != nil {
		value := filters.SourceKind.String()
		sourceKind = &value
	}
	var sourceAction *string
	if filters.SourceAction != nil {
		value := filters.SourceAction.String()
		sourceAction = &value
	}

	rows, err := dbgen.New(tx).ListEventSourceRefsByOrganization(
		ctx,
		dbgen.ListEventSourceRefsByOrganizationParams{
			OrgSlug:         orgSlug,
			SourceKind:      postgres.ToNullableText(sourceKind),
			SourceAction:    postgres.ToNullableText(sourceAction),
			ProjectSlug:     postgres.OptionalSlugToText(filters.ProjectSlug),
			EnvironmentSlug: postgres.OptionalSlugToText(filters.EnvironmentSlug),
			LimitCount:      int64(limit),
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("list event source refs: %w", err)
	}

	ret := make([]model.EventSourceRefOption, 0, len(rows))
	for _, row := range rows {
		value, parseErr := slug.New(row.SourceRef)
		if parseErr != nil {
			continue
		}
		label := row.SourceLabel
		if label == "" {
			label = value.String()
		}
		ret = append(ret, model.EventSourceRefOption{Value: value, Label: label})
	}
	return ret, nil
}

func (r *repository) PruneOlderThan(
	ctx context.Context,
	tx postgres.DBTX,
	cutoff time.Time,
) (int64, error) {
	queries := dbgen.New(tx)
	return queries.DeleteEventsOccurredBefore(ctx, postgres.ToTimestamptz(cutoff))
}
