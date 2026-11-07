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
	service := &fakePipelineWebhookService{
		listFn: func(_ context.Context, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, req model.ListRequest) (model.ListResponse[pipelinewebhooksvc.DeliveryDiagnostic], error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, must.Slug("operations"), projectSlug)
			require.Equal(t, must.Slug("prod"), environmentSlug)
			require.Equal(t, must.Slug("customer-hook"), triggerSlug)
			require.Equal(t, 10, req.Limit)
			return model.ListResponse[pipelinewebhooksvc.DeliveryDiagnostic]{Items: []pipelinewebhooksvc.DeliveryDiagnostic{delivery}}, nil
		},
		replayFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (pipelinewebhooksvc.DeliveryDiagnostic, error) {
			delivery.Status = "queued"
			return delivery, nil
		},
	}
	actions := make([]model.Action, 0, 2)
	app, store := newTestApp(t, testServerConfig{
		pipelineWebhookService: service,
		authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
			require.Equal(t, model.ResourcePipelineTriggers, input.Resource)
			actions = append(actions, input.Action)
			return true, nil
		}},
	})

	listResponse := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodGet,
		"/organizations/acme/projects/operations/environments/prod/pipeline-triggers/customer-hook/deliveries?limit=10", nil))
	defer func() { _ = listResponse.Body.Close() }()
	list := parseResponse[api.PipelineWebhookDeliveryListResponse](t, listResponse)
	require.Len(t, list.Deliveries, 1)
	require.Equal(t, "delivery-1", list.Deliveries[0].DeliveryId)
	require.NotNil(t, list.Deliveries[0].MappedInputNames)

	replayResponse := authenticatedRequest(t, app, store, must.Slug("alice"), jsonRequest(t, http.MethodPost,
		"/organizations/acme/projects/operations/environments/prod/pipeline-triggers/customer-hook/deliveries/delivery-1/replay", nil))
	require.Equal(t, http.StatusOK, replayResponse.StatusCode)
	require.NoError(t, replayResponse.Body.Close())
	require.Equal(t, []model.Action{model.Read, model.Update}, actions)
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
	listFn   func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[pipelinewebhooksvc.DeliveryDiagnostic], error)
	getFn    func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (pipelinewebhooksvc.DeliveryDiagnostic, error)
	replayFn func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (pipelinewebhooksvc.DeliveryDiagnostic, error)
}

func (f *fakePipelineWebhookService) Accept(_ context.Context, request pipelinewebhooksvc.AcceptRequest) (pipelinewebhooksvc.Acceptance, error) {
	f.calls++
	f.request = request
	return pipelinewebhooksvc.Acceptance{}, nil
}

func (f *fakePipelineWebhookService) ListDeliveries(ctx context.Context, orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug, req model.ListRequest) (model.ListResponse[pipelinewebhooksvc.DeliveryDiagnostic], error) {
	if f.listFn != nil {
		return f.listFn(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, req)
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
