package http

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func TestCreatePipelineTriggerMapsScheduleRequest(t *testing.T) {
	t.Parallel()

	runtimeInputKeys := []string{"tenant_id", "dry_run"}
	triggerSlug := api.Slug("nightly-users")
	trigger := testHTTPPipelineTrigger()

	app, store := newTestApp(t, testServerConfig{
		pipelineTriggersService: &fakePipelineTriggersService{
			CreatePipelineTriggerFn: func(
				_ context.Context,
				orgSlug, projectSlug, environmentSlug slug.Slug,
				newTrigger model.NewPipelineTrigger,
				actor model.EventActor,
			) (*model.PipelineTrigger, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, must.Slug("nightly-users"), newTrigger.Slug)
				assert.Equal(t, model.PipelineTriggerTypeSchedule, newTrigger.Type)
				assert.Equal(t, must.Slug("sync-users"), newTrigger.PipelineSlug)
				assert.Equal(t, []string{"tenant_id", "dry_run"}, newTrigger.RuntimeInputKeys)
				require.NotNil(t, newTrigger.ScheduleConfig)
				assert.Equal(t, "0 2 * * *", newTrigger.ScheduleConfig.Cron)
				assertUserEventActor(t, actor, must.Slug("alice"))
				return trigger, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Contains(t, []string{"pipeline_triggers:create", "pipelines:execute"}, input.Resource.String()+":"+input.Action.String())
				return true, nil
			},
		},
	})

	resp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(
		t,
		http.MethodPost,
		"/organizations/acme/projects/operations/environments/prod/pipeline-triggers",
		api.NewPipelineTrigger{
			Name:             "Nightly Users",
			Slug:             &triggerSlug,
			Type:             api.PipelineTriggerTypeSchedule,
			Enabled:          true,
			PipelineSlug:     "sync-users",
			VersionPolicy:    testHTTPTriggerLatestPolicyAPI(),
			RuntimeInputKeys: runtimeInputKeys,
			RetryPolicy:      testHTTPTriggerNoRetryPolicyAPI(),
			Schedule: &api.PipelineTriggerScheduleConfig{
				Cron:          "0 2 * * *",
				Timezone:      "UTC",
				MisfirePolicy: api.PipelineTriggerScheduleMisfirePolicySkip,
			},
		},
	))
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	body := parseResponse[api.PipelineTriggerIssuanceResponse](t, resp).PipelineTrigger
	assert.Equal(t, pipelineTriggerToAPI(trigger), body)
	assert.Equal(t, "0 2 * * *", body.Schedule.Cron)
}

func TestPipelineTriggerToAPIPreservesEmptyRuntimeInputKeys(t *testing.T) {
	t.Parallel()

	got := pipelineTriggerToAPI(&model.PipelineTrigger{})

	require.NotNil(t, got.RuntimeInputKeys)
	require.Empty(t, got.RuntimeInputKeys)
}

func TestCreatePipelineTriggerReturnsForbiddenWhenUnauthorized(t *testing.T) {
	t.Parallel()

	runtimeInputKeys := []string{"tenant_id", "dry_run"}
	triggerSlug := api.Slug("nightly-users")
	app, store := newTestApp(t, testServerConfig{
		pipelineTriggersService: &fakePipelineTriggersService{
			CreatePipelineTriggerFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
				slug.Slug,
				model.NewPipelineTrigger,
				model.EventActor,
			) (*model.PipelineTrigger, error) {
				t.Fatal("pipeline trigger service should not be called when authorization is denied")
				return &model.PipelineTrigger{}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.Create, input.Action)
				assert.Equal(t, model.ResourcePipelineTriggers, input.Resource)
				return false, nil
			},
		},
	})

	resp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(
		t,
		http.MethodPost,
		"/organizations/acme/projects/operations/environments/prod/pipeline-triggers",
		api.NewPipelineTrigger{
			Name:             "Nightly Users",
			Slug:             &triggerSlug,
			Type:             api.PipelineTriggerTypeSchedule,
			Enabled:          true,
			PipelineSlug:     "sync-users",
			VersionPolicy:    testHTTPTriggerLatestPolicyAPI(),
			RuntimeInputKeys: runtimeInputKeys,
			RetryPolicy:      testHTTPTriggerNoRetryPolicyAPI(),
			Schedule: &api.PipelineTriggerScheduleConfig{
				Cron:          "0 2 * * *",
				Timezone:      "UTC",
				MisfirePolicy: api.PipelineTriggerScheduleMisfirePolicySkip,
			},
		},
	))
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.Forbidden("pipeline_triggers", "create"))
}

