package http

import (
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

func TestListParams_ToOrganizationListRequest(t *testing.T) {
	t.Parallel()

	t.Run("MapsKnownSortBy", func(t *testing.T) {
		t.Parallel()

		limit := api.LimitParam(17)
		cursor := api.CursorParam("org-cursor")
		sortBy := api.ListOrganizationsParamsSortByOrganizationName
		order := api.ListOrganizationsParamsOrderAsc
		direction := api.ListOrganizationsParamsDirectionPrev

		req := toOrganizationListRequest(api.ListOrganizationsParams{
			Limit:     &limit,
			Cursor:    &cursor,
			SortBy:    &sortBy,
			Order:     &order,
			Direction: &direction,
		})

		require.Equal(
			t,
			expectedListRequest(17, "org-cursor", model.ListSortName, "asc", "prev"),
			req,
		)
	})

	t.Run("UnknownSortByFallsBackToEmpty", func(t *testing.T) {
		t.Parallel()

		sortBy := api.ListOrganizationsParamsSortBy("unknown")
		req := toOrganizationListRequest(api.ListOrganizationsParams{SortBy: &sortBy})
		require.Empty(t, req.SortBy)
	})
}

func TestListParams_ToMembersListRequest(t *testing.T) {
	t.Parallel()

	limit := api.LimitParam(21)
	cursor := api.CursorParam("members-cursor")
	sortBy := api.ListMembersParamsSortByMemberEmail
	order := api.ListMembersParamsOrderDesc
	direction := api.ListMembersParamsDirectionNext

	req := toMembersListRequest(api.ListMembersParams{
		Limit:     &limit,
		Cursor:    &cursor,
		SortBy:    &sortBy,
		Order:     &order,
		Direction: &direction,
	})

	require.Equal(
		t,
		expectedListRequest(21, "members-cursor", model.ListSortEmail, "desc", "next"),
		req,
	)
}

func TestListParams_ToRolesListRequest(t *testing.T) {
	t.Parallel()

	limit := api.LimitParam(22)
	cursor := api.CursorParam("roles-cursor")
	sortBy := api.ListRolesParamsSortByRoleUpdatedAt
	order := api.ListRolesParamsOrder(api.OrderParamAsc)
	direction := api.ListRolesParamsDirection(api.PageDirectionParamNext)

	req := toRolesListRequest(api.ListRolesParams{
		Limit:     &limit,
		Cursor:    &cursor,
		SortBy:    &sortBy,
		Order:     &order,
		Direction: &direction,
	})

	require.Equal(
		t,
		expectedListRequest(22, "roles-cursor", model.ListSortUpdatedAt, "asc", "next"),
		req,
	)
}

func TestListParams_ToDataSourcesListRequest(t *testing.T) {
	t.Parallel()

	limit := api.LimitParam(23)
	cursor := api.CursorParam("datasources-cursor")
	sortBy := api.ListDataSourcesParamsSortByDataSourceCreatedAt
	order := api.ListDataSourcesParamsOrderDesc
	direction := api.ListDataSourcesParamsDirectionPrev

	req := toDataSourcesListRequest(api.ListDataSourcesParams{
		Limit:     &limit,
		Cursor:    &cursor,
		SortBy:    &sortBy,
		Order:     &order,
		Direction: &direction,
	})

	require.Equal(
		t,
		expectedListRequest(23, "datasources-cursor", model.ListSortCreatedAt, "desc", "prev"),
		req,
	)
}

func TestListParams_ToQueriesListRequest(t *testing.T) {
	t.Parallel()

	limit := api.LimitParam(24)
	cursor := api.CursorParam("queries-cursor")
	sortBy := api.ListQueriesParamsSortByQueryName
	order := api.ListQueriesParamsOrderAsc
	direction := api.ListQueriesParamsDirectionPrev

	req := toQueriesListRequest(api.ListQueriesParams{
		Limit:     &limit,
		Cursor:    &cursor,
		SortBy:    &sortBy,
		Order:     &order,
		Direction: &direction,
	})

	require.Equal(
		t,
		expectedListRequest(24, "queries-cursor", model.ListSortName, "asc", "prev"),
		req,
	)
}

func TestListParams_ToQueryExecutionsListRequest(t *testing.T) {
	t.Parallel()

	t.Run("MapsKnownSortBy", func(t *testing.T) {
		t.Parallel()

		limit := api.LimitParam(27)
		cursor := api.CursorParam("executions-cursor")
		sortBy := api.ListQueryExecutionsParamsSortByQueryExecutionExecutedAt
		order := api.ListQueryExecutionsParamsOrderDesc
		direction := api.ListQueryExecutionsParamsDirectionPrev

		req := toQueryExecutionsListRequest(api.ListQueryExecutionsParams{
			Limit:     &limit,
			Cursor:    &cursor,
			SortBy:    &sortBy,
			Order:     &order,
			Direction: &direction,
		})

		require.Equal(
			t,
			expectedListRequest(27, "executions-cursor", model.ListSortExecutedAt, "desc", "prev"),
			req,
		)
	})

	t.Run("UnknownSortByFallsBackToEmpty", func(t *testing.T) {
		t.Parallel()

		sortBy := api.ListQueryExecutionsParamsSortBy("unknown")
		req := toQueryExecutionsListRequest(api.ListQueryExecutionsParams{SortBy: &sortBy})
		require.Empty(t, req.SortBy)
	})
}

func TestListParams_ToPipelineRunsListRequest(t *testing.T) {
	t.Parallel()

	t.Run("MapsKnownSortBy", func(t *testing.T) {
		t.Parallel()

		limit := api.LimitParam(28)
		cursor := api.CursorParam("runs-cursor")
		sortBy := api.ListPipelineRunsParamsSortByRunCreatedAt
		order := api.ListPipelineRunsParamsOrder(api.OrderParamAsc)
		direction := api.ListPipelineRunsParamsDirection(api.PageDirectionParamNext)

		req := toPipelineRunsListRequest(api.ListPipelineRunsParams{
			Limit:     &limit,
			Cursor:    &cursor,
			SortBy:    &sortBy,
			Order:     &order,
			Direction: &direction,
		})

		require.Equal(
			t,
			expectedListRequest(28, "runs-cursor", model.ListSortCreatedAt, "asc", "next"),
			req,
		)
	})

	t.Run("UnknownSortByFallsBackToEmpty", func(t *testing.T) {
		t.Parallel()

		sortBy := api.ListPipelineRunsParamsSortBy("unknown")
		req := toPipelineRunsListRequest(api.ListPipelineRunsParams{SortBy: &sortBy})
		require.Empty(t, req.SortBy)
	})
}

func TestListParams_ToEventsListRequest(t *testing.T) {
	t.Parallel()

	t.Run("MapsKnownSortBy", func(t *testing.T) {
		t.Parallel()

		limit := api.LimitParam(30)
		cursor := api.CursorParam("events-cursor")
		sortBy := api.ListEventsParamsSortByEventOccurredAt
		order := api.ListEventsParamsOrder(api.OrderParamDesc)
		direction := api.ListEventsParamsDirection(api.PageDirectionParamPrev)

		req := toEventsListRequest(api.ListEventsParams{
			Limit:     &limit,
			Cursor:    &cursor,
			SortBy:    &sortBy,
			Order:     &order,
			Direction: &direction,
		})

		require.Equal(
			t,
			expectedListRequest(30, "events-cursor", model.ListSortOccurredAt, "desc", "prev"),
			req,
		)
	})

	t.Run("UnknownSortByFallsBackToEmpty", func(t *testing.T) {
		t.Parallel()

		sortBy := api.ListEventsParamsSortBy("unknown")
		req := toEventsListRequest(api.ListEventsParams{SortBy: &sortBy})
		require.Empty(t, req.SortBy)
	})
}

func TestListParams_ToSecurityAuditEventsListRequest(t *testing.T) {
	t.Parallel()

	limit := api.LimitParam(40)
	cursor := api.CursorParam("audit-events-cursor")
	sortBy := api.ListSecurityAuditEventsParamsSortBy(
		api.SecurityAuditEventFieldParamSecurityAuditEventOccurredAt,
	)
	order := api.ListSecurityAuditEventsParamsOrder(
		api.SecurityAuditEventOrderParamSecurityAuditEventOrderAsc,
	)
	direction := api.ListSecurityAuditEventsParamsDirection(
		api.SecurityAuditEventPageDirectionParamSecurityAuditEventPageNext,
	)

	req := toSecurityAuditEventsListRequest(api.ListSecurityAuditEventsParams{
		Limit:     &limit,
		Cursor:    &cursor,
		SortBy:    &sortBy,
		Order:     &order,
		Direction: &direction,
	})

	require.Equal(
		t,
		expectedListRequest(40, "audit-events-cursor", model.ListSortOccurredAt, "asc", "next"),
		req,
	)
}

func TestListParams_ToPipelinesListRequest(t *testing.T) {
	t.Parallel()

	limit := api.LimitParam(29)
	cursor := api.CursorParam("pipelines-cursor")
	direction := api.ListPipelinesParamsDirection(api.PageDirectionParamPrev)

	req := toPipelinesListRequest(api.ListPipelinesParams{
		Limit:     &limit,
		Cursor:    &cursor,
		Direction: &direction,
	})

	require.Equal(
		t,
		expectedListRequest(29, "pipelines-cursor", "", "", "prev"),
		req,
	)
}

func expectedListRequest(limit int, cursor, sortBy, order, direction string) model.ListRequest {
	return model.ListRequest{
		Limit:     limit,
		Cursor:    cursor,
		SortBy:    sortBy,
		Order:     order,
		Direction: direction,
	}
}
