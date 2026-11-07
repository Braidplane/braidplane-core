package http

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

const (
	securityAuditExportPageSize    = 100
	securityAuditExportRecordLimit = 10_000
	securityAuditExportPageLimit   = securityAuditExportRecordLimit/securityAuditExportPageSize + 1
)

var securityAuditExportHeader = []string{
	"schema_version",
	"organization_slug",
	"event_slug",
	"occurred_at",
	"recorded_at",
	"actor_type",
	"actor_slug",
	"entity_type",
	"entity_slug",
	"action",
	"request_id",
	"metadata_json",
}

// ListEvents returns durable organization activity records.
func (s *server) ListEvents(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListEventsParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceEvents); err != nil {
		return err
	}

	filters, err := toEventFilters(params)
	if err != nil {
		return err
	}

	response, err := s.eventsService.ListEvents(
		c.Context(),
		orgSlug,
		filters,
		toEventsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	events := make([]api.EventRecord, len(response.Items))
	for i, item := range response.Items {
		events[i] = toAPIEventRecord(item)
	}

	return c.JSON(api.EventListResponse{
		Events:     events,
		HasMore:    response.HasMore,
		NextCursor: response.NextCursor,
		PrevCursor: response.PrevCursor,
	})
}

// ListSecurityAuditEvents returns retained organization security audit records.
func (s *server) ListSecurityAuditEvents(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListSecurityAuditEventsParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceEvents); err != nil {
		return err
	}

	filters, err := toSecurityAuditEventFilters(securityAuditListFilterParams(params))
	if err != nil {
		return err
	}

	response, err := s.eventsService.ListEvents(
		c.Context(),
		orgSlug,
		filters,
		toSecurityAuditEventsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	events := make([]api.EventRecord, len(response.Items))
	for i, item := range response.Items {
		events[i] = toAPIEventRecord(item)
	}

	retentionPolicy, err := s.orgService.GetRetentionPolicy(c.Context(), orgSlug)
	if err != nil {
		return mapServiceError(err)
	}
	retentionDays := retentionPolicy.EventLogRetentionDays

	return c.JSON(api.EventListResponse{
		Events:                events,
		HasMore:               response.HasMore,
		NextCursor:            response.NextCursor,
		PrevCursor:            response.PrevCursor,
		EventLogRetentionDays: &retentionDays,
	})
}

// ExportSecurityAuditEvents exports matching retained security audit records as deterministic CSV.
func (s *server) ExportSecurityAuditEvents(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ExportSecurityAuditEventsParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceEvents); err != nil {
		return err
	}

	filters, err := toSecurityAuditEventFilters(securityAuditExportFilterParams(params))
	if err != nil {
		return err
	}

	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(securityAuditExportHeader); err != nil {
		return apierror.InternalServerError()
	}

	cursor := ""
	exportedRecords := 0
	for page := 0; ; page++ {
		if page >= securityAuditExportPageLimit {
			return apierror.InternalServerError()
		}
		response, listErr := s.eventsService.ListEvents(
			c.Context(),
			orgSlug,
			filters,
			model.ListRequest{
				Limit:     securityAuditExportPageSize,
				Cursor:    cursor,
				SortBy:    model.ListSortOccurredAt,
				Order:     model.ListOrderAsc,
				Direction: model.ListDirectionNext,
			},
		)
		if listErr != nil {
			return mapServiceError(listErr)
		}

		for _, event := range response.Items {
			if exportedRecords >= securityAuditExportRecordLimit {
				return securityAuditExportLimitExceeded()
			}
			row, rowErr := securityAuditEventCSVRow(orgSlug, event)
			if rowErr != nil {
				return apierror.InternalServerError()
			}
			if err := writer.Write(row); err != nil {
				return apierror.InternalServerError()
			}
			exportedRecords++
		}

		if !response.HasMore {
			break
		}
		if exportedRecords >= securityAuditExportRecordLimit {
			return securityAuditExportLimitExceeded()
		}
		if response.NextCursor == nil || *response.NextCursor == "" || *response.NextCursor == cursor {
			return apierror.InternalServerError()
		}
		cursor = *response.NextCursor
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return apierror.InternalServerError()
	}

	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(
		fiber.HeaderContentDisposition,
		fmt.Sprintf(`attachment; filename="braidplane-security-audit-events-%s.csv"`, orgSlug),
	)
	return c.Send(buffer.Bytes())
}

func securityAuditExportLimitExceeded() error {
	return apierror.ValidationFailed(
		"security audit export exceeds 10000 records; narrow occurred_from and occurred_before",
	)
}