func TestCreatePipelineWebhookTriggerMapsSafeConfigAndOneTimeSecret(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{pipelineTriggersService: &fakePipelineTriggersService{
		CreatePipelineTriggerFn: func(_ context.Context, _, _, environment slug.Slug, input model.NewPipelineTrigger, _ model.EventActor) (*model.PipelineTrigger, error) {
			require.Equal(t, model.PipelineTriggerTypeWebhook, input.Type)
			require.Equal(t, []model.PipelineWebhookInputMapping{{JSONPointer: "/tenant/id", RuntimeInputKey: "tenant_id"}}, input.WebhookConfig.InputMappings)
			return &model.PipelineTrigger{Slug: must.Slug("customer-event"), Name: input.Name, Type: input.Type, EnvironmentSlug: environment, PipelineSlug: input.PipelineSlug, VersionPolicy: input.VersionPolicy, RuntimeInputKeys: input.RuntimeInputKeys, RetryPolicy: input.RetryPolicy, WebhookConfig: &model.PipelineTriggerWebhookConfig{ReceiverID: "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG", VerificationStrategy: "hmac_sha256", InputMappings: input.WebhookConfig.InputMappings, EnabledKeyVersions: []int{1}, SecretConfigured: true}, WebhookSecret: &model.PipelineWebhookSecret{KeyVersion: 1, Value: "one-time-secret"}}, nil
		},
	}, authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) { return true, nil }}})
	body := map[string]any{"name": "Customer event", "type": "webhook", "enabled": false, "pipeline_slug": "sync-users", "version_policy": testHTTPTriggerLatestPolicyAPI(), "runtime_input_keys": []string{"tenant_id"}, "retry_policy": testHTTPTriggerNoRetryPolicyAPI(), "webhook": map[string]any{"input_mappings": []map[string]string{{"json_pointer": "/tenant/id", "runtime_input_key": "tenant_id"}}}}
	resp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodPost, "/organizations/acme/projects/operations/environments/prod/pipeline-triggers", body))
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	got := parseResponse[api.PipelineTriggerIssuanceResponse](t, resp)
	require.NotNil(t, got.PipelineTrigger.Webhook)
	require.Equal(t, "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG", got.PipelineTrigger.Webhook.ReceiverId)
	require.Equal(t, api.PipelineWebhookVerificationStrategyHmacSha256, got.PipelineTrigger.Webhook.VerificationStrategy)
	require.Equal(t, []api.PipelineWebhookInputMapping{{JsonPointer: "/tenant/id", RuntimeInputKey: "tenant_id"}}, got.PipelineTrigger.Webhook.InputMappings)
	require.Equal(t, []int{1}, got.PipelineTrigger.Webhook.EnabledKeyVersions)
	require.True(t, got.PipelineTrigger.Webhook.SecretConfigured)
	require.NotNil(t, got.WebhookSecret)
	require.Equal(t, 1, got.WebhookSecret.KeyVersion)
	require.Equal(t, "one-time-secret", got.WebhookSecret.Value)
}

