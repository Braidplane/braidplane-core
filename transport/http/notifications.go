package http

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
)

const (
	defaultAlertDeliveryAttemptsPerNotification = 5
	maxAlertDeliveryAttemptsPerNotification     = 10
	maxAlertDeliveryAttemptNotificationSlugs    = 50
)

// ListNotifications returns display-safe generic notification records.
func (s *server) ListNotifications(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListNotificationsParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceNotifications); err != nil {
		return err
	}

	filters, err := toNotificationFilters(params)
	if err != nil {
		return err
	}

	response, err := s.notificationsService.ListNotifications(
		c.Context(),
		orgSlug,
		filters,
		toNotificationsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	notifications := make([]api.NotificationRecord, len(response.Items))
	for i, item := range response.Items {
		notifications[i] = toAPINotificationRecord(item)
	}

	return c.JSON(api.NotificationListResponse{
		Notifications: notifications,
		HasMore:       response.HasMore,
		NextCursor:    response.NextCursor,
		PrevCursor:    response.PrevCursor,
	})
}

// GetNotification returns one display-safe generic notification record.
func (s *server) GetNotification(
	c fiber.Ctx,
	slugParam api.SlugParam,
	notificationSlugParam api.NotificationSlugParam,
) error {
	orgSlug, notificationSlug, err := parseOrgAndResourceSlug(slugParam, notificationSlugParam, "notification")
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceNotifications); err != nil {
		return err
	}

	notification, err := s.notificationsService.GetNotification(c.Context(), orgSlug, notificationSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPINotificationRecord(notification))
}

// ListNotificationDeliveryAttempts returns alert-owned delivery diagnostics for one notification.
func (s *server) ListNotificationDeliveryAttempts(
	c fiber.Ctx,
	slugParam api.SlugParam,
	notificationSlugParam api.NotificationSlugParam,
	params api.ListNotificationDeliveryAttemptsParams,
) error {
	orgSlug, notificationSlug, err := parseOrgAndResourceSlug(slugParam, notificationSlugParam, "notification")
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceNotifications); err != nil {
		return err
	}

	response, err := s.notificationsService.ListAlertDeliveryAttempts(
		c.Context(),
		orgSlug,
		notificationSlug,
		toAlertDeliveryAttemptsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	attempts := make([]api.AlertDeliveryAttempt, 0, len(response.Items))
	for _, attempt := range response.Items {
		attempts = append(attempts, toAPIAlertDeliveryAttempt(attempt))
	}

	return c.JSON(api.AlertDeliveryAttemptListResponse{
		AlertDeliveryAttempts: attempts,
		HasMore:               response.HasMore,
		NextCursor:            response.NextCursor,
		PrevCursor:            response.PrevCursor,
	})
}

// AcknowledgeNotification marks one tenant-scoped notification as operator-acknowledged.
func (s *server) AcknowledgeNotification(
	c fiber.Ctx,
	slugParam api.SlugParam,
	notificationSlugParam api.NotificationSlugParam,
) error {
	orgSlug, notificationSlug, err := parseOrgAndResourceSlug(slugParam, notificationSlugParam, "notification")
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceNotifications); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	notification, err := s.notificationsService.AcknowledgeNotification(
		c.Context(),
		principal,
		orgSlug,
		notificationSlug,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPINotificationRecord(notification))
}

// ResolveNotification closes one tenant-scoped notification and removes it from the active list.
func (s *server) ResolveNotification(
	c fiber.Ctx,
	slugParam api.SlugParam,
	notificationSlugParam api.NotificationSlugParam,
) error {
	orgSlug, notificationSlug, err := parseOrgAndResourceSlug(slugParam, notificationSlugParam, "notification")
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceNotifications); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	notification, err := s.notificationsService.ResolveNotification(
		c.Context(),
		principal,
		orgSlug,
		notificationSlug,
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPINotificationRecord(notification))
}

// ListAlertDeliveryAttempts returns capped delivery diagnostics for multiple notifications.
func (s *server) ListAlertDeliveryAttempts(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListAlertDeliveryAttemptsParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceNotifications); err != nil {
		return err
	}

	notificationSlugs, err := notificationSlugsFromParam(params.NotificationSlugs)
	if err != nil {
		return err
	}
	limitPerNotification := defaultAlertDeliveryAttemptsPerNotification
	if params.LimitPerNotification != nil {
		limitPerNotification = *params.LimitPerNotification
	}
	if limitPerNotification <= 0 || limitPerNotification > maxAlertDeliveryAttemptsPerNotification {
		return apierror.ValidationFailed("limit_per_notification must be between 1 and 10")
	}

	attempts, err := s.notificationsService.ListRecentAlertDeliveryAttemptsForRecords(
		c.Context(),
		orgSlug,
		notificationSlugs,
		limitPerNotification,
	)
	if err != nil {
		return mapServiceError(err)
	}

	apiAttempts := make([]api.AlertDeliveryAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		apiAttempts = append(apiAttempts, toAPIAlertDeliveryAttempt(attempt))
	}

	return c.JSON(api.AlertDeliveryAttemptListResponse{
		AlertDeliveryAttempts: apiAttempts,
		HasMore:               false,
	})
}

