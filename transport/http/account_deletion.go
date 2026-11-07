package http

import (
	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/service/accountclosuresvc"
)

func (s *server) RequestAccountDeletion(c fiber.Ctx) error {
	user, err := s.canSessionProfile(c, model.Delete)
	if err != nil {
		return err
	}
	var request api.AccountDeletionConfirmation
	if err := c.Bind().Body(&request); err != nil {
		return apierror.InvalidRequestBody()
	}
	if request.Confirmation != "DELETE MY ACCOUNT" {
		return apierror.ValidationFailed("confirm account removal with DELETE MY ACCOUNT")
	}
	result, err := s.accountClosureService.Request(c.Context(), user)
	if err != nil {
		return mapServiceError(err)
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	status := fiber.StatusAccepted
	if result.State == accountclosuresvc.StateCompleted {
		status = fiber.StatusOK
	}
	if result.State == accountclosuresvc.StateOwnershipRequired {
		status = fiber.StatusConflict
	}
	return c.Status(status).JSON(accountDeletionToHTTP(result))
}

func accountDeletionToHTTP(result accountclosuresvc.Result) api.AccountDeletionRequest {
	response := api.AccountDeletionRequest{
		Slug: api.Slug(result.Slug), State: api.AccountDeletionRequestState(result.State.String()),
		RequestedAt: result.RequestedAt, CompletedAt: result.CompletedAt,
		OwnershipRequired:           make([]api.Slug, 0, len(result.OwnershipRequired)),
		OrganizationRemovalRequired: make([]api.Slug, 0, len(result.OrganizationRemovalRequired)),
	}
	for _, org := range result.OwnershipRequired {
		response.OwnershipRequired = append(response.OwnershipRequired, api.Slug(org))
	}
	for _, org := range result.OrganizationRemovalRequired {
		response.OrganizationRemovalRequired = append(response.OrganizationRemovalRequired, api.Slug(org))
	}
	return response
}