func TestRotatePipelineWebhookSecret(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{
		pipelineTriggersService: &fakePipelineTriggersService{RotatePipelineWebhookSecretFn: func(
			_ context.Context,
			org, project, environment, trigger slug.Slug,
			actor model.EventActor,
		) (model.PipelineWebhookSecret, error) {
			require.Equal(t, must.Slug("acme"), org)
			require.Equal(t, must.Slug("operations"), project)
			require.Equal(t, must.Slug("prod"), environment)
			require.Equal(t, must.Slug("incoming"), trigger)
			assertUserEventActor(t, actor, must.Slug("alice"))
			return model.PipelineWebhookSecret{KeyVersion: 4, Value: "rotated-secret"}, nil
		}},
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			require.Equal(t, model.Update, input.Action)
			require.Equal(t, model.ResourcePipelineTriggers, input.Resource)
			return true, nil
		}},
	})

	resp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(
		t, http.MethodPost, "/organizations/acme/projects/operations/environments/prod/pipeline-triggers/incoming/webhook-secret/rotate", nil,
	))
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	got := parseResponse[api.PipelineWebhookSecretIssuance](t, resp)
	require.Equal(t, 4, got.KeyVersion)
	require.Equal(t, "rotated-secret", got.Value)
}

func TestRotatePipelineWebhookSecretReturnsForbiddenWhenUnauthorized(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{
		pipelineTriggersService: &fakePipelineTriggersService{RotatePipelineWebhookSecretFn: func(
			context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.EventActor,
		) (model.PipelineWebhookSecret, error) {
			t.Fatal("service must not be called when authorization fails")
			return model.PipelineWebhookSecret{}, nil
		}},
		authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return false, nil }},
	})

	resp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(
		t, http.MethodPost, "/organizations/acme/projects/operations/environments/prod/pipeline-triggers/incoming/webhook-secret/rotate", nil,
	))
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.Forbidden("pipeline_triggers", "update"))
}

func TestRotatePipelineWebhookSecretMapsServiceError(t *testing.T) {
	t.Parallel()
	app, store := newTestApp(t, testServerConfig{
		pipelineTriggersService: &fakePipelineTriggersService{RotatePipelineWebhookSecretFn: func(
			context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.EventActor,
		) (model.PipelineWebhookSecret, error) {
			return model.PipelineWebhookSecret{}, xerror.Unexpected().Detail("rotation failed")
		}},
		authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) { return true, nil }},
	})

	resp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(
		t, http.MethodPost, "/organizations/acme/projects/operations/environments/prod/pipeline-triggers/incoming/webhook-secret/rotate", nil,
	))
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.New(500, "INTERNAL_ERROR", "internal error"))
}

