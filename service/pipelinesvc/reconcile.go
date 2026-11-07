package pipelinesvc

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
)

// ReconcilePipelinesRequest controls one API-driven pipeline reconcile operation.
type ReconcilePipelinesRequest struct {
	ExpectedSource *ReconcilePipelinesExpectedSource
	DryRun         bool
	PipelineDir    string
	PipelineSlugs  []slug.Slug
}

var ErrPipelineReconcileSourceChanged = xerror.New(
	xerror.KindAlreadyExists,
	"PIPELINE_RECONCILE_SOURCE_CHANGED",
	"Git source changed since preview; preview again before applying",
)

type ReconcilePipelinesExpectedSource struct {
	DataSourceSlug slug.Slug
	Repo           string
	Ref            string
	Commit         string
}

// ReconcilePipelinesResult reports the effect of one API-driven reconcile operation.
type ReconcilePipelinesResult struct {
	DryRun                 bool
	PipelineDir            string
	RequestedPipelineSlugs []slug.Slug
	Source                 ReconcilePipelinesSourceMetadata
	Summary                ReconcilePipelinesSummary
	Synced                 []ReconcilePipelinesSyncedPipeline
	Deleted                []ReconcilePipelinesDeletedPipeline
	Skipped                []ReconcilePipelinesSkippedPipeline
	DriftResets            []ReconcilePipelinesDriftReset
	Failures               []ReconcilePipelinesFailure
}

// ReconcilePipelinesSourceMetadata captures source metadata recorded on reconciled specs.
type ReconcilePipelinesSourceMetadata struct {
	DataSourceSlug *slug.Slug
	Repo           *string
	Ref            *string
	Commit         *string
}

// ReconcilePipelinesSummary contains aggregate reconcile counters.
type ReconcilePipelinesSummary struct {
	Scanned    int
	Synced     int
	Deleted    int
	Skipped    int
	Failed     int
	DriftReset int
}

type ReconcilePipelinesSyncedPipeline struct {
	PipelineSlug slug.Slug
	SourcePath   string
	SpecHash     string
}

type ReconcilePipelinesDeletedPipeline struct {
	PipelineSlug       slug.Slug
	PreviousSourcePath *string
}

type ReconcilePipelinesSkippedPipeline struct {
	PipelineSlug slug.Slug
	SourcePath   *string
	Reason       string
}

type ReconcilePipelinesDriftReset struct {
	PipelineSlug     slug.Slug
	SourcePath       string
	PreviousSpecHash string
	AppliedSpecHash  string
}

type ReconcilePipelinesFailure struct {
	PipelinePath *string
	PipelineSlug *slug.Slug
	Stage        string
	Message      string
}

