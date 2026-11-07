//go:generate go tool -modfile=../../tools/go.mod enumer -type=State -linecomment -output=state_enumer.go
package accountclosuresvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/accountclosures"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

type State int

const (
	StatePending           State = iota // pending
	StateCompleted                      // completed
	StateOwnershipRequired              // ownership_required
	StateFailed                         // failed
)

type Result struct {
	Slug                        slug.Slug
	State                       State
	RequestedAt                 time.Time
	CompletedAt                 *time.Time
	OwnershipRequired           []slug.Slug
	OrganizationRemovalRequired []slug.Slug
}

type Service struct {
	txm          postgres.TransactionManager
	repo         accountclosures.Repository
	events       workerproducer.EventProducer
	queue        *queue.Client
	beforeDelete func(context.Context, postgres.DBTX, int32) error
}

func New(txm postgres.TransactionManager, repo accountclosures.Repository, events workerproducer.EventProducer, queueClient *queue.Client, options ...Option) (*Service, error) {
	if txm == nil || events == nil || queueClient == nil {
		return nil, xerror.Unexpected().Detail("account closure requires transactions, durable audit events and queue cancellation")
	}
	s := &Service{txm: txm, repo: repo, events: events, queue: queueClient}
	for _, option := range options {
		option(s)
	}
	return s, nil
}

// Request acts only for the authenticated subject. Acceptance, access revocation,
// and durable retry admission commit together or leave the account unchanged.
func (s *Service) Request(ctx context.Context, user slug.Slug) (Result, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (Result, error) {
		subject, err := s.repo.LockSubject(ctx, tx, user)
		if err != nil {
			return Result{}, err
		}
		if subject.Deleted {
			request, err := s.repo.FindRequest(ctx, tx, subject.ID)
			if err != nil {
				return Result{}, err
			}
			return toResult(request)
		}
		memberships, err := s.repo.LockOrganizations(ctx, tx, subject.ID)
		if err != nil {
			return Result{}, err
		}
		result := Result{Slug: slug.Slug("delete-" + uuid.New().String()), State: StateOwnershipRequired, RequestedAt: time.Now()}
		for _, membership := range memberships {
			otherMember, err := s.repo.HasOtherMember(ctx, tx, membership.OrganizationSlug, subject.ID)
			if err != nil {
				return Result{}, err
			}
			if !otherMember {
				result.OrganizationRemovalRequired = append(result.OrganizationRemovalRequired, membership.OrganizationSlug)
				continue
			}
			otherAdmin, err := s.repo.HasOtherAdmin(ctx, tx, membership.OrganizationSlug, subject.ID)
			if err != nil {
				return Result{}, err
			}
			if !otherAdmin {
				result.OwnershipRequired = append(result.OwnershipRequired, membership.OrganizationSlug)
			}
		}
		if len(result.OwnershipRequired) > 0 || len(result.OrganizationRemovalRequired) > 0 {
			return result, nil
		}
		legacyPending, err := s.repo.LegacyProcessingPending(ctx, tx, subject.ID)
		if err != nil {
			return Result{}, err
		}
		if legacyPending {
			return Result{}, xerror.Unavailable(xerror.User).Detail("unattributed processing must be resolved before account deletion")
		}
		request, err := s.repo.CreateRequest(ctx, tx, subject.ID, result.Slug)
		if err != nil {
			return Result{}, err
		}
		if err := s.repo.CancelExecutions(ctx, tx, subject.ID); err != nil {
			return Result{}, err
		}
		jobIDs, err := s.repo.LockJobs(ctx, tx, user)
		if err != nil {
			return Result{}, err
		}
		for _, id := range jobIDs {
			if err := s.queue.CancelJob(ctx, tx, int64(id)); err != nil {
				return Result{}, err
			}
		}
		if err := s.repo.DeletePATs(ctx, tx, subject.ID); err != nil {
			return Result{}, err
		}
		if err := s.repo.DeleteMFAChallenges(ctx, tx, subject.ID); err != nil {
			return Result{}, err
		}
		if err := s.repo.RevokePendingInvitations(ctx, tx, subject.Email); err != nil {
			return Result{}, err
		}
		for _, membership := range memberships {
			_, err := s.events.EnqueueRequired(ctx, tx, membership.OrganizationSlug, model.UserEventActor(user), memberRemoved{user: user, role: membership.RoleSlug})
			if err != nil {
				return Result{}, err
			}
		}
		if err := s.repo.DeleteMemberships(ctx, tx, subject.ID); err != nil {
			return Result{}, err
		}
		if err := s.repo.Revoke(ctx, tx, subject.ID); err != nil {
			return Result{}, err
		}
		subject.Deleted = true
		request, err = s.finish(ctx, tx, request, subject)
		if err != nil {
			return Result{}, err
		}
		if request.State != StateCompleted.String() {
			_, err = queue.NewEnqueuer(s.queue).EnqueueTx(ctx, tx, queue.EnqueueRequest{Args: jobs.AccountDeletionArgs{Request: request.Slug}})
			if err != nil {
				return Result{}, err
			}
		}
		return toResult(request)
	})
}

