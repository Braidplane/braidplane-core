package jobs

import (
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const NotificationDeliveryQueue = "notification_delivery"

// NotificationDeliveryArgs is the durable River payload for one notification
// dispatch. It intentionally carries only slugs; renderable message data,
// recipients, provider responses, configs, and credentials stay out of River args.
type NotificationDeliveryArgs struct {
	Test             bool      `json:"test,omitempty"`
	OrganizationSlug slug.Slug `json:"organization_slug" river:"unique"`
	RecordSlug       slug.Slug `json:"record_slug" river:"unique"`
	DestinationSlug  slug.Slug `json:"destination_slug" river:"unique"`
	AlertPolicySlug  slug.Slug `json:"alert_policy_slug"`
}

// Kind returns the stable River job kind for notification delivery.
func (NotificationDeliveryArgs) Kind() string {
	return "notification_delivery"
}

// InsertOpts routes dispatch jobs to the notification delivery queue and
// deduplicates the safe slug identity while jobs are in default unique states.
func (NotificationDeliveryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: NotificationDeliveryQueue,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: rivertype.UniqueOptsByStateDefault(),
		},
	}
}
