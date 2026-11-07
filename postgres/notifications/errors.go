package notifications

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	ErrDestinationNotFound     = xerror.NotFound(xerror.AlertDestination)
	ErrDestinationSlugTaken    = xerror.AlreadyExists(xerror.AlertDestination)
	ErrDestinationInvalidInput = xerror.InvalidInput(xerror.AlertDestination)
	ErrAlertPolicyNotFound     = xerror.NotFound(xerror.AlertPolicy)
	ErrAlertPolicySlugTaken    = xerror.AlreadyExists(xerror.AlertPolicy)
	ErrAlertPolicyInvalidInput = xerror.InvalidInput(xerror.AlertPolicy)
	ErrRecordNotFound          = xerror.NotFound(xerror.Notification)
	ErrRecordSlugTaken         = xerror.AlreadyExists(xerror.Notification)
	ErrRecordInvalidInput      = xerror.InvalidInput(xerror.Notification)
	ErrDeliveryNotFound        = xerror.NotFound(xerror.Notification)
	ErrDeliveryInvalidInput    = xerror.InvalidInput(xerror.Notification)
)
