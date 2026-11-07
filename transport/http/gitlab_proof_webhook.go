package http

import (
	"context"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	gitlabprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/gitlab"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/gitlabproofwebhooksvc"
	"github.com/Braidplane/braidplane-core/service/proofwebhooksvc"
)

const GitLabProofWebhookPath = "/api/webhooks/gitlab/merge-requests/:routeKey"

type GitLabProofWebhookResolver interface {
	Resolve(context.Context, string) (gitlabproofwebhooksvc.ResolvedWebhook, error)
}

func RegisterGitLabProofWebhookRoute(app *fiber.App, resolver GitLabProofWebhookResolver, acceptor ProofWebhookAcceptor, logger *slog.Logger) error {
	if app == nil || resolver == nil || acceptor == nil {
		return xerror.Unexpected().Detail("GitLab Proof webhook dependencies are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	app.Post(GitLabProofWebhookPath, func(c fiber.Ctx) error {
		resolved, err := resolver.Resolve(c.Context(), c.Params("routeKey"))
		if err != nil {
			if !xerror.Is(err, gitprovider.ErrChangeAuthentication) {
				logger.ErrorContext(c.Context(), "GitLab Proof webhook binding resolution failed", slog.String("error_code", "PROOF_WEBHOOK_BINDING_UNAVAILABLE"))
				return fiber.ErrServiceUnavailable
			}
			logger.WarnContext(c.Context(), "GitLab Proof webhook rejected", slog.String("error_code", gitprovider.ErrChangeAuthentication.Code()))
			return fiber.ErrUnauthorized
		}
		event, err := gitlabprovider.VerifyChangeWebhook(
			resolved.SigningToken, c.Get("webhook-id"), c.Get("webhook-timestamp"), c.Get("webhook-signature"),
			c.Get("X-Gitlab-Event"), c.Body(), time.Now(), resolved.Authority,
		)
		if err != nil {
			switch {
			case xerror.Is(err, gitprovider.ErrChangeActionIgnored):
				return c.SendStatus(fiber.StatusNoContent)
			case xerror.Is(err, gitprovider.ErrChangeAuthentication):
				logger.WarnContext(c.Context(), "GitLab Proof webhook rejected", slog.String("error_code", gitprovider.ErrChangeAuthentication.Code()))
				return fiber.ErrUnauthorized
			case xerror.Is(err, gitprovider.ErrChangePayloadTooLarge):
				return fiber.ErrRequestEntityTooLarge
			default:
				logger.WarnContext(c.Context(), "GitLab Proof webhook rejected", slog.String("error_code", "GIT_CHANGE_DELIVERY_INVALID"))
				return fiber.ErrBadRequest
			}
		}
		event.OrganizationSlug = resolved.Organization
		queued, err := acceptor.Accept(c.Context(), event)
		if err != nil {
			code := "PROOF_WEBHOOK_ENQUEUE_FAILED"
			var safe *xerror.Error
			if xerror.As(err, &safe) {
				code = safe.Code()
			}
			if xerror.Is(err, proofwebhooksvc.ErrDeliveryRejected) {
				logger.WarnContext(c.Context(), "GitLab Proof webhook rejected", slog.String("error_code", code), slog.String("delivery_id", event.DeliveryID))
				return fiber.ErrUnprocessableEntity
			}
			logger.ErrorContext(c.Context(), "GitLab Proof webhook enqueue failed", slog.String("error_code", code), slog.String("delivery_id", event.DeliveryID))
			return fiber.ErrServiceUnavailable
		}
		logger.InfoContext(c.Context(), "GitLab Proof webhook accepted", slog.String("delivery_id", event.DeliveryID), slog.String("queue_state", queued.State))
		return c.SendStatus(fiber.StatusAccepted)
	})
	return nil
}
