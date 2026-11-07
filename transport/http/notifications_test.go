package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
)

// testAlertDestinationConfig builds the generated alert destination config union for API DTO assertions.
func testAlertDestinationConfig(t *testing.T, value map[string]any) api.AlertDestinationConfig {
	t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(t, err)

	var ret api.AlertDestinationConfig
	require.NoError(t, ret.UnmarshalJSON(raw))
	return ret
}

// assertSessionAlertPrincipal verifies session-authenticated alert management calls.
func assertSessionAlertPrincipal(t *testing.T, principal notificationsvc.AlertManagementPrincipal, userSlug slug.Slug) {
	t.Helper()

	assert.Equal(t, userSlug, principal.UserSlug)
	assert.Empty(t, principal.APIKeySlug)
	assert.Empty(t, principal.APIKeyOrgSlug)
	assert.Empty(t, principal.APIKeyRoleSlug)
}

// assertAPIKeyAlertPrincipal verifies API-key-authenticated alert management calls.
func assertAPIKeyAlertPrincipal(
	t *testing.T,
	principal notificationsvc.AlertManagementPrincipal,
	apiKeySlug slug.Slug,
	orgSlug slug.Slug,
	roleSlug slug.Slug,
) {
	t.Helper()

	assert.Empty(t, principal.UserSlug)
	assert.Equal(t, apiKeySlug, principal.APIKeySlug)
	assert.Equal(t, orgSlug, principal.APIKeyOrgSlug)
	assert.Equal(t, roleSlug, principal.APIKeyRoleSlug)
}

