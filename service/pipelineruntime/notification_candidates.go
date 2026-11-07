package pipelineruntime

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
)

func runNotificationCandidates(org, project, environment, pipeline slug.Slug, result *pipelinerun.RunResult) ([]model.NotificationCandidate, error) {
	candidates := []model.NotificationCandidate{}
	if n := len(result.Timeline.Events); result.Metadata.Status == "succeeded" && n > 0 {
		identity := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%s", org, project, environment, result.Metadata.RunSlug)))
		record := slug.Slug(fmt.Sprintf("run-succeeded-%x", identity))
		candidates = append(candidates, model.NotificationCandidate{
			OrgSlug: org, ProjectSlug: &project, EnvironmentSlug: &environment, RecordSlug: &record,
			NotificationType: model.NotificationTypePipelineRunSucceeded, Severity: model.NotificationSeverityInfo,
			LifecycleState: model.NotificationLifecycleStateResolved, DedupeFingerprint: record.String(),
			ObservedAt:       result.Timeline.Events[n-1].At,
			SourceEntityType: model.EventEntityTypePipelineRun, SourceAction: model.EventActionExecute,
			SourceRefs:   map[string]string{"project_slug": project.String(), "environment_slug": environment.String(), "pipeline_slug": pipeline.String(), "run_slug": result.Metadata.RunSlug},
			SafeMetadata: map[string]any{"summary": "Pipeline run completed successfully. All required sink writes completed."},
		})
	}
	// Trigger owners decide retry eligibility after the runtime persists the attempt.
	if n := len(result.Timeline.Events); result.Metadata.Status == "failed" && n > 0 &&
		result.Metadata.Source.Type != model.PipelineRunSourceTypeSchedule && result.Metadata.Source.Type != model.PipelineRunSourceTypeWebhook &&
		result.Metadata.Source.Schedule == nil && result.Metadata.Source.Webhook == nil {
		candidates = append(candidates, notificationsvc.RunFailedCandidate(org, project, environment, pipeline,
			slug.Slug(result.Metadata.RunSlug), result.Metadata.Source.RootRunSlug, result.Timeline.Events[n-1].At))
	}
	base := notificationsvc.ContractFailureParams{
		OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment,
		PipelineSlug: pipeline, RunSlug: slug.Slug(result.Metadata.RunSlug),
	}
	failedChecks := false
	for _, check := range result.Timeline.StructuralChecks {
		if check.Status != pipelinerun.CheckStatusFailed {
			continue
		}
		failedChecks = true
		p := base
		p.CheckID, p.StepSlug = check.ID, check.Step
		p.Rule, p.ReasonCode = check.Phase, check.ReasonCode
		declarationPrefix := check.Origin + "/" + check.Phase
		if check.Input != "" {
			declarationPrefix += "/" + check.Input
		}
		if reference, ok := strings.CutPrefix(check.Declaration, declarationPrefix+"/"); ok {
			if parts := strings.SplitN(reference, "/", 3); len(parts) == 3 {
				p.ContractName, p.ContractRevision = parts[0], parts[1]
			}
		}
		if check.CompletedAt != nil {
			p.ObservedAt = *check.CompletedAt
		}
		applyRunContractDiagnostic(&p, check.ContractFailure)
		if result.Timeline.Failure != nil && result.Timeline.Failure.Step == check.Step {
			applyRunContractDiagnostic(&p, result.Timeline.Failure.Contract)
		}
		candidate, err := notificationsvc.ContractFailureCandidate(p)
		if err != nil {
			return nil, err
		}
		candidate.SafeMetadata["declaration"] = check.Declaration
		candidate.SafeMetadata["projection_digest"] = check.ProjectionDigest
		candidate.SafeMetadata["compiled_plan_digest"] = check.CompiledPlanDigest
		candidate.SafeMetadata["deployment_resolution_digest"] = check.DeploymentResolutionDigest
		candidates = append(candidates, candidate)
	}
	if !failedChecks && pipelinerun.HasContractFailure(result.Timeline) {
		p := base
		failure := result.Timeline.Failure
		p.CheckID, p.StepSlug, p.ReasonCode, p.ObservedAt = "failure", failure.Step, failure.ReasonCode.String(), failure.At
		applyRunContractDiagnostic(&p, failure.Contract)
		candidate, err := notificationsvc.ContractFailureCandidate(p)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func applyRunContractDiagnostic(p *notificationsvc.ContractFailureParams, diagnostic *pipelinerun.ContractFailureDiagnostic) {
	if diagnostic == nil {
		return
	}
	p.Field, p.Rule = diagnostic.FieldName, diagnostic.Rule
	if p.ContractName == "" {
		p.ContractName = diagnostic.ContractName
	}
}