func TestPipelineTriggerReadUpdateAndDeleteHandlers(t *testing.T) {
	t.Parallel()

	trigger := testHTTPPipelineTrigger()
	nextCursor := "next-page"
	runtimeInputKeys := []string{"tenant_id"}
	expectedActions := []string{"pipeline_triggers:read", "pipeline_triggers:read", "pipeline_triggers:update", "pipelines:execute", "pipeline_triggers:delete"}
	authCall := 0

	app, store := newTestApp(t, testServerConfig{
		pipelineTriggersService: &fakePipelineTriggersService{
			ListPipelineTriggersFn: func(
				_ context.Context,
				orgSlug, projectSlug, environmentSlug slug.Slug,
				req model.ListRequest,
			) (model.ListResponse[model.PipelineTrigger], error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, 25, req.Limit)
				return model.ListResponse[model.PipelineTrigger]{
					Items:      []model.PipelineTrigger{*trigger},
					HasMore:    true,
					NextCursor: &nextCursor,
				}, nil
			},
			GetPipelineTriggerFn: func(
				_ context.Context,
				orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
			) (*model.PipelineTrigger, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, must.Slug("nightly-users"), triggerSlug)
				return trigger, nil
			},
			UpdatePipelineTriggerFn: func(
				_ context.Context,
				orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
				update model.UpdatePipelineTrigger,
				actor model.EventActor,
			) (*model.PipelineTrigger, error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, must.Slug("nightly-users"), triggerSlug)
				assertUserEventActor(t, actor, must.Slug("alice"))

				updated := *trigger
				updated.Name = update.Name
				updated.Type = update.Type
				updated.Enabled = update.Enabled
				updated.Revision = 2
				updated.RuntimeInputKeys = update.RuntimeInputKeys
				updated.UpdatedBy = actor
				updated.UpdatedAt = trigger.UpdatedAt.Add(time.Hour)

				require.NotNil(t, update.ScheduleConfig)
				assert.Equal(t, "0 * * * *", update.ScheduleConfig.Cron)
				updated.ScheduleConfig = update.ScheduleConfig
				return &updated, nil
			},
			DeletePipelineTriggerFn: func(
				_ context.Context,
				orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
				actor model.EventActor,
			) error {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, must.Slug("nightly-users"), triggerSlug)
				assertUserEventActor(t, actor, must.Slug("alice"))
				return nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Less(t, authCall, len(expectedActions))
				assert.Equal(t, expectedActions[authCall], input.Resource.String()+":"+input.Action.String())
				authCall++
				return true, nil
			},
		},
	})

	listResp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodGet, "/organizations/acme/projects/operations/environments/prod/pipeline-triggers?limit=25", nil))
	defer func() { require.NoError(t, listResp.Body.Close()) }()
	require.Equal(t, http.StatusOK, listResp.StatusCode)
	assertResponse(t, listResp, api.PipelineTriggerListResponse{
		PipelineTriggers: []api.PipelineTrigger{pipelineTriggerToAPI(trigger)},
		HasMore:          true,
		NextCursor:       &nextCursor,
	})

	getResp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodGet, "/organizations/acme/projects/operations/environments/prod/pipeline-triggers/nightly-users", nil))
	defer func() { require.NoError(t, getResp.Body.Close()) }()
	require.Equal(t, http.StatusOK, getResp.StatusCode)
	assertResponse(t, getResp, pipelineTriggerToAPI(trigger))

	updateResp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(
		t,
		http.MethodPut,
		"/organizations/acme/projects/operations/environments/prod/pipeline-triggers/nightly-users",
		api.UpdatePipelineTrigger{
			Name:             "Hourly Users",
			Type:             api.PipelineTriggerTypeSchedule,
			Enabled:          false,
			PipelineSlug:     "sync-users",
			VersionPolicy:    testHTTPTriggerLatestPolicyAPI(),
			RuntimeInputKeys: runtimeInputKeys,
			RetryPolicy:      testHTTPTriggerNoRetryPolicyAPI(),
			Schedule: &api.PipelineTriggerScheduleConfig{
				Cron:          "0 * * * *",
				Timezone:      "UTC",
				MisfirePolicy: api.PipelineTriggerScheduleMisfirePolicySkip,
			},
		},
	))
	defer func() { require.NoError(t, updateResp.Body.Close()) }()
	require.Equal(t, http.StatusOK, updateResp.StatusCode)
	updatedBody := parseResponse[api.PipelineTrigger](t, updateResp)
	assert.Equal(t, api.PipelineTriggerTypeSchedule, updatedBody.Type)
	assert.Equal(t, "0 * * * *", updatedBody.Schedule.Cron)

	deleteResp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodDelete, "/organizations/acme/projects/operations/environments/prod/pipeline-triggers/nightly-users", nil))
	defer func() { require.NoError(t, deleteResp.Body.Close()) }()
	require.Equal(t, http.StatusNoContent, deleteResp.StatusCode)
	require.Equal(t, len(expectedActions), authCall)
}

