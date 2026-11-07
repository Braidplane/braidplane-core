package http

import (
	"context"
	"log/slog"

	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/proofwebhooksvc"
)

const GitHubProofWebhookPath = "/api/webhooks/github/pull-requests"

type ProofWebhookAcceptor interface {
	Accept(context.Context, gitprovider.VerifiedChangeEvent) (queue.QueuedJob, error)
}

func RegisterGitHubProofWebhookRoute(app *fiber.App, acceptor ProofWebhookAcceptor, lifecycle githubAppLifecycleAcceptor, secret []byte, logger *slog.Logger) error {
	if app == nil || acceptor == nil || lifecycle == nil || len(secret) == 0 {
		return xerror.Unexpected().Detail("GitHub Proof webhook dependencies are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	app.Post(
		GitHubProofWebhookPath,
		func(c fiber.Ctx) error {
			eventName := c.Get("X-GitHub-Event")
			if eventName == "installation" || eventName == "installation_repositories" {
				event, err := githubprovider.VerifyInstallationLifecycleWebhook(secret, c.Get("X-Hub-Signature-256"), eventName, c.Get("X-GitHub-Delivery"), c.Body())
				if err != nil {
					if xerror.Is(err, gitprovider.ErrChangeActionIgnored) {
						return c.SendStatus(fiber.StatusNoContent)
					}
					if xerror.Is(err, gitprovider.ErrChangeAuthentication) {
						logger.WarnContext(c.Context(), "GitHub App lifecycle webhook rejected", slog.String("error_code", gitprovider.ErrChangeAuthentication.Code()))
						return fiber.ErrUnauthorized
					}
					if xerror.Is(err, gitprovider.ErrChangePayloadTooLarge) {
						logger.WarnContext(c.Context(), "GitHub App lifecycle webhook rejected", slog.String("error_code", gitprovider.ErrChangePayloadTooLarge.Code()))
						return fiber.ErrRequestEntityTooLarge
					}
					return fiber.ErrBadRequest
				}
				if err := lifecycle.ApplyLifecycle(c.Context(), event); err != nil {
					logger.ErrorContext(c.Context(), "GitHub App lifecycle update failed", slog.String("delivery_id", event.DeliveryID), slog.String("action", event.Action))
					return fiber.ErrServiceUnavailable
				}
				logger.InfoContext(c.Context(), "GitHub App lifecycle updated", slog.String("delivery_id", event.DeliveryID), slog.String("action", event.Action))
				return c.SendStatus(fiber.StatusNoContent)
			}
			event, err := githubprovider.VerifyChangeWebhook(
				secret,
				c.Get("X-Hub-Signature-256"),
				eventName,
				c.Get("X-GitHub-Delivery"),
				c.Body(),
			)
			if err != nil {
				code := "GIT_CHANGE_DELIVERY_INVALID"
				switch {
				case xerror.Is(err, gitprovider.ErrChangeActionIgnored):
					return c.SendStatus(fiber.StatusNoContent)
				case xerror.Is(err, gitprovider.ErrChangeAuthentication):
					code = gitprovider.ErrChangeAuthentication.Code()
					logger.WarnContext(c.Context(), "GitHub Proof webhook rejected", slog.String("error_code", code))
					return fiber.ErrUnauthorized
				case xerror.Is(err, gitprovider.ErrChangePayloadTooLarge):
					code = gitprovider.ErrChangePayloadTooLarge.Code()
					logger.WarnContext(c.Context(), "GitHub Proof webhook rejected", slog.String("error_code", code))
					return fiber.ErrRequestEntityTooLarge
				default:
					logger.WarnContext(c.Context(), "GitHub Proof webhook rejected", slog.String("error_code", code))
					return fiber.ErrBadRequest
				}
			}
			queued, err := acceptor.Accept(c.Context(), event)
			if err != nil {
				code := "PROOF_WEBHOOK_ENQUEUE_FAILED"
				var safe *xerror.Error
				if xerror.As(err, &safe) {
					code = safe.Code()
				}
				if xerror.Is(err, proofwebhooksvc.ErrDeliveryRejected) {
					logger.WarnContext(c.Context(), "GitHub Proof webhook rejected", slog.String("error_code", code), slog.String("delivery_id", event.DeliveryID))
					return fiber.ErrUnprocessableEntity
				}
				logger.ErrorContext(c.Context(), "GitHub Proof webhook enqueue failed",
					slog.String("error_code", code),
					slog.String("delivery_id", event.DeliveryID))
				return fiber.ErrServiceUnavailable
			}
			logger.InfoContext(c.Context(), "GitHub Proof webhook accepted",
				slog.String("delivery_id", event.DeliveryID), slog.String("queue_state", queued.State))
			return c.SendStatus(fiber.StatusAccepted)
		},
	)
	return nil
}
