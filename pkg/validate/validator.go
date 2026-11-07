package validate

import (
	"sync"

	"github.com/Braidplane/braidplane-core/pkg/structvalidate"
	"github.com/go-playground/validator/v10"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

var (
	once             sync.Once
	defaultValidator *validator.Validate
)

// Default returns the singleton validator instance with all custom validations registered.
func Default() *validator.Validate {
	once.Do(func() {
		defaultValidator = createValidator()
	})
	return defaultValidator
}

// New creates a new validator with all custom validations registered.
// Prefer Default() for typical use cases; use New() for testing or when you need multiple
// instances.
func New() *validator.Validate {
	return createValidator()
}

// createValidator creates and configures a validator with all custom validations.
func createValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())

	// Register custom validators
	err := v.RegisterValidation("datasourcetype", validateDataSourceType)
	if err != nil {
		panic(err)
	}

	// Register custom type function for model.Role
	err = v.RegisterValidation("role", validateRoleType)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("runsourcetype", validateRunSourceType)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("runsourceentrypoint", validateRunSourceEntrypoint)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("queryparamdatatype", validateQueryParameterDataType)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("eventactortype", validateEventActorType)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("evententitytype", validateEventEntityType)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("eventaction", validateEventAction)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("alertdestinationtype", validateAlertDestinationType)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("alertpolicyscope", validateAlertPolicyScope)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("environmentcolor", validateEnvironmentColor)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("notificationseverity", validateNotificationSeverity)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("notificationtype", validateNotificationType)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("notificationlifecyclestate", validateNotificationLifecycleState)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("schedulemisfireresult", validateScheduleMisfireResult)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("wishlistfocus", validateWishlistFocus)
	if err != nil {
		panic(err)
	}

	err = v.RegisterValidation("wishlistcommunicationrequestversion", validateWishlistCommunicationRequestVersion)
	if err != nil {
		panic(err)
	}

	err = structvalidate.RegisterPipelineValidationTags(v)
	if err != nil {
		panic(err)
	}

	return v
}

// roleTypeFunc validates that a field of type model.Role is valid.
func validateRoleType(fl validator.FieldLevel) bool {
	role, ok := fl.Field().Interface().(model.Role)
	if !ok {
		return false
	}

	return role.IsARole()
}

// validateDataSourceType validates that a DataSourceType is valid.
func validateDataSourceType(fl validator.FieldLevel) bool {
	dsType, ok := fl.Field().Interface().(model.DataSourceType)
	if !ok {
		return false
	}

	// Check if the value is a valid DataSourceType
	// Valid values are: DataSourceTypePostgres (0), DataSourceTypeMySQL (1)
	return dsType.IsADataSourceType()
}

// validateQueryParameterDataType rejects unknown query parameter data types.
func validateQueryParameterDataType(fl validator.FieldLevel) bool {
	value, ok := fl.Field().Interface().(model.ParameterDataType)
	if !ok {
		return false
	}

	return value.IsAParameterDataType() && value != model.ParameterDataTypeUnknown
}

// validateEventActorType rejects unknown event actor types.
func validateEventActorType(fl validator.FieldLevel) bool {
	value, ok := fl.Field().Interface().(model.EventActorType)
	if !ok {
		return false
	}

	return value.IsAEventActorType() && value != model.EventActorTypeUnknown
}

// validateEventEntityType rejects unknown event entity types.
func validateEventEntityType(fl validator.FieldLevel) bool {
	value, ok := eventEntityTypeValidationValue(fl.Field().Interface())
	if !ok {
		return false
	}

	return value.IsAEventEntityType() && value != model.EventEntityTypeUnknown
}

// validateEventAction rejects unknown event action values.
func validateEventAction(fl validator.FieldLevel) bool {
	value, ok := eventActionValidationValue(fl.Field().Interface())
	if !ok {
		return false
	}

	return value.IsAEventAction() && value != model.EventActionUnknown
}

// validateAlertDestinationType rejects unknown alert destination types.
func validateAlertDestinationType(fl validator.FieldLevel) bool {
	value, ok := fl.Field().Interface().(model.AlertDestinationType)
	if !ok {
		return false
	}

	return value.IsAAlertDestinationType() && value != model.AlertDestinationTypeUnknown
}

// validateAlertPolicyScope rejects unknown alert policy scopes.
func validateAlertPolicyScope(fl validator.FieldLevel) bool {
	value, ok := fl.Field().Interface().(model.AlertPolicyScope)
	if !ok {
		return false
	}

	return value.IsAAlertPolicyScope() && value != model.AlertPolicyScopeUnknown
}

// validateEnvironmentColor rejects unknown environment palette values.
func validateEnvironmentColor(fl validator.FieldLevel) bool {
	value, ok := fl.Field().Interface().(model.EnvironmentColor)
	if !ok {
		return false
	}

	return value.IsAEnvironmentColor() && value != model.EnvironmentColorUnknown
}