// ListAlertDestinations returns tenant-scoped alert destinations for management.
func (s *server) ListAlertDestinations(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListAlertDestinationsParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceAlertDestinations); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	response, err := s.notificationsService.ListAlertDestinations(
		c.Context(),
		principal,
		orgSlug,
		toAlertDestinationsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	destinations := make([]api.AlertDestination, 0, len(response.Items))
	for _, destination := range response.Items {
		destinations = append(destinations, toAPIAlertDestination(destination))
	}

	return c.JSON(api.AlertDestinationListResponse{
		AlertDestinations: destinations,
		HasMore:           response.HasMore,
		NextCursor:        response.NextCursor,
		PrevCursor:        response.PrevCursor,
	})
}

// CreateAlertDestination creates one tenant-scoped alert destination.
func (s *server) CreateAlertDestination(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Create, model.ResourceAlertDestinations); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	var req api.NewAlertDestination
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	params, err := toCreateAlertDestinationParams(orgSlug, req)
	if err != nil {
		return err
	}
	destination, err := s.notificationsService.CreateAlertDestination(c.Context(), principal, params)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusCreated).JSON(toAPIAlertDestination(destination))
}

// GetAlertDestination returns one tenant-scoped alert destination.
func (s *server) GetAlertDestination(
	c fiber.Ctx,
	slugParam api.SlugParam,
	destinationSlugParam api.AlertDestinationSlugParam,
) error {
	orgSlug, destinationSlug, err := parseOrgAndResourceSlug(slugParam, destinationSlugParam, "alert destination")
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceAlertDestinations); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	destination, err := s.notificationsService.GetAlertDestination(c.Context(), principal, orgSlug, destinationSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIAlertDestination(destination))
}

// UpdateAlertDestination replaces one tenant-scoped alert destination.
func (s *server) UpdateAlertDestination(
	c fiber.Ctx,
	slugParam api.SlugParam,
	destinationSlugParam api.AlertDestinationSlugParam,
) error {
	orgSlug, destinationSlug, err := parseOrgAndResourceSlug(slugParam, destinationSlugParam, "alert destination")
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceAlertDestinations); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	var req api.UpdateAlertDestination
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	params, err := toUpdateAlertDestinationParams(orgSlug, destinationSlug, req)
	if err != nil {
		return err
	}
	destination, err := s.notificationsService.UpdateAlertDestination(c.Context(), principal, params)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIAlertDestination(destination))
}

// DeleteAlertDestination removes one tenant-scoped alert destination.
func (s *server) DeleteAlertDestination(
	c fiber.Ctx,
	slugParam api.SlugParam,
	destinationSlugParam api.AlertDestinationSlugParam,
) error {
	orgSlug, destinationSlug, err := parseOrgAndResourceSlug(slugParam, destinationSlugParam, "alert destination")
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceAlertDestinations); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	if err := s.notificationsService.DeleteAlertDestination(c.Context(), principal, orgSlug, destinationSlug); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// TestAlertDestination enqueues an ephemeral test delivery through one enabled destination.
func (s *server) TestAlertDestination(
	c fiber.Ctx,
	slugParam api.SlugParam,
	destinationSlugParam api.AlertDestinationSlugParam,
) error {
	orgSlug, destinationSlug, err := parseOrgAndResourceSlug(slugParam, destinationSlugParam, "alert destination")
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceAlertDestinations); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "notification.delivery_test", false); err != nil {
		return err
	}

	result, err := s.notificationsService.TestAlertDestination(
		c.Context(),
		principal,
		notificationsvc.TestAlertDestinationParams{
			OrgSlug:         orgSlug,
			DestinationSlug: destinationSlug,
		},
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusAccepted).JSON(toAPIAlertDestinationTestResponse(result))
}

// ListAlertPolicies returns tenant-scoped alert policies for management.
func (s *server) ListAlertPolicies(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListAlertPoliciesParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceAlertPolicies); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	response, err := s.notificationsService.ListAlertPolicies(
		c.Context(),
		principal,
		orgSlug,
		toAlertPoliciesListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	policies := make([]api.AlertPolicy, 0, len(response.Items))
	for _, policy := range response.Items {
		policies = append(policies, toAPIAlertPolicy(policy))
	}

	return c.JSON(api.AlertPolicyListResponse{
		AlertPolicies: policies,
		HasMore:       response.HasMore,
		NextCursor:    response.NextCursor,
		PrevCursor:    response.PrevCursor,
	})
}

