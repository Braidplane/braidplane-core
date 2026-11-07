package notificationsvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
)

var (
	ErrDestinationNotFound        = reponotifications.ErrDestinationNotFound
	ErrDestinationSlugTaken       = reponotifications.ErrDestinationSlugTaken
	ErrDestinationDisabled        = xerror.New(xerror.KindInvalidInput, "ALERT_DESTINATION_DISABLED", "alert destination disabled")
	ErrDestinationTestUnsupported = xerror.New(
		xerror.KindUnsupported,
		"ALERT_DESTINATION_TEST_UNSUPPORTED",
		"alert destination test unsupported",
	)
	ErrAlertPolicyNotFound  = reponotifications.ErrAlertPolicyNotFound
	ErrAlertPolicySlugTaken = reponotifications.ErrAlertPolicySlugTaken
	ErrRecordNotFound       = reponotifications.ErrRecordNotFound
	ErrRecordSlugTaken      = reponotifications.ErrRecordSlugTaken
	ErrDeliveryNotFound     = reponotifications.ErrDeliveryNotFound
	ErrInvalidInput         = xerror.ValidationFailed()
)
