package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
)

var testOrg = &model.Organization{
	Name: "Acme Corp",
	Slug: "acme",
	Timestamps: model.Timestamps{
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	},
}

var (
	testOrgAPI         = orgToHTTP(testOrg)
	testOrgWithRoleAPI = orgWithRoleToHTTP(
		model.OrganizationWithRole{Organization: *testOrg, MemberRole: "admin"},
	)
)

func TestListOrganizations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		authAllowed bool
		listOrgsFn  func(ctx context.Context, userSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.OrganizationWithRole], error)
		wantStatus  int
		wantBody    *api.OrganizationListResponse
		wantErr     *apierror.Error
	}{
		{
			name:        "ReturnsOrganizationsList",
			authAllowed: true,
			listOrgsFn: func(_ context.Context, _ slug.Slug, _ model.ListRequest) (model.ListResponse[model.OrganizationWithRole], error) {
				return model.ListResponse[model.OrganizationWithRole]{
					Items: []model.OrganizationWithRole{
						{Organization: *testOrg, MemberRole: "admin"},
					},
				}, nil
			},
			wantStatus: http.StatusOK,
			wantBody: &api.OrganizationListResponse{
				Organizations: []api.OrganizationWithRole{testOrgWithRoleAPI},
				HasMore:       false,
			},
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("organizations", "read"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				orgService: &fakeOrgService{
					ListOrganizationsFn: tt.listOrgsFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodGet, "/organizations", nil)
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

func TestCreateOrganization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        any
		authAllowed bool
		createOrgFn func(ctx context.Context, userSlug slug.Slug, actor model.EventActor, newOrg model.NewOrganization) (*model.Organization, error)
		wantStatus  int
		wantBody    *api.Organization
		wantErr     *apierror.Error
	}{
		{
			name:        "CreatesOrganizationAndReturns201",
			body:        api.NewOrganization{Name: "Acme Corp", Slug: ptr.Ptr(api.Slug("acme"))},
			authAllowed: true,
			createOrgFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ model.NewOrganization) (*model.Organization, error) {
				return testOrg, nil
			},
			wantStatus: http.StatusCreated,
			wantBody:   &testOrgAPI,
		},
		{
			name:        "Returns409WhenSlugAlreadyExists",
			body:        api.NewOrganization{Name: "Acme Corp", Slug: ptr.Ptr(api.Slug("acme"))},
			authAllowed: true,
			createOrgFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ model.NewOrganization) (*model.Organization, error) {
				return nil, orgsvc.ErrSlugAlreadyExists
			},
			wantStatus: http.StatusConflict,
			wantErr: apierror.New(
				409,
				"ORGANIZATION_ALREADY_EXISTS",
				"organization already exists",
			),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			body:        api.NewOrganization{Name: "Acme Corp", Slug: ptr.Ptr(api.Slug("acme"))},
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("organizations", "create"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				orgService: &fakeOrgService{
					CreateOrganizationFn: tt.createOrgFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodPost, "/organizations", tt.body)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody != nil {
				assertResponse(t, resp, tt.wantBody)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestGetOrganization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		slug        string
		authAllowed bool
		getOrgFn    func(ctx context.Context, slug slug.Slug) (*model.Organization, error)
		wantStatus  int
		wantBody    *api.Organization
		wantErr     *apierror.Error
	}{
		{
			name:        "ReturnsOrganizationBySlug",
			slug:        "acme",
			authAllowed: true,
			getOrgFn: func(_ context.Context, _ slug.Slug) (*model.Organization, error) {
				return testOrg, nil
			},
			wantStatus: http.StatusOK,
			wantBody:   &testOrgAPI,
		},
		{
			name:        "Returns404WhenOrganizationNotFound",
			slug:        "nonexistent",
			authAllowed: true,
			getOrgFn: func(_ context.Context, _ slug.Slug) (*model.Organization, error) {
				return nil, orgsvc.ErrOrganizationNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "ORGANIZATION_NOT_FOUND", "organization not found"),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			slug:        "acme",
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("organizations", "read"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				orgService: &fakeOrgService{
					GetOrganizationFn: tt.getOrgFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodGet, "/organizations/"+tt.slug, nil)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantBody != nil {
				assertResponse(t, resp, tt.wantBody)
			}
			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestDeleteOrganization(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		confirmation string
		allowed      bool
		serviceErr   error
		status       int
		called       bool
	}{
		{name: "Deletes", confirmation: "acme", allowed: true, status: http.StatusNoContent, called: true},
		{name: "RequiresConfirmation", confirmation: "wrong", allowed: true, status: http.StatusBadRequest},
		{name: "Denied", confirmation: "acme", status: http.StatusForbidden},
		{name: "WorkloadBlocks", confirmation: "acme", allowed: true, serviceErr: orgsvc.ErrDeletionBlocked, status: http.StatusConflict, called: true},
		{name: "Missing", confirmation: "acme", allowed: true, serviceErr: orgsvc.ErrOrganizationNotFound, status: http.StatusNotFound, called: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			called := false
			app, store := newTestApp(t, testServerConfig{
				orgService: &fakeOrgService{DeleteOrganizationFn: func(_ context.Context, s slug.Slug) error {
					require.Equal(t, slug.Slug("acme"), s)
					called = true
					return test.serviceErr
				}},
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.Delete, input.Action)
					require.Equal(t, model.ResourceOrganizations, input.Resource)
					require.NotNil(t, input.OrgSlug)
					require.Equal(t, slug.Slug("acme"), *input.OrgSlug)
					return test.allowed, nil
				}},
			})
			req := jsonRequest(t, http.MethodDelete, "/organizations/acme", api.DeleteOrganizationJSONBody{Confirmation: test.confirmation})
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
			require.Equal(t, test.status, resp.StatusCode)
			require.Equal(t, test.called, called)
			if xerror.Is(test.serviceErr, orgsvc.ErrDeletionBlocked) {
				var payload apierror.ErrorResponse
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
				require.NotNil(t, payload.Error)
				require.Equal(t, "ORGANIZATION_DELETION_BLOCKED", payload.Error.Code)
				require.Contains(t, payload.Error.Message, "wait for Snowflake workload identity cleanup")
			}
		})
	}
}

func TestUpdateOrganization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		slug        string
		body        any
		authAllowed bool
		updateOrgFn func(ctx context.Context, slug slug.Slug, actor model.EventActor, update model.UpdateOrganization) (*model.Organization, error)
		wantStatus  int
		wantErr     *apierror.Error
	}{
		{
			name:        "UpdatesOrganizationAndReturns204",
			slug:        "acme",
			body:        api.UpdateOrganization{Name: "New Name"},
			authAllowed: true,
			updateOrgFn: func(_ context.Context, _ slug.Slug, actor model.EventActor, _ model.UpdateOrganization) (*model.Organization, error) {
				require.Equal(t, model.EventActorTypeUser, actor.Type)
				require.NotNil(t, actor.Slug)
				require.Equal(t, must.Slug("johndoe"), *actor.Slug)
				return testOrg, nil
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:        "Returns404WhenOrganizationNotFound",
			slug:        "nonexistent",
			body:        api.UpdateOrganization{Name: "New Name"},
			authAllowed: true,
			updateOrgFn: func(_ context.Context, _ slug.Slug, _ model.EventActor, _ model.UpdateOrganization) (*model.Organization, error) {
				return nil, orgsvc.ErrOrganizationNotFound
			},
			wantStatus: http.StatusNotFound,
			wantErr:    apierror.New(404, "ORGANIZATION_NOT_FOUND", "organization not found"),
		},
		{
			name:        "Returns403WhenAuthorizationDenied",
			slug:        "acme",
			body:        api.UpdateOrganization{Name: "New Name"},
			authAllowed: false,
			wantStatus:  http.StatusForbidden,
			wantErr:     apierror.Forbidden("organizations", "update"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				orgService: &fakeOrgService{
					UpdateOrganizationFn: tt.updateOrgFn,
				},
				authService: fakeAuthService{
					CanFn: func(_ context.Context, _ authorization.Input) (bool, error) {
						return tt.authAllowed, nil
					},
				},
			})

			req := jsonRequest(t, http.MethodPut, "/organizations/"+tt.slug, tt.body)
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() {
				require.NoError(t, resp.Body.Close())
			}()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)

			if tt.wantErr != nil {
				assertErrorResponse(t, resp, tt.wantErr)
			}
		})
	}
}

