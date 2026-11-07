package jobs

import (
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// NotificationCandidateArgs carries only validated declaration identities and
// safe diagnostics. Rendered messages, credentials and provider data stay out.
type NotificationCandidateArgs struct {
	OrganizationSlug slug.Slug                   `json:"organization_slug" river:"unique"`
	ObservationSlug  slug.Slug                   `json:"observation_slug" river:"unique"`
	Candidate        model.NotificationCandidate `json:"candidate"`
}

func (NotificationCandidateArgs) Kind() string { return "notification_candidate" }

func (NotificationCandidateArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: NotificationDeliveryQueue, UniqueOpts: river.UniqueOpts{
		ByArgs: true, ByState: rivertype.UniqueOptsByStateDefault(),
	}}
}
