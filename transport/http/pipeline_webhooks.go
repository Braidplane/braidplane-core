package http

import (
	"context"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/pipelinewebhooksvc"
)

type pipelineWebhookService interface {
	Accept(context.Context, pipelinewebhooksvc.AcceptRequest) (pipelinewebhooksvc.Acceptance, error)
	ListDeliveries(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[pipelinewebhooksvc.DeliveryDiagnostic], error)
	GetDelivery(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (pipelinewebhooksvc.DeliveryDiagnostic, error)
	ReplayDelivery(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, string) (pipelinewebhooksvc.DeliveryDiagnostic, error)
}

func (s *server) ReceivePipelineWebhook(
	c fiber.Ctx,
	receiverID string,
	params api.ReceivePipelineWebhookParams,
) error {
	if len(c.Body()) > pipelinewebhooksvc.MaxDeliveryBodyBytes {
		return apierror.New(fiber.StatusRequestEntityTooLarge, apierror.CodeValidationFailed, "Webhook body exceeds the delivery limit")
	}
	_, err := s.pipelineWebhookService.Accept(c.Context(), pipelinewebhooksvc.AcceptRequest{
		ReceiverID: receiverID,
		KeyVersion: params.BraidplaneWebhookKeyVersion,
		Timestamp:  params.BraidplaneWebhookTimestamp,
		DeliveryID: params.BraidplaneWebhookDeliveryID,
		Signature:  params.BraidplaneWebhookSignature,
		Body:       append([]byte(nil), c.Body()...),
	})
	if err != nil {
		return mapServiceError(err)
	}
	return c.Status(fiber.StatusAccepted).Send(nil)
}

func (s *server) ListPipelineWebhookDeliveries(c fiber.Ctx, slugParam api.SlugParam, projectSlugParam api.ProjectSlugParam, environmentSlugParam api.EnvironmentSlugParam, triggerSlugParam api.PipelineTriggerSlugParam, params api.ListPipelineWebhookDeliveriesParams) error {
	orgSlug, projectSlug, environmentSlug, triggerSlug, err := parsePipelineTriggerSlugs(slugParam, projectSlugParam, environmentSlugParam, triggerSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelineTriggers); err != nil {
		return err
	}
	response, err := s.pipelineWebhookService.ListDeliveries(c.Context(), orgSlug, projectSlug, environmentSlug, triggerSlug, model.ListRequest{
		Limit: int(valueOrZero(params.Limit)), Cursor: valueOrZero(params.Cursor), Direction: string(valueOrZero(params.Direction)),
	})
	if err != nil {
		return mapServiceError(err)
	}
	items := make([]api.PipelineWebhookDelivery, len(response.Items))
	for i := range response.Items {
		items[i] = pipelineWebhookDeliveryToAPI(response.Items[i])
	}
	return c.JSON(api.PipelineWebhookDeliveryListResponse{Deliveries: items, HasMore: response.HasMore, NextCursor: response.NextCursor, PrevCursor: response.PrevCursor})
}

func (s *server) GetPipelineWebhookDelivery(c fiber.Ctx, slugParam api.SlugParam, projectSlugParam api.ProjectSlugParam, environmentSlugParam api.EnvironmentSlugParam, triggerSlugParam api.PipelineTriggerSlugParam, deliveryID api.PipelineWebhookDeliveryIDParam) error {
	orgSlug, projectSlug, environmentSlug, triggerSlug, err := parsePipelineTriggerSlugs(slugParam, projectSlugParam, environmentSlugParam, triggerSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Read, model.ResourcePipelineTriggers); err != nil {
		return err
	}
	delivery, err := s.pipelineWebhookService.GetDelivery(c.Context(), orgSlug, projectSlug, environmentSlug, triggerSlug, deliveryID)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(pipelineWebhookDeliveryToAPI(delivery))
}

func (s *server) ReplayPipelineWebhookDelivery(c fiber.Ctx, slugParam api.SlugParam, projectSlugParam api.ProjectSlugParam, environmentSlugParam api.EnvironmentSlugParam, triggerSlugParam api.PipelineTriggerSlugParam, deliveryID api.PipelineWebhookDeliveryIDParam) error {
	orgSlug, projectSlug, environmentSlug, triggerSlug, err := parsePipelineTriggerSlugs(slugParam, projectSlugParam, environmentSlugParam, triggerSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, orgSlug, model.Update, model.ResourcePipelineTriggers); err != nil {
		return err
	}
	delivery, err := s.pipelineWebhookService.ReplayDelivery(c.Context(), orgSlug, projectSlug, environmentSlug, triggerSlug, deliveryID)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(pipelineWebhookDeliveryToAPI(delivery))
}

func pipelineWebhookDeliveryToAPI(delivery pipelinewebhooksvc.DeliveryDiagnostic) api.PipelineWebhookDelivery {
	var failureKind *api.PipelineWebhookDeliveryFailureKind
	if delivery.FailureKind != nil {
		value := api.PipelineWebhookDeliveryFailureKind(*delivery.FailureKind)
		failureKind = &value
	}
	return api.PipelineWebhookDelivery{
		ReceiverId: delivery.ReceiverID, DeliveryId: delivery.DeliveryID, PayloadDigest: delivery.PayloadDigest,
		PayloadSizeBytes: delivery.PayloadSizeBytes, TriggerSlug: api.Slug(delivery.TriggerSlug), TriggerRevision: delivery.TriggerRevision,
		PipelineSlug: api.Slug(delivery.PipelineSlug), Status: api.PipelineWebhookDeliveryStatus(delivery.Status),
		MappedInputNames: append([]string(nil), delivery.MappedInputNames...), LinkedRunSlug: nullableSlug(delivery.LinkedRunSlug),
		AttemptNumber: delivery.AttemptNumber, DuplicateCount: delivery.DuplicateCount,
		FailureKind: failureKind, FailureReason: delivery.FailureReason, NextRetryAt: nullableTimestamp(delivery.NextRetryAt),
		ReceivedAt: delivery.ReceivedAt, StartedAt: nullableTimestamp(delivery.StartedAt), CompletedAt: nullableTimestamp(delivery.CompletedAt),
		LastDuplicateAt: nullableTimestamp(delivery.LastDuplicateAt), UpdatedAt: delivery.UpdatedAt,
		ReplayAvailable: delivery.ReplayAvailable, ReplayUnavailableReason: delivery.ReplayUnavailableReason,
	}
}

func valueOrZero[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}