func TestListPipelineScheduleFiresMapsDiagnostics(t *testing.T) {
	t.Parallel()

	runSlug := must.Slug("run-nightly-users")
	sourceEventSlug := must.Slug("evt-schedule-fire")
	errorSummary := "connector timeout"
	nextCursor := "2026-06-07T12:00:00Z_fire-nightly-users"
	fire := model.PipelineScheduleFire{
		FireSlug:        must.Slug("fire-nightly-users"),
		TriggerSlug:     must.Slug("nightly-users"),
		TriggerRevision: 3,
		PipelineSlug:    must.Slug("sync-users"),
		ScheduledFireAt: time.Date(2026, time.June, 7, 12, 0, 0, 0, time.UTC),
		ActualFireAt:    time.Date(2026, time.June, 7, 12, 0, 5, 0, time.UTC),
		Status:          model.PipelineScheduleFireStatusFailed,
		MisfireResult:   model.PipelineTriggerScheduleMisfireResultFireOnce,
		RunSlug:         &runSlug,
		SourceEventSlug: &sourceEventSlug,
		ErrorSummary:    &errorSummary,
		Timestamps: model.Timestamps{
			CreatedAt: time.Date(2026, time.June, 7, 12, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, time.June, 7, 12, 1, 0, 0, time.UTC),
		},
	}

	app, store := newTestApp(t, testServerConfig{
		pipelineTriggersService: &fakePipelineTriggersService{
			ListPipelineScheduleFiresFn: func(
				_ context.Context,
				orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
				req model.ListRequest,
			) (model.ListResponse[model.PipelineScheduleFire], error) {
				assert.Equal(t, must.Slug("acme"), orgSlug)
				assert.Equal(t, must.Slug("operations"), projectSlug)
				assert.Equal(t, must.Slug("prod"), environmentSlug)
				assert.Equal(t, must.Slug("nightly-users"), triggerSlug)
				assert.Equal(t, 10, req.Limit)
				assert.Equal(t, "prev-fire", req.Cursor)
				assert.Equal(t, model.ListDirectionPrev, req.Direction)
				return model.ListResponse[model.PipelineScheduleFire]{
					Items:      []model.PipelineScheduleFire{fire},
					HasMore:    true,
					NextCursor: &nextCursor,
				}, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				assert.Equal(t, model.ResourcePipelineTriggers, input.Resource)
				assert.Equal(t, model.Read, input.Action)
				return true, nil
			},
		},
	})

	resp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(
		t,
		http.MethodGet,
		"/organizations/acme/projects/operations/environments/prod/pipeline-triggers/nightly-users/fires?limit=10&cursor=prev-fire&direction=prev",
		nil,
	))
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assertResponse(t, resp, api.PipelineScheduleFireListResponse{
		ScheduleFires: []api.PipelineScheduleFire{pipelineScheduleFireToAPI(&fire)},
		HasMore:       true,
		NextCursor:    &nextCursor,
	})
}

func TestPipelineTriggerReadUpdateAndDeleteHandlersReturnForbiddenWhenUnauthorized(t *testing.T) {
	t.Parallel()

	runtimeInputKeys := []string{"tenant_id"}
	updateBody := api.UpdatePipelineTrigger{
		Name:             "Nightly Users",
		Type:             api.PipelineTriggerTypeSchedule,
		Enabled:          true,
		PipelineSlug:     "sync-users",
		VersionPolicy:    testHTTPTriggerLatestPolicyAPI(),
		RuntimeInputKeys: runtimeInputKeys,
		RetryPolicy:      testHTTPTriggerNoRetryPolicyAPI(),
		Schedule: &api.PipelineTriggerScheduleConfig{
			Cron:          "0 2 * * *",
			Timezone:      "UTC",
			MisfirePolicy: api.PipelineTriggerScheduleMisfirePolicySkip,
		},
	}

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		wantAction model.Action
	}{
		{
			name:       "List",
			method:     http.MethodGet,
			path:       "/organizations/acme/projects/operations/environments/prod/pipeline-triggers",
			wantAction: model.Read,
		},
		{
			name:       "Get",
			method:     http.MethodGet,
			path:       "/organizations/acme/projects/operations/environments/prod/pipeline-triggers/nightly-users",
			wantAction: model.Read,
		},
		{
			name:       "Update",
			method:     http.MethodPut,
			path:       "/organizations/acme/projects/operations/environments/prod/pipeline-triggers/nightly-users",
			body:       updateBody,
			wantAction: model.Update,
		},
		{
			name:       "Delete",
			method:     http.MethodDelete,
			path:       "/organizations/acme/projects/operations/environments/prod/pipeline-triggers/nightly-users",
			wantAction: model.Delete,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app, store := newTestApp(t, testServerConfig{
				authService: fakeAuthService{
					CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
						assert.Equal(t, model.ResourcePipelineTriggers, input.Resource)
						assert.Equal(t, tt.wantAction, input.Action)
						return false, nil
					},
				},
			})

			resp := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, tt.method, tt.path, tt.body))
			defer func() { require.NoError(t, resp.Body.Close()) }()

			require.Equal(t, http.StatusForbidden, resp.StatusCode)
			assertErrorResponse(t, resp, apierror.Forbidden("pipeline_triggers", tt.wantAction.String()))
		})
	}
}

