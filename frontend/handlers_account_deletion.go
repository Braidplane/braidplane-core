package frontend

import (
	"log/slog"
	"net/http"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
)

func (r *Router) handleAccountDeletionSubmit(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	if c.FormValue("confirmation") != "DELETE MY ACCOUNT" {
		c.Status(fiber.StatusBadRequest)
		return renderTempl(c, views.AccountDeletionOutcome(nil, "Type DELETE MY ACCOUNT to confirm removal."))
	}
	response, err := r.apiClient.RequestAccountDeletionWithResponse(c.Context(), api.RequestAccountDeletionJSONRequestBody{
		Confirmation: api.DELETEMYACCOUNT,
	}, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		r.logger.Error("account removal response unavailable", slog.Any("error", err))
		c.Status(fiber.StatusBadGateway)
		return renderTempl(c, views.AccountDeletionOutcome(nil, "We could not confirm the request outcome. Contact your instance administrator before retrying."))
	}
	if response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, "/account")
	}
	if response.StatusCode() == http.StatusConflict && response.JSON409 != nil && response.JSON409.State == "ownership_required" {
		c.Status(fiber.StatusConflict)
		return renderTempl(c, views.AccountDeletionOutcome(response.JSON409, ""))
	}
	receipt := response.JSON202
	valid := response.StatusCode() == http.StatusAccepted && receipt != nil && receipt.State == "pending"
	if response.StatusCode() == http.StatusOK {
		receipt = response.JSON200
		valid = receipt != nil && receipt.State == "completed" && receipt.CompletedAt != nil
	}
	if !valid {
		status := response.StatusCode()
		if status < 400 {
			status = fiber.StatusBadGateway
		}
		c.Status(status)
		return renderTempl(c, views.AccountDeletionOutcome(nil, "Account removal could not be confirmed. Contact your instance administrator if the problem persists."))
	}
	// The backend already revoked every indexed session. Do not fetch profile or
	// shell data now; render the receipt even if expiring the browser cookie fails.
	sess, err := r.sessionStore.Get(c)
	if err == nil {
		defer sess.Release()
		err = sess.Destroy()
	}
	if err != nil {
		r.logger.Error("expire removed account session cookie", slog.Any("error", err))
	}
	return renderTempl(c, views.AccountDeletionOutcome(receipt, ""))
}
