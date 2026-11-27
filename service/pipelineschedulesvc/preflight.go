package pipelineschedulesvc

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelineschedule "github.com/Braidplane/braidplane-core/pkg/pipeline/schedule"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type preflightPipelineService interface {
	ResolveScheduledPreflight(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.PipelineTriggerVersionPolicy, *string, *string) (*pipelinesvc.ScheduledPreflightTarget, error)
	ValidateScheduledPreflight(context.Context, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*model.PipelineSchedulePreflightDiagnostic, error)
}

type preflightSourceService interface {
	RefreshSchema(context.Context, slug.Slug, slug.Slug, model.EventActor) (*datasourcessvc.SchemaImportResult, error)
	FindBySlug(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error)
}

func WithPreflightServices(pipelines preflightPipelineService, sources preflightSourceService) Option {
	return func(s *Service) {
		s.preflightPipelines, s.preflightSources = pipelines, sources
	}
}

func (s *Service) dispatchSchedulePreflights(ctx context.Context, now time.Time, triggers []DueScheduleTrigger) (int, error) {
	var enqueued int
	for _, trigger := range triggers {
		lead := trigger.ScheduleConfig.PreflightLeadTime
		if !trigger.Enabled || !pipelineschedule.ValidPreflightLeadTime(lead) {
			continue
		}
		location, err := time.LoadLocation(trigger.ScheduleConfig.Timezone)
		if err != nil {
			continue // The normal schedule scan reports invalid schedule definitions.
		}
		schedule, err := pipelineschedule.ParsePreflightCron(trigger.ScheduleConfig.Cron)
		if err != nil {
			continue
		}
		cursor := now
		if last := trigger.LastPreflightOccurrenceAt; last != nil && last.After(cursor) {
			cursor = *last
		}
		if trigger.TriggerUpdatedAt.After(cursor) {
			cursor = trigger.TriggerUpdatedAt
		}
		next := schedule.Next(cursor.In(location))
		horizon := now.Add(lead)
		if next.IsZero() || next.After(horizon) {
			continue
		}
		var claimedCount int
		err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
			for ; !next.IsZero() && !next.After(horizon); next = schedule.Next(next) {
				ref := ScheduleFireExecutionRef{
					OrgSlug: trigger.OrgSlug, ProjectSlug: trigger.ProjectSlug, EnvironmentSlug: trigger.EnvironmentSlug,
					TriggerSlug: trigger.TriggerSlug, TriggerRevision: trigger.TriggerRevision, ScheduledFireAt: next.UTC(),
				}
				claimed, err := s.repo.ClaimSchedulePreflight(ctx, tx, ref, next.Add(-lead).UTC())
				if err != nil {
					return err
				}
				if !claimed {
					continue
				}
				if _, err := s.producer.EnqueueSchedulePreflight(ctx, tx, jobs.PipelineSchedulePreflightArgs{
					OrganizationSlug: ref.OrgSlug, ProjectSlug: ref.ProjectSlug, EnvironmentSlug: ref.EnvironmentSlug,
					TriggerSlug: ref.TriggerSlug, TriggerRevision: ref.TriggerRevision, ScheduledFireAt: ref.ScheduledFireAt,
				}); err != nil {
					return err
				}
				claimedCount++
			}
			return nil
		})
		if err != nil {
			return enqueued, err
		}
		enqueued += claimedCount
	}
	return enqueued, nil
}

// ExecuteSchedulePreflight returns true when the worker should wait for schema imports.
func (s *Service) ExecuteSchedulePreflight(ctx context.Context, args jobs.PipelineSchedulePreflightArgs) (bool, error) {
	if s.preflightPipelines == nil || s.preflightSources == nil {
		return false, xerror.Unexpected().Detail("pipeline and data source services are required for schedule preflight")
	}
	ref := ScheduleFireExecutionRef{
		OrgSlug: args.OrganizationSlug, ProjectSlug: args.ProjectSlug, EnvironmentSlug: args.EnvironmentSlug,
		TriggerSlug: args.TriggerSlug, TriggerRevision: args.TriggerRevision, ScheduledFireAt: args.ScheduledFireAt,
	}
	var waiting bool
	err := s.txm.RunInTx(postgres.WithConsistentSnapshot(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		pending, err := s.repo.LoadPendingSchedulePreflight(ctx, tx, ref)
		if err != nil || pending == nil {
			return err
		}
		progress := pipelinetriggers.SchedulePreflightProgress{
			VersionHash: pending.VersionHash, ResolutionDigest: pending.ResolutionDigest, SchemaRefreshes: pending.SchemaRefreshes,
		}
		if !s.clock.Now().Before(ref.ScheduledFireAt) {
			progress.Diagnostics = []model.PipelineSchedulePreflightDiagnostic{{
				Code: "PREFLIGHT_TIMED_OUT", Message: "Preflight did not finish before the scheduled run. Check worker availability and source schema refresh status.",
			}}
		} else {
			waiting, err = s.checkSchedulePreflight(ctx, ref, pending, &progress)
			if err != nil {
				return err
			}
		}
		if waiting && pending.SchemaRefreshes != nil {
			return nil // No new work was queued during this poll.
		}
		if !waiting {
			completed := s.clock.Now().UTC()
			progress.CompletedAt = &completed
			progress.SchemaRefreshes = nil
			if s.notifier != nil && len(progress.Diagnostics) > 0 {
				candidate := preflightFailureNotificationCandidate(ref, pending.PipelineSlug, completed, progress.Diagnostics[0].Code)
				if err := s.notifier.EnqueueCandidate(ctx, tx, candidate); err != nil {
					return err
				}
			}
		}
		return s.repo.SaveSchedulePreflight(ctx, tx, ref, progress)
	})
	return waiting && err == nil, err
}