func (s *service) ReconcilePipelines(
	ctx context.Context,
	orgSlug, projectSlug, memberSlug slug.Slug,
	actor model.EventActor,
	req ReconcilePipelinesRequest,
) (*ReconcilePipelinesResult, error) {
	if expected := req.ExpectedSource; expected != nil {
		if expected.DataSourceSlug == "" || strings.TrimSpace(expected.Repo) == "" || strings.TrimSpace(expected.Ref) == "" || strings.TrimSpace(expected.Commit) == "" {
			return nil, xerror.InvalidInput(xerror.Pipeline).Detail("expected source requires a data source, repository, ref, and commit")
		}
	}
	pipelineDir := strings.TrimSpace(req.PipelineDir)
	if pipelineDir == "" {
		pipelineDir = "pipelines"
	}

	gitSource, err := s.gitSourceResolver.Resolve(ctx, orgSlug, pipelinegit.SourceRequest{
		ProjectSlug: projectSlug,
	})
	if err != nil {
		return nil, err
	}
	if gitSource.Cleanup != nil {
		defer gitSource.Cleanup()
	}
	if expected := req.ExpectedSource; expected != nil {
		if expected.DataSourceSlug != gitSource.DataSourceSlug || expected.Repo != ptr.Value(gitSource.SourceRepo) || expected.Ref != ptr.Value(gitSource.SourceRef) || expected.Commit != ptr.Value(gitSource.LastAppliedCommit) {
			return nil, ErrPipelineReconcileSourceChanged
		}
	}

	result, err := s.pipelineStore.ReconcileFromGit(
		ctx,
		orgSlug,
		projectSlug,
		gitSource.RepoStore,
		ReconcileFromGitParams{
			PipelineDir:       pipelineDir,
			PipelineSlugs:     req.PipelineSlugs,
			SourceRef:         gitSource.SourceRef,
			LastAppliedCommit: gitSource.LastAppliedCommit,
			DryRun:            req.DryRun,
		},
	)
	if err != nil {
		return nil, err
	}

	serviceResult := toReconcilePipelinesResult(
		req.DryRun,
		pipelineDir,
		req.PipelineSlugs,
		ReconcilePipelinesSourceMetadata{
			DataSourceSlug: &gitSource.DataSourceSlug,
			Repo:           gitSource.SourceRepo,
			Ref:            gitSource.SourceRef,
			Commit:         gitSource.LastAppliedCommit,
		},
		result,
	)
	if !req.DryRun {
		for _, synced := range serviceResult.Synced {
			version, err := s.pipelineStore.FindVersionBySpecHash(ctx, orgSlug, projectSlug, synced.PipelineSlug, synced.SpecHash)
			if err != nil {
				return serviceResult, xerror.Wrapf("load reconciled pipeline version: %w", err)
			}
			pipeline, err := pipelineFromJSON(version.SpecJSONB)
			if err != nil {
				return serviceResult, xerror.Wrapf("decode reconciled pipeline version: %w", err)
			}
			// Keep refs used by older deployed versions while making imported refs bindable.
			if _, err := s.dataSourceRefResolver.SyncPipelineDataSourceRefs(ctx, orgSlug, projectSlug, pipeline, actor); err != nil {
				return serviceResult, xerror.Wrapf("register reconciled pipeline data source refs: %w", err)
			}
		}
		if err := s.recordReconcileEvents(ctx, orgSlug, projectSlug, actor, serviceResult); err != nil {
			return serviceResult, xerror.Wrapf("record pipeline reconcile event: %w", err)
		}
	}

	return serviceResult, nil
}

func (s *service) recordReconcileEvents(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	actor model.EventActor,
	result *ReconcilePipelinesResult,
) error {
	if result == nil {
		return nil
	}

	resetBySlug := make(map[slug.Slug]ReconcilePipelinesDriftReset, len(result.DriftResets))
	for _, item := range result.DriftResets {
		resetBySlug[item.PipelineSlug] = item
	}

	for _, item := range result.Synced {
		if reset, ok := resetBySlug[item.PipelineSlug]; ok {
			metadata := reconcileEventMetadata(projectSlug, result.Source, "drift_reset")
			metadata["source_path"] = reset.SourcePath
			metadata["previous_spec_hash"] = reset.PreviousSpecHash
			metadata["pipeline_version_spec_hash"] = reset.AppliedSpecHash
			if err := s.recordPipelineEventWithMetadataInTx(
				ctx,
				orgSlug,
				projectSlug,
				actor,
				item.PipelineSlug,
				model.EventActionReconcile,
				metadata,
			); err != nil {
				return err
			}
			continue
		}

		metadata := reconcileEventMetadata(projectSlug, result.Source, "synced")
		metadata["source_path"] = item.SourcePath
		metadata["pipeline_version_spec_hash"] = item.SpecHash
		if err := s.recordPipelineEventWithMetadataInTx(
			ctx,
			orgSlug,
			projectSlug,
			actor,
			item.PipelineSlug,
			model.EventActionReconcile,
			metadata,
		); err != nil {
			return err
		}
	}

	for _, item := range result.Deleted {
		metadata := reconcileEventMetadata(projectSlug, result.Source, "deleted")
		if item.PreviousSourcePath != nil {
			metadata["previous_source_path"] = *item.PreviousSourcePath
		}
		if err := s.recordPipelineEventWithMetadataInTx(
			ctx,
			orgSlug,
			projectSlug,
			actor,
			item.PipelineSlug,
			model.EventActionReconcile,
			metadata,
		); err != nil {
			return err
		}
	}
	return nil
}

func reconcileEventMetadata(
	projectSlug slug.Slug,
	source ReconcilePipelinesSourceMetadata,
	result string,
) map[string]any {
	metadata := map[string]any{
		"project_slug":     projectSlug.String(),
		"source_kind":      "git",
		"reconcile_result": result,
	}
	if source.DataSourceSlug != nil {
		metadata["source_data_source_slug"] = source.DataSourceSlug.String()
	}
	if source.Ref != nil {
		metadata["source_ref"] = *source.Ref
	}
	if source.Commit != nil {
		metadata["source_commit"] = *source.Commit
	}
	return metadata
}