func TestOrganizationRetentionPolicy(t *testing.T) {
	t.Parallel()

	t.Run("Returns403WhenAuthorizationDenied", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			orgService: &fakeOrgService{},
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.Read, input.Action)
					require.Equal(t, model.ResourceOrganizations, input.Resource)
					return false, nil
				},
			},
		})

		resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), jsonRequest(t, http.MethodGet, "/organizations/acme/retention", nil))
		defer func() {
			require.NoError(t, resp.Body.Close())
		}()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("Returns403WhenUpdateAuthorizationDenied", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			orgService: &fakeOrgService{},
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.Update, input.Action)
					require.Equal(t, model.ResourceOrganizations, input.Resource)
					return false, nil
				},
			},
		})

		resp := authenticatedRequest(
			t,
			app,
			store,
			must.Slug("johndoe"),
			jsonRequest(
				t,
				http.MethodPut,
				"/organizations/acme/retention",
				api.OrganizationRetentionPolicy{},
			),
		)
		defer func() {
			require.NoError(t, resp.Body.Close())
		}()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	policy := model.DefaultOrganizationRetentionPolicy()
	app, store := newTestApp(t, testServerConfig{orgService: &fakeOrgService{
		GetRetentionPolicyFn: func(_ context.Context, orgSlug slug.Slug) (model.OrganizationRetentionPolicy, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			return policy, nil
		},
		UpdateRetentionPolicyFn: func(_ context.Context, orgSlug slug.Slug, _ model.EventActor, got model.OrganizationRetentionPolicy) (model.OrganizationRetentionPolicy, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, 14, got.QueryExecutionRetentionDays)
			require.Equal(t, 7, got.ReplayCheckpointRetentionDays)
			require.Equal(t, 30, got.RunHistoryRetentionDays)
			require.Equal(t, 30, got.EventLogRetentionDays)
			return got, nil
		},
	}})
	userSlug := must.Slug("johndoe")
	resp := authenticatedRequest(t, app, store, userSlug, jsonRequest(t, http.MethodGet, "/organizations/acme/retention", nil))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	updated := retentionPolicyToHTTP(policy)
	updated.QueryExecutionRetentionDays = 14
	resp = authenticatedRequest(t, app, store, userSlug, jsonRequest(t, http.MethodPut, "/organizations/acme/retention", updated))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
}