// CreateAlertPolicy creates one tenant-scoped alert policy and destination bindings.
func (s *server) CreateAlertPolicy(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Create, model.ResourceAlertPolicies); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	var req api.NewAlertPolicy
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	params, err := toCreateAlertPolicyParams(orgSlug, req)
	if err != nil {
		return err
	}
	policy, err := s.notificationsService.CreateAlertPolicy(c.Context(), principal, params)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusCreated).JSON(toAPIAlertPolicy(policy))
}

// GetAlertPolicy returns one tenant-scoped alert policy and destination bindings.
func (s *server) GetAlertPolicy(
	c fiber.Ctx,
	slugParam api.SlugParam,
	policySlugParam api.AlertPolicySlugParam,
) error {
	orgSlug, policySlug, err := parseOrgAndResourceSlug(slugParam, policySlugParam, "alert policy")
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceAlertPolicies); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	policy, err := s.notificationsService.GetAlertPolicy(c.Context(), principal, orgSlug, policySlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIAlertPolicy(policy))
}

// UpdateAlertPolicy replaces one tenant-scoped alert policy and its destination bindings.
func (s *server) UpdateAlertPolicy(
	c fiber.Ctx,
	slugParam api.SlugParam,
	policySlugParam api.AlertPolicySlugParam,
) error {
	orgSlug, policySlug, err := parseOrgAndResourceSlug(slugParam, policySlugParam, "alert policy")
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceAlertPolicies); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	var req api.UpdateAlertPolicy
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	params, err := toUpdateAlertPolicyParams(orgSlug, policySlug, req)
	if err != nil {
		return err
	}
	policy, err := s.notificationsService.UpdateAlertPolicy(c.Context(), principal, params)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(toAPIAlertPolicy(policy))
}

// DeleteAlertPolicy removes one tenant-scoped alert policy and destination bindings.
func (s *server) DeleteAlertPolicy(
	c fiber.Ctx,
	slugParam api.SlugParam,
	policySlugParam api.AlertPolicySlugParam,
) error {
	orgSlug, policySlug, err := parseOrgAndResourceSlug(slugParam, policySlugParam, "alert policy")
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceAlertPolicies); err != nil {
		return err
	}
	principal, err := s.alertManagementPrincipal(c)
	if err != nil {
		return err
	}

	if err := s.notificationsService.DeleteAlertPolicy(c.Context(), principal, orgSlug, policySlug); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// toNotificationFilters maps generated query params into service filters.
func toNotificationFilters(params api.ListNotificationsParams) (notificationsvc.ListNotificationsFilters, error) {
	filters := notificationsvc.ListNotificationsFilters{}

	if params.LifecycleState != nil {
		lifecycleState, err := model.NotificationLifecycleStateString(string(*params.LifecycleState))
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid notification lifecycle state: %v", err))
		}
		filters.LifecycleState = &lifecycleState
	}

	if params.NotificationType != nil {
		notificationType, err := model.NotificationTypeString(string(*params.NotificationType))
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid notification type: %v", err))
		}
		filters.NotificationType = &notificationType
	}

	if params.Severity != nil {
		severity, err := model.NotificationSeverityString(string(*params.Severity))
		if err != nil {
			return filters, apierror.ValidationFailed(fmt.Sprintf("Invalid notification severity: %v", err))
		}
		filters.Severity = &severity
	}

	if params.SourceKind != nil {
		sourceKind, sourceKindSet, err := nullableEventEntityTypeParam(params.SourceKind)
		if err != nil {
			return filters, err
		}
		if sourceKindSet {
			filters.SourceKind = &sourceKind
		}
	}

	if params.SourceAction != nil {
		if params.SourceKind == nil {
			return filters, apierror.ValidationFailed("source_action requires source_kind")
		}
		sourceAction, sourceActionSet, err := nullableEventActionParam(params.SourceAction)
		if err != nil {
			return filters, err
		}
		if sourceActionSet {
			filters.SourceAction = &sourceAction
		}
	}

	if params.SourceRef != nil {
		sourceRef, sourceRefSet, err := nullableSourceRefParam(params.SourceRef)
		if err != nil {
			return filters, err
		}
		if sourceRefSet {
			filters.SourceRef = &sourceRef
		}
	}

	return filters, nil
}

// toCreateAlertDestinationParams maps an API create request into service input.
func toCreateAlertDestinationParams(
	orgSlug slug.Slug,
	req api.NewAlertDestination,
) (notificationsvc.CreateAlertDestinationParams, error) {
	destinationSlug, err := optionalSlugParam(req.Slug, "alert destination")
	if err != nil {
		return notificationsvc.CreateAlertDestinationParams{}, err
	}
	destinationType, err := model.AlertDestinationTypeString(string(req.Type))
	if err != nil {
		return notificationsvc.CreateAlertDestinationParams{}, apierror.ValidationFailed(fmt.Sprintf("Invalid alert destination type: %v", err))
	}
	return notificationsvc.CreateAlertDestinationParams{
		OrgSlug:         orgSlug,
		Slug:            destinationSlug,
		Name:            req.Name,
		Type:            destinationType,
		NonSecretConfig: alertDestinationConfigMap(req.NonSecretConfig),
	}, nil
}