func (s *Service) checkSchedulePreflight(ctx context.Context, ref ScheduleFireExecutionRef, pending *pipelinetriggers.PendingSchedulePreflight, progress *pipelinetriggers.SchedulePreflightProgress) (bool, error) {
	if pending.SchemaRefreshes != nil {
		waiting, diagnostics, err := s.checkSchemaRefreshes(ctx, ref.OrgSlug, pending.SchemaRefreshes)
		if err != nil {
			return false, err
		}
		if len(diagnostics) > 0 {
			progress.Diagnostics = diagnostics
			return false, nil
		}
		if waiting {
			return true, nil
		}
	}

	target, err := s.preflightPipelines.ResolveScheduledPreflight(ctx, ref.OrgSlug, ref.ProjectSlug, ref.EnvironmentSlug, pending.PipelineSlug, pending.VersionPolicy, pending.VersionHash, pending.ResolutionDigest)
	if target != nil {
		progress.VersionHash, progress.ResolutionDigest = &target.VersionHash, target.ResolutionDigest
	}
	if err != nil {
		var typed *xerror.Error
		if !xerror.As(err, &typed) || (typed.Kind() != xerror.KindNotFound && typed.Kind() != xerror.KindInvalidInput) {
			return false, err
		}
		progress.Diagnostics = []model.PipelineSchedulePreflightDiagnostic{{
			Code: typed.Code(), Message: typed.Message() + ". Check the schedule version, environment deployment, and data source bindings.",
		}}
		return false, nil
	}

	sources := make(map[slug.Slug]struct{})
	for _, step := range target.Pipeline.Steps {
		if step.Query != nil && step.Query.DataSourceSlug != "" {
			sources[step.Query.DataSourceSlug] = struct{}{}
		}
	}
	if pending.SchemaRefreshes == nil && len(sources) > 0 {
		progress.SchemaRefreshes = make(map[slug.Slug]time.Time, len(sources))
		for _, source := range slices.Sorted(maps.Keys(sources)) {
			refresh, err := s.preflightSources.RefreshSchema(ctx, ref.OrgSlug, source, model.SystemEventActor())
			if xerror.Is(err, datasourcessvc.ErrDataSourceNotFound) {
				progress.Diagnostics = append(progress.Diagnostics, schemaRefreshFailure(source, nil))
				continue
			}
			if err != nil {
				return false, err
			}
			if refresh.StartedAt == nil {
				progress.Diagnostics = append(progress.Diagnostics, schemaRefreshFailure(source, refresh.Diagnostic))
				continue
			}
			progress.SchemaRefreshes[source] = *refresh.StartedAt
		}
		return len(progress.Diagnostics) == 0, nil
	}
	if pending.SchemaRefreshes != nil && !maps.EqualFunc(sources, pending.SchemaRefreshes, func(struct{}, time.Time) bool { return true }) {
		progress.Diagnostics = []model.PipelineSchedulePreflightDiagnostic{{
			Code: "PREFLIGHT_SOURCES_CHANGED", Message: "Pipeline source bindings changed during preflight. Check the current bindings and validate the pipeline again.",
		}}
		return false, nil
	}
	diagnostic, err := s.preflightPipelines.ValidateScheduledPreflight(ctx, ref.OrgSlug, ref.EnvironmentSlug, target.Pipeline)
	if diagnostic != nil {
		progress.Diagnostics = append(progress.Diagnostics, *diagnostic)
	}
	return false, err
}

func (s *Service) checkSchemaRefreshes(ctx context.Context, org slug.Slug, refreshes map[slug.Slug]time.Time) (bool, []model.PipelineSchedulePreflightDiagnostic, error) {
	var waiting bool
	var diagnostics []model.PipelineSchedulePreflightDiagnostic
	for _, source := range slices.Sorted(maps.Keys(refreshes)) {
		dataSource, err := s.preflightSources.FindBySlug(ctx, org, source)
		if xerror.Is(err, datasourcessvc.ErrDataSourceNotFound) {
			diagnostics = append(diagnostics, schemaRefreshFailure(source, nil))
			continue
		}
		if err != nil {
			return false, nil, err
		}
		if dataSource.SchemaImportStartedAt == nil || dataSource.SchemaImportStartedAt.Before(refreshes[source]) {
			diagnostics = append(diagnostics, schemaRefreshFailure(source, nil))
			continue
		}
		switch dataSource.SchemaImportStatus {
		case model.DataSourceSchemaImportStatusImporting:
			waiting = true
		case model.DataSourceSchemaImportStatusReady:
			if !dataSource.SchemaCached {
				diagnostics = append(diagnostics, schemaRefreshFailure(source, nil))
			}
		default:
			diagnostics = append(diagnostics, schemaRefreshFailure(source, dataSource.SchemaImportDiagnostic))
		}
	}
	return waiting, diagnostics, nil
}

func schemaRefreshFailure(source slug.Slug, diagnostic *datasourcessvc.SchemaImportDiagnostic) model.PipelineSchedulePreflightDiagnostic {
	message := "Schema refresh did not produce current metadata. Check the source connection and refresh its schema."
	if diagnostic != nil && diagnostic.Message != "" {
		message = diagnostic.Message
	}
	return model.PipelineSchedulePreflightDiagnostic{Code: "SCHEMA_REFRESH_FAILED", Message: message, DataSourceSlug: &source}
}
