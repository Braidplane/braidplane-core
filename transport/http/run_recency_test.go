package http

import (
	"context"
	"net/http"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/runrecencysvc"
	"github.com/stretchr/testify/require"
)

type fakeRunRecencyService struct {
	inspect func(context.Context, runrecencysvc.Scope, time.Time) (*runrecencysvc.Inspection, error)
	update  func(context.Context, runrecencysvc.Scope, int64, time.Duration, bool, time.Time) (*runrecencysvc.Expectation, error)
}

func (f fakeRunRecencyService) Inspect(ctx context.Context, s runrecencysvc.Scope, now time.Time) (*runrecencysvc.Inspection, error) {
	return f.inspect(ctx, s, now)
}

func (f fakeRunRecencyService) Update(ctx context.Context, s runrecencysvc.Scope, r int64, age time.Duration, enabled bool, now time.Time) (*runrecencysvc.Expectation, error) {
	return f.update(ctx, s, r, age, enabled, now)
}

func TestRunRecencyAPI(t *testing.T) {
	for _, operation := range []string{"read", "create", "edit", "disable", "conflict", "denied", "other-tenant", "invalid-age", "missing-deployment"} {
		t.Run(operation, func(t *testing.T) {
			calls, updates := 0, 0
			service := fakeRunRecencyService{}
			service.inspect = func(_ context.Context, s runrecencysvc.Scope, now time.Time) (*runrecencysvc.Inspection, error) {
				calls++
				require.Equal(t, slug.Slug("ops"), s.Project)
				require.Equal(t, slug.Slug("dev"), s.Environment)
				require.Equal(t, slug.Slug("orders"), s.Pipeline)
				if s.Org != "acme" {
					return nil, xerror.NotFound(xerror.Pipeline)
				}
				return &runrecencysvc.Inspection{Expectation: runrecencysvc.Expectation{Scope: s, Revision: 2, MaximumAge: time.Hour, Enabled: true, DeploymentID: 3, DeploymentRevision: 7, EnabledAt: now, DeployedAt: now}, Outcome: model.RunRecencyOutcomeGrace, EvaluatedAt: now}, nil
			}
			service.update = func(_ context.Context, s runrecencysvc.Scope, revision int64, age time.Duration, enabled bool, _ time.Time) (*runrecencysvc.Expectation, error) {
				updates++
				require.Equal(t, slug.Slug("acme"), s.Org)
				require.Equal(t, time.Hour, age)
				require.Equal(t, operation != "disable", enabled)
				expected := int64(0)
				if operation == "edit" || operation == "disable" {
					expected = 1
				}
				require.Equal(t, expected, revision)
				if operation == "conflict" {
					return nil, runrecencysvc.ErrRevisionConflict
				}
				return &runrecencysvc.Expectation{Revision: revision + 1}, nil
			}
			cfg := testServerConfig{runRecencyService: service, pipelinesService: &fakePipelinesService{GetDeploymentFn: func(_ context.Context, org, project, environment, pipeline slug.Slug) (*pipelinesvc.PipelineDeploymentDetail, error) {
				if operation == "missing-deployment" {
					return nil, xerror.NotFound(xerror.Pipeline)
				}
				return &pipelinesvc.PipelineDeploymentDetail{}, nil
			}}, authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, model.ResourcePipelines, input.Resource)
				return operation != "denied", nil
			}}}
			app, store := newTestApp(t, cfg)
			method := http.MethodPut
			var body any = map[string]any{"expected_revision": 0, "maximum_age_seconds": 3600, "enabled": operation != "disable"}
			switch operation {
			case "read", "other-tenant":
				method = http.MethodGet
				body = nil
			case "edit", "disable":
				body = map[string]any{"expected_revision": 1, "maximum_age_seconds": 3600, "enabled": operation != "disable"}
			case "invalid-age":
				body = map[string]any{"expected_revision": 0, "maximum_age_seconds": 9223372037, "enabled": true}
			}
			org := "acme"
			if operation == "other-tenant" {
				org = "other"
			}
			req := jsonRequest(t, method, "/organizations/"+org+"/projects/ops/environments/dev/pipelines/orders/successful-run-recency", body)
			resp := authenticatedRequest(t, app, store, "test-user", req)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			switch operation {
			case "other-tenant", "missing-deployment":
				require.Equal(t, 404, resp.StatusCode)
				require.Zero(t, updates)
			case "denied":
				require.Equal(t, 403, resp.StatusCode)
				require.Zero(t, calls)
				require.Zero(t, updates)
			case "conflict":
				require.Equal(t, 409, resp.StatusCode)
			case "invalid-age":
				require.Equal(t, 400, resp.StatusCode)
				require.Zero(t, updates)
			default:
				require.Equal(t, 200, resp.StatusCode)
				result := parseResponse[api.SuccessfulRunRecency](t, resp)
				require.EqualValues(t, 7, *result.DeploymentRevision)
				require.True(t, result.CapabilityEnabled)
			}
		})
	}
}

