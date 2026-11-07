package producer

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type notificationCandidateProducer struct{ enqueuer queue.Enqueuer }

func NewNotificationCandidateProducer(enqueuer queue.Enqueuer) (notificationsvc.CandidateProducer, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("queue enqueuer is required")
	}
	return &notificationCandidateProducer{enqueuer: enqueuer}, nil
}

func (p *notificationCandidateProducer) EnqueueCandidate(ctx context.Context, tx postgres.DBTX, candidate notificationsvc.CreateCandidateParams) error {
	if err := notificationsvc.ValidateCandidate(candidate); err != nil {
		return err
	}
	if candidate.RecordSlug == nil {
		identity := sha256.Sum256([]byte(candidate.OrgSlug.String() + "/" + candidate.DedupeFingerprint))
		record := slug.Slug(fmt.Sprintf("notification-%x", identity))
		candidate.RecordSlug = &record
	}
	_, err := p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Queue: jobs.NotificationDeliveryQueue,
		Args:  jobs.NotificationCandidateArgs{OrganizationSlug: candidate.OrgSlug, ObservationSlug: *candidate.RecordSlug, Candidate: candidate},
	})
	return err
}