// toUpdateAlertDestinationParams maps an API update request into service input.
func toUpdateAlertDestinationParams(
	orgSlug slug.Slug,
	destinationSlug slug.Slug,
	req api.UpdateAlertDestination,
) (notificationsvc.UpdateAlertDestinationParams, error) {
	destinationType, err := model.AlertDestinationTypeString(string(req.Type))
	if err != nil {
		return notificationsvc.UpdateAlertDestinationParams{}, apierror.ValidationFailed(fmt.Sprintf("Invalid alert destination type: %v", err))
	}
	return notificationsvc.UpdateAlertDestinationParams{
		OrgSlug:         orgSlug,
		Slug:            destinationSlug,
		Name:            req.Name,
		Type:            destinationType,
		NonSecretConfig: alertDestinationConfigMap(req.NonSecretConfig),
		Enabled:         req.Enabled,
	}, nil
}

// toCreateAlertPolicyParams maps an API create request into service input.
func toCreateAlertPolicyParams(
	orgSlug slug.Slug,
	req api.NewAlertPolicy,
) (notificationsvc.CreateAlertPolicyParams, error) {
	policySlug, err := optionalSlugParam(req.Slug, "alert policy")
	if err != nil {
		return notificationsvc.CreateAlertPolicyParams{}, err
	}
	scope, err := alertPolicyScope(req.Scope)
	if err != nil {
		return notificationsvc.CreateAlertPolicyParams{}, err
	}
	notificationTypeValue, notificationTypeSet, err := nullableNotificationTypeParam(req.NotificationType)
	if err != nil {
		return notificationsvc.CreateAlertPolicyParams{}, err
	}
	var notificationType *model.NotificationType
	if notificationTypeSet {
		notificationType = &notificationTypeValue
	}
	severityValue, severitySet, err := nullableNotificationSeverityParam(req.Severity)
	if err != nil {
		return notificationsvc.CreateAlertPolicyParams{}, err
	}
	var severity *model.NotificationSeverity
	if severitySet {
		severity = &severityValue
	}
	lifecycleStateValue, lifecycleStateSet, err := nullableNotificationLifecycleStateParam(req.LifecycleState)
	if err != nil {
		return notificationsvc.CreateAlertPolicyParams{}, err
	}
	var lifecycleState *model.NotificationLifecycleState
	if lifecycleStateSet {
		lifecycleState = &lifecycleStateValue
	}
	projectSlugValue, projectSlugSet, err := nullableSlugParam(req.ProjectSlug, "project")
	if err != nil {
		return notificationsvc.CreateAlertPolicyParams{}, err
	}
	environmentSlugValue, environmentSlugSet, err := nullableSlugParam(req.EnvironmentSlug, "environment")
	if err != nil {
		return notificationsvc.CreateAlertPolicyParams{}, err
	}
	var projectSlug *slug.Slug
	if projectSlugSet {
		projectSlug = &projectSlugValue
	}
	var environmentSlug *slug.Slug
	if environmentSlugSet {
		environmentSlug = &environmentSlugValue
	}
	sourceKindValue, sourceKindSet, err := nullableEventEntityTypeParam(req.SourceKind)
	if err != nil {
		return notificationsvc.CreateAlertPolicyParams{}, err
	}
	var sourceKind *model.EventEntityType
	if sourceKindSet {
		sourceKind = &sourceKindValue
	}
	sourceActionValue, sourceActionSet, err := nullableEventActionParam(req.SourceAction)
	if err != nil {
		return notificationsvc.CreateAlertPolicyParams{}, err
	}
	var sourceAction *model.EventAction
	if sourceActionSet {
		sourceAction = &sourceActionValue
	}
	sourceRefValue, sourceRefSet, err := nullableSourceRefParam(req.SourceRef)
	if err != nil {
		return notificationsvc.CreateAlertPolicyParams{}, err
	}
	var sourceRef *slug.Slug
	if sourceRefSet {
		sourceRef = &sourceRefValue
	}
	destinationSlugs, err := slugsFromAPI(req.DestinationSlugs, "alert policy destination")
	if err != nil {
		return notificationsvc.CreateAlertPolicyParams{}, err
	}
	return notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             req.Name,
		Scope:            scope,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		NotificationType: notificationType,
		Severity:         severity,
		LifecycleState:   lifecycleState,
		SourceKind:       sourceKind,
		SourceAction:     sourceAction,
		SourceRef:        sourceRef,
		DestinationSlugs: destinationSlugs,
	}, nil
}