func TestPipelineTriggerAPIMappersRejectUnsupportedEnums(t *testing.T) {
	t.Parallel()

	_, err := pipelineTriggerTypeFromAPI(api.PipelineTriggerType("unsupported"))
	require.Error(t, err)

	_, err = pipelineTriggerVersionPolicyFromAPI(api.PipelineTriggerVersionPolicy{
		Mode: api.PipelineTriggerVersionPolicyMode("unsupported"),
	})
	require.Error(t, err)

	_, err = pipelineTriggerRetryPolicyFromAPI(api.PipelineTriggerRetryPolicy{
		BackoffStrategy: api.PipelineTriggerBackoffStrategy("unsupported"),
	})
	require.Error(t, err)

	_, err = pipelineTriggerRetryFailureKindsFromAPI([]api.PipelineTriggerRetryFailureKind{
		api.PipelineTriggerRetryFailureKind("unsupported"),
	})
	require.Error(t, err)

	_, err = pipelineTriggerScheduleConfigFromAPI(&api.PipelineTriggerScheduleConfig{
		Cron:          "0 * * * *",
		Timezone:      "UTC",
		MisfirePolicy: api.PipelineTriggerScheduleMisfirePolicy("unsupported"),
	})
	require.Error(t, err)
}

func testHTTPPipelineTrigger() *model.PipelineTrigger {
	actor := model.UserEventActor(must.Slug("alice"))
	return &model.PipelineTrigger{
		Slug:            must.Slug("nightly-users"),
		Name:            "Nightly Users",
		Type:            model.PipelineTriggerTypeSchedule,
		Enabled:         true,
		Revision:        1,
		EnvironmentSlug: must.Slug("prod"),
		PipelineSlug:    must.Slug("sync-users"),
		VersionPolicy: model.PipelineTriggerVersionPolicy{
			Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed,
		},
		RuntimeInputKeys: []string{"customer_id", "dry_run"},
		RetryPolicy: model.PipelineTriggerRetryPolicy{
			Enabled:         false,
			MaxAttempts:     1,
			BackoffStrategy: model.PipelineTriggerBackoffStrategyNone,
		},
		ScheduleConfig: &model.PipelineTriggerScheduleConfig{
			Cron:          "0 2 * * *",
			Timezone:      "UTC",
			MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip,
		},
		CreatedBy: actor,
		UpdatedBy: actor,
		Timestamps: model.Timestamps{
			CreatedAt: time.Date(2026, time.June, 7, 12, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, time.June, 7, 12, 0, 0, 0, time.UTC),
		},
	}
}

func testHTTPTriggerLatestPolicyAPI() api.PipelineTriggerVersionPolicy {
	return api.PipelineTriggerVersionPolicy{
		Mode: api.PipelineTriggerVersionPolicyModeLatestDeployed,
	}
}

func testHTTPTriggerNoRetryPolicyAPI() api.PipelineTriggerRetryPolicy {
	return api.PipelineTriggerRetryPolicy{
		Enabled:               false,
		MaxAttempts:           1,
		BackoffStrategy:       api.PipelineTriggerBackoffStrategyNone,
		RetryableFailureKinds: []api.PipelineTriggerRetryFailureKind{},
	}
}

