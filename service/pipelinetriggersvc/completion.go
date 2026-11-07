package pipelinetriggersvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func (s *service) validateCompletionTarget(ctx context.Context, tx postgres.DBTX, org, project, environment, trigger, pipeline slug.Slug, enabled bool, config *model.PipelineTriggerCompletionConfig) error {
	if config == nil {
		return nil
	}
	if (enabled || trigger == "") && !s.completionTriggersEnabled {
		return ErrInvalidPipelineTriggerInput.Detail("completion triggers are disabled by execution.completion_triggers_enabled")
	}
	protected, err := s.repo.LockPipelineTriggerTopology(ctx, tx, org, project, environment)
	if err != nil {
		return err
	}
	if (enabled || trigger == "") && protected {
		return ErrInvalidPipelineTriggerInput.Detail("completion triggers cannot target protected environments")
	}
	exists, err := s.repo.PipelineExists(ctx, tx, org, project, config.UpstreamPipelineSlug)
	if err != nil {
		return err
	}
	if !exists {
		return ErrPipelineNotFound
	}
	edges, err := s.repo.ListPipelineCompletionEdges(ctx, tx, org, project, environment)
	if err != nil {
		return err
	}
	return validateCompletionGraph(edges, trigger, pipeline, enabled, *config)
}

func validateCompletionGraph(edges []pipelinetriggers.CompletionEdge, trigger, downstream slug.Slug, enabled bool, config model.PipelineTriggerCompletionConfig) error {
	adjacency := map[slug.Slug][]slug.Slug{}
	outgoing := 0
	for _, edge := range edges {
		if edge.TriggerSlug == trigger {
			continue
		}
		adjacency[edge.Config.UpstreamPipelineSlug] = append(adjacency[edge.Config.UpstreamPipelineSlug], edge.PipelineSlug)
		if edge.Enabled && edge.Config.UpstreamPipelineSlug == config.UpstreamPipelineSlug {
			outgoing++
		}
	}
	if enabled && outgoing >= 10 {
		return ErrInvalidPipelineTriggerInput.Detail("a pipeline can have at most ten enabled outgoing completion triggers per environment")
	}
	// Disabled edges participate so re-enabling cannot introduce a hidden cycle.
	pending := []slug.Slug{downstream}
	visited := map[slug.Slug]bool{}
	for len(pending) > 0 {
		node := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if node == config.UpstreamPipelineSlug {
			return ErrInvalidPipelineTriggerInput.Detail("completion trigger would create a cycle")
		}
		if visited[node] {
			continue
		}
		visited[node] = true
		pending = append(pending, adjacency[node]...)
	}
	return nil
}

// CheckCompletionSnapshot is used at admission and again immediately before
// execution. Outer transaction owners must use WithConsistentSnapshot and retain
// its revocation locks until the admission/execution decision commits.
func (s *service) CheckCompletionSnapshot(ctx context.Context, org, project, environment slug.Slug, id int) (*model.PipelineCompletionSnapshot, error) {
	if !s.completionTriggersEnabled {
		return nil, ErrInvalidPipelineTriggerInput.Detail("completion triggers are disabled")
	}
	var snapshot *model.PipelineCompletionSnapshot
	var refusal error
	err := s.txm.RunInTx(postgres.WithConsistentSnapshot(ctx), func(ctx context.Context, db postgres.DBTX) error {
		if _, err := s.repo.LockPipelineTriggerTopology(ctx, db, org, project, environment); err != nil {
			return err
		}
		var err error
		snapshot, err = s.repo.LoadCompletionSnapshot(ctx, db, org, project, environment, id)
		if err != nil {
			return err
		}
		reason := snapshot.DecisionReason
		if reason == "capacity_waiting" || reason == "dispatch_retry_pending" {
			reason = ""
		}
		if reason == "" && (snapshot.State != "pending" && snapshot.State != "accepted") {
			reason = "completion_already_resolved"
		}
		if reason == "" && (snapshot.CausalDepth < 1 || snapshot.CausalDepth > 8) {
			reason = "causal_depth_exceeded"
		}
		if reason == "" {
			for _, authority := range []model.CompletionAuthoritySnapshot{snapshot.ClosureAuthority, snapshot.CurrentAuthority} {
				allowed, err := authorization.CanCapturedCompletionAuthority(ctx, authority)
				if err != nil {
					return err
				}
				if !allowed {
					reason = "authority_revoked"
					break
				}
			}
		}
		if reason == "" {
			if err := s.completionValidator.ValidateCompletionExecutionContext(ctx, org, project, environment, snapshot); err != nil {
				var typed *xerror.Error
				if !xerror.As(err, &typed) || typed.Kind() == xerror.KindInternal || typed.Kind() == xerror.KindTimeout || typed.Kind() == xerror.KindConnectionFailed || typed.Kind() == xerror.KindUnavailable {
					return err
				}
				reason = "execution_context_unavailable"
			}
		}
		if reason != "" {
			snapshot.DecisionReason = reason
			refusal = ErrInvalidPipelineTriggerInput.Detail(reason)
			if snapshot.State == "pending" {
				if err := s.repo.RefuseCompletionSnapshot(ctx, db, org, id, reason); err != nil {
					return err
				}
				snapshot.State, snapshot.DecisionReason = "refused", reason
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snapshot, refusal
}