// Complete is idempotent. A pending result is not evidence that external
// processing stopped; the worker retries without a retention-time shortcut.
func (s *Service) Complete(ctx context.Context, requestSlug slug.Slug) (bool, error) {
	request, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (accountclosures.Request, error) {
		request, err := s.repo.LockRequest(ctx, tx, requestSlug)
		if err != nil || request.State == StateCompleted.String() {
			return request, err
		}
		subject, err := s.repo.LockSubjectByID(ctx, tx, request.UserID)
		if err != nil {
			return request, err
		}
		if !subject.Deleted {
			return request, xerror.Unexpected().Detail("account access must be revoked before deletion")
		}
		return s.finish(ctx, tx, request, subject)
	})
	if err != nil {
		failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		markErr := s.txm.RunInTx(failureCtx, func(ctx context.Context, tx postgres.DBTX) error {
			return s.repo.Fail(ctx, tx, requestSlug)
		})
		if markErr != nil {
			return false, xerror.Wrapf("delete account: %w; record deletion failure: %w", err, markErr)
		}
	}
	return request.State == StateCompleted.String(), err
}

func (s *Service) finish(ctx context.Context, tx postgres.DBTX, request accountclosures.Request, subject accountclosures.Subject) (accountclosures.Request, error) {
	pending, err := s.repo.ProcessingPending(ctx, tx, subject)
	if err != nil || pending {
		return request, err
	}
	if s.beforeDelete != nil {
		if err := s.beforeDelete(ctx, tx, subject.ID); err != nil {
			return request, err
		}
	}
	if err := s.repo.HardDelete(ctx, tx, subject); err != nil {
		return request, err
	}
	return s.repo.Complete(ctx, tx, request.ID)
}

func toResult(request accountclosures.Request) (Result, error) {
	state, err := StateString(request.State)
	if err != nil {
		return Result{}, xerror.Unexpected().With(err)
	}
	return Result{Slug: request.Slug, State: state, RequestedAt: request.RequestedAt, CompletedAt: request.CompletedAt}, nil
}

type memberRemoved struct{ user, role slug.Slug }

func (e memberRemoved) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix: "evt-account-member-delete", EventClass: model.EventClassSecurityAudit,
		EntityType: model.EventEntityTypeMember, EntitySlug: e.user, Action: model.EventActionDelete,
		SchemaVersion: model.CurrentEventSchemaVersion, Metadata: map[string]any{"role_slug": e.role.String()},
	}, nil
}

type Option func(*Service)

func WithBeforeDelete(beforeDelete func(context.Context, postgres.DBTX, int32) error) Option {
	return func(s *Service) { s.beforeDelete = beforeDelete }
}
