package http

import (
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

func TestListEvents(t *testing.T) {
	t.Parallel()

	actorSlug := must.Slug("jane-admin")
	entitySlug := must.Slug("users-sync")
	occurredAt := time.Date(2026, 5, 22, 12, 30, 0, 0, time.UTC)
	createdAt := time.Date(2026, 5, 22, 12, 30, 1, 0, time.UTC)

	tests := []struct {
		name        string
		authAllowed bool
		listFn      func(
			ctx context.Context,
			orgSlug slug.Slug,
			filters eventssvc.ListEventsFilters,
			req model.ListRequest,
		) (model.ListResponse[*model.Event], error)
		wantStatus int
		wantBody   *api.EventListResponse
		wantErr    *apierror.Error
	}{
		{
			name:        "ReturnsEventsForOrganization",
			authAllowed: true,
			listFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				filters eventssvc.ListEventsFilters,
				req model.ListRequest,
			) (model.ListResponse[*model.Event], error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, model.ListRequest{
					Limit:     10,
					Cursor:    "page-1",
					SortBy:    model.ListSortOccurredAt,
					Order:     "desc",
					Direction: "next",
				}, req)
				require.NotNil(t, filters.ActorType)
				require.NotNil(t, filters.ActorSlug)
				require.NotNil(t, filters.EntityType)
				require.NotNil(t, filters.EntitySlug)
				require.NotNil(t, filters.Action)
				assert.Equal(t, model.EventActorTypeUser, *filters.ActorType)
				assert.Equal(t, actorSlug, *filters.ActorSlug)
				assert.Equal(t, model.EventEntityTypePipeline, *filters.EntityType)
				assert.Equal(t, entitySlug, *filters.EntitySlug)
				assert.Equal(t, model.EventActionUpdate, *filters.Action)

				return model.ListResponse[*model.Event]{
					Items: []*model.Event{{
						SchemaVersion: 1,
						Slug:          "event-001",
						EventClass:    model.EventClassActivity,
						ActorType:     model.EventActorTypeUser,
						ActorSlug:     &actorSlug,
						EntityType:    model.EventEntityTypePipeline,
						EntitySlug:    &entitySlug,
						Action:        model.EventActionUpdate,
						OccurredAt:    occurredAt,
						Metadata: map[string]any{
							"source": "pipeline",
						},
						CreatedAt: createdAt,
					}},
					HasMore:    true,
					NextCursor: ptr.Ptr("page-2"),
				}, nil
			},
			wantStatus: http.StatusOK,
			wantBody: &api.EventListResponse{
				Events: []api.EventRecord{{
					SchemaVersion: 1,
					Slug:          "event-001",
					EventClass:    api.EventClassActivity,
					ActorType:     api.EventActorTypeUser,
					ActorSlug:     ptr.Ptr(api.NullableSlug("jane-admin")),
					EntityType:    api.EventEntityTypePipeline,
					EntitySlug:    ptr.Ptr(api.NullableSlug("users-sync")),
					Action:        api.EventActionUpdate,
					OccurredAt:    occurredAt,
					Metadata: map[string]any{
						"source": "pipeline",
					},
					CreatedAt: createdAt,
				}},
				HasMore:    true,
				NextCursor: ptr.Ptr("page-2"),
			},
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("events", "read"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				eventsService: &fakeEventsService{
					ListEventsFn: tt.listFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourceEvents, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				"/organizations/acme/events?limit=10&cursor=page-1&sort_by=event_occurred_at&order=desc&direction=next&actor_type=user&actor_slug=jane-admin&entity_type=pipeline&entity_slug=users-sync&action=update",
				nil,
			)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody != nil {
				assertResponse(t, resp, *tt.wantBody)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestListSecurityAuditEventsMapsTenantFilters(t *testing.T) {
	t.Parallel()

	occurredFrom := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	occurredBefore := time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC)
	requestID := "req-audit-123"
	actorSlug := must.Slug("jane-admin")
	entitySlug := must.Slug("prod-key")

	app, store := newTestApp(t, testServerConfig{
		orgService: &fakeOrgService{
			GetRetentionPolicyFn: func(_ context.Context, orgSlug slug.Slug) (model.OrganizationRetentionPolicy, error) {
				require.Equal(t, must.Slug("acme"), orgSlug)
				policy := model.DefaultOrganizationRetentionPolicy()
				policy.EventLogRetentionDays = 21
				return policy, nil
			},
		},
		eventsService: &fakeEventsService{
			ListEventsFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				filters eventssvc.ListEventsFilters,
				req model.ListRequest,
			) (model.ListResponse[*model.Event], error) {
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.Equal(t, model.ListRequest{
					Limit:     25,
					Cursor:    "audit-page-1",
					SortBy:    model.ListSortOccurredAt,
					Order:     model.ListOrderDesc,
					Direction: model.ListDirectionNext,
				}, req)
				require.NotNil(t, filters.EventClass)
				require.Equal(t, model.EventClassSecurityAudit, *filters.EventClass)
				require.Equal(t, &requestID, filters.RequestID)
				require.Equal(t, &occurredFrom, filters.OccurredFrom)
				require.Equal(t, &occurredBefore, filters.OccurredBefore)
				require.Equal(t, model.EventActorTypeUser, *filters.ActorType)
				require.Equal(t, actorSlug, *filters.ActorSlug)
				require.Equal(t, model.EventEntityTypeAPIKey, *filters.EntityType)
				require.Equal(t, entitySlug, *filters.EntitySlug)
				require.Equal(t, model.EventActionRotate, *filters.Action)

				return model.ListResponse[*model.Event]{
					Items: []*model.Event{{
						Slug:          "event-001",
						EventClass:    model.EventClassSecurityAudit,
						ActorType:     model.EventActorTypeUser,
						ActorSlug:     &actorSlug,
						EntityType:    model.EventEntityTypeAPIKey,
						EntitySlug:    &entitySlug,
						Action:        model.EventActionRotate,
						OccurredAt:    occurredFrom,
						RequestID:     &requestID,
						SchemaVersion: 1,
						Metadata:      map[string]any{"credential_material": "redacted"},
						CreatedAt:     occurredFrom.Add(time.Second),
					}},
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, model.Read, input.Action)
				require.Equal(t, model.ResourceEvents, input.Resource)
				require.NotNil(t, input.OrgSlug)
				require.Equal(t, must.Slug("acme"), *input.OrgSlug)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/security-audit-events?limit=25&cursor=audit-page-1&sort_by=event_occurred_at&order=desc&direction=next&actor_type=user&actor_slug=jane-admin&entity_type=api_key&entity_slug=prod-key&action=rotate&request_id=req-audit-123&occurred_from=2026-07-01T00%3A00%3A00Z&occurred_before=2026-07-02T00%3A00%3A00Z",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"schema_version":1`)
	require.Contains(t, string(body), `"request_id":"req-audit-123"`)
	require.Contains(t, string(body), `"event_log_retention_days":21`)
}

func TestListSecurityAuditEventsAuthorizesBeforeParsingOrQuerying(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		eventsService: &fakeEventsService{
			ListEventsFn: func(
				context.Context,
				slug.Slug,
				eventssvc.ListEventsFilters,
				model.ListRequest,
			) (model.ListResponse[*model.Event], error) {
				t.Fatal("ListEvents must not be called after authorization denial")
				return model.ListResponse[*model.Event]{}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, model.ResourceEvents, input.Resource)
				return false, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/security-audit-events?actor_slug=not%20a%20slug",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestListSecurityAuditEventsRejectsInvertedOccurrenceRange(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		eventsService: &fakeEventsService{
			ListEventsFn: func(
				context.Context,
				slug.Slug,
				eventssvc.ListEventsFilters,
				model.ListRequest,
			) (model.ListResponse[*model.Event], error) {
				t.Fatal("ListEvents must not be called for an invalid occurrence range")
				return model.ListResponse[*model.Event]{}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(context.Context, authorization.Input) (bool, error) {
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/security-audit-events?occurred_from=2026-07-02T00%3A00%3A00Z&occurred_before=2026-07-01T00%3A00%3A00Z",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "occurred_from must be before occurred_before")
}

func TestExportSecurityAuditEventsReturnsDeterministicEscapedCSV(t *testing.T) {
	t.Parallel()

	firstOccurredAt := time.Date(2026, time.July, 1, 10, 0, 0, 123, time.UTC)
	secondOccurredAt := firstOccurredAt.Add(time.Second)
	firstActor := must.Slug("alice-admin")
	firstEntity := must.Slug("production-key")
	requestID := `=HYPERLINK("https://example.invalid","request")`
	calls := 0

	app, store := newTestApp(t, testServerConfig{
		eventsService: &fakeEventsService{
			ListEventsFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				filters eventssvc.ListEventsFilters,
				req model.ListRequest,
			) (model.ListResponse[*model.Event], error) {
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.NotNil(t, filters.EventClass)
				require.Equal(t, model.EventClassSecurityAudit, *filters.EventClass)
				require.Equal(t, model.ListOrderAsc, req.Order)
				require.Equal(t, model.ListSortOccurredAt, req.SortBy)
				require.Equal(t, model.ListDirectionNext, req.Direction)
				require.Equal(t, securityAuditExportPageSize, req.Limit)

				calls++
				if calls == 1 {
					require.Empty(t, req.Cursor)
					return model.ListResponse[*model.Event]{
						Items: []*model.Event{{
							Slug:          "event-001",
							EventClass:    model.EventClassSecurityAudit,
							ActorType:     model.EventActorTypeUser,
							ActorSlug:     &firstActor,
							EntityType:    model.EventEntityTypeAPIKey,
							EntitySlug:    &firstEntity,
							Action:        model.EventActionRotate,
							OccurredAt:    firstOccurredAt,
							RequestID:     &requestID,
							SchemaVersion: 1,
							Metadata: map[string]any{
								"note": "rotated, then verified\nby admin",
							},
							CreatedAt: firstOccurredAt.Add(time.Millisecond),
						}},
						HasMore:    true,
						NextCursor: ptr.Ptr("page-2"),
					}, nil
				}

				require.Equal(t, "page-2", req.Cursor)
				return model.ListResponse[*model.Event]{
					Items: []*model.Event{{
						Slug:          "event-002",
						EventClass:    model.EventClassSecurityAudit,
						ActorType:     model.EventActorTypeSystem,
						EntityType:    model.EventEntityTypeOrganization,
						Action:        model.EventActionUpdate,
						OccurredAt:    secondOccurredAt,
						SchemaVersion: 1,
						Metadata:      map[string]any{},
						CreatedAt:     secondOccurredAt.Add(time.Millisecond),
					}},
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, model.ResourceEvents, input.Resource)
				require.Equal(t, model.Read, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/security-audit-events/export",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/csv; charset=utf-8", resp.Header.Get("Content-Type"))
	require.Equal(
		t,
		`attachment; filename="braidplane-security-audit-events-acme.csv"`,
		resp.Header.Get("Content-Disposition"),
	)

	records, err := csv.NewReader(resp.Body).ReadAll()
	require.NoError(t, err)
	require.Equal(t, securityAuditExportHeader, records[0])
	require.Len(t, records, 3)
	require.Equal(t, "event-001", records[1][2])
	require.Equal(t, firstOccurredAt.Format(time.RFC3339Nano), records[1][3])
	require.Equal(t, "alice-admin", records[1][6])
	require.Equal(t, "'"+requestID, records[1][10])
	require.JSONEq(t, `{"note":"rotated, then verified\nby admin"}`, records[1][11])
	require.Equal(t, "event-002", records[2][2])
	require.Empty(t, records[2][6])
	require.Empty(t, records[2][8])
	require.Empty(t, records[2][10])
	require.Equal(t, 2, calls)
}

func TestSpreadsheetSafeCSVCell(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		input string
		want  string
	}{
		{input: "", want: ""},
		{input: "request-123", want: "request-123"},
		{input: "=1+1", want: "'=1+1"},
		{input: "+1", want: "'+1"},
		{input: "-1", want: "'-1"},
		{input: "@SUM(A1:A2)", want: "'@SUM(A1:A2)"},
		{input: "\tformula", want: "'\tformula"},
		{input: "\rformula", want: "'\rformula"},
	} {
		require.Equal(t, tt.want, spreadsheetSafeCSVCell(tt.input))
	}
}

func TestExportSecurityAuditEventsAuthorizesBeforeParsingOrQuerying(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		eventsService: &fakeEventsService{
			ListEventsFn: func(
				context.Context,
				slug.Slug,
				eventssvc.ListEventsFilters,
				model.ListRequest,
			) (model.ListResponse[*model.Event], error) {
				t.Fatal("ListEvents must not be called after authorization denial")
				return model.ListResponse[*model.Event]{}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(context.Context, authorization.Input) (bool, error) {
				return false, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/security-audit-events/export?occurred_from=2026-07-02T00%3A00%3A00Z&occurred_before=2026-07-01T00%3A00%3A00Z",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestExportSecurityAuditEventsRejectsMoreThanRecordLimit(t *testing.T) {
	t.Parallel()

	event := &model.Event{
		Slug:          "event-001",
		EventClass:    model.EventClassSecurityAudit,
		ActorType:     model.EventActorTypeSystem,
		EntityType:    model.EventEntityTypeOrganization,
		Action:        model.EventActionUpdate,
		OccurredAt:    time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		SchemaVersion: 1,
		Metadata:      map[string]any{},
		CreatedAt:     time.Date(2026, time.July, 1, 0, 0, 1, 0, time.UTC),
	}
	page := make([]*model.Event, securityAuditExportPageSize)
	for i := range page {
		page[i] = event
	}
	calls := 0

	app, store := newTestApp(t, testServerConfig{
		eventsService: &fakeEventsService{
			ListEventsFn: func(
				context.Context,
				slug.Slug,
				eventssvc.ListEventsFilters,
				model.ListRequest,
			) (model.ListResponse[*model.Event], error) {
				calls++
				nextCursor := strings.Repeat("p", calls)
				return model.ListResponse[*model.Event]{
					Items:      page,
					HasMore:    true,
					NextCursor: &nextCursor,
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(context.Context, authorization.Input) (bool, error) {
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/security-audit-events/export",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, securityAuditExportRecordLimit/securityAuditExportPageSize, calls)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "narrow occurred_from and occurred_before")
}

func TestExportSecurityAuditEventsRejectsUnboundedEmptyPages(t *testing.T) {
	t.Parallel()

	calls := 0
	app, store := newTestApp(t, testServerConfig{
		eventsService: &fakeEventsService{
			ListEventsFn: func(
				context.Context,
				slug.Slug,
				eventssvc.ListEventsFilters,
				model.ListRequest,
			) (model.ListResponse[*model.Event], error) {
				calls++
				nextCursor := strings.Repeat("p", calls)
				return model.ListResponse[*model.Event]{
					HasMore:    true,
					NextCursor: &nextCursor,
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(context.Context, authorization.Input) (bool, error) {
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/security-audit-events/export",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	require.Equal(t, securityAuditExportPageLimit, calls)
}

func TestListEventsAcceptsEnvironmentEntityTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		entityType    api.EventEntityType
		wantModelType model.EventEntityType
	}{
		{
			name:          "Environment",
			entityType:    api.EventEntityTypeEnvironment,
			wantModelType: model.EventEntityTypeEnvironment,
		},
		{
			name:          "ProjectEnvironment",
			entityType:    api.EventEntityTypeProjectEnvironment,
			wantModelType: model.EventEntityTypeProjectEnvironment,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				eventsService: &fakeEventsService{
					ListEventsFn: func(
						_ context.Context,
						orgSlug slug.Slug,
						filters eventssvc.ListEventsFilters,
						_ model.ListRequest,
					) (model.ListResponse[*model.Event], error) {
						assert.Equal(t, must.Slug("acme"), orgSlug)
						require.NotNil(t, filters.EntityType)
						assert.Equal(t, tt.wantModelType, *filters.EntityType)
						return model.ListResponse[*model.Event]{}, nil
					},
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourceEvents, input.Resource)
						return true, nil
					},
				},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				"/organizations/acme/events?entity_type="+string(tt.entityType),
				nil,
			)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}
}

func TestListAlertPolicySourceMetadata(t *testing.T) {
	t.Parallel()

	projectSlug := must.Slug("ops")
	environmentSlug := must.Slug("prod")
	sourceKind := model.EventEntityTypeProject
	sourceAction := model.EventActionCreate
	sourceRef := must.Slug("ops-project")

	app, store := newTestApp(t, testServerConfig{
		eventsService: &fakeEventsService{
			ListSourceMetadataFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				filters eventssvc.SourceMetadataFilters,
			) (model.EventSourceMetadataResponse, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, model.AlertPolicyScopeProjectEnvironment, filters.Scope)
				require.NotNil(t, filters.ProjectSlug)
				require.NotNil(t, filters.EnvironmentSlug)
				require.NotNil(t, filters.SourceKind)
				require.NotNil(t, filters.SourceAction)
				assert.Equal(t, projectSlug, *filters.ProjectSlug)
				assert.Equal(t, environmentSlug, *filters.EnvironmentSlug)
				assert.Equal(t, sourceKind, *filters.SourceKind)
				assert.Equal(t, sourceAction, *filters.SourceAction)

				return model.EventSourceMetadataResponse{
					NotificationTypes: model.ProducerBackedNotificationTypes(),
					Sources: []model.EventSourceMetadata{{
						SourceKind:    sourceKind,
						SourceActions: []model.EventAction{sourceAction, model.EventActionUpdate},
					}},
					SourceRefs: []model.EventSourceRefOption{{
						Value: sourceRef,
						Label: "Ops project",
					}},
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourceAlertPolicies, input.Resource)
				assert.Equal(t, model.Read, input.Action)
				return true, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/alert-policy-source-metadata?scope=project_environment&project_slug=ops&environment_slug=prod&source_kind=project&source_action=create",
		nil,
	)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, api.AlertPolicySourceMetadataResponse{
		NotificationTypes: []api.NotificationType{api.NotificationTypePipelineRunSucceeded, api.NotificationTypePipelineRetryExhausted, api.NotificationTypePipelineContractFailure, api.NotificationTypePipelineRunFailed, api.NotificationTypePipelineScheduledRunDelayed, api.NotificationTypePipelineScheduledRunMissing, api.NotificationTypePipelineFreshnessSLABreached},
		Sources: []api.AlertPolicySourceMetadata{{
			SourceKind:    api.EventEntityTypeProject,
			SourceActions: []api.EventAction{api.EventActionCreate, api.EventActionUpdate},
		}},
		SourceRefs: []api.AlertPolicySourceRefOption{{
			Value: "ops-project",
			Label: "Ops project",
		}},
	})
}

func TestListAlertPolicySourceMetadataRejectsInvalidFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
	}{
		{
			name:  "InvalidSourceKindToken",
			query: "source_kind=not-a-kind",
		},
		{
			name:  "UnsupportedSourceKind",
			query: "source_kind=pipeline_checkpoint",
		},
		{
			name:  "InvalidSourceActionToken",
			query: "source_kind=project&source_action=not-an-action",
		},
		{
			name:  "UnsupportedSourceActionForKind",
			query: "source_kind=project&source_action=test_connection",
		},
		{
			name:  "InvalidProjectSlug",
			query: "project_slug=not%20valid",
		},
		{
			name:  "InvalidEnvironmentSlug",
			query: "environment_slug=not%20valid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				eventsService: &fakeEventsService{
					ListSourceMetadataFn: func(
						context.Context,
						slug.Slug,
						eventssvc.SourceMetadataFilters,
					) (model.EventSourceMetadataResponse, error) {
						t.Fatal("ListSourceMetadata should not be called for invalid filters")
						return model.EventSourceMetadataResponse{}, nil
					},
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourceAlertPolicies, input.Resource)
						assert.Equal(t, model.Read, input.Action)
						return true, nil
					},
				},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				"/organizations/acme/alert-policy-source-metadata?"+tt.query,
				nil,
			)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	}
}
