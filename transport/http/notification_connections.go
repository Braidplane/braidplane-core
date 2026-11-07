package http

import (
	"net/http"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/gofiber/fiber/v3"
)

func (s *server) ListNotificationConnections(c fiber.Ctx, rawOrg api.SlugParam) error {
	org, err := slug.New(rawOrg)
	if err != nil {
		return apierror.ValidationFailed("Invalid organization slug")
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceAlertDestinations); err != nil {
		return err
	}
	connections, err := s.credentialsService.ListNotificationConnections(c.Context(), org)
	if err != nil {
		return mapServiceError(err)
	}
	result := make([]api.NotificationConnection, 0, len(connections))
	for _, connection := range connections {
		result = append(result, notificationConnectionToHTTP(connection))
	}
	return c.JSON(result)
}

func (s *server) CreateNotificationConnection(c fiber.Ctx, rawOrg api.SlugParam) error {
	org, err := slug.New(rawOrg)
	if err != nil {
		return apierror.ValidationFailed("Invalid organization slug")
	}
	if err := s.canOrg(c, org, model.Create, model.ResourceAlertDestinations); err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "credential.mutation", true); err != nil {
		return err
	}
	var req api.NewNotificationConnection
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	if !req.Provider.Valid() {
		return apierror.ValidationFailed("A supported notification provider is required")
	}
	var connectionSlug slug.Slug
	if req.Slug != nil {
		connectionSlug, err = slug.New(*req.Slug)
	}
	if err != nil {
		return apierror.ValidationFailed("Invalid connection slug")
	}
	connection, err := s.credentialsService.CreateNotificationConnection(c.Context(), credentialssvc.CreateNotificationConnectionParams{OrgSlug: org, Slug: connectionSlug, Name: req.Name, Provider: string(req.Provider)})
	if err != nil {
		return mapServiceError(err)
	}
	return c.Status(http.StatusCreated).JSON(notificationConnectionToHTTP(connection))
}

func notificationConnectionToHTTP(connection credentialssvc.NotificationConnection) api.NotificationConnection {
	var metadata api.NotificationConnectionMetadata
	// Only provider-owned, explicitly safe fields cross the HTTP boundary.
	_ = metadata.FromSlackNotificationConnectionMetadata(api.SlackNotificationConnectionMetadata{
		WorkspaceId: connection.Account.ID, WorkspaceName: connection.Account.Name,
		ChannelId: connection.Account.ChannelID, ChannelName: connection.Account.ChannelName,
	})
	return api.NotificationConnection{
		Slug: connection.Slug.String(), Name: connection.Name, Provider: api.NotificationProvider(connection.Provider),
		Status: api.CredentialStatus(connection.Status.String()), Metadata: metadata, UpdatedAt: connection.UpdatedAt,
	}
}

func (s *server) ConnectNotificationConnection(c fiber.Ctx, rawOrg api.SlugParam, rawConnection string) error {
	owner, err := s.notificationConnectionOwner(c, rawOrg, rawConnection)
	if err != nil {
		return err
	}
	install, err := s.credentialsService.ConnectNotificationConnection(c.Context(), owner, s.eventActor(c))
	if err != nil {
		return mapServiceError(err)
	}
	return c.Status(http.StatusCreated).JSON(providerInstallToHTTP(install))
}

func (s *server) DisconnectNotificationConnection(c fiber.Ctx, rawOrg api.SlugParam, rawConnection string) error {
	owner, err := s.notificationConnectionOwner(c, rawOrg, rawConnection)
	if err != nil {
		return err
	}
	metadata, err := s.credentialsService.Revoke(c.Context(), credentialssvc.RevokeParams{Owner: owner, Actor: s.eventActor(c)})
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(credentialMetadataToHTTP(metadata))
}

func (s *server) notificationConnectionOwner(c fiber.Ctx, rawOrg, rawConnection string) (credentialssvc.CredentialOwner, error) {
	org, err := slug.New(rawOrg)
	if err != nil {
		return credentialssvc.CredentialOwner{}, apierror.ValidationFailed("Invalid organization slug")
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceAlertDestinations); err != nil {
		return credentialssvc.CredentialOwner{}, err
	}
	connection, err := slug.New(rawConnection)
	if err != nil {
		return credentialssvc.CredentialOwner{}, apierror.ValidationFailed("Invalid connection slug")
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "credential.mutation", true); err != nil {
		return credentialssvc.CredentialOwner{}, err
	}
	return credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindNotificationConnection, OrgSlug: org, NotificationConnectionSlug: connection}, nil
}
