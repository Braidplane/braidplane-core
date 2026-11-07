package eventssvc

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
)

const alertPolicySourceRefOptionLimit = 100

func (s *service) CreateEvent(
	ctx context.Context,
	params CreateEventParams,
) (*model.Event, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.Event, error) {
		return s.createEvent(ctx, tx, params)
	})
}

func (s *service) createEvent(
	ctx context.Context,
	tx postgres.DBTX,
	params CreateEventParams,
) (*model.Event, error) {
	params, err := PrepareCreateEventParams(ctx, params)
	if err != nil {
		return nil, err
	}
	metadata, err := normalizeMetadata(params.EventClass, params.Metadata)
	if err != nil {
		return nil, err
	}

	occurredAt := params.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = s.clk.Now().UTC()
	}

	event, err := s.repo.Create(ctx, tx, params.OrgSlug, &repoevents.CreateParams{
		Slug:          params.Slug,
		EventClass:    params.EventClass,
		ActorType:     params.ActorType,
		ActorSlug:     params.ActorSlug,
		EntityType:    params.EntityType,
		EntitySlug:    params.EntitySlug,
		Action:        params.Action,
		OccurredAt:    occurredAt,
		RequestID:     params.RequestID,
		SchemaVersion: int32(params.SchemaVersion), // #nosec G115 -- bounded by PrepareCreateEventParams.
		Metadata:      metadata,
	})
	if err != nil {
		return nil, xerror.Wrapf("create event: %w", err)
	}

	ret, err := toEvent(event)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// PrepareCreateEventParams applies the canonical event defaults and validates
// the complete persistence payload before a direct insert or transactional handoff.
func PrepareCreateEventParams(
	ctx context.Context,
	params CreateEventParams,
) (CreateEventParams, error) {
	params = normalizeCreateEventParams(ctx, params)
	if err := validateCreateEventInput(params); err != nil {
		return CreateEventParams{}, err
	}
	if _, err := normalizeMetadata(params.EventClass, params.Metadata); err != nil {
		return CreateEventParams{}, err
	}
	return params, nil
}

func normalizeCreateEventParams(ctx context.Context, params CreateEventParams) CreateEventParams {
	if params.EventClass == model.EventClassUnknown {
		params.EventClass = model.EventClassActivity
	}
	if params.SchemaVersion == 0 {
		params.SchemaVersion = model.CurrentEventSchemaVersion
	}
	if params.RequestID == nil {
		params.RequestID = reqctx.GetRequestID(ctx)
	}
	if params.RequestID != nil {
		requestID := strings.TrimSpace(*params.RequestID)
		if requestID == "" {
			params.RequestID = nil
		} else {
			params.RequestID = &requestID
		}
	}
	return params
}