func TestOperationalCapabilitiesReflectProducerAvailability(t *testing.T) {
	app, store := newTestApp(t, testServerConfig{})
	req := jsonRequest(t, http.MethodGet, "/organizations/acme/pipeline-operational-capabilities", nil)
	resp := authenticatedRequest(t, app, store, "test-user", req)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, 200, resp.StatusCode)
	result := parseResponse[api.PipelineOperationalCapabilities](t, resp)
	require.True(t, result.SuccessfulRunRecency)
	require.Contains(t, result.NotificationTypes, api.NotificationTypePipelineFreshnessSLABreached)
	require.Len(t, result.NotificationTypes, len(model.ProducerBackedNotificationTypes()))
}

func TestRecencyNotificationPolicy(t *testing.T) {
	called := false
	app, store := newTestApp(t, testServerConfig{notificationsService: &fakeNotificationsService{CreateAlertPolicyFn: func(_ context.Context, _ notificationsvc.AlertManagementPrincipal, p notificationsvc.CreateAlertPolicyParams) (*model.AlertPolicy, error) {
		called = true
		require.Equal(t, model.AlertPolicyScopeProjectEnvironment, p.Scope)
		require.Equal(t, slug.Slug("ops"), *p.ProjectSlug)
		require.Equal(t, slug.Slug("dev"), *p.EnvironmentSlug)
		require.Equal(t, model.EventEntityTypePipeline, *p.SourceKind)
		require.Equal(t, slug.Slug("orders"), *p.SourceRef)
		return &model.AlertPolicy{Slug: "recency-alert", Name: p.Name, Scope: p.Scope, NotificationType: p.NotificationType, ProjectSlug: p.ProjectSlug, EnvironmentSlug: p.EnvironmentSlug, SourceKind: p.SourceKind, SourceRef: p.SourceRef, DestinationSlugs: p.DestinationSlugs, Enabled: true}, nil
	}}})
	req := jsonRequest(t, http.MethodPost, "/organizations/acme/alert-policies", map[string]any{"name": "Recency alerts", "scope": "project_environment", "project_slug": "ops", "environment_slug": "dev", "notification_type": "pipeline.freshness_sla_breached", "source_kind": "pipeline", "source_ref": "orders", "destination_slugs": []string{"ops-email"}})
	resp := authenticatedRequest(t, app, store, "test-user", req)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, 201, resp.StatusCode)
	require.True(t, called)
}

func TestRecencyAlertSourceMetadata(t *testing.T) {
	app, store := newTestApp(t, testServerConfig{eventsService: &fakeEventsService{ListSourceMetadataFn: func(context.Context, slug.Slug, eventssvc.SourceMetadataFilters) (model.EventSourceMetadataResponse, error) {
		return model.EventSourceMetadataResponse{NotificationTypes: model.ProducerBackedNotificationTypes()}, nil
	}}})
	req := jsonRequest(t, http.MethodGet, "/organizations/acme/alert-policy-source-metadata", nil)
	resp := authenticatedRequest(t, app, store, "test-user", req)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, 200, resp.StatusCode)
	result := parseResponse[api.AlertPolicySourceMetadataResponse](t, resp)
	require.Contains(t, result.NotificationTypes, api.NotificationTypePipelineFreshnessSLABreached)
	require.Len(t, result.NotificationTypes, len(model.ProducerBackedNotificationTypes()))
}
