package http

import (
	"fmt"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/gofiber/fiber/v3"
)

func (s *server) GetPipelineRunCompletion(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, environmentParam api.EnvironmentSlugParam, runParam api.RunSlugParam) error {
	org, project, environment, err := parseOrgProjectEnvironmentSlugs(orgParam, projectParam, environmentParam)
	if err != nil {
		return err
	}
	run, err := slug.New(runParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid run slug: %v", err))
	}
	if err = s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	if _, err = s.pipelinesService.GetRun(c.Context(), org, project, environment, run.String()); err != nil {
		return mapServiceError(err)
	}
	evidence, err := s.pipelinesService.GetRunCompletion(c.Context(), org, project, environment, run)
	if err != nil {
		return mapServiceError(err)
	}
	if err := s.canReadCompletionAttempts(c, org, project, environment, run, evidence); err != nil {
		return err
	}
	result, err := s.authorizedCompletion(c, org, project, environment, evidence)
	if err != nil {
		return err
	}
	return c.JSON(result)
}

func (s *server) authorizedCompletion(c fiber.Ctx, org, project, environment slug.Slug, evidence *model.PipelineRunCompletion) (*api.PipelineRunCompletion, error) {
	filtered := *evidence
	filtered.Parent = nil
	filtered.Children = []model.PipelineCompletionDecision{}
	if evidence.Parent != nil {
		readable, err := s.completionDecisionReadable(c, org, project, environment, *evidence.Parent)
		if err != nil {
			return nil, err
		}
		if !readable {
			filtered.ContextUnavailable = true
		}
		if readable {
			filtered.Parent = evidence.Parent
		}
	}
	for _, decision := range evidence.Children {
		readable, err := s.completionDecisionReadable(c, org, project, environment, decision)
		if err != nil {
			return nil, err
		}
		if !readable {
			filtered.ContextUnavailable = true
		}
		if readable {
			filtered.Children = append(filtered.Children, decision)
		}
	}
	return toAPICompletion(&filtered), nil
}

func toAPICompletion(evidence *model.PipelineRunCompletion) *api.PipelineRunCompletion {
	result := api.PipelineRunCompletion{PipelineSlug: new(evidence.PipelineSlug.String()), Truncated: new(evidence.Truncated), ContextUnavailable: new(evidence.ContextUnavailable), RootRunSlug: evidence.RootRunSlug.String(), Closed: evidence.Closed, WaitingForRetry: evidence.WaitingForRetry, Outcome: evidence.Outcome, FinalAttemptRunSlug: nullableSlugValue(evidence.FinalAttemptRunSlug), DecisionReason: evidence.DecisionReason, Attempts: []api.PipelineCompletionAttempt{}, Children: []api.PipelineCompletionDecision{}}
	for _, attempt := range evidence.Attempts {
		result.Attempts = append(result.Attempts, api.PipelineCompletionAttempt{RunSlug: attempt.RunSlug.String(), AttemptNumber: attempt.AttemptNumber, Status: attempt.Status, VersionHash: attempt.VersionHash, DeploymentRevision: attempt.DeploymentRevision})
	}
	if evidence.Parent != nil {
		parent := toAPICompletionDecision(*evidence.Parent)
		result.Parent = &parent
	}
	for _, decision := range evidence.Children {
		result.Children = append(result.Children, toAPICompletionDecision(decision))
	}
	return &result
}

func (s *server) completionDecisionReadable(c fiber.Ctx, org, project, environment slug.Slug, decision model.PipelineCompletionDecision) (bool, error) {
	if err := s.canTarget(c, org, model.Read, model.ResourcePipelineTriggers, model.AuthorizationTarget{Kind: "pipeline_triggers", Slug: decision.TriggerSlug, ProjectSlug: project, EnvironmentSlug: environment}); err != nil {
		return completionReadResult(err)
	}
	for _, run := range []slug.Slug{decision.RootRunSlug, decision.FinalAttemptRunSlug} {
		if err := s.canTarget(c, org, model.Read, model.ResourcePipelines, model.AuthorizationTarget{Kind: "runs", Slug: run, ProjectSlug: project, EnvironmentSlug: environment}); err != nil {
			return completionReadResult(err)
		}
		if _, err := s.pipelinesService.GetRun(c.Context(), org, project, environment, run.String()); err != nil {
			return completionReadResult(err)
		}
	}
	if decision.ChildRunSlug != nil {
		if err := s.canTarget(c, org, model.Read, model.ResourcePipelines, model.AuthorizationTarget{Kind: "runs", Slug: *decision.ChildRunSlug, ProjectSlug: project, EnvironmentSlug: environment}); err != nil {
			return completionReadResult(err)
		}
		_, err := s.pipelinesService.GetRun(c.Context(), org, project, environment, decision.ChildRunSlug.String())
		return completionReadResult(err)
	}
	downstream := model.AuthorizationTarget{Kind: "pipelines", Slug: decision.DownstreamPipelineSlug, ProjectSlug: project}
	if err := s.authorizeOrgTargetFor(c, org, model.Read, model.ResourcePipelines, downstream, true, "related_pipeline"); err != nil {
		return completionReadResult(err)
	}
	_, err := s.pipelinesService.Get(c.Context(), org, project, decision.DownstreamPipelineSlug, reqctx.GetUserSlug(c, s.sessionStore))
	return completionReadResult(err)
}

