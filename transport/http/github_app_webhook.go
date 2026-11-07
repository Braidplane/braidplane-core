package http

import (
	"context"
	"log/slog"

	"github.com/gofiber/fiber/v3"

	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// GitHubAppEndpointPath remains the shared GitHub App endpoint when Proof pull-request intake lands.
const GitHubAppEndpointPath = "/api/webhooks/github/pull-requests"

type githubAppLifecycleAcceptor interface {
	ApplyLifecycle(context.Context, githubprovider.InstallationLifecycleEvent) error
}

func RegisterGitHubAppWebhookRoute(app *fiber.App, lifecycle githubAppLifecycleAcceptor, secret []byte, logger *slog.Logger) error {
	if app == nil || lifecycle == nil || len(secret) == 0 {
		return xerror.Unexpected().Detail("GitHub App webhook dependencies are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	app.Post(GitHubAppEndpointPath, func(c fiber.Ctx) error {
		event, err := githubprovider.VerifyInstallationLifecycleWebhook(
			secret,
			c.Get("X-Hub-Signature-256"),
			c.Get("X-GitHub-Event"),
			c.Get("X-GitHub-Delivery"),
			c.Body(),
		)
		if err != nil {
			switch {
			case xerror.Is(err, githubprovider.ErrInstallationLifecycleIgnored):
				return c.SendStatus(fiber.StatusNoContent)
			case xerror.Is(err, githubprovider.ErrInstallationLifecycleAuthentication):
				return fiber.ErrUnauthorized
			case xerror.Is(err, githubprovider.ErrInstallationLifecycleTooLarge):
				return fiber.ErrRequestEntityTooLarge
			default:
				return fiber.ErrBadRequest
			}
		}
		if err := lifecycle.ApplyLifecycle(c.Context(), event); err != nil {
			logger.ErrorContext(c.Context(), "GitHub App lifecycle update failed", slog.String("delivery_id", event.DeliveryID), slog.String("action", event.Action))
			return fiber.ErrServiceUnavailable
		}
		logger.InfoContext(c.Context(), "GitHub App lifecycle updated", slog.String("delivery_id", event.DeliveryID), slog.String("action", event.Action))
		return c.SendStatus(fiber.StatusNoContent)
	})
	return nil
}