// toUpdateAlertPolicyParams maps an API update request into service input.
func toUpdateAlertPolicyParams(
	orgSlug slug.Slug,
	policySlug slug.Slug,
	req api.UpdateAlertPolicy,
) (notificationsvc.UpdateAlertPolicyParams, error) {
	scope, err := alertPolicyScope(req.Scope)
	if err != nil {
		return notificationsvc.UpdateAlertPolicyParams{}, err
	}
	notificationTypeValue, notificationTypeSet, err := nullableNotificationTypeParam(req.NotificationType)
	if err != nil {
		return notificationsvc.UpdateAlertPolicyParams{}, err
	}
	var notificationType *model.NotificationType
	if notificationTypeSet {
		notificationType = &notificationTypeValue
	}
	severityValue, severitySet, err := nullableNotificationSeverityParam(req.Severity)
	if err != nil {
		return notificationsvc.UpdateAlertPolicyParams{}, err
	}
	var severity *model.NotificationSeverity
	if severitySet {
		severity = &severityValue
	}
	lifecycleStateValue, lifecycleStateSet, err := nullableNotificationLifecycleStateParam(req.LifecycleState)
	if err != nil {
		return notificationsvc.UpdateAlertPolicyParams{}, err
	}
	var lifecycleState *model.NotificationLifecycleState
	if lifecycleStateSet {
		lifecycleState = &lifecycleStateValue
	}
	projectSlugValue, projectSlugSet, err := nullableSlugParam(req.ProjectSlug, "project")
	if err != nil {
		return notificationsvc.UpdateAlertPolicyParams{}, err
	}
	environmentSlugValue, environmentSlugSet, err := nullableSlugParam(req.EnvironmentSlug, "environment")
	if err != nil {
		return notificationsvc.UpdateAlertPolicyParams{}, err
	}
	var projectSlug *slug.Slug
	if projectSlugSet {
		projectSlug = &projectSlugValue
	}
	var environmentSlug *slug.Slug
	if environmentSlugSet {
		environmentSlug = &environmentSlugValue
	}
	sourceKindValue, sourceKindSet, err := nullableEventEntityTypeParam(req.SourceKind)
	if err != nil {
		return notificationsvc.UpdateAlertPolicyParams{}, err
	}
	var sourceKind *model.EventEntityType
	if sourceKindSet {
		sourceKind = &sourceKindValue
	}
	sourceActionValue, sourceActionSet, err := nullableEventActionParam(req.SourceAction)
	if err != nil {
		return notificationsvc.UpdateAlertPolicyParams{}, err
	}
	var sourceAction *model.EventAction
	if sourceActionSet {
		sourceAction = &sourceActionValue
	}
	sourceRefValue, sourceRefSet, err := nullableSourceRefParam(req.SourceRef)
	if err != nil {
		return notificationsvc.UpdateAlertPolicyParams{}, err
	}
	var sourceRef *slug.Slug
	if sourceRefSet {
		sourceRef = &sourceRefValue
	}
	destinationSlugs, err := slugsFromAPI(req.DestinationSlugs, "alert policy destination")
	if err != nil {
		return notificationsvc.UpdateAlertPolicyParams{}, err
	}
	return notificationsvc.UpdateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             req.Name,
		Scope:            scope,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		NotificationType: notificationType,
		Severity:         severity,
		LifecycleState:   lifecycleState,
		SourceKind:       sourceKind,
		SourceAction:     sourceAction,
		SourceRef:        sourceRef,
		DestinationSlugs: destinationSlugs,
		Enabled:          req.Enabled,
	}, nil
}

// alertPolicyScope maps API scope text into the handwritten service enum.
func alertPolicyScope(scopeValue api.AlertPolicyScope) (model.AlertPolicyScope, error) {
	scope, err := model.AlertPolicyScopeString(string(scopeValue))
	if err != nil {
		return model.AlertPolicyScopeUnknown, apierror.ValidationFailed(fmt.Sprintf("Invalid alert policy scope: %v", err))
	}
	return scope, nil
}

// nullableNotificationTypeParam validates an optional notification type selector.
func nullableNotificationTypeParam(value *api.NotificationType) (model.NotificationType, bool, error) {
	if value == nil {
		return model.NotificationTypeUnknown, false, nil
	}
	parsed, err := model.NotificationTypeString(string(*value))
	if err != nil || parsed == model.NotificationTypeUnknown {
		return model.NotificationTypeUnknown, false, apierror.ValidationFailed("Invalid notification type")
	}
	return parsed, true, nil
}

// nullableNotificationSeverityParam validates an optional severity selector.
func nullableNotificationSeverityParam(value *api.NotificationSeverity) (model.NotificationSeverity, bool, error) {
	if value == nil {
		return model.NotificationSeverityUnknown, false, nil
	}
	parsed, err := model.NotificationSeverityString(string(*value))
	if err != nil || parsed == model.NotificationSeverityUnknown {
		return model.NotificationSeverityUnknown, false, apierror.ValidationFailed("Invalid notification severity")
	}
	return parsed, true, nil
}

