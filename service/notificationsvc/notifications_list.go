package notificationsvc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
)

// ListNotifications returns a tenant-scoped, display-safe notification page.
func (s *service) ListNotifications(
	ctx context.Context,
	orgSlug slug.Slug,
	filters ListNotificationsFilters,
	req model.ListRequest,
) (model.ListResponse[*model.NotificationListItem], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortLastSeenAt)
	repoFilters := toRepoListRecordFilters(filters)

	var ret []*model.NotificationListItem
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		records, listErr := s.repo.ListRecordsByOrganization(ctx, tx, orgSlug, repoFilters, queryReq)
		if listErr != nil {
			return xerror.Wrapf("list notification records: %w", listErr)
		}

		ret = make([]*model.NotificationListItem, 0, len(records))
		for _, record := range records {
			converted, transformErr := toRecord(record)
			if transformErr != nil {
				return transformErr
			}
			item, itemErr := toNotificationListItem(converted)
			if itemErr != nil {
				return itemErr
			}
			ret = append(ret, item)
		}
		return nil
	})
	if err != nil {
		return model.ListResponse[*model.NotificationListItem]{}, err
	}

	ret, hasMore := model.FinalizePage(ret, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		ret,
		normalized,
		hasMore,
		func(item *model.NotificationListItem, _ string) (value, slugValue string) {
			return item.LastSeenAt.Format(time.RFC3339Nano), item.Slug.String()
		},
	)
	responseHasMore := hasMore
	if normalized.Direction == model.ListDirectionPrev &&
		normalized.Cursor != "" &&
		len(ret) > 0 {
		responseHasMore = true
		last := ret[len(ret)-1]
		nextCursor = model.EncodeCursor(
			last.LastSeenAt.Format(time.RFC3339Nano),
			last.Slug.String(),
		)
		if !hasMore {
			prevCursor = nil
		}
	}

	return model.ListResponse[*model.NotificationListItem]{
		Items:      ret,
		HasMore:    responseHasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// GetNotification returns one tenant-scoped, display-safe notification by slug.
func (s *service) GetNotification(
	ctx context.Context,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
) (*model.NotificationListItem, error) {
	if err := validateNotificationLifecycleActionInput(orgSlug, recordSlug); err != nil {
		return nil, err
	}

	return postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*model.NotificationListItem, error) {
		record, findErr := s.repo.FindRecordBySlug(ctx, db, orgSlug, recordSlug)
		if findErr != nil {
			return nil, xerror.Wrapf("find notification record: %w", findErr)
		}
		converted, transformErr := toRecord(record)
		if transformErr != nil {
			return nil, transformErr
		}
		return toNotificationListItem(converted)
	})
}

// toRepoListRecordFilters maps service filters into the repository query contract.
func toRepoListRecordFilters(filters ListNotificationsFilters) reponotifications.ListRecordFilters {
	includeResolved := filters.IncludeResolved
	if filters.LifecycleState != nil && *filters.LifecycleState == model.NotificationLifecycleStateResolved {
		includeResolved = true
	}
	return reponotifications.ListRecordFilters{
		LifecycleState:   filters.LifecycleState,
		NotificationType: filters.NotificationType,
		Severity:         filters.Severity,
		SourceKind:       filters.SourceKind,
		SourceAction:     filters.SourceAction,
		SourceRef:        filters.SourceRef,
		IncludeResolved:  includeResolved,
	}
}

// toNotificationListItem strips persistence-only fields from a notification record.
func toNotificationListItem(record *model.NotificationRecord) (*model.NotificationListItem, error) {
	if record == nil {
		return nil, xerror.Unexpected().Detail("notification record is required")
	}

	dispatchStatuses, err := notificationDispatchStatuses(record.DispatchSummary)
	if err != nil {
		return nil, err
	}

	return &model.NotificationListItem{
		Slug:             record.Slug,
		NotificationType: record.NotificationType,
		Severity:         record.Severity,
		LifecycleState:   record.LifecycleState,
		Summary:          notificationSummaryFromMap(record.SafeMetadata, record.Slug),
		SourceEventSlug:  record.SourceEventSlug,
		SourceEntityType: record.SourceEntityType,
		SourceAction:     record.SourceAction,
		SourceRefs:       record.SourceRefs,
		SafeMetadata:     record.SafeMetadata,
		OccurrenceCount:  record.OccurrenceCount,
		FirstSeenAt:      record.FirstSeenAt,
		LastSeenAt:       record.LastSeenAt,
		LastOccurrenceAt: record.LastOccurrenceAt,
		DispatchStatuses: dispatchStatuses,
		AcknowledgedAt:   record.AcknowledgedAt,
		ResolvedAt:       record.ResolvedAt,
		CreatedAt:        record.CreatedAt,
		UpdatedAt:        record.UpdatedAt,
	}, nil
}

// notificationSummaryFromMap derives concise display copy from safe metadata.
func notificationSummaryFromMap(metadata map[string]any, recordSlug slug.Slug) string {
	if summary, ok := metadata["summary"].(string); ok && strings.TrimSpace(summary) != "" {
		return strings.TrimSpace(summary)
	}
	return fmt.Sprintf("Open notification %s", recordSlug)
}

// notificationDispatchStatuses converts the persisted dispatch summary map into typed rows.
func notificationDispatchStatuses(summary map[string]any) ([]model.NotificationDispatchSummary, error) {
	ret := make([]model.NotificationDispatchSummary, 0, len(summary))
	for destinationSlug, raw := range summary {
		statusSummary, err := notificationDispatchStatus(destinationSlug, raw)
		if err != nil {
			return nil, err
		}
		ret = append(ret, statusSummary)
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].DestinationSlug < ret[j].DestinationSlug
	})
	return ret, nil
}

// notificationDispatchStatus decodes one safe per-destination dispatch summary.
func notificationDispatchStatus(destinationSlug string, raw any) (model.NotificationDispatchSummary, error) {
	fields, ok := raw.(map[string]any)
	if !ok {
		return model.NotificationDispatchSummary{}, xerror.Unexpected().
			Detail("notification dispatch summary entry must be an object")
	}

	status := model.NotificationDispatchStatusUnknown
	if rawStatus, ok := fields["status"].(string); ok {
		parsed, err := model.NotificationDispatchStatusString(rawStatus)
		if err == nil {
			status = parsed
		}
	}

	queuedAt, hasQueuedAt, err := optionalTimeFromSummary(fields["queued_at"])
	if err != nil {
		return model.NotificationDispatchSummary{}, err
	}
	var queuedAtPtr *time.Time
	if hasQueuedAt {
		queuedAtPtr = &queuedAt
	}

	return model.NotificationDispatchSummary{
		DestinationSlug: slug.FromString(destinationSlug),
		Status:          status,
		QueuedAt:        queuedAtPtr,
	}, nil
}

// optionalStringFromSummary extracts a non-empty optional string from JSONB summary data.
func optionalStringFromSummary(raw any) *string {
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	value = strings.TrimSpace(value)
	return &value
}

// optionalTimeFromSummary parses an optional RFC3339 timestamp from JSONB summary data.
func optionalTimeFromSummary(raw any) (time.Time, bool, error) {
	value := optionalStringFromSummary(raw)
	if value == nil {
		return time.Time{}, false, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return time.Time{}, false, xerror.Wrapf(
			"decode notification dispatch summary queued_at: %w",
			xerror.Unexpected().With(err),
		)
	}
	return parsed, true, nil
}
