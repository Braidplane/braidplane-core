package http

import (
	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
)

const (
	organizationSortByCreatedAt = "organization_created_at"
	organizationSortByUpdatedAt = "organization_updated_at"
	organizationSortByName      = "organization_name"

	memberSortByCreatedAt = "member_created_at"
	memberSortByUpdatedAt = "member_updated_at"
	memberSortByEmail     = "member_email"

	memberInvitationSortByCreatedAt = "invitation_created_at"
	memberInvitationSortByUpdatedAt = "invitation_updated_at"
	memberInvitationSortByEmail     = "invitation_email"
	memberInvitationSortByExpiresAt = "invitation_expires_at"

	roleSortByCreatedAt = "role_created_at"
	roleSortByUpdatedAt = "role_updated_at"
	roleSortByName      = "role_name"

	apiKeySortByCreatedAt = "api_key_created_at"
	apiKeySortByUpdatedAt = "api_key_updated_at"
	apiKeySortByName      = "api_key_name"

	personalAccessTokenSortByCreatedAt = "personal_access_token_created_at"
	personalAccessTokenSortByUpdatedAt = "personal_access_token_updated_at"
	personalAccessTokenSortByName      = "personal_access_token_name"

	dataSourceSortByCreatedAt = "data_source_created_at"
	dataSourceSortByUpdatedAt = "data_source_updated_at"
	dataSourceSortByName      = "data_source_name"

	querySortByCreatedAt = "query_created_at"
	querySortByUpdatedAt = "query_updated_at"
	querySortByName      = "query_name"

	projectSortByCreatedAt = "project_created_at"
	projectSortByUpdatedAt = "project_updated_at"
	projectSortByName      = "project_name"

	environmentSortByCreatedAt = "environment_created_at"
	environmentSortByUpdatedAt = "environment_updated_at"
	environmentSortByName      = "environment_name"

	queryExecutionSortByExecutedAt = "query_execution_executed_at"

	runSortByCreatedAt = "run_created_at"

	eventSortByOccurredAt = "event_occurred_at"

	notificationSortByLastSeenAt = "notification_last_seen_at"

	alertDestinationSortByCreatedAt = "alert_destination_created_at"

	alertPolicySortByCreatedAt = "alert_policy_created_at"

	alertDeliveryAttemptSortByCreatedAt = "alert_delivery_attempt_created_at"
)

var (
	organizationSortByMap = map[string]string{
		organizationSortByCreatedAt: model.ListSortCreatedAt,
		organizationSortByUpdatedAt: model.ListSortUpdatedAt,
		organizationSortByName:      model.ListSortName,
	}

	memberSortByMap = map[string]string{
		memberSortByCreatedAt: model.ListSortCreatedAt,
		memberSortByUpdatedAt: model.ListSortUpdatedAt,
		memberSortByEmail:     model.ListSortEmail,
	}

	memberInvitationSortByMap = map[string]string{
		memberInvitationSortByCreatedAt: model.ListSortCreatedAt,
		memberInvitationSortByUpdatedAt: model.ListSortUpdatedAt,
		memberInvitationSortByEmail:     model.ListSortEmail,
		memberInvitationSortByExpiresAt: model.ListSortExpiresAt,
	}

	roleSortByMap = map[string]string{
		roleSortByCreatedAt: model.ListSortCreatedAt,
		roleSortByUpdatedAt: model.ListSortUpdatedAt,
		roleSortByName:      model.ListSortName,
	}

	apiKeySortByMap = map[string]string{
		apiKeySortByCreatedAt: model.ListSortCreatedAt,
		apiKeySortByUpdatedAt: model.ListSortUpdatedAt,
		apiKeySortByName:      model.ListSortName,
	}

	personalAccessTokenSortByMap = map[string]string{
		personalAccessTokenSortByCreatedAt: model.ListSortCreatedAt,
		personalAccessTokenSortByUpdatedAt: model.ListSortUpdatedAt,
		personalAccessTokenSortByName:      model.ListSortName,
	}

	dataSourceSortByMap = map[string]string{
		dataSourceSortByCreatedAt: model.ListSortCreatedAt,
		dataSourceSortByUpdatedAt: model.ListSortUpdatedAt,
		dataSourceSortByName:      model.ListSortName,
	}

	querySortByMap = map[string]string{
		querySortByCreatedAt: model.ListSortCreatedAt,
		querySortByUpdatedAt: model.ListSortUpdatedAt,
		querySortByName:      model.ListSortName,
	}

	projectSortByMap = map[string]string{
		projectSortByCreatedAt: model.ListSortCreatedAt,
		projectSortByUpdatedAt: model.ListSortUpdatedAt,
		projectSortByName:      model.ListSortName,
	}

	environmentSortByMap = map[string]string{
		environmentSortByCreatedAt: model.ListSortCreatedAt,
		environmentSortByUpdatedAt: model.ListSortUpdatedAt,
		environmentSortByName:      model.ListSortName,
	}

	queryExecutionSortByMap = map[string]string{
		queryExecutionSortByExecutedAt: model.ListSortExecutedAt,
	}

	pipelineRunSortByMap = map[string]string{
		runSortByCreatedAt: model.ListSortCreatedAt,
	}

	eventSortByMap = map[string]string{
		eventSortByOccurredAt: model.ListSortOccurredAt,
	}

	notificationSortByMap = map[string]string{
		notificationSortByLastSeenAt: model.ListSortLastSeenAt,
	}

	alertDestinationSortByMap = map[string]string{
		alertDestinationSortByCreatedAt: model.ListSortCreatedAt,
	}

	alertPolicySortByMap = map[string]string{
		alertPolicySortByCreatedAt: model.ListSortCreatedAt,
	}

	alertDeliveryAttemptSortByMap = map[string]string{
		alertDeliveryAttemptSortByCreatedAt: model.ListSortCreatedAt,
	}
)