// ListAlertPolicySourceMetadata returns source selector options for alert policy forms.
func (s *server) ListAlertPolicySourceMetadata(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListAlertPolicySourceMetadataParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceAlertPolicies); err != nil {
		return err
	}

	filters, err := toSourceMetadataFilters(params)
	if err != nil {
		return err
	}

	response, err := s.eventsService.ListSourceMetadata(c.Context(), orgSlug, filters)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIAlertPolicySourceMetadataResponse(response))
}

func toEventFilters(params api.ListEventsParams) (eventssvc.ListEventsFilters, error) {
	filters := eventssvc.ListEventsFilters{}

	if params.ActorType != nil {
		actorType, err := model.EventActorTypeString(string(*params.ActorType))
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid actor type: %v", err))
		}
		filters.ActorType = &actorType
	}

	if params.ActorSlug != nil {
		actorSlug, err := slug.New(*params.ActorSlug)
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid actor slug: %v", err))
		}
		filters.ActorSlug = &actorSlug
	}

	if params.EntityType != nil {
		entityType, err := model.EventEntityTypeString(string(*params.EntityType))
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid entity type: %v", err))
		}
		filters.EntityType = &entityType
	}

	if params.EntitySlug != nil {
		entitySlug, err := slug.New(*params.EntitySlug)
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid entity slug: %v", err))
		}
		filters.EntitySlug = &entitySlug
	}

	if params.Action != nil {
		action, err := model.EventActionString(string(*params.Action))
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid event action: %v", err))
		}
		filters.Action = &action
	}

	return filters, nil
}

type securityAuditEventFilterParams struct {
	ActorType      *api.EventActorType
	ActorSlug      *string
	EntityType     *api.EventEntityType
	EntitySlug     *string
	Action         *api.EventAction
	RequestID      *string
	OccurredFrom   *time.Time
	OccurredBefore *time.Time
}

func securityAuditListFilterParams(params api.ListSecurityAuditEventsParams) securityAuditEventFilterParams {
	return securityAuditEventFilterParams{
		ActorType:      params.ActorType,
		ActorSlug:      params.ActorSlug,
		EntityType:     params.EntityType,
		EntitySlug:     params.EntitySlug,
		Action:         params.Action,
		RequestID:      params.RequestId,
		OccurredFrom:   params.OccurredFrom,
		OccurredBefore: params.OccurredBefore,
	}
}

func securityAuditExportFilterParams(params api.ExportSecurityAuditEventsParams) securityAuditEventFilterParams {
	return securityAuditEventFilterParams{
		ActorType:      params.ActorType,
		ActorSlug:      params.ActorSlug,
		EntityType:     params.EntityType,
		EntitySlug:     params.EntitySlug,
		Action:         params.Action,
		RequestID:      params.RequestId,
		OccurredFrom:   params.OccurredFrom,
		OccurredBefore: params.OccurredBefore,
	}
}

func toSecurityAuditEventFilters(params securityAuditEventFilterParams) (eventssvc.ListEventsFilters, error) {
	eventClass := model.EventClassSecurityAudit
	filters := eventssvc.ListEventsFilters{
		EventClass:     &eventClass,
		RequestID:      params.RequestID,
		OccurredFrom:   params.OccurredFrom,
		OccurredBefore: params.OccurredBefore,
	}

	if filters.OccurredFrom != nil && filters.OccurredBefore != nil &&
		!filters.OccurredFrom.Before(*filters.OccurredBefore) {
		return filters, apierror.ValidationFailed("occurred_from must be before occurred_before")
	}

	if params.ActorType != nil {
		actorType, err := model.EventActorTypeString(string(*params.ActorType))
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid actor type: %v", err))
		}
		filters.ActorType = &actorType
	}
	if params.ActorSlug != nil {
		actorSlug, err := slug.New(*params.ActorSlug)
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid actor slug: %v", err))
		}
		filters.ActorSlug = &actorSlug
	}
	if params.EntityType != nil {
		entityType, err := model.EventEntityTypeString(string(*params.EntityType))
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid entity type: %v", err))
		}
		filters.EntityType = &entityType
	}
	if params.EntitySlug != nil {
		entitySlug, err := slug.New(*params.EntitySlug)
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid entity slug: %v", err))
		}
		filters.EntitySlug = &entitySlug
	}
	if params.Action != nil {
		action, err := model.EventActionString(string(*params.Action))
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid event action: %v", err))
		}
		filters.Action = &action
	}

	return filters, nil
}