func TestOrganizationRetentionRuns(t *testing.T) {
	t.Parallel()

	t.Run("RequiresOrganizationReadPermission", func(t *testing.T) {
		t.Parallel()

		app, store := newTestApp(t, testServerConfig{
			orgService:       &fakeOrgService{},
			retentionService: &fakeRetentionService{},
			authService: fakeAuthService{
				CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.Read, input.Action)
					require.Equal(t, model.ResourceOrganizations, input.Resource)
					require.Equal(t, must.Slug("acme"), *input.OrgSlug)
					return false, nil
				},
			},
		})

		resp := authenticatedRequest(
			t,
			app,
			store,
			must.Slug("johndoe"),
			jsonRequest(t, http.MethodGet, "/organizations/acme/retention/runs", nil),
		)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("ReturnsOnlySanitizedProductFields", func(t *testing.T) {
		t.Parallel()

		startedAt := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
		completedAt := startedAt.Add(2 * time.Second)
		policy := model.DefaultOrganizationRetentionPolicy()
		app, store := newTestApp(t, testServerConfig{
			orgService: &fakeOrgService{
				GetOrganizationFn: func(_ context.Context, orgSlug slug.Slug) (*model.Organization, error) {
					require.Equal(t, must.Slug("acme"), orgSlug)
					return &model.Organization{Slug: orgSlug}, nil
				},
			},
			retentionService: &fakeRetentionService{
				ListMaintenanceRunsFn: func(_ context.Context, orgSlug slug.Slug) ([]model.OrganizationRetentionRun, error) {
					require.Equal(t, must.Slug("acme"), orgSlug)
					return []model.OrganizationRetentionRun{
						{
							Slug:                     must.Slug("retention-run-succeeded"),
							Attempt:                  1,
							Status:                   model.OrganizationRetentionRunStatusSucceeded,
							PolicySnapshot:           &policy,
							QueryExecutionsDeleted:   4,
							ReplayCheckpointsDeleted: 3,
							PipelineRunsDeleted:      2,
							EventsDeleted:            1,
							StartedAt:                startedAt,
							CompletedAt:              completedAt,
						},
						{
							Slug:           must.Slug("retention-run-failed"),
							Attempt:        2,
							Status:         model.OrganizationRetentionRunStatusFailed,
							PolicySnapshot: &policy,
							FailureCode:    model.OrganizationRetentionRunFailureCodeMaintenanceFailed,
							StartedAt:      startedAt,
							CompletedAt:    completedAt,
						},
					}, nil
				},
			},
		})

		resp := authenticatedRequest(
			t,
			app,
			store,
			must.Slug("johndoe"),
			jsonRequest(t, http.MethodGet, "/organizations/acme/retention/runs", nil),
		)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var payload map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
		assert.ElementsMatch(t, []string{"schedule", "runs"}, stringMapKeys(payload))

		runs, ok := payload["runs"].([]any)
		require.True(t, ok)
		require.Len(t, runs, 2)
		succeededRun, ok := runs[0].(map[string]any)
		require.True(t, ok)
		assert.ElementsMatch(
			t,
			[]string{"slug", "status", "attempt", "policy_snapshot", "outcome", "failure_code", "started_at", "completed_at"},
			stringMapKeys(succeededRun),
		)
		assert.Nil(t, succeededRun["failure_code"])
		outcome, ok := succeededRun["outcome"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, map[string]any{
			"query_executions_deleted":   float64(4),
			"replay_checkpoints_deleted": float64(3),
			"pipeline_runs_deleted":      float64(2),
			"event_logs_deleted":         float64(1),
		}, outcome)

		failedRun, ok := runs[1].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "maintenance_failed", failedRun["failure_code"])
		assert.Equal(t, map[string]any{
			"query_executions_deleted":   float64(0),
			"replay_checkpoints_deleted": float64(0),
			"pipeline_runs_deleted":      float64(0),
			"event_logs_deleted":         float64(0),
		}, failedRun["outcome"])
		rendered := fmt.Sprint(payload)
		assert.NotContains(t, rendered, "source_job")
		assert.NotContains(t, rendered, "queue")
		assert.NotContains(t, rendered, "sql")
		assert.NotContains(t, rendered, "connector")
		assert.NotContains(t, rendered, "credential")
	})
}

func stringMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