func toOrganizationListRequest(params api.ListOrganizationsParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		organizationSortByMap,
	)
}

func toMembersListRequest(params api.ListMembersParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		memberSortByMap,
	)
}

func toMemberInvitationsListRequest(params api.ListMemberInvitationsParams) model.ListRequest {
	req := toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		memberInvitationSortByMap,
	)
	if params.Status != nil {
		req.Status = string(*params.Status)
	}
	return req
}

func toMyMemberInvitationsListRequest(params api.ListMyMemberInvitationsParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		memberInvitationSortByMap,
	)
}

func toRolesListRequest(params api.ListRolesParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		roleSortByMap,
	)
}

func toAPIKeysListRequest(params api.ListAPIKeysParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		apiKeySortByMap,
	)
}

func toPersonalAccessTokensListRequest(params api.ListPersonalAccessTokensParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		personalAccessTokenSortByMap,
	)
}

func toDataSourcesListRequest(params api.ListDataSourcesParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		dataSourceSortByMap,
	)
}

func toQueriesListRequest(params api.ListQueriesParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		querySortByMap,
	)
}

func toProjectsListRequest(params api.ListProjectsParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		projectSortByMap,
	)
}

func toEnvironmentsListRequest(params api.ListEnvironmentsParams) model.ListEnvironmentsRequest {
	return model.ListEnvironmentsRequest{
		ListRequest: toListRequest(
			int(ptr.Value(params.Limit)),
			ptr.Value(params.Cursor),
			string(ptr.Value(params.Order)),
			string(ptr.Value(params.SortBy)),
			string(ptr.Value(params.Direction)),
			environmentSortByMap,
		),
		IncludeDeleted: ptr.Value(params.IncludeDeleted),
	}
}

func toProjectEnvironmentsListRequest(params api.ListProjectEnvironmentsParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		environmentSortByMap,
	)
}

func toEnvironmentRuntimeVariablesListRequest(params api.ListEnvironmentRuntimeVariablesParams) model.ListRequest {
	return toEnvironmentValueListRequest(params.Limit, params.Cursor, params.Order, params.Direction)
}

func toEnvironmentSecretsListRequest(params api.ListEnvironmentSecretsParams) model.ListRequest {
	return toEnvironmentValueListRequest(params.Limit, params.Cursor, params.Order, params.Direction)
}

func toEnvironmentValueListRequest[O, D ~string](
	limit *api.LimitParam,
	cursor *api.CursorParam,
	order *O,
	direction *D,
) model.ListRequest {
	return model.ListRequest{
		Limit:     int(ptr.Value(limit)),
		Cursor:    ptr.Value(cursor),
		Order:     string(ptr.Value(order)),
		SortBy:    model.ListSortUpdatedAt,
		Direction: string(ptr.Value(direction)),
	}
}

func toQueryExecutionsListRequest(params api.ListQueryExecutionsParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		queryExecutionSortByMap,
	)
}

func toPipelineRunsListRequest(params api.ListPipelineRunsParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		pipelineRunSortByMap,
	)
}

func toEventsListRequest(params api.ListEventsParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		eventSortByMap,
	)
}

func toSecurityAuditEventsListRequest(params api.ListSecurityAuditEventsParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		eventSortByMap,
	)
}

func toNotificationsListRequest(params api.ListNotificationsParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		notificationSortByMap,
	)
}

func toAlertDestinationsListRequest(params api.ListAlertDestinationsParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		alertDestinationSortByMap,
	)
}

func toAlertPoliciesListRequest(params api.ListAlertPoliciesParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		alertPolicySortByMap,
	)
}

func toAlertDeliveryAttemptsListRequest(params api.ListNotificationDeliveryAttemptsParams) model.ListRequest {
	return toListRequest(
		int(ptr.Value(params.Limit)),
		ptr.Value(params.Cursor),
		string(ptr.Value(params.Order)),
		string(ptr.Value(params.SortBy)),
		string(ptr.Value(params.Direction)),
		alertDeliveryAttemptSortByMap,
	)
}

func toPipelinesListRequest(params api.ListPipelinesParams) model.ListRequest {
	return model.ListRequest{
		Limit:     int(ptr.Value(params.Limit)),
		Cursor:    ptr.Value(params.Cursor),
		Direction: string(ptr.Value(params.Direction)),
	}
}

func toListRequest(
	limit int,
	cursor string,
	order string,
	sortBy string,
	direction string,
	sortByMap map[string]string,
) model.ListRequest {
	return model.ListRequest{
		Limit:     limit,
		Cursor:    cursor,
		Order:     order,
		SortBy:    sortByMap[sortBy],
		Direction: direction,
	}
}