// nullableNotificationLifecycleStateParam validates an optional lifecycle selector.
func nullableNotificationLifecycleStateParam(value *api.NotificationLifecycleState) (model.NotificationLifecycleState, bool, error) {
	if value == nil {
		return model.NotificationLifecycleStateUnknown, false, nil
	}
	parsed, err := model.NotificationLifecycleStateString(string(*value))
	if err != nil || parsed == model.NotificationLifecycleStateUnknown {
		return model.NotificationLifecycleStateUnknown, false, apierror.ValidationFailed("Invalid notification lifecycle state")
	}
	return parsed, true, nil
}

// optionalSlugParam validates an optional create-request slug.
func optionalSlugParam(value *api.Slug, label string) (slug.Slug, error) {
	if value == nil {
		return "", nil
	}
	parsed, err := slug.New(*value)
	if err != nil {
		return "", apierror.ValidationFailed(fmt.Sprintf("Invalid %s slug: %v", label, err))
	}
	return parsed, nil
}

// nullableSlugParam validates an optional nullable slug request field.
func nullableSlugParam(value *api.NullableSlug, label string) (slug.Slug, bool, error) {
	if value == nil {
		return "", false, nil
	}
	parsed, err := slug.New(*value)
	if err != nil {
		return "", false, apierror.ValidationFailed(fmt.Sprintf("Invalid %s slug: %v", label, err))
	}
	return parsed, true, nil
}

// nullableEventEntityTypeParam validates an optional alert-policy source kind selector.
func nullableEventEntityTypeParam(value *api.AlertPolicySourceKind) (model.EventEntityType, bool, error) {
	if value == nil {
		return model.EventEntityTypeUnknown, false, nil
	}
	if !value.Valid() {
		return model.EventEntityTypeUnknown, false, apierror.ValidationFailed("Invalid alert policy source kind")
	}
	parsed, err := model.EventEntityTypeString(string(*value))
	if err != nil || parsed == model.EventEntityTypeUnknown {
		return model.EventEntityTypeUnknown, false, apierror.ValidationFailed("Invalid alert policy source kind")
	}
	return parsed, true, nil
}

// nullableEventActionParam validates an optional alert-policy source action selector.
func nullableEventActionParam(value *api.AlertPolicySourceAction) (model.EventAction, bool, error) {
	if value == nil {
		return model.EventActionUnknown, false, nil
	}
	if !value.Valid() {
		return model.EventActionUnknown, false, apierror.ValidationFailed("Invalid alert policy source action")
	}
	parsed, err := model.EventActionString(string(*value))
	if err != nil || parsed == model.EventActionUnknown {
		return model.EventActionUnknown, false, apierror.ValidationFailed("Invalid alert policy source action")
	}
	return parsed, true, nil
}

// nullableSourceRefParam validates optional alert policy source refs as slugs.
func nullableSourceRefParam(value *string) (slug.Slug, bool, error) {
	if value == nil {
		return "", false, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return "", false, nil
	}
	parsed, err := slug.New(trimmed)
	if err != nil {
		return "", false, apierror.ValidationFailed(fmt.Sprintf("Invalid alert policy source ref: %v", err))
	}
	return parsed, true, nil
}

// slugsFromAPI validates API slug arrays before service calls.
func slugsFromAPI(values []api.Slug, label string) ([]slug.Slug, error) {
	ret := make([]slug.Slug, 0, len(values))
	for _, value := range values {
		parsed, err := slug.New(value)
		if err != nil {
			return nil, apierror.ValidationFailed(fmt.Sprintf("Invalid %s slug: %v", label, err))
		}
		ret = append(ret, parsed)
	}
	return ret, nil
}

// notificationSlugsFromParam validates bounded bulk diagnostics slugs.
func notificationSlugsFromParam(raw api.NotificationSlugsParam) ([]slug.Slug, error) {
	if len(raw) == 0 || len(raw) > maxAlertDeliveryAttemptNotificationSlugs {
		return nil, apierror.ValidationFailed("notification_slugs must contain between 1 and 50 slugs")
	}

	ret := make([]slug.Slug, 0, len(raw))
	seen := map[slug.Slug]struct{}{}
	for _, part := range raw {
		value := strings.TrimSpace(part)
		if value == "" {
			return nil, apierror.ValidationFailed("notification_slugs must not contain blank values")
		}
		parsed, err := slug.New(value)
		if err != nil {
			return nil, apierror.ValidationFailed(fmt.Sprintf("Invalid notification slug: %v", err))
		}
		if _, ok := seen[parsed]; ok {
			continue
		}
		seen[parsed] = struct{}{}
		ret = append(ret, parsed)
	}
	return ret, nil
}