func (s *service) GetEvent(
	ctx context.Context,
	orgSlug, eventSlug slug.Slug,
) (*model.Event, error) {
	var ret *model.Event
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		event, findErr := s.repo.FindBySlug(ctx, tx, orgSlug, eventSlug)
		if findErr != nil {
			return xerror.Wrapf("get event: %w", findErr)
		}

		var transformErr error
		ret, transformErr = toEvent(event)
		return transformErr
	})
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *service) ListEvents(
	ctx context.Context,
	orgSlug slug.Slug,
	filters ListEventsFilters,
	req model.ListRequest,
) (model.ListResponse[*model.Event], error) {
	if err := validateListEventsFilters(filters); err != nil {
		return model.ListResponse[*model.Event]{}, err
	}
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortOccurredAt)
	repoFilters := toRepoFilters(filters)

	var ret []*model.Event
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		events, listErr := s.repo.ListByOrganization(ctx, tx, orgSlug, repoFilters, queryReq)
		if listErr != nil {
			return xerror.Wrapf("list events: %w", listErr)
		}

		ret = make([]*model.Event, 0, len(events))
		for _, event := range events {
			converted, transformErr := toEvent(event)
			if transformErr != nil {
				return transformErr
			}
			ret = append(ret, converted)
		}

		return nil
	})
	if err != nil {
		return model.ListResponse[*model.Event]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item *model.Event, _ string) (value, slugValue string) {
			return item.OccurredAt.Format("2006-01-02T15:04:05.999999999Z07:00"), item.Slug.String()
		},
	)

	return model.ListResponse[*model.Event]{
		Items:      ret,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// ListSourceMetadata returns alert-policy source choices and scoped source refs.
func (s *service) ListSourceMetadata(
	ctx context.Context,
	orgSlug slug.Slug,
	filters SourceMetadataFilters,
) (model.EventSourceMetadataResponse, error) {
	filters, err := normalizeSourceMetadataFilters(filters)
	if err != nil {
		return model.EventSourceMetadataResponse{}, err
	}

	ret := model.EventSourceMetadataResponse{
		NotificationTypes: model.ProducerBackedNotificationTypes(),
		Sources:           model.AlertPolicyEventSourceMetadata(),
	}
	if filters.SourceKind == nil || !sourceMetadataCanListSourceRefs(filters) {
		return ret, nil
	}

	err = s.txm.RunRead(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		sourceRefs, listErr := s.repo.ListSourceRefsByOrganization(
			ctx,
			tx,
			orgSlug,
			repoevents.SourceMetadataFilters{
				SourceKind:      filters.SourceKind,
				SourceAction:    filters.SourceAction,
				ProjectSlug:     filters.ProjectSlug,
				EnvironmentSlug: filters.EnvironmentSlug,
			},
			alertPolicySourceRefOptionLimit,
		)
		if listErr != nil {
			return xerror.Wrapf("list source metadata: %w", listErr)
		}

		ret.SourceRefs = sourceRefs
		return nil
	})
	if err != nil {
		return model.EventSourceMetadataResponse{}, err
	}

	return ret, nil
}

func toRepoFilters(filters ListEventsFilters) *repoevents.ListFilters {
	return &repoevents.ListFilters{
		EventClass:     filters.EventClass,
		RequestID:      filters.RequestID,
		OccurredFrom:   filters.OccurredFrom,
		OccurredBefore: filters.OccurredBefore,
		ActorType:      filters.ActorType,
		ActorSlug:      filters.ActorSlug,
		EntityType:     filters.EntityType,
		EntitySlug:     filters.EntitySlug,
		Action:         filters.Action,
	}
}

// normalizeSourceMetadataFilters validates selector dependencies before querying events.
func normalizeSourceMetadataFilters(filters SourceMetadataFilters) (SourceMetadataFilters, error) {
	if filters.Scope == model.AlertPolicyScopeUnknown {
		filters.Scope = model.AlertPolicyScopeOrganization
	}
	switch filters.Scope {
	case model.AlertPolicyScopeOrganization:
		filters.ProjectSlug = nil
		filters.EnvironmentSlug = nil
	case model.AlertPolicyScopeProject:
		filters.EnvironmentSlug = nil
	case model.AlertPolicyScopeProjectEnvironment:
	default:
		return filters, ErrInvalidInput.Detail("alert policy source metadata scope is invalid")
	}
	if filters.SourceAction != nil && filters.SourceKind == nil {
		return filters, ErrInvalidInput.Detail("alert policy source_kind is required when source_action is provided")
	}
	if filters.SourceKind != nil && !model.AlertPolicySupportsEventSource(*filters.SourceKind, nil) {
		return filters, ErrInvalidInput.Detail("alert policy source_kind is not supported")
	}
	if filters.SourceKind != nil && filters.SourceAction != nil && !model.AlertPolicySupportsEventSource(*filters.SourceKind, filters.SourceAction) {
		return filters, ErrInvalidInput.Detail("alert policy source_action is not supported for source_kind")
	}
	return filters, nil
}

// sourceMetadataCanListSourceRefs prevents incomplete scope filters from widening source-ref lookup.
func sourceMetadataCanListSourceRefs(filters SourceMetadataFilters) bool {
	switch filters.Scope {
	case model.AlertPolicyScopeOrganization:
		return true
	case model.AlertPolicyScopeProject:
		return filters.ProjectSlug != nil && *filters.ProjectSlug != ""
	case model.AlertPolicyScopeProjectEnvironment:
		return filters.ProjectSlug != nil && *filters.ProjectSlug != "" &&
			filters.EnvironmentSlug != nil && *filters.EnvironmentSlug != ""
	default:
		return false
	}
}
