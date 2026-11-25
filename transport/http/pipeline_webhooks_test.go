package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/pipelinewebhooksvc"
)

func TestReceivePipelineWebhookPreservesRawBodyAndRejectsBoundaryViolations(t *testing.T) {
	t.Parallel()

	service := &fakePipelineWebhookService{}
	app, _ := newTestApp(t, testServerConfig{pipelineWebhookService: service})
	body := []byte(`{"event":{"id":17}}`)

	request := webhookHTTPRequest(body)
	response, err := app.Test(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.Equal(t, body, service.request.Body)
	require.Equal(t, "receiver-opaque", service.request.ReceiverID)

	tooLarge := webhookHTTPRequest(bytes.Repeat([]byte("x"), pipelinewebhooksvc.MaxDeliveryBodyBytes+1))
	response, err = app.Test(tooLarge)
	require.NoError(t, err)
	require.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.Equal(t, 1, service.calls)

	duplicate := webhookHTTPRequest(body)
	duplicate.Header.Add("Braidplane-Webhook-Delivery-ID", "delivery-2")
	response, err = app.Test(duplicate)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.Equal(t, 1, service.calls)
}

func TestPipelineWebhookDeliveryReadAndReplayUseProtectedTriggerPermissions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	delivery := pipelinewebhooksvc.DeliveryDiagnostic{
		ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", PayloadDigest: strings.Repeat("a", 64), PayloadSizeBytes: 12,
		TriggerSlug: must.Slug("customer-hook"), TriggerRevision: 3, PipelineSlug: must.Slug("customer-sync"),
		Status: "failed", MappedInputNames: []string{"customer"}, AttemptNumber: 1, ReceivedAt: now, UpdatedAt: now,
		ReplayAvailable: true,
	}
	replays := 0
	service := &fakePipelineWebhookService{
		listFn: func(_ context.Context, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, includeHistory bool, req model.ListRequest) (model.ListResponse[pipelinewebhooksvc.DeliveryDiagnostic], error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, must.Slug("operations"), projectSlug)
			require.Equal(t, must.Slug("prod"), environmentSlug)
			require.Equal(t, must.Slug("customer-hook"), triggerSlug)
			require.False(t, includeHistory)
			require.Equal(t, 10, req.Limit)
			return model.ListResponse[pipelinewebhooksvc.DeliveryDiagnostic]{Items: []pipelinewebhooksvc.DeliveryDiagnostic{delivery}}, nil
		},
		getFn: func(_ context.Context, org, project, env, trigger slug.Slug, id string) (pipelinewebhooksvc.DeliveryDiagnostic, error) {
			require.Equal(t, []slug.Slug{"acme", "operations", "prod", "customer-hook"}, []slug.Slug{org, project, env, trigger})
			require.Equal(t, delivery.DeliveryID, id)
			return delivery, nil
		},
		replayFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (pipelinewebhooksvc.DeliveryDiagnostic, error) {
			replays++
			delivery.Status = "queued"
			return delivery, nil
		},
	}
	actions := make([]model.Action, 0, 5)
	grantedPipeline := slug.Slug("retargeted")
	app, store := newTestApp(t, testServerConfig{
		pipelineWebhookService: service,
		pipelineTriggersService: &fakePipelineTriggersService{GetPipelineTriggerFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.PipelineTrigger, error) {
			return &model.PipelineTrigger{PipelineSlug: "retargeted"}, nil
		}},
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			actions = append(actions, input.Action)
			if input.Action == model.Execute {
				require.Equal(t, model.ResourcePipelines, input.Resource)
				require.Equal(t, model.AuthorizationTarget{Kind: "deployed_pipelines", Slug: delivery.PipelineSlug, ProjectSlug: "operations", EnvironmentSlug: "prod"}, input.Target)
				return input.Target.Slug == grantedPipeline, nil
			}
			require.Equal(t, model.ResourcePipelineTriggers, input.Resource)
			return input.CheckPurpose != "delivery_history", nil
		}},
	})

	listResponse := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodGet,
		"/organizations/acme/projects/operations/environments/prod/pipeline-triggers/customer-hook/deliveries?limit=10", nil))
	defer func() { _ = listResponse.Body.Close() }()
	list := parseResponse[api.PipelineWebhookDeliveryListResponse](t, listResponse)
	require.Len(t, list.Deliveries, 1)
	require.Equal(t, "delivery-1", list.Deliveries[0].DeliveryId)
	require.NotNil(t, list.Deliveries[0].MappedInputNames)

	replayPath := "/organizations/acme/projects/operations/environments/prod/pipeline-triggers/customer-hook/deliveries/delivery-1/replay"
	denied := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodPost, replayPath, nil))
	require.Equal(t, http.StatusForbidden, denied.StatusCode)
	assertErrorResponse(t, denied, apierror.Forbidden("pipelines", "execute"))
	require.NoError(t, denied.Body.Close())
	require.Zero(t, replays)

	grantedPipeline = delivery.PipelineSlug
	replayResponse := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodPost, replayPath, nil))
	require.Equal(t, http.StatusOK, replayResponse.StatusCode)
	require.NoError(t, replayResponse.Body.Close())
	require.Equal(t, 1, replays)
	require.Equal(t, []model.Action{model.Read, model.Read, model.Update, model.Execute, model.Update, model.Execute}, actions)
}

func webhookHTTPRequest(body []byte) *http.Request {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks/pipelines/receiver-opaque", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Braidplane-Webhook-Key-Version", "1")
	request.Header.Set("Braidplane-Webhook-Timestamp", "1787313600")
	request.Header.Set("Braidplane-Webhook-Delivery-ID", "delivery-1")
	request.Header.Set("Braidplane-Webhook-Signature", "v1="+strings.Repeat("0", 64))
	return request
}

type fakePipelineWebhookService struct {
	calls    int
	request  pipelinewebhooksvc.AcceptRequest
	listFn   func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, bool, model.ListRequest) (model.ListResponse[pipelinewebhooksvc.DeliveryDiagnostic], error)
	getFn    func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (pipelinewebhooksvc.DeliveryDiagnostic, error)
	replayFn func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (pipelinewebhooksvc.DeliveryDiagnostic, error)
}

func (f *fakePipelineWebhookService) Accept(_ context.Context, request pipelinewebhooksvc.AcceptRequest) (pipelinewebhooksvc.Acceptance, error) {
	f.calls++
	f.request = request
	return pipelinewebhooksvc.Acceptance{}, nil
}

func (f *fakePipelineWebhookService) ListDeliveries(ctx context.Context, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, includeHistory bool, req model.ListRequest) (model.ListResponse[pipelinewebhooksvc.DeliveryDiagnostic], error) {
	if f.listFn != nil {
		return f.listFn(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, includeHistory, req)
	}
	return model.ListResponse[pipelinewebhooksvc.DeliveryDiagnostic]{Items: []pipelinewebhooksvc.DeliveryDiagnostic{}}, nil
}

func (f *fakePipelineWebhookService) GetDelivery(ctx context.Context, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, deliveryID string) (pipelinewebhooksvc.DeliveryDiagnostic, error) {
	if f.getFn != nil {
		return f.getFn(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, deliveryID)
	}
	return pipelinewebhooksvc.DeliveryDiagnostic{}, nil
}

func (f *fakePipelineWebhookService) ReplayDelivery(ctx context.Context, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, deliveryID string) (pipelinewebhooksvc.DeliveryDiagnostic, error) {
	if f.replayFn != nil {
		return f.replayFn(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, deliveryID)
	}
	return pipelinewebhooksvc.DeliveryDiagnostic{}, nil
}
