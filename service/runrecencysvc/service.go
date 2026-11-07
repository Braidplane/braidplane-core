package runrecencysvc

import (
	"context"
	"strconv"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/runrecency"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
)

type Repository interface {
	Inspect(context.Context, postgres.DBTX, runrecency.Scope) (runrecency.Expectation, error)
	InspectEpisodes(context.Context, postgres.DBTX, runrecency.Scope) ([]runrecency.EpisodeEvidence, error)
	FindCurrent(context.Context, postgres.DBTX, runrecency.Scope) (runrecency.Expectation, error)
	LockScope(context.Context, postgres.DBTX, runrecency.Scope) error
	FindLatest(context.Context, postgres.DBTX, runrecency.Scope) (*runrecency.Expectation, error)
	CreateExpectation(context.Context, postgres.DBTX, runrecency.Expectation) (*runrecency.Expectation, error)
	ListForSystem(context.Context, postgres.DBTX) ([]runrecency.Expectation, error)
	ActiveEpisodes(context.Context, postgres.DBTX, runrecency.Expectation) ([]runrecency.Episode, error)
	CreateEpisode(context.Context, postgres.DBTX, runrecency.Expectation, slug.Slug, time.Time, time.Time) error
	ResolveEpisode(context.Context, postgres.DBTX, runrecency.Expectation, runrecency.Episode, string, time.Time) error
}

type Notifications interface {
	CreateCandidate(context.Context, notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error)
	ResolveNotification(context.Context, notificationsvc.AlertManagementPrincipal, slug.Slug, slug.Slug) (*model.NotificationListItem, error)
}

type Service struct {
	txm           postgres.TransactionManager
	repo          Repository
	notifications Notifications
}

func New(txm postgres.TransactionManager, repo Repository, notifications Notifications) *Service {
	if txm == nil || repo == nil || notifications == nil {
		panic("run recency transaction manager, repository and notifications are required")
	}
	return &Service{txm: txm, repo: repo, notifications: notifications}
}

func (s *Service) Evaluate(ctx context.Context, now time.Time) error {
	var expectations []runrecency.Expectation
	if err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		var err error
		expectations, err = s.repo.ListForSystem(ctx, db)
		return err
	}); err != nil {
		return err
	}
	var failures []error
	for _, selected := range expectations {
		err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
			if err := s.repo.LockScope(ctx, tx, selected.Scope); err != nil {
				return err
			}
			current, err := s.repo.FindCurrent(ctx, tx, selected.Scope)
			if xerror.Is(err, runrecency.ErrExpectationNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			return s.evaluateExpectation(ctx, tx, current, now)
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return xerror.Join(failures...)
}

func (s *Service) evaluateExpectation(ctx context.Context, tx postgres.DBTX, e runrecency.Expectation, now time.Time) error {
	episodes, err := s.repo.ActiveEpisodes(ctx, tx, e)
	if err != nil {
		return err
	}
	active := false
	for _, episode := range episodes {
		reason := resolutionReason(e, episode)
		if reason == "" {
			active = true
			continue
		}
		if err := s.repo.ResolveEpisode(ctx, tx, e, episode, reason, now); err != nil {
			return err
		}
		if _, err := s.notifications.ResolveNotification(ctx, notificationsvc.AlertManagementPrincipal{}, e.Org, episode.Slug); err != nil {
			return err
		}
	}
	window := Window{EnabledAt: e.EnabledAt, DeployedAt: e.DeployedAt, MaximumAge: e.MaximumAge}
	if e.ScopeDisabled || !e.Enabled || e.DeploymentID == 0 || active || !window.Breached(now, e.SucceededAt) {
		return nil
	}
	deadline := window.Deadline(e.SucceededAt)
	record, err := slug.WithRandomSuffix("recency", 32)
	if err != nil {
		return err
	}
	if err := s.repo.CreateEpisode(ctx, tx, e, record, deadline, now); err != nil {
		return err
	}
	_, err = s.notifications.CreateCandidate(ctx, breachCandidate(e, record, deadline, now))
	return err
}

func resolutionReason(e runrecency.Expectation, episode runrecency.Episode) string {
	if e.ScopeDisabled {
		return "environment_unavailable"
	}
	if e.DeploymentID == 0 {
		return "deployment_unavailable"
	}
	if episode.ExpectationID != e.ID {
		return "expectation_replaced"
	}
	if !e.Enabled {
		return "expectation_disabled"
	}
	if episode.DeploymentID != e.DeploymentID || episode.DeploymentRevision != e.DeploymentRevision {
		return "deployment_changed"
	}
	// A late-persisted older success cannot recover an already expired window.
	if e.SucceededAt != nil && e.SucceededAt.After(episode.Deadline.Add(-e.MaximumAge)) {
		return "qualifying_success"
	}
	return ""
}

func breachCandidate(e runrecency.Expectation, record slug.Slug, deadline, now time.Time) model.NotificationCandidate {
	refs := map[string]string{"project_slug": e.Project.String(), "environment_slug": e.Environment.String(), "pipeline_slug": e.Pipeline.String(), "expectation_revision": strconv.FormatInt(e.Revision, 10), "deployment_revision": strconv.FormatInt(e.DeploymentRevision, 10), "enabled_at": e.EnabledAt.UTC().Format(time.RFC3339Nano), "deployed_at": e.DeployedAt.UTC().Format(time.RFC3339Nano), "deadline_at": deadline.UTC().Format(time.RFC3339Nano)}
	refs["source_ref"] = e.Pipeline.String()
	if e.ResolutionDigest != nil {
		refs["deployment_resolution_digest"] = *e.ResolutionDigest
	}
	if e.SuccessRun != nil {
		refs["last_success_run_slug"] = *e.SuccessRun
	}
	if e.SucceededAt != nil {
		refs["last_success_at"] = e.SucceededAt.UTC().Format(time.RFC3339Nano)
	}
	return model.NotificationCandidate{OrgSlug: e.Org, ProjectSlug: &e.Project, EnvironmentSlug: &e.Environment, RecordSlug: &record, NotificationType: model.NotificationTypePipelineFreshnessSLABreached, Severity: model.NotificationSeverityWarning, DedupeFingerprint: record.String(), ObservedAt: now, SourceEntityType: model.EventEntityTypePipeline, SourceAction: model.EventActionExecute, SourceRefs: refs, SafeMetadata: map[string]any{"summary": "Deployed pipeline exceeded its successful-run recency window.", "maximum_age_seconds": int64(e.MaximumAge / time.Second)}}
}