// parseOrgAndResourceSlug validates common organization/resource path slugs.
func parseOrgAndResourceSlug(
	orgSlugParam api.SlugParam,
	resourceSlugParam string,
	resourceLabel string,
) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(orgSlugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}
	resourceSlug, err := slug.New(resourceSlugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid %s slug: %v", resourceLabel, err))
	}
	return orgSlug, resourceSlug, nil
}

// alertManagementPrincipal maps the authenticated request actor to the notification service auth input.
func (s *server) alertManagementPrincipal(c fiber.Ctx) (notificationsvc.AlertManagementPrincipal, error) {
	if principal, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		return notificationsvc.AlertManagementPrincipal{
			APIKeySlug:     principal.Slug,
			APIKeyOrgSlug:  principal.OrgSlug,
			APIKeyRoleSlug: principal.Role,
		}, nil
	}

	userPrincipal, ok := reqctx.GetUserPrincipal(c, s.sessionStore)
	if !ok {
		return notificationsvc.AlertManagementPrincipal{}, apierror.Unauthorized()
	}
	return notificationsvc.AlertManagementPrincipal{UserSlug: userPrincipal.Slug}, nil
}

// toAPIAlertDestination maps a service alert destination to the API shape.
func toAPIAlertDestination(destination *model.AlertDestination) api.AlertDestination {
	return api.AlertDestination{
		Slug:            destination.Slug.String(),
		Name:            destination.Name,
		Type:            api.AlertDestinationType(destination.Type.String()),
		NonSecretConfig: toAPIAlertDestinationConfig(destination.NonSecretConfig),
		Enabled:         destination.Enabled,
		CreatedAt:       destination.CreatedAt,
		UpdatedAt:       destination.UpdatedAt,
	}
}

// alertDestinationConfigMap decodes the generated OpenAPI union into the service map contract.
func alertDestinationConfigMap(config api.AlertDestinationConfig) map[string]any {
	raw, err := json.Marshal(config)
	if err != nil || string(raw) == "null" {
		return map[string]any{}
	}
	ret := map[string]any{}
	if err := json.Unmarshal(raw, &ret); err != nil {
		return map[string]any{}
	}
	return ret
}

// toAPIAlertDestinationConfig preserves the stored safe adapter config in the generated union wrapper.
func toAPIAlertDestinationConfig(config map[string]any) api.AlertDestinationConfig {
	raw, err := json.Marshal(config)
	if err != nil {
		raw = []byte(`{}`)
	}
	var ret api.AlertDestinationConfig
	_ = ret.UnmarshalJSON(raw)
	return ret
}

// toAPIAlertDestinationTestResponse maps the safe destination test result to the API response.
func toAPIAlertDestinationTestResponse(result *notificationsvc.AlertDestinationTestResult) api.AlertDestinationTestResponse {
	jobs := make([]api.AlertDestinationTestJob, 0, len(result.EnqueuedJobs))
	for _, job := range result.EnqueuedJobs {
		jobs = append(jobs, api.AlertDestinationTestJob{
			Kind:  job.Kind,
			Queue: job.Queue,
			State: job.State,
		})
	}
	return api.AlertDestinationTestResponse{
		Status:               "accepted",
		DestinationSlug:      result.DestinationSlug.String(),
		DestinationType:      api.AlertDestinationType(result.DestinationType.String()),
		RecipientCount:       result.RecipientCount,
		DiagnosticsPersisted: result.DiagnosticsPersisted,
		EnqueuedJobs:         jobs,
	}
}

// toAPIAlertPolicy maps a service alert policy to the API shape.
func toAPIAlertPolicy(policy *model.AlertPolicy) api.AlertPolicy {
	return api.AlertPolicy{
		Slug:             policy.Slug.String(),
		Name:             policy.Name,
		Scope:            api.AlertPolicyScope(policy.Scope.String()),
		ProjectSlug:      toAPINullableSlug(policy.ProjectSlug),
		EnvironmentSlug:  toAPINullableSlug(policy.EnvironmentSlug),
		NotificationType: toAPINullableNotificationType(policy.NotificationType),
		Severity:         toAPINullableNotificationSeverity(policy.Severity),
		LifecycleState:   toAPINullableNotificationLifecycleState(policy.LifecycleState),
		SourceKind:       toAPINullableAlertPolicySourceKind(policy.SourceKind),
		SourceAction:     toAPINullableAlertPolicySourceAction(policy.SourceAction),
		SourceRef:        toAPINullableSlugString(policy.SourceRef),
		DestinationSlugs: toAPISlugs(policy.DestinationSlugs),
		Enabled:          policy.Enabled,
		CreatedAt:        policy.CreatedAt,
		UpdatedAt:        policy.UpdatedAt,
	}
}

// toAPINullableSlugString maps optional slugs into nullable API string fields.
func toAPINullableSlugString(value *slug.Slug) *string {
	if value == nil {
		return nil
	}
	ret := value.String()
	return &ret
}