func completionReadResult(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	var apiErr *apierror.Error
	if xerror.As(err, &apiErr) && (apiErr.HTTPStatus == fiber.StatusForbidden || apiErr.HTTPStatus == fiber.StatusNotFound) {
		return false, nil
	}
	var typed *xerror.Error
	if xerror.As(err, &typed) && (typed.Kind() == xerror.KindNotFound || typed.Kind() == xerror.KindForbidden) {
		return false, nil
	}
	return false, mapServiceError(err)
}

func toAPICompletionDecision(d model.PipelineCompletionDecision) api.PipelineCompletionDecision {
	var condition *api.PipelineCompletionCondition
	if d.Condition != nil {
		value := api.PipelineCompletionCondition(d.Condition.String())
		condition = &value
	}
	return api.PipelineCompletionDecision{TriggerSlug: d.TriggerSlug.String(), TriggerRevision: d.TriggerRevision, UpstreamPipelineSlug: d.UpstreamPipelineSlug.String(), RootRunSlug: d.RootRunSlug.String(), FinalAttemptRunSlug: d.FinalAttemptRunSlug.String(), DownstreamPipelineSlug: d.DownstreamPipelineSlug.String(), Condition: condition, State: d.State, DecisionReason: d.DecisionReason, ChildRunSlug: nullableSlugValue(d.ChildRunSlug), ChildStatus: d.ChildStatus, ChildOutcome: d.ChildOutcome, SelectedVersionHash: d.SelectedVersionHash, SelectedDeploymentRevision: d.SelectedDeploymentRevision, SelectedResolutionDigest: d.SelectedResolutionDigest}
}

func (s *server) GetPipelineRunConnected(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, environmentParam api.EnvironmentSlugParam, runParam api.RunSlugParam) error {
	org, project, environment, err := parseOrgProjectEnvironmentSlugs(orgParam, projectParam, environmentParam)
	if err != nil {
		return err
	}
	run, err := slug.New(runParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid run slug: %v", err))
	}
	if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	if _, err := s.pipelinesService.GetRun(c.Context(), org, project, environment, run.String()); err != nil {
		return mapServiceError(err)
	}
	graph, err := pipelinesvc.InspectConnectedRuns(c.Context(), run, func(candidate slug.Slug) (*model.PipelineRunCompletion, error) {
		if err := s.canTarget(c, org, model.Read, model.ResourcePipelines, model.AuthorizationTarget{Kind: "runs", Slug: candidate, ProjectSlug: project, EnvironmentSlug: environment}); err != nil {
			_, e := completionReadResult(err)
			if e == nil {
				e = pipelinesvc.ErrPipelineRunNotFound
			}
			return nil, e
		}
		if _, err := s.pipelinesService.GetRun(c.Context(), org, project, environment, candidate.String()); err != nil {
			_, e := completionReadResult(err)
			if e == nil {
				e = pipelinesvc.ErrPipelineRunNotFound
			}
			return nil, e
		}
		evidence, err := s.pipelinesService.GetRunCompletion(c.Context(), org, project, environment, candidate)
		if err != nil {
			_, e := completionReadResult(err)
			if e == nil {
				e = pipelinesvc.ErrPipelineRunNotFound
			}
			return nil, e
		}
		if err := s.canReadCompletionAttempts(c, org, project, environment, candidate, evidence); err != nil {
			if _, failure := completionReadResult(err); failure != nil {
				return nil, failure
			}
			return nil, pipelinesvc.ErrPipelineRunNotFound
		}
		return evidence, nil
	}, func(decision model.PipelineCompletionDecision) (bool, error) {
		return s.completionDecisionReadable(c, org, project, environment, decision)
	})
	if err != nil {
		return err
	}
	result := api.PipelineRunConnected{SelectedRunSlug: run.String(), Nodes: []api.PipelineConnectedRun{}, Truncated: graph.Truncated, ContextUnavailable: graph.ContextUnavailable}
	for _, node := range graph.Nodes {
		result.Nodes = append(result.Nodes, api.PipelineConnectedRun{PipelineSlug: node.PipelineSlug.String(), Completion: *toAPICompletion(node)})
	}
	return c.JSON(result)
}

// Completion outcomes and retry state describe the group as a whole.
func (s *server) canReadCompletionAttempts(c fiber.Ctx, org, project, env, selected slug.Slug, evidence *model.PipelineRunCompletion) error {
	runs := make([]slug.Slug, 0, len(evidence.Attempts)+2)
	runs = append(runs, evidence.RootRunSlug)
	if evidence.FinalAttemptRunSlug != nil {
		runs = append(runs, *evidence.FinalAttemptRunSlug)
	}
	for _, attempt := range evidence.Attempts {
		runs = append(runs, attempt.RunSlug)
	}
	return s.canReadRelatedRuns(c, org, project, env, selected, runs)
}

func (s *server) canReadRelatedRuns(c fiber.Ctx, org, project, env, selected slug.Slug, runs []slug.Slug) error {
	checked := map[slug.Slug]bool{selected: true, "": true}
	for _, run := range runs {
		if checked[run] {
			continue
		}
		if err := s.canTarget(c, org, model.Read, model.ResourcePipelines, model.AuthorizationTarget{Kind: "runs", Slug: run, ProjectSlug: project, EnvironmentSlug: env}); err != nil {
			return err
		}
		checked[run] = true
	}
	return nil
}