// validateNotificationSeverity rejects unknown notification severity values.
func validateNotificationSeverity(fl validator.FieldLevel) bool {
	value, ok := notificationSeverityValidationValue(fl.Field().Interface())
	if !ok {
		return false
	}

	return value.IsANotificationSeverity() && value != model.NotificationSeverityUnknown
}

// validateNotificationType rejects unknown notification type values.
func validateNotificationType(fl validator.FieldLevel) bool {
	value, ok := notificationTypeValidationValue(fl.Field().Interface())
	if !ok {
		return false
	}

	return value.IsANotificationType() && value != model.NotificationTypeUnknown
}

// validateNotificationLifecycleState rejects unknown notification lifecycle states.
func validateNotificationLifecycleState(fl validator.FieldLevel) bool {
	value, ok := notificationLifecycleStateValidationValue(fl.Field().Interface())
	if !ok {
		return false
	}

	return value.IsANotificationLifecycleState() && value != model.NotificationLifecycleStateUnknown
}

// eventEntityTypeValidationValue unwraps optional event entity type selectors for validation tags.
func eventEntityTypeValidationValue(raw any) (model.EventEntityType, bool) {
	switch value := raw.(type) {
	case model.EventEntityType:
		return value, true
	case *model.EventEntityType:
		if value == nil {
			return model.EventEntityTypeUnknown, false
		}
		return *value, true
	default:
		return model.EventEntityTypeUnknown, false
	}
}

// eventActionValidationValue unwraps optional event action selectors for validation tags.
func eventActionValidationValue(raw any) (model.EventAction, bool) {
	switch value := raw.(type) {
	case model.EventAction:
		return value, true
	case *model.EventAction:
		if value == nil {
			return model.EventActionUnknown, false
		}
		return *value, true
	default:
		return model.EventActionUnknown, false
	}
}

// notificationSeverityValidationValue unwraps optional notification severities for validation tags.
func notificationSeverityValidationValue(raw any) (model.NotificationSeverity, bool) {
	switch value := raw.(type) {
	case model.NotificationSeverity:
		return value, true
	case *model.NotificationSeverity:
		if value == nil {
			return model.NotificationSeverityUnknown, false
		}
		return *value, true
	default:
		return model.NotificationSeverityUnknown, false
	}
}

// notificationTypeValidationValue unwraps optional notification types for validation tags.
func notificationTypeValidationValue(raw any) (model.NotificationType, bool) {
	switch value := raw.(type) {
	case model.NotificationType:
		return value, true
	case *model.NotificationType:
		if value == nil {
			return model.NotificationTypeUnknown, false
		}
		return *value, true
	default:
		return model.NotificationTypeUnknown, false
	}
}

// notificationLifecycleStateValidationValue unwraps optional notification lifecycle states for validation tags.
func notificationLifecycleStateValidationValue(raw any) (model.NotificationLifecycleState, bool) {
	switch value := raw.(type) {
	case model.NotificationLifecycleState:
		return value, true
	case *model.NotificationLifecycleState:
		if value == nil {
			return model.NotificationLifecycleStateUnknown, false
		}
		return *value, true
	default:
		return model.NotificationLifecycleStateUnknown, false
	}
}

// validateScheduleMisfireResult rejects unknown schedule fire result values.
func validateScheduleMisfireResult(fl validator.FieldLevel) bool {
	value, ok := fl.Field().Interface().(model.PipelineTriggerScheduleMisfireResult)
	if !ok {
		return false
	}

	return value.IsAPipelineTriggerScheduleMisfireResult() &&
		value != model.PipelineTriggerScheduleMisfireResultUnknown
}

// validateSinkIdempotencyClass rejects unknown sink idempotency classes.
func validateWishlistFocus(fl validator.FieldLevel) bool {
	value, ok := fl.Field().Interface().(model.WishlistFocus)
	if !ok {
		return false
	}

	return value.IsAWishlistFocus() && value != model.WishlistFocusUnknown
}

// validateWishlistCommunicationRequestVersion accepts current and historically persisted request wording.
func validateWishlistCommunicationRequestVersion(fl validator.FieldLevel) bool {
	value, ok := fl.Field().Interface().(string)
	return ok && (value == model.WishlistCommunicationRequestVersionPreAlphaInvitationV1 ||
		value == model.WishlistCommunicationRequestVersionPreAlphaInvitationV2)
}

func validateRunSourceType(fl validator.FieldLevel) bool {
	value, ok := fl.Field().Interface().(model.PipelineRunSourceType)
	if !ok {
		return false
	}

	return value.IsAPipelineRunSourceType() && value != model.PipelineRunSourceTypeUnknown
}

// validateRunSourceEntrypoint accepts the launch-supported run entrypoints.
func validateRunSourceEntrypoint(fl validator.FieldLevel) bool {
	value, ok := fl.Field().Interface().(model.PipelineRunSourceEntrypoint)
	if !ok {
		return false
	}

	return value.IsAPipelineRunSourceEntrypoint() &&
		value != model.PipelineRunSourceEntrypointUnknown
}