func TestListNotifications(t *testing.T) {
	t.Parallel()

	eventSlug := must.Slug("evt-run-failed")
	now := time.Date(2026, 6, 30, 10, 15, 0, 0, time.UTC)
	queuedAt := now.Add(time.Minute)
	resolved := model.NotificationLifecycleStateResolved
	notificationType := model.NotificationTypePipelineRunFailed
	severity := model.NotificationSeverityCritical
	sourceKind := model.EventEntityTypePipelineRun
	sourceAction := model.EventActionExecute
	sourceRef := must.Slug("users-sync")

	tests := []struct {
		name        string
		authAllowed bool
		path        string
		listFn      func(
			t *testing.T,
			ctx context.Context,
			orgSlug slug.Slug,
			filters notificationsvc.ListNotificationsFilters,
			req model.ListRequest,
		) (model.ListResponse[*model.NotificationListItem], error)
		wantStatus int
		wantBody   *api.NotificationListResponse
		wantErr    *apierror.Error
	}{
		{
			name:        "ReturnsNotificationsForOrganization",
			authAllowed: true,
			path:        "/organizations/acme/notifications?limit=10&cursor=page-1&sort_by=notification_last_seen_at&order=desc&direction=next&lifecycle_state=resolved&notification_type=pipeline.run_failed&severity=critical&source_kind=pipeline_run&source_action=execute&source_ref=users-sync",
			listFn: func(
				t *testing.T,
				_ context.Context,
				orgSlug slug.Slug,
				filters notificationsvc.ListNotificationsFilters,
				req model.ListRequest,
			) (model.ListResponse[*model.NotificationListItem], error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, model.ListRequest{
					Limit:     10,
					Cursor:    "page-1",
					SortBy:    model.ListSortLastSeenAt,
					Order:     "desc",
					Direction: "next",
				}, req)
				require.NotNil(t, filters.LifecycleState)
				require.NotNil(t, filters.NotificationType)
				require.NotNil(t, filters.Severity)
				require.NotNil(t, filters.SourceKind)
				require.NotNil(t, filters.SourceAction)
				require.NotNil(t, filters.SourceRef)
				assert.Equal(t, resolved, *filters.LifecycleState)
				assert.Equal(t, notificationType, *filters.NotificationType)
				assert.Equal(t, severity, *filters.Severity)
				assert.Equal(t, sourceKind, *filters.SourceKind)
				assert.Equal(t, sourceAction, *filters.SourceAction)
				assert.Equal(t, sourceRef, *filters.SourceRef)

				return model.ListResponse[*model.NotificationListItem]{
					Items: []*model.NotificationListItem{{
						Slug:             "notif-001",
						NotificationType: notificationType,
						Severity:         severity,
						LifecycleState:   resolved,
						Summary:          "Pipeline users-sync failed in prod",
						SourceEventSlug:  &eventSlug,
						SourceEntityType: model.EventEntityTypePipelineRun,
						SourceAction:     model.EventActionExecute,
						SourceRefs: map[string]string{
							"project_slug":     "ops",
							"environment_slug": "prod",
							"run_slug":         "run-001",
						},
						SafeMetadata: map[string]any{
							"summary": "Pipeline users-sync failed in prod",
						},
						OccurrenceCount:  3,
						FirstSeenAt:      now.Add(-2 * time.Hour),
						LastSeenAt:       now,
						LastOccurrenceAt: now.Add(-5 * time.Minute),
						DispatchStatuses: []model.NotificationDispatchSummary{{
							DestinationSlug: "ops-email",
							Status:          model.NotificationDispatchStatusQueued,
							QueuedAt:        &queuedAt,
						}},
						ResolvedAt: ptr.Ptr(now),
						CreatedAt:  now.Add(-2 * time.Hour),
						UpdatedAt:  now,
					}},
					HasMore:    true,
					NextCursor: ptr.Ptr("page-2"),
				}, nil
			},
			wantStatus: http.StatusOK,
			wantBody: &api.NotificationListResponse{
				Notifications: []api.NotificationRecord{{
					Slug:             "notif-001",
					NotificationType: api.NotificationType("pipeline.run_failed"),
					Severity:         api.NotificationSeverityCritical,
					LifecycleState:   api.NotificationLifecycleStateResolved,
					Summary:          "Pipeline users-sync failed in prod",
					SourceEventSlug:  ptr.Ptr(api.NullableSlug("evt-run-failed")),
					SourceEntityType: api.EventEntityTypePipelineRun,
					SourceAction:     api.EventActionExecute,
					SourceRefs: map[string]string{
						"project_slug":     "ops",
						"environment_slug": "prod",
						"run_slug":         "run-001",
					},
					SafeMetadata: map[string]any{
						"summary": "Pipeline users-sync failed in prod",
					},
					OccurrenceCount:  3,
					FirstSeenAt:      now.Add(-2 * time.Hour),
					LastSeenAt:       now,
					LastOccurrenceAt: now.Add(-5 * time.Minute),
					DispatchStatuses: []api.NotificationDispatchSummary{{
						DestinationSlug: "ops-email",
						Status:          api.NotificationDispatchStatusQueued,
						QueuedAt:        &queuedAt,
					}},
					ResolvedAt: ptr.Ptr(now),
					CreatedAt:  now.Add(-2 * time.Hour),
					UpdatedAt:  now,
				}},
				HasMore:    true,
				NextCursor: ptr.Ptr("page-2"),
			},
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			authAllowed: false,
			path:        "/organizations/acme/notifications?limit=10&cursor=page-1&sort_by=notification_last_seen_at&order=desc&direction=next&lifecycle_state=resolved&notification_type=pipeline.run_failed&severity=critical",
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("notifications", "read"),
		},
		{
			name:        "Returns400ForInvalidLifecycleState",
			authAllowed: true,
			path:        "/organizations/acme/notifications?lifecycle_state=bogus",
			listFn: func(
				t *testing.T,
				_ context.Context,
				_ slug.Slug,
				_ notificationsvc.ListNotificationsFilters,
				_ model.ListRequest,
			) (model.ListResponse[*model.NotificationListItem], error) {
				t.Fatal("invalid filters must not reach notification service")
				return model.ListResponse[*model.NotificationListItem]{}, nil
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.ValidationFailed("Invalid notification lifecycle state: bogus does not belong to NotificationLifecycleState values"),
		},
		{
			name:        "Returns400ForInvalidNotificationType",
			authAllowed: true,
			path:        "/organizations/acme/notifications?notification_type=bogus",
			listFn: func(
				t *testing.T,
				_ context.Context,
				_ slug.Slug,
				_ notificationsvc.ListNotificationsFilters,
				_ model.ListRequest,
			) (model.ListResponse[*model.NotificationListItem], error) {
				t.Fatal("invalid filters must not reach notification service")
				return model.ListResponse[*model.NotificationListItem]{}, nil
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.ValidationFailed("Invalid notification type: bogus does not belong to NotificationType values"),
		},
		{
			name:        "Returns400ForInvalidSeverity",
			authAllowed: true,
			path:        "/organizations/acme/notifications?severity=bogus",
			listFn: func(
				t *testing.T,
				_ context.Context,
				_ slug.Slug,
				_ notificationsvc.ListNotificationsFilters,
				_ model.ListRequest,
			) (model.ListResponse[*model.NotificationListItem], error) {
				t.Fatal("invalid filters must not reach notification service")
				return model.ListResponse[*model.NotificationListItem]{}, nil
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.ValidationFailed("Invalid notification severity: bogus does not belong to NotificationSeverity values"),
		},
		{
			name:        "Returns400WhenSourceActionOmitsSourceKind",
			authAllowed: true,
			path:        "/organizations/acme/notifications?source_action=execute",
			listFn: func(
				t *testing.T,
				_ context.Context,
				_ slug.Slug,
				_ notificationsvc.ListNotificationsFilters,
				_ model.ListRequest,
			) (model.ListResponse[*model.NotificationListItem], error) {
				t.Fatal("invalid filters must not reach notification service")
				return model.ListResponse[*model.NotificationListItem]{}, nil
			},
			wantStatus: http.StatusBadRequest,
			wantErr:    apierror.ValidationFailed("source_action requires source_kind"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				notificationsService: &fakeNotificationsService{
					ListNotificationsFn: func(
						ctx context.Context,
						orgSlug slug.Slug,
						filters notificationsvc.ListNotificationsFilters,
						req model.ListRequest,
					) (model.ListResponse[*model.NotificationListItem], error) {
						if tt.listFn == nil {
							return model.ListResponse[*model.NotificationListItem]{}, errFakeNotificationsServiceFnNotSet
						}
						return tt.listFn(t, ctx, orgSlug, filters, req)
					},
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.Read, input.Action)
						assert.Equal(t, model.ResourceNotifications, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(
				t,
				http.MethodGet,
				tt.path,
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

func TestGetNotification(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 3, 10, 15, 0, 0, time.UTC)

	tests := []struct {
		name        string
		authAllowed bool
		path        string
		setup       func(*fakeNotificationsService)
		wantStatus  int
		wantBody    *api.NotificationRecord
		wantErr     *apierror.Error
	}{
		{
			name:        "ReturnsNotificationForOrganization",
			authAllowed: true,
			path:        "/organizations/acme/notifications/notif-001",
			setup: func(fake *fakeNotificationsService) {
				fake.GetNotificationFn = func(
					_ context.Context,
					orgSlug slug.Slug,
					recordSlug slug.Slug,
				) (*model.NotificationListItem, error) {
					assert.Equal(t, must.Slug("acme"), orgSlug)
					assert.Equal(t, must.Slug("notif-001"), recordSlug)
					return &model.NotificationListItem{
						Slug:             recordSlug,
						NotificationType: model.NotificationTypePipelineRunFailed,
						Severity:         model.NotificationSeverityCritical,
						LifecycleState:   model.NotificationLifecycleStateOpen,
						Summary:          "Pipeline users-sync failed in prod",
						SourceEntityType: model.EventEntityTypePipelineRun,
						SourceAction:     model.EventActionExecute,
						SourceRefs:       map[string]string{"pipeline_slug": "users-sync"},
						SafeMetadata:     map[string]any{"summary": "Pipeline users-sync failed in prod"},
						OccurrenceCount:  1,
						FirstSeenAt:      now.Add(-time.Hour),
						LastSeenAt:       now,
						LastOccurrenceAt: now,
						CreatedAt:        now.Add(-time.Hour),
						UpdatedAt:        now,
					}, nil
				}
			},
			wantStatus: http.StatusOK,
			wantBody: &api.NotificationRecord{
				Slug:             "notif-001",
				NotificationType: api.NotificationTypePipelineRunFailed,
				Severity:         api.NotificationSeverityCritical,
				LifecycleState:   api.NotificationLifecycleStateOpen,
				Summary:          "Pipeline users-sync failed in prod",
				SourceEntityType: api.EventEntityTypePipelineRun,
				SourceAction:     api.EventActionExecute,
				SourceRefs:       map[string]string{"pipeline_slug": "users-sync"},
				SafeMetadata:     map[string]any{"summary": "Pipeline users-sync failed in prod"},
				OccurrenceCount:  1,
				FirstSeenAt:      now.Add(-time.Hour),
				LastSeenAt:       now,
				LastOccurrenceAt: now,
				DispatchStatuses: []api.NotificationDispatchSummary{},
				CreatedAt:        now.Add(-time.Hour),
				UpdatedAt:        now,
			},
		},
		{
			name:        "Returns403WhenReadAuthorizationDenied",
			authAllowed: false,
			path:        "/organizations/acme/notifications/notif-001",
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("notifications", "read"),
		},
		{
			name:        "Returns404WhenNotificationMissing",
			authAllowed: true,
			path:        "/organizations/acme/notifications/notif-missing",
			setup: func(fake *fakeNotificationsService) {
				fake.GetNotificationFn = func(
					_ context.Context,
					orgSlug slug.Slug,
					recordSlug slug.Slug,
				) (*model.NotificationListItem, error) {
					assert.Equal(t, must.Slug("acme"), orgSlug)
					assert.Equal(t, must.Slug("notif-missing"), recordSlug)
					return nil, notificationsvc.ErrRecordNotFound
				}
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(http.StatusNotFound, "NOTIFICATION_NOT_FOUND", "notification not found"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeService := &fakeNotificationsService{}
			if tt.setup != nil {
				tt.setup(fakeService)
			}
			app, store := newTestApp(t, testServerConfig{
				notificationsService: fakeService,
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.Read, input.Action)
						assert.Equal(t, model.ResourceNotifications, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodGet, tt.path, nil)
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

func TestNotificationLifecycleActions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 16, 30, 0, 0, time.UTC)
	userSlug := must.Slug("johndoe")

	tests := []struct {
		name        string
		method      string
		path        string
		authAllowed bool
		setup       func(*fakeNotificationsService)
		wantStatus  int
		wantBody    *api.NotificationRecord
		wantErr     *apierror.Error
	}{
		{
			name:        "AcknowledgeNotification",
			method:      http.MethodPost,
			path:        "/organizations/acme/notifications/notif-001/acknowledge",
			authAllowed: true,
			setup: func(fake *fakeNotificationsService) {
				fake.AcknowledgeNotificationFn = func(
					_ context.Context,
					principal notificationsvc.AlertManagementPrincipal,
					orgSlug slug.Slug,
					recordSlug slug.Slug,
				) (*model.NotificationListItem, error) {
					assertSessionAlertPrincipal(t, principal, userSlug)
					assert.Equal(t, must.Slug("acme"), orgSlug)
					assert.Equal(t, must.Slug("notif-001"), recordSlug)
					return &model.NotificationListItem{
						Slug:             recordSlug,
						NotificationType: model.NotificationTypePipelineRunFailed,
						Severity:         model.NotificationSeverityCritical,
						LifecycleState:   model.NotificationLifecycleStateAcknowledged,
						Summary:          "Pipeline users-sync failed in prod",
						SourceEntityType: model.EventEntityTypePipelineRun,
						SourceAction:     model.EventActionExecute,
						SourceRefs:       map[string]string{},
						SafeMetadata:     map[string]any{},
						OccurrenceCount:  1,
						FirstSeenAt:      now.Add(-time.Hour),
						LastSeenAt:       now,
						LastOccurrenceAt: now,
						AcknowledgedAt:   ptr.Ptr(now),
						CreatedAt:        now.Add(-time.Hour),
						UpdatedAt:        now,
					}, nil
				}
			},
			wantStatus: http.StatusOK,
			wantBody: &api.NotificationRecord{
				Slug:             "notif-001",
				NotificationType: api.NotificationTypePipelineRunFailed,
				Severity:         api.NotificationSeverityCritical,
				LifecycleState:   api.NotificationLifecycleStateAcknowledged,
				Summary:          "Pipeline users-sync failed in prod",
				SourceEntityType: api.EventEntityTypePipelineRun,
				SourceAction:     api.EventActionExecute,
				SourceRefs:       map[string]string{},
				SafeMetadata:     map[string]any{},
				OccurrenceCount:  1,
				FirstSeenAt:      now.Add(-time.Hour),
				LastSeenAt:       now,
				LastOccurrenceAt: now,
				DispatchStatuses: []api.NotificationDispatchSummary{},
				AcknowledgedAt:   ptr.Ptr(now),
				CreatedAt:        now.Add(-time.Hour),
				UpdatedAt:        now,
			},
		},
		{
			name:        "Returns403WhenAcknowledgeUpdateAuthorizationDenied",
			method:      http.MethodPost,
			path:        "/organizations/acme/notifications/notif-001/acknowledge",
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("notifications", "update"),
		},
		{
			name:        "ResolveNotification",
			method:      http.MethodPost,
			path:        "/organizations/acme/notifications/notif-001/resolve",
			authAllowed: true,
			setup: func(fake *fakeNotificationsService) {
				fake.ResolveNotificationFn = func(
					_ context.Context,
					principal notificationsvc.AlertManagementPrincipal,
					orgSlug slug.Slug,
					recordSlug slug.Slug,
				) (*model.NotificationListItem, error) {
					assertSessionAlertPrincipal(t, principal, userSlug)
					assert.Equal(t, must.Slug("acme"), orgSlug)
					assert.Equal(t, must.Slug("notif-001"), recordSlug)
					return &model.NotificationListItem{
						Slug:             recordSlug,
						NotificationType: model.NotificationTypePipelineRunFailed,
						Severity:         model.NotificationSeverityCritical,
						LifecycleState:   model.NotificationLifecycleStateResolved,
						Summary:          "Pipeline users-sync failed in prod",
						SourceEntityType: model.EventEntityTypePipelineRun,
						SourceAction:     model.EventActionExecute,
						SourceRefs:       map[string]string{},
						SafeMetadata:     map[string]any{},
						OccurrenceCount:  1,
						FirstSeenAt:      now.Add(-time.Hour),
						LastSeenAt:       now,
						LastOccurrenceAt: now,
						ResolvedAt:       ptr.Ptr(now),
						CreatedAt:        now.Add(-time.Hour),
						UpdatedAt:        now,
					}, nil
				}
			},
			wantStatus: http.StatusOK,
			wantBody: &api.NotificationRecord{
				Slug:             "notif-001",
				NotificationType: api.NotificationTypePipelineRunFailed,
				Severity:         api.NotificationSeverityCritical,
				LifecycleState:   api.NotificationLifecycleStateResolved,
				Summary:          "Pipeline users-sync failed in prod",
				SourceEntityType: api.EventEntityTypePipelineRun,
				SourceAction:     api.EventActionExecute,
				SourceRefs:       map[string]string{},
				SafeMetadata:     map[string]any{},
				OccurrenceCount:  1,
				FirstSeenAt:      now.Add(-time.Hour),
				LastSeenAt:       now,
				LastOccurrenceAt: now,
				DispatchStatuses: []api.NotificationDispatchSummary{},
				ResolvedAt:       ptr.Ptr(now),
				CreatedAt:        now.Add(-time.Hour),
				UpdatedAt:        now,
			},
		},
		{
			name:        "Returns403WhenUpdateAuthorizationDenied",
			method:      http.MethodPost,
			path:        "/organizations/acme/notifications/notif-001/resolve",
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("notifications", "update"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeService := &fakeNotificationsService{}
			if tt.setup != nil {
				tt.setup(fakeService)
			}
			app, store := newTestApp(t, testServerConfig{
				notificationsService: fakeService,
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.Update, input.Action)
						assert.Equal(t, model.ResourceNotifications, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, tt.method, tt.path, nil)
			resp := authenticatedRequest(t, app, store, userSlug, req)
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

func TestListNotificationDeliveryAttempts(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 14, 15, 0, 0, time.UTC)
	sentAt := now.Add(2 * time.Minute)
	errorCategory := "EMAIL_DELIVERY_FAILED"
	errorMessage := "SMTP delivery failed"

	tests := []struct {
		name        string
		authAllowed bool
		path        string
		listFn      func(
			t *testing.T,
			ctx context.Context,
			orgSlug slug.Slug,
			recordSlug slug.Slug,
			req model.ListRequest,
		) (model.ListResponse[*model.AlertDeliveryAttempt], error)
		wantStatus int
		wantBody   *api.AlertDeliveryAttemptListResponse
		wantErr    *apierror.Error
	}{
		{
			name:        "ReturnsAttemptsForNotification",
			authAllowed: true,
			path:        "/organizations/acme/notifications/notif-001/delivery-attempts?limit=5&cursor=page-1&sort_by=alert_delivery_attempt_created_at&order=desc&direction=next",
			listFn: func(
				t *testing.T,
				_ context.Context,
				orgSlug slug.Slug,
				recordSlug slug.Slug,
				req model.ListRequest,
			) (model.ListResponse[*model.AlertDeliveryAttempt], error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("notif-001"), recordSlug)
				assert.Equal(t, model.ListRequest{
					Limit:     5,
					Cursor:    "page-1",
					SortBy:    model.ListSortCreatedAt,
					Order:     "desc",
					Direction: "next",
				}, req)

				return model.ListResponse[*model.AlertDeliveryAttempt]{
					Items: []*model.AlertDeliveryAttempt{{
						Slug:                     "alert-delivery-001",
						NotificationRecordSlug:   "notif-001",
						AlertPolicySlug:          ptr.Ptr(slug.Slug("critical-pipeline-email")),
						DestinationSlug:          ptr.Ptr(slug.Slug("ops-email")),
						DestinationName:          ptr.Ptr("Operations Email"),
						DestinationType:          model.AlertDestinationTypeEmail,
						Status:                   model.AlertDeliveryAttemptStatusSent,
						ProviderAttempt:          2,
						ProviderResponseMetadata: map[string]any{"message_id_present": true},
						ErrorCategory:            &errorCategory,
						ErrorMessage:             &errorMessage,
						QueuedAt:                 now,
						SentAt:                   &sentAt,
						CreatedAt:                now,
						UpdatedAt:                sentAt,
					}},
					HasMore:    true,
					NextCursor: ptr.Ptr("page-2"),
				}, nil
			},
			wantStatus: http.StatusOK,
			wantBody: &api.AlertDeliveryAttemptListResponse{
				AlertDeliveryAttempts: []api.AlertDeliveryAttempt{{
					Slug:                     "alert-delivery-001",
					NotificationRecordSlug:   "notif-001",
					AlertPolicySlug:          ptr.Ptr(api.NullableSlug("critical-pipeline-email")),
					DestinationSlug:          ptr.Ptr(api.NullableSlug("ops-email")),
					DestinationName:          ptr.Ptr("Operations Email"),
					DestinationType:          api.AlertDestinationTypeEmail,
					Status:                   api.AlertDeliveryAttemptStatusSent,
					ProviderAttempt:          2,
					ProviderResponseMetadata: map[string]any{"message_id_present": true},
					ErrorCategory:            &errorCategory,
					ErrorMessage:             &errorMessage,
					QueuedAt:                 now,
					SentAt:                   &sentAt,
					CreatedAt:                now,
					UpdatedAt:                sentAt,
				}},
				HasMore:    true,
				NextCursor: ptr.Ptr("page-2"),
			},
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			authAllowed: false,
			path:        "/organizations/acme/notifications/notif-001/delivery-attempts",
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("notifications", "read"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				notificationsService: &fakeNotificationsService{
					ListAlertDeliveryAttemptsFn: func(
						ctx context.Context,
						orgSlug slug.Slug,
						recordSlug slug.Slug,
						req model.ListRequest,
					) (model.ListResponse[*model.AlertDeliveryAttempt], error) {
						if tt.listFn == nil {
							return model.ListResponse[*model.AlertDeliveryAttempt]{}, errFakeNotificationsServiceFnNotSet
						}
						return tt.listFn(t, ctx, orgSlug, recordSlug, req)
					},
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.Read, input.Action)
						assert.Equal(t, model.ResourceNotifications, input.Resource)
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodGet, tt.path, nil)
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

func TestListAlertDeliveryAttempts(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 14, 20, 0, 0, time.UTC)
	sentAt := now.Add(90 * time.Second)

	app, store := newTestApp(t, testServerConfig{
		notificationsService: &fakeNotificationsService{
			ListRecentAlertDeliveryAttemptsForRecordsFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				recordSlugs []slug.Slug,
				limitPerRecord int,
			) ([]*model.AlertDeliveryAttempt, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, []slug.Slug{must.Slug("notif-001"), must.Slug("notif-002")}, recordSlugs)
				assert.Equal(t, 3, limitPerRecord)

				return []*model.AlertDeliveryAttempt{{
					Slug:                     "alert-delivery-001",
					NotificationRecordSlug:   "notif-001",
					AlertPolicySlug:          ptr.Ptr(slug.Slug("critical-pipeline-email")),
					DestinationSlug:          ptr.Ptr(slug.Slug("ops-email")),
					DestinationName:          ptr.Ptr("Operations Email"),
					DestinationType:          model.AlertDestinationTypeEmail,
					Status:                   model.AlertDeliveryAttemptStatusSent,
					ProviderAttempt:          1,
					ProviderResponseMetadata: map[string]any{},
					QueuedAt:                 now,
					SentAt:                   &sentAt,
					CreatedAt:                now,
					UpdatedAt:                sentAt,
				}}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.Read, input.Action)
				assert.Equal(t, model.ResourceNotifications, input.Resource)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/organizations/acme/alert-delivery-attempts?notification_slugs=notif-001,notif-002&limit_per_notification=3", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, api.AlertDeliveryAttemptListResponse{
		AlertDeliveryAttempts: []api.AlertDeliveryAttempt{{
			Slug:                     "alert-delivery-001",
			NotificationRecordSlug:   "notif-001",
			AlertPolicySlug:          ptr.Ptr(api.NullableSlug("critical-pipeline-email")),
			DestinationSlug:          ptr.Ptr(api.NullableSlug("ops-email")),
			DestinationName:          ptr.Ptr("Operations Email"),
			DestinationType:          api.AlertDestinationTypeEmail,
			Status:                   api.AlertDeliveryAttemptStatusSent,
			ProviderAttempt:          1,
			ProviderResponseMetadata: map[string]any{},
			QueuedAt:                 now,
			SentAt:                   &sentAt,
			CreatedAt:                now,
			UpdatedAt:                sentAt,
		}},
		HasMore: false,
	})
}

func TestListAlertDeliveryAttemptsRejectsInvalidLimit(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		notificationsService: &fakeNotificationsService{},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.Read, input.Action)
				assert.Equal(t, model.ResourceNotifications, input.Resource)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/organizations/acme/alert-delivery-attempts?notification_slugs=notif-001&limit_per_notification=11", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.ValidationFailed("limit_per_notification must be between 1 and 10"))
}

func TestListAlertDeliveryAttemptsRejectsInvalidNotificationSlugs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		query   string
		wantErr *apierror.Error
	}{
		{
			name:    "RejectsBlankNotificationSlug",
			query:   "notification_slugs=notif-001,,notif-002",
			wantErr: apierror.ValidationFailed("notification_slugs must not contain blank values"),
		},
		{
			name:  "RejectsInvalidNotificationSlug",
			query: "notification_slugs=INVALID",
			wantErr: apierror.ValidationFailed(
				"Invalid notification slug: invalid slug format: must be 1-100 lowercase alphanumeric characters with hyphens",
			),
		},
		{
			name:    "RejectsOversizedNotificationSlugList",
			query:   "notification_slugs=notif-001,notif-002,notif-003,notif-004,notif-005,notif-006,notif-007,notif-008,notif-009,notif-010,notif-011,notif-012,notif-013,notif-014,notif-015,notif-016,notif-017,notif-018,notif-019,notif-020,notif-021,notif-022,notif-023,notif-024,notif-025,notif-026,notif-027,notif-028,notif-029,notif-030,notif-031,notif-032,notif-033,notif-034,notif-035,notif-036,notif-037,notif-038,notif-039,notif-040,notif-041,notif-042,notif-043,notif-044,notif-045,notif-046,notif-047,notif-048,notif-049,notif-050,notif-051",
			wantErr: apierror.ValidationFailed("notification_slugs must contain between 1 and 50 slugs"),
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				notificationsService: &fakeNotificationsService{},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.Read, input.Action)
						assert.Equal(t, model.ResourceNotifications, input.Resource)
						return true, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodGet, "/organizations/acme/alert-delivery-attempts?"+tt.query, nil)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assertErrorResponse(t, resp, tt.wantErr)
		})
	}
}

func TestCreateAlertDestination(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	destinationSlug := must.Slug("ops-email")

	cases := []struct {
		name string
		body map[string]any
		want api.AlertDestination
	}{
		{
			name: "CreatesEmailDestination",
			body: map[string]any{
				"slug": "ops-email",
				"name": "Operations Email",
				"type": "email",
				"non_secret_config": map[string]any{
					"recipients": []string{"ops@example.com"},
				},
			},
			want: api.AlertDestination{
				Slug: "ops-email",
				Name: "Operations Email",
				Type: api.AlertDestinationTypeEmail,
				NonSecretConfig: testAlertDestinationConfig(t, map[string]any{
					"recipients": []any{"ops@example.com"},
				}),
				Enabled:   true,
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				notificationsService: &fakeNotificationsService{
					CreateAlertDestinationFn: func(
						_ context.Context,
						principal notificationsvc.AlertManagementPrincipal,
						params notificationsvc.CreateAlertDestinationParams,
					) (*model.AlertDestination, error) {
						assertSessionAlertPrincipal(t, principal, must.Slug("johndoe"))
						assert.Equal(t, must.Slug("acme"), params.OrgSlug)
						assert.Equal(t, destinationSlug, params.Slug)
						assert.Equal(t, "Operations Email", params.Name)
						assert.Equal(t, model.AlertDestinationTypeEmail, params.Type)
						assert.Equal(t, []any{"ops@example.com"}, params.NonSecretConfig["recipients"])

						return &model.AlertDestination{
							Slug:            destinationSlug,
							Name:            params.Name,
							Type:            params.Type,
							NonSecretConfig: params.NonSecretConfig,
							Enabled:         true,
							CreatedAt:       now,
							UpdatedAt:       now,
						}, nil
					},
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.Create, input.Action)
						assert.Equal(t, model.ResourceAlertDestinations, input.Resource)
						return true, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodPost, "/organizations/acme/alert-destinations", tc.body)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, http.StatusCreated, resp.StatusCode)
			assertResponse(t, resp, tc.want)
		})
	}
}

func TestCreateAlertDestinationForwardsAPIKeyPrincipal(t *testing.T) {
	t.Parallel()

	apiKeySlug := must.Slug("deploy-key")
	apiKeyOrgSlug := must.Slug("acme")
	apiKeyRoleSlug := must.Slug("admin")
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	app, _ := newTestApp(t, testServerConfig{
		notificationsService: &fakeNotificationsService{
			CreateAlertDestinationFn: func(
				_ context.Context,
				principal notificationsvc.AlertManagementPrincipal,
				params notificationsvc.CreateAlertDestinationParams,
			) (*model.AlertDestination, error) {
				assertAPIKeyAlertPrincipal(t, principal, apiKeySlug, apiKeyOrgSlug, apiKeyRoleSlug)
				assert.Equal(t, must.Slug("acme"), params.OrgSlug)
				assert.Equal(t, must.Slug("api-key-email"), params.Slug)
				assert.Equal(t, "API key email", params.Name)

				return &model.AlertDestination{
					Slug:            params.Slug,
					Name:            params.Name,
					Type:            params.Type,
					NonSecretConfig: params.NonSecretConfig,
					Enabled:         true,
					CreatedAt:       now,
					UpdatedAt:       now,
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, apiKeySlug, input.APIKeySlug)
				assert.Equal(t, apiKeyRoleSlug, input.APIKeyRoleSlug)
				require.NotNil(t, input.OrgSlug)
				assert.Equal(t, apiKeyOrgSlug, *input.OrgSlug)
				assert.Equal(t, model.Create, input.Action)
				assert.Equal(t, model.ResourceAlertDestinations, input.Resource)
				return true, nil
			},
		},
		middleware: []fiber.Handler{
			func(c fiber.Ctx) error {
				reqctx.SetAPIKeyPrincipal(c, reqctx.APIKeyPrincipal{
					Slug:    apiKeySlug,
					OrgSlug: apiKeyOrgSlug,
					Role:    apiKeyRoleSlug,
				})
				return c.Next()
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/alert-destinations", map[string]any{
		"slug": "api-key-email",
		"name": "API key email",
		"type": "email",
		"non_secret_config": map[string]any{
			"recipients": []string{"ops@example.com"},
		},
	})
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestUpdateAlertDestinationForwardsSessionPrincipal(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 12, 15, 0, 0, time.UTC)
	destinationSlug := must.Slug("ops-email")

	app, store := newTestApp(t, testServerConfig{
		notificationsService: &fakeNotificationsService{
			UpdateAlertDestinationFn: func(
				_ context.Context,
				principal notificationsvc.AlertManagementPrincipal,
				params notificationsvc.UpdateAlertDestinationParams,
			) (*model.AlertDestination, error) {
				assertSessionAlertPrincipal(t, principal, must.Slug("johndoe"))
				assert.Equal(t, must.Slug("acme"), params.OrgSlug)
				assert.Equal(t, destinationSlug, params.Slug)
				assert.Equal(t, "Operations Email", params.Name)
				assert.Equal(t, model.AlertDestinationTypeEmail, params.Type)
				assert.Equal(t, []any{"ops@example.com"}, params.NonSecretConfig["recipients"])
				assert.False(t, params.Enabled)

				return &model.AlertDestination{
					Slug:            params.Slug,
					Name:            params.Name,
					Type:            params.Type,
					NonSecretConfig: params.NonSecretConfig,
					Enabled:         params.Enabled,
					CreatedAt:       now.Add(-time.Hour),
					UpdatedAt:       now,
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.Update, input.Action)
				assert.Equal(t, model.ResourceAlertDestinations, input.Resource)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPut, "/organizations/acme/alert-destinations/ops-email", map[string]any{
		"name":    "Operations Email",
		"type":    "email",
		"enabled": false,
		"non_secret_config": map[string]any{
			"recipients": []string{"ops@example.com"},
		},
	})
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAlertDestinationTestForwardsSessionPrincipal(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		notificationsService: &fakeNotificationsService{
			TestAlertDestinationFn: func(
				_ context.Context,
				principal notificationsvc.AlertManagementPrincipal,
				params notificationsvc.TestAlertDestinationParams,
			) (*notificationsvc.AlertDestinationTestResult, error) {
				assertSessionAlertPrincipal(t, principal, must.Slug("johndoe"))
				assert.Equal(t, must.Slug("acme"), params.OrgSlug)
				assert.Equal(t, must.Slug("ops-email"), params.DestinationSlug)
				return &notificationsvc.AlertDestinationTestResult{
					DestinationSlug:      params.DestinationSlug,
					DestinationType:      model.AlertDestinationTypeEmail,
					RecipientCount:       2,
					DiagnosticsPersisted: false,
					EnqueuedJobs: []notificationsvc.AlertDestinationTestJob{
						{Kind: "email_send", Queue: "email", State: "queued"},
						{Kind: "email_send", Queue: "email", State: "queued"},
					},
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.Update, input.Action)
				assert.Equal(t, model.ResourceAlertDestinations, input.Resource)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/alert-destinations/ops-email/test", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assertResponse(t, resp, map[string]any{
		"status":                "accepted",
		"destination_slug":      "ops-email",
		"destination_type":      "email",
		"recipient_count":       float64(2),
		"diagnostics_persisted": false,
		"enqueued_jobs": []any{
			map[string]any{"kind": "email_send", "queue": "email", "state": "queued"},
			map[string]any{"kind": "email_send", "queue": "email", "state": "queued"},
		},
	})
}

func TestAlertDestinationTestRequiresUpdatePermission(t *testing.T) {
	t.Parallel()

	serviceCalled := false
	app, store := newTestApp(t, testServerConfig{
		notificationsService: &fakeNotificationsService{
			TestAlertDestinationFn: func(
				context.Context,
				notificationsvc.AlertManagementPrincipal,
				notificationsvc.TestAlertDestinationParams,
			) (*notificationsvc.AlertDestinationTestResult, error) {
				serviceCalled = true
				return nil, errFakeNotificationsServiceFnNotSet
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.Update, input.Action)
				assert.Equal(t, model.ResourceAlertDestinations, input.Resource)
				return false, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/alert-destinations/ops-email/test", nil)
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.False(t, serviceCalled)
}

func TestCreateAlertPolicyForwardsSessionPrincipal(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 12, 20, 0, 0, time.UTC)
	policySlug := must.Slug("pipeline-critical")

	app, store := newTestApp(t, testServerConfig{
		notificationsService: &fakeNotificationsService{
			CreateAlertPolicyFn: func(
				_ context.Context,
				principal notificationsvc.AlertManagementPrincipal,
				params notificationsvc.CreateAlertPolicyParams,
			) (*model.AlertPolicy, error) {
				assertSessionAlertPrincipal(t, principal, must.Slug("johndoe"))
				assert.Equal(t, must.Slug("acme"), params.OrgSlug)
				assert.Equal(t, policySlug, params.Slug)
				assert.Equal(t, "Critical pipeline failures", params.Name)
				assert.Equal(t, model.AlertPolicyScopeProjectEnvironment, params.Scope)
				require.NotNil(t, params.ProjectSlug)
				require.NotNil(t, params.EnvironmentSlug)
				assert.Equal(t, must.Slug("operations"), *params.ProjectSlug)
				assert.Equal(t, must.Slug("production"), *params.EnvironmentSlug)
				assert.Equal(t, ptr.Ptr(model.NotificationTypePipelineRunFailed), params.NotificationType)
				assert.Equal(t, ptr.Ptr(model.NotificationSeverityCritical), params.Severity)
				assert.Equal(t, ptr.Ptr(model.NotificationLifecycleStateOpen), params.LifecycleState)
				require.NotNil(t, params.SourceKind)
				require.NotNil(t, params.SourceAction)
				require.NotNil(t, params.SourceRef)
				assert.Equal(t, model.EventEntityTypePipelineRun, *params.SourceKind)
				assert.Equal(t, model.EventActionExecute, *params.SourceAction)
				assert.Equal(t, must.Slug("daily-ledger"), *params.SourceRef)
				assert.Equal(t, []slug.Slug{must.Slug("ops-email")}, params.DestinationSlugs)

				return &model.AlertPolicy{
					Slug:             policySlug,
					Name:             params.Name,
					Scope:            params.Scope,
					ProjectSlug:      params.ProjectSlug,
					EnvironmentSlug:  params.EnvironmentSlug,
					NotificationType: params.NotificationType,
					Severity:         params.Severity,
					LifecycleState:   params.LifecycleState,
					SourceKind:       params.SourceKind,
					SourceAction:     params.SourceAction,
					SourceRef:        params.SourceRef,
					DestinationSlugs: params.DestinationSlugs,
					Enabled:          true,
					CreatedAt:        now,
					UpdatedAt:        now,
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.Create, input.Action)
				assert.Equal(t, model.ResourceAlertPolicies, input.Resource)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/alert-policies", map[string]any{
		"slug":              "pipeline-critical",
		"name":              "Critical pipeline failures",
		"scope":             "project_environment",
		"project_slug":      "operations",
		"environment_slug":  "production",
		"notification_type": "pipeline.run_failed",
		"severity":          "critical",
		"lifecycle_state":   "open",
		"source_kind":       "pipeline_run",
		"source_action":     "execute",
		"source_ref":        "daily-ledger",
		"destination_slugs": []string{"ops-email"},
	})
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestCreateAlertPolicyRejectsInvalidSourceRefSlug(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		notificationsService: &fakeNotificationsService{
			CreateAlertPolicyFn: func(
				_ context.Context,
				_ notificationsvc.AlertManagementPrincipal,
				_ notificationsvc.CreateAlertPolicyParams,
			) (*model.AlertPolicy, error) {
				t.Fatalf("CreateAlertPolicy should not run for invalid source_ref")
				return nil, assert.AnError
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/alert-policies", map[string]any{
		"slug":              "pipeline-critical",
		"name":              "Critical pipeline failures",
		"scope":             "organization",
		"notification_type": "pipeline.run_failed",
		"severity":          "critical",
		"lifecycle_state":   "open",
		"source_kind":       "pipeline_run",
		"source_ref":        "Daily Ledger",
		"destination_slugs": []string{"ops-email"},
	})
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestUpdateAlertPolicy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 30, 12, 30, 0, 0, time.UTC)

	cases := []struct {
		name string
		body map[string]any
		want api.AlertPolicy
	}{
		{
			name: "UpdatesProjectEnvironmentPolicy",
			body: map[string]any{
				"name":              "Critical pipeline failures",
				"scope":             "project_environment",
				"project_slug":      "operations",
				"environment_slug":  "production",
				"notification_type": "pipeline.run_failed",
				"severity":          "critical",
				"lifecycle_state":   "open",
				"source_kind":       "pipeline_run",
				"source_action":     "execute",
				"source_ref":        "daily-ledger",
				"destination_slugs": []string{"ops-email"},
				"enabled":           false,
			},
			want: api.AlertPolicy{
				Slug:             "pipeline-critical",
				Name:             "Critical pipeline failures",
				Scope:            api.AlertPolicyScopeProjectEnvironment,
				ProjectSlug:      ptr.Ptr(api.NullableSlug("operations")),
				EnvironmentSlug:  ptr.Ptr(api.NullableSlug("production")),
				NotificationType: ptr.Ptr(api.NotificationTypePipelineRunFailed),
				Severity:         ptr.Ptr(api.NotificationSeverityCritical),
				LifecycleState:   ptr.Ptr(api.NotificationLifecycleStateOpen),
				SourceKind:       ptr.Ptr(api.EventEntityTypePipelineRun),
				SourceAction:     ptr.Ptr(api.EventActionExecute),
				SourceRef:        ptr.Ptr("daily-ledger"),
				DestinationSlugs: []api.Slug{"ops-email"},
				Enabled:          false,
				CreatedAt:        now.Add(-time.Hour),
				UpdatedAt:        now,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				notificationsService: &fakeNotificationsService{
					UpdateAlertPolicyFn: func(
						_ context.Context,
						principal notificationsvc.AlertManagementPrincipal,
						params notificationsvc.UpdateAlertPolicyParams,
					) (*model.AlertPolicy, error) {
						assertSessionAlertPrincipal(t, principal, must.Slug("johndoe"))
						assert.Equal(t, must.Slug("acme"), params.OrgSlug)
						assert.Equal(t, must.Slug("pipeline-critical"), params.Slug)
						assert.Equal(t, "Critical pipeline failures", params.Name)
						assert.Equal(t, model.AlertPolicyScopeProjectEnvironment, params.Scope)
						require.NotNil(t, params.ProjectSlug)
						require.NotNil(t, params.EnvironmentSlug)
						assert.Equal(t, must.Slug("operations"), *params.ProjectSlug)
						assert.Equal(t, must.Slug("production"), *params.EnvironmentSlug)
						assert.Equal(t, ptr.Ptr(model.NotificationTypePipelineRunFailed), params.NotificationType)
						assert.Equal(t, ptr.Ptr(model.NotificationSeverityCritical), params.Severity)
						assert.Equal(t, ptr.Ptr(model.NotificationLifecycleStateOpen), params.LifecycleState)
						require.NotNil(t, params.SourceKind)
						require.NotNil(t, params.SourceAction)
						require.NotNil(t, params.SourceRef)
						assert.Equal(t, model.EventEntityTypePipelineRun, *params.SourceKind)
						assert.Equal(t, model.EventActionExecute, *params.SourceAction)
						assert.Equal(t, must.Slug("daily-ledger"), *params.SourceRef)
						assert.Equal(t, []slug.Slug{must.Slug("ops-email")}, params.DestinationSlugs)
						assert.False(t, params.Enabled)

						return &model.AlertPolicy{
							Slug:             params.Slug,
							Name:             params.Name,
							Scope:            params.Scope,
							ProjectSlug:      params.ProjectSlug,
							EnvironmentSlug:  params.EnvironmentSlug,
							NotificationType: params.NotificationType,
							Severity:         params.Severity,
							LifecycleState:   params.LifecycleState,
							SourceKind:       params.SourceKind,
							SourceAction:     params.SourceAction,
							SourceRef:        params.SourceRef,
							DestinationSlugs: params.DestinationSlugs,
							Enabled:          params.Enabled,
							CreatedAt:        now.Add(-time.Hour),
							UpdatedAt:        now,
						}, nil
					},
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.Update, input.Action)
						assert.Equal(t, model.ResourceAlertPolicies, input.Resource)
						return true, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodPut, "/organizations/acme/alert-policies/pipeline-critical", tc.body)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assertResponse(t, resp, tc.want)
		})
	}
}

func TestUpdateAlertPolicyForwardsAPIKeyPrincipal(t *testing.T) {
	t.Parallel()

	apiKeySlug := must.Slug("deploy-key")
	apiKeyOrgSlug := must.Slug("acme")
	apiKeyRoleSlug := must.Slug("admin")
	now := time.Date(2026, 6, 30, 12, 40, 0, 0, time.UTC)

	app, _ := newTestApp(t, testServerConfig{
		notificationsService: &fakeNotificationsService{
			UpdateAlertPolicyFn: func(
				_ context.Context,
				principal notificationsvc.AlertManagementPrincipal,
				params notificationsvc.UpdateAlertPolicyParams,
			) (*model.AlertPolicy, error) {
				assertAPIKeyAlertPrincipal(t, principal, apiKeySlug, apiKeyOrgSlug, apiKeyRoleSlug)
				assert.Equal(t, must.Slug("acme"), params.OrgSlug)
				assert.Equal(t, must.Slug("pipeline-critical"), params.Slug)
				assert.Equal(t, "Critical pipeline failures", params.Name)

				return &model.AlertPolicy{
					Slug:             params.Slug,
					Name:             params.Name,
					Scope:            params.Scope,
					ProjectSlug:      params.ProjectSlug,
					EnvironmentSlug:  params.EnvironmentSlug,
					NotificationType: params.NotificationType,
					Severity:         params.Severity,
					LifecycleState:   params.LifecycleState,
					DestinationSlugs: params.DestinationSlugs,
					Enabled:          params.Enabled,
					CreatedAt:        now.Add(-time.Hour),
					UpdatedAt:        now,
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, apiKeySlug, input.APIKeySlug)
				assert.Equal(t, apiKeyRoleSlug, input.APIKeyRoleSlug)
				require.NotNil(t, input.OrgSlug)
				assert.Equal(t, apiKeyOrgSlug, *input.OrgSlug)
				assert.Equal(t, model.Update, input.Action)
				assert.Equal(t, model.ResourceAlertPolicies, input.Resource)
				return true, nil
			},
		},
		middleware: []fiber.Handler{
			func(c fiber.Ctx) error {
				reqctx.SetAPIKeyPrincipal(c, reqctx.APIKeyPrincipal{
					Slug:    apiKeySlug,
					OrgSlug: apiKeyOrgSlug,
					Role:    apiKeyRoleSlug,
				})
				return c.Next()
			},
		},
	})

	req := jsonRequest(t, http.MethodPut, "/organizations/acme/alert-policies/pipeline-critical", map[string]any{
		"name":              "Critical pipeline failures",
		"scope":             "organization",
		"notification_type": "pipeline.run_failed",
		"severity":          "critical",
		"lifecycle_state":   "open",
		"destination_slugs": []string{"ops-email"},
		"enabled":           true,
	})
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