func TestCompletionTriggerRequiresReadManagementAndExecutionAuthority(t *testing.T) {
	for _, denied := range []string{"", "pipelines:read", "pipeline_triggers:update", "pipelines:execute"} {
		t.Run(denied, func(t *testing.T) {
			called := false
			trigger := testHTTPPipelineTrigger()
			trigger.Type = model.PipelineTriggerTypeCompletion
			trigger.ScheduleConfig = nil
			trigger.CompletionConfig = &model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "upstream", Condition: model.PipelineCompletionConditionSuccess, ExecutionGrantEnabled: true}
			app, store := newTestApp(t, testServerConfig{
				pipelineTriggersService: &fakePipelineTriggersService{CreatePipelineTriggerFn: func(_ context.Context, _, _, _ slug.Slug, input model.NewPipelineTrigger, _ model.EventActor) (*model.PipelineTrigger, error) {
					called = true
					require.Equal(t, trigger.CompletionConfig, input.CompletionConfig)
					return trigger, nil
				}},
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					return input.Resource.String()+":"+input.Action.String() != denied, nil
				}},
			})
			response := authenticatedRequest(t, app, store, "alice", jsonRequest(t, http.MethodPost, "/organizations/acme/projects/operations/environments/prod/pipeline-triggers", api.NewPipelineTrigger{
				Name: "After upstream", Type: api.PipelineTriggerTypeCompletion, Enabled: true, PipelineSlug: "sync-users", VersionPolicy: testHTTPTriggerLatestPolicyAPI(), RuntimeInputKeys: []string{}, RetryPolicy: testHTTPTriggerNoRetryPolicyAPI(),
				Completion: &api.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "upstream", Condition: api.PipelineCompletionConditionSuccess, ExecutionGrantEnabled: true},
			}))
			defer func() { require.NoError(t, response.Body.Close()) }()
			if denied == "" {
				require.Equal(t, http.StatusCreated, response.StatusCode)
				require.True(t, called)
			} else {
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				require.False(t, called)
			}
		})
	}
}

func TestCompletionTriggerRevocationOnlyRequiresTriggerManagement(t *testing.T) {
	trigger := testHTTPPipelineTrigger()
	trigger.Type = model.PipelineTriggerTypeCompletion
	trigger.ScheduleConfig = nil
	trigger.Enabled = false
	trigger.CompletionConfig = &model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "upstream", Condition: model.PipelineCompletionConditionSuccess, ExecutionGrantEnabled: false}
	called := false
	app, store := newTestApp(t, testServerConfig{
		pipelineTriggersService: &fakePipelineTriggersService{UpdatePipelineTriggerFn: func(_ context.Context, _, _, _, _ slug.Slug, input model.UpdatePipelineTrigger, _ model.EventActor) (*model.PipelineTrigger, error) {
			called = true
			require.False(t, input.Enabled)
			require.False(t, input.CompletionConfig.ExecutionGrantEnabled)
			return trigger, nil
		}},
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			return input.Resource == model.ResourcePipelineTriggers && input.Action == model.Update, nil
		}},
	})
	response := authenticatedRequest(t, app, store, "alice", jsonRequest(t, http.MethodPut, "/organizations/acme/projects/operations/environments/prod/pipeline-triggers/after-upstream", api.UpdatePipelineTrigger{
		Name: "After upstream", Type: api.PipelineTriggerTypeCompletion, Enabled: false, PipelineSlug: "sync-users", VersionPolicy: testHTTPTriggerLatestPolicyAPI(), RuntimeInputKeys: []string{}, RetryPolicy: testHTTPTriggerNoRetryPolicyAPI(),
		Completion: &api.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "upstream", Condition: api.PipelineCompletionConditionSuccess, ExecutionGrantEnabled: false},
	}))
	defer func() { require.NoError(t, response.Body.Close()) }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.True(t, called)
}