// toAPINullableNotificationType maps optional service notification type selectors.
func toAPINullableNotificationType(value *model.NotificationType) *api.NotificationType {
	if value == nil {
		return nil
	}
	ret := api.NotificationType(value.String())
	return &ret
}

// toAPINullableNotificationSeverity maps optional service severity selectors.
func toAPINullableNotificationSeverity(value *model.NotificationSeverity) *api.NotificationSeverity {
	if value == nil {
		return nil
	}
	ret := api.NotificationSeverity(value.String())
	return &ret
}

// toAPINullableNotificationLifecycleState maps optional service lifecycle selectors.
func toAPINullableNotificationLifecycleState(value *model.NotificationLifecycleState) *api.NotificationLifecycleState {
	if value == nil {
		return nil
	}
	ret := api.NotificationLifecycleState(value.String())
	return &ret
}

// toAPINullableAlertPolicySourceKind maps optional service event entity selectors.
func toAPINullableAlertPolicySourceKind(value *model.EventEntityType) *api.AlertPolicySourceKind {
	if value == nil {
		return nil
	}
	apiValue := api.AlertPolicySourceKind(value.String())
	return &apiValue
}

// toAPINullableAlertPolicySourceAction maps optional service event action selectors.
func toAPINullableAlertPolicySourceAction(value *model.EventAction) *api.AlertPolicySourceAction {
	if value == nil {
		return nil
	}
	apiValue := api.AlertPolicySourceAction(value.String())
	return &apiValue
}

// toAPINotificationRecord maps a service notification list item to the API shape.
func toAPINotificationRecord(item *model.NotificationListItem) api.NotificationRecord {
	return api.NotificationRecord{
		Slug:             item.Slug.String(),
		NotificationType: api.NotificationType(item.NotificationType.String()),
		Severity:         api.NotificationSeverity(item.Severity.String()),
		LifecycleState:   api.NotificationLifecycleState(item.LifecycleState.String()),
		Summary:          item.Summary,
		SourceEventSlug:  toAPINullableSlug(item.SourceEventSlug),
		SourceEntityType: api.EventEntityType(item.SourceEntityType.String()),
		SourceAction:     api.EventAction(item.SourceAction.String()),
		SourceRefs:       item.SourceRefs,
		SafeMetadata:     item.SafeMetadata,
		OccurrenceCount:  item.OccurrenceCount,
		FirstSeenAt:      item.FirstSeenAt,
		LastSeenAt:       item.LastSeenAt,
		LastOccurrenceAt: item.LastOccurrenceAt,
		DispatchStatuses: toAPINotificationDispatchStatuses(item.DispatchStatuses),
		AcknowledgedAt:   toAPITimestampPtr(item.AcknowledgedAt),
		ResolvedAt:       toAPITimestampPtr(item.ResolvedAt),
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
}

// toAPINotificationDispatchStatuses maps typed dispatch snapshots to API rows.
func toAPINotificationDispatchStatuses(
	statuses []model.NotificationDispatchSummary,
) []api.NotificationDispatchSummary {
	ret := make([]api.NotificationDispatchSummary, 0, len(statuses))
	for _, status := range statuses {
		ret = append(ret, api.NotificationDispatchSummary{
			DestinationSlug: status.DestinationSlug.String(),
			Status:          api.NotificationDispatchStatus(status.Status.String()),
			QueuedAt:        toAPITimestampPtr(status.QueuedAt),
		})
	}
	return ret
}

// toAPIAlertDeliveryAttempt maps a service delivery attempt to the API shape.
func toAPIAlertDeliveryAttempt(attempt *model.AlertDeliveryAttempt) api.AlertDeliveryAttempt {
	return api.AlertDeliveryAttempt{
		Slug:                     attempt.Slug.String(),
		NotificationRecordSlug:   attempt.NotificationRecordSlug.String(),
		AlertPolicySlug:          toAPINullableSlug(attempt.AlertPolicySlug),
		DestinationSlug:          toAPINullableSlug(attempt.DestinationSlug),
		DestinationName:          attempt.DestinationName,
		DestinationType:          api.AlertDestinationType(attempt.DestinationType.String()),
		Status:                   api.AlertDeliveryAttemptStatus(attempt.Status.String()),
		ProviderAttempt:          attempt.ProviderAttempt,
		ProviderResponseMetadata: attempt.ProviderResponseMetadata,
		ErrorCategory:            attempt.ErrorCategory,
		ErrorMessage:             attempt.ErrorMessage,
		QueuedAt:                 attempt.QueuedAt,
		SentAt:                   toAPITimestampPtr(attempt.SentAt),
		FailedAt:                 toAPITimestampPtr(attempt.FailedAt),
		CreatedAt:                attempt.CreatedAt,
		UpdatedAt:                attempt.UpdatedAt,
	}
}

// toAPITimestampPtr maps optional service timestamps to nullable API timestamps.
func toAPITimestampPtr(value *time.Time) *api.Timestamp {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