// toSourceMetadataFilters validates metadata query filters before service lookup.
func toSourceMetadataFilters(params api.ListAlertPolicySourceMetadataParams) (eventssvc.SourceMetadataFilters, error) {
	filters := eventssvc.SourceMetadataFilters{
		Scope: model.AlertPolicyScopeOrganization,
	}

	if params.Scope != nil {
		scope, err := alertPolicyScope(*params.Scope)
		if err != nil {
			return filters, err
		}
		filters.Scope = scope
	}

	if params.ProjectSlug != nil {
		projectSlug, err := slug.New(*params.ProjectSlug)
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid project slug: %v", err))
		}
		filters.ProjectSlug = &projectSlug
	}

	if params.EnvironmentSlug != nil {
		environmentSlug, err := slug.New(*params.EnvironmentSlug)
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid environment slug: %v", err))
		}
		filters.EnvironmentSlug = &environmentSlug
	}

	if params.SourceKind != nil {
		sourceKind, err := model.EventEntityTypeString(string(*params.SourceKind))
		if err != nil || sourceKind == model.EventEntityTypeUnknown {
			return filters, apierror.ValidationFailed("Invalid alert policy source kind")
		}
		if !model.AlertPolicySupportsEventSource(sourceKind, nil) {
			return filters, apierror.ValidationFailed("Invalid alert policy source kind")
		}
		filters.SourceKind = &sourceKind
	}

	if params.SourceAction != nil {
		sourceAction, err := model.EventActionString(string(*params.SourceAction))
		if err != nil || sourceAction == model.EventActionUnknown {
			return filters, apierror.ValidationFailed("Invalid alert policy source action")
		}
		if filters.SourceKind == nil || !model.AlertPolicySupportsEventSource(*filters.SourceKind, &sourceAction) {
			return filters, apierror.ValidationFailed("Invalid alert policy source action")
		}
		filters.SourceAction = &sourceAction
	}

	return filters, nil
}

func toAPIEventRecord(event *model.Event) api.EventRecord {
	return api.EventRecord{
		SchemaVersion: int32(event.SchemaVersion), // #nosec G115 -- persistence stores this value as int32.
		EventClass:    api.EventClass(event.EventClass.String()),
		Slug:          event.Slug.String(),
		ActorType:     api.EventActorType(event.ActorType.String()),
		ActorSlug:     toAPINullableSlug(event.ActorSlug),
		EntityType:    api.EventEntityType(event.EntityType.String()),
		EntitySlug:    toAPINullableSlug(event.EntitySlug),
		Action:        api.EventAction(event.Action.String()),
		RequestId:     event.RequestID,
		OccurredAt:    event.OccurredAt,
		Metadata:      event.Metadata,
		CreatedAt:     event.CreatedAt,
	}
}

func securityAuditEventCSVRow(orgSlug slug.Slug, event *model.Event) ([]string, error) {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return nil, err
	}

	row := []string{
		strconv.Itoa(event.SchemaVersion),
		orgSlug.String(),
		event.Slug.String(),
		event.OccurredAt.UTC().Format(time.RFC3339Nano),
		event.CreatedAt.UTC().Format(time.RFC3339Nano),
		event.ActorType.String(),
		optionalSlugString(event.ActorSlug),
		event.EntityType.String(),
		optionalSlugString(event.EntitySlug),
		event.Action.String(),
		optionalStringValue(event.RequestID),
		string(metadata),
	}
	for i := range row {
		row[i] = spreadsheetSafeCSVCell(row[i])
	}
	return row, nil
}

func spreadsheetSafeCSVCell(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	default:
		return value
	}
}

func optionalSlugString(value *slug.Slug) string {
	if value == nil {
		return ""
	}
	return value.String()
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// toAPIAlertPolicySourceMetadataResponse maps service metadata into the public API shape.
func toAPIAlertPolicySourceMetadataResponse(response model.EventSourceMetadataResponse) api.AlertPolicySourceMetadataResponse {
	notificationTypes := make([]api.NotificationType, 0, len(response.NotificationTypes))
	for _, kind := range response.NotificationTypes {
		notificationTypes = append(notificationTypes, api.NotificationType(kind.String()))
	}
	sources := make([]api.AlertPolicySourceMetadata, 0, len(response.Sources))
	for _, source := range response.Sources {
		actions := make([]api.EventAction, 0, len(source.SourceActions))
		for _, action := range source.SourceActions {
			actions = append(actions, api.EventAction(action.String()))
		}
		sources = append(sources, api.AlertPolicySourceMetadata{
			SourceKind:    api.EventEntityType(source.SourceKind.String()),
			SourceActions: actions,
		})
	}

	sourceRefs := make([]api.AlertPolicySourceRefOption, 0, len(response.SourceRefs))
	for _, option := range response.SourceRefs {
		sourceRefs = append(sourceRefs, api.AlertPolicySourceRefOption{
			Value: option.Value.String(),
			Label: option.Label,
		})
	}

	return api.AlertPolicySourceMetadataResponse{
		NotificationTypes: notificationTypes,
		Sources:           sources,
		SourceRefs:        sourceRefs,
	}
}