func toReconcilePipelinesResult(
	dryRun bool,
	pipelineDir string,
	requestedPipelineSlugs []slug.Slug,
	source ReconcilePipelinesSourceMetadata,
	result *ReconcileFromGitResult,
) *ReconcilePipelinesResult {
	if result == nil {
		result = &ReconcileFromGitResult{}
	}

	return &ReconcilePipelinesResult{
		DryRun:                 dryRun,
		PipelineDir:            pipelineDir,
		RequestedPipelineSlugs: append([]slug.Slug(nil), requestedPipelineSlugs...),
		Source:                 source,
		Summary: ReconcilePipelinesSummary{
			Scanned:    result.Scanned,
			Synced:     result.Synced,
			Deleted:    result.Deleted,
			Skipped:    result.Skipped,
			Failed:     result.Failed,
			DriftReset: result.ResetDrifted,
		},
		Synced:      toReconcilePipelinesSynced(result.SyncedPipelines),
		Deleted:     toReconcilePipelinesDeleted(result.DeletedPipelines),
		Skipped:     toReconcilePipelinesSkipped(result.SkippedPipelines),
		DriftResets: toReconcilePipelinesDriftResets(result.ResetDriftedPipelines),
		Failures:    toReconcilePipelinesFailures(result.Failures),
	}
}

func toReconcilePipelinesSynced(
	items []ReconcileFromGitSynced,
) []ReconcilePipelinesSyncedPipeline {
	out := make([]ReconcilePipelinesSyncedPipeline, len(items))
	for i, item := range items {
		out[i] = ReconcilePipelinesSyncedPipeline{
			PipelineSlug: slug.Slug(item.PipelineSlug),
			SourcePath:   item.SourcePath,
			SpecHash:     item.SpecHash,
		}
	}
	return out
}

func toReconcilePipelinesDeleted(
	items []ReconcileFromGitDeleted,
) []ReconcilePipelinesDeletedPipeline {
	out := make([]ReconcilePipelinesDeletedPipeline, len(items))
	for i, item := range items {
		out[i] = ReconcilePipelinesDeletedPipeline{
			PipelineSlug:       slug.Slug(item.PipelineSlug),
			PreviousSourcePath: item.PreviousSourcePath,
		}
	}
	return out
}

func toReconcilePipelinesSkipped(
	items []ReconcileFromGitSkipped,
) []ReconcilePipelinesSkippedPipeline {
	out := make([]ReconcilePipelinesSkippedPipeline, len(items))
	for i, item := range items {
		out[i] = ReconcilePipelinesSkippedPipeline{
			PipelineSlug: slug.Slug(item.PipelineSlug),
			SourcePath:   item.SourcePath,
			Reason:       item.Reason,
		}
	}
	return out
}

func toReconcilePipelinesDriftResets(
	items []ReconcileFromGitReset,
) []ReconcilePipelinesDriftReset {
	out := make([]ReconcilePipelinesDriftReset, len(items))
	for i, item := range items {
		out[i] = ReconcilePipelinesDriftReset{
			PipelineSlug:     slug.Slug(item.PipelineSlug),
			SourcePath:       item.SourcePath,
			PreviousSpecHash: item.PreviousSpecHash,
			AppliedSpecHash:  item.AppliedSpecHash,
		}
	}
	return out
}

func toReconcilePipelinesFailures(
	items []ReconcileFromGitFailure,
) []ReconcilePipelinesFailure {
	out := make([]ReconcilePipelinesFailure, len(items))
	for i, item := range items {
		var pipelinePath *string
		if strings.TrimSpace(item.PipelinePath) != "" {
			path := item.PipelinePath
			pipelinePath = &path
		}

		var pipelineSlug *slug.Slug
		if strings.TrimSpace(item.PipelineSlug) != "" {
			converted := slug.Slug(item.PipelineSlug)
			pipelineSlug = &converted
		}

		out[i] = ReconcilePipelinesFailure{
			PipelinePath: pipelinePath,
			PipelineSlug: pipelineSlug,
			Stage:        item.Stage,
			Message:      item.Message,
		}
	}
	return out
}
