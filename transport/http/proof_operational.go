package http

import (
	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/gofiber/fiber/v3"
)

func (s *server) CompileOperationalProofReceipt(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, environment api.EnvironmentSlugParam) error {
	c.Set("Cache-Control", "no-store")
	service, ok := s.proofReceiptService.(proofreceiptsvc.OperationalService)
	if !ok {
		return fiber.ErrNotFound
	}
	o, p, e, err := parseOrgProjectEnvironmentSlugs(org, project, environment)
	if err != nil {
		return err
	}
	scope := proofreceiptsvc.ObservationScope{Organization: o, Project: p, Environment: e}
	actor, err := s.canOrgUserPrincipal(c, o, model.Execute, model.ResourcePipelines)
	if err != nil {
		s.recordObservationAccessRejection(c, scope, model.Execute, model.ResourcePipelines)
		return err
	}
	for _, resource := range []model.Resource{model.ResourcePipelines, model.ResourceDataSources} {
		if err := s.canObservationResource(c, scope, model.Read, resource); err != nil {
			return err
		}
	}
	var body api.CompileOperationalProofReceiptInput
	if c.Bind().Body(&body) != nil {
		return apierror.InvalidRequestBody()
	}
	source, err := slug.New(body.SourceReceiptId)
	if err != nil {
		return apierror.ValidationFailed("invalid source Proof receipt")
	}
	selection, err := fromAPIOperationalSelection(body.Selection)
	if err != nil {
		return err
	}
	stored, err := service.CompileOperational(c.Context(), scope, actor, source, selection)
	if err != nil {
		return mapProofObservationError(err)
	}
	detail, err := service.Get(c.Context(), o, p, e, stored.Value.ID)
	if err != nil {
		return mapProofObservationError(err)
	}
	if err := proofreceiptsvc.AuditReceiptRead(c.Context(), s.eventsService, scope, model.UserEventActor(actor), stored.Value.ID); err != nil {
		return mapProofObservationError(err)
	}
	return c.JSON(toAPIProofReceipt(detail))
}

func fromAPIOperationalSelection(value api.ProofOperationalSelection) (model.ProofOperationalSelection, error) {
	candidate := value.Candidate
	selection := model.ProofOperationalSelection{
		Candidate: model.ProofCandidateIdentity{
			RepositoryID: candidate.RepositoryId, ChangeRequestID: candidate.ChangeRequestId, HeadSHA: candidate.HeadSha, TargetRef: candidate.TargetRef, PipelinePath: candidate.PipelinePath,
			SpecHash: candidate.SpecHash, BindingFingerprint: candidate.BindingFingerprint, CapabilityFingerprint: candidate.CapabilityFingerprint, CompilerVersion: candidate.CompilerVersion, PolicyVersion: candidate.PolicyVersion,
		},
		VersionCommit: value.VersionCommit, CompiledPlanDigest: value.CompiledPlanDigest, DeploymentResolutionDigest: value.DeploymentResolutionDigest, Attempt: value.Attempt, CheckID: value.CheckId,
		Input: value.Input, Origin: string(value.Origin), Phase: string(value.Phase), Declaration: value.Declaration, ProjectionDigest: value.ProjectionDigest, Evaluator: value.Evaluator, EvaluatorPolicy: value.EvaluatorPolicy,
		StartedAt: value.StartedAt, CompletedAt: value.CompletedAt, EvidenceDigest: value.EvidenceDigest,
		Window: model.StructuralCheckWindow{Kind: string(value.Window.Kind), HasLowerBound: value.Window.HasLowerBound, HasUpperBound: value.Window.HasUpperBound, BackfillSeriesSlug: ptr.Value(value.Window.BackfillSeriesSlug), BackfillChunkIndex: ptr.Value(value.Window.BackfillChunkIndex)},
	}
	for _, field := range []struct {
		raw    string
		target *slug.Slug
	}{
		{candidate.OrganizationSlug, &selection.Candidate.OrganizationSlug},
		{candidate.ProjectSlug, &selection.Candidate.ProjectSlug},
		{candidate.EnvironmentSlug, &selection.Candidate.EnvironmentSlug},
		{candidate.ProviderConnectionSlug, &selection.Candidate.ProviderConnectionSlug},
		{value.CandidateSlug, &selection.CandidateSlug},
		{value.Pipeline, &selection.Pipeline},
		{value.Run, &selection.Run},
		{value.Step, &selection.Step},
	} {
		id, err := slug.New(field.raw)
		if err != nil {
			return model.ProofOperationalSelection{}, apierror.ValidationFailed("invalid operational Proof identity")
		}
		*field.target = id
	}
	if value.Target != "" {
		id, err := slug.New(value.Target)
		if err != nil {
			return model.ProofOperationalSelection{}, apierror.ValidationFailed("invalid operational Proof target")
		}
		selection.Target = id
	}
	if selection.Window.BackfillSeriesSlug != "" {
		if _, err := slug.New(selection.Window.BackfillSeriesSlug); err != nil {
			return model.ProofOperationalSelection{}, apierror.ValidationFailed("invalid operational Proof backfill")
		}
	}
	selection.Window.SourceSteps = make([]slug.Slug, len(value.Window.SourceSteps))
	for i, raw := range value.Window.SourceSteps {
		id, err := slug.New(raw)
		if err != nil {
			return model.ProofOperationalSelection{}, apierror.ValidationFailed("invalid operational Proof window")
		}
		selection.Window.SourceSteps[i] = id
	}
	return selection, nil
}

func toAPIOperationalSelection(value model.ProofOperationalSelection) api.ProofOperationalSelection {
	window := api.PipelineRunCheckWindow{Kind: api.PipelineRunCheckWindowKind(value.Window.Kind), HasLowerBound: value.Window.HasLowerBound, HasUpperBound: value.Window.HasUpperBound, SourceSteps: make([]api.Slug, len(value.Window.SourceSteps))}
	for i, id := range value.Window.SourceSteps {
		window.SourceSteps[i] = id.String()
	}
	if value.Window.BackfillSeriesSlug != "" {
		window.BackfillSeriesSlug, window.BackfillChunkIndex = &value.Window.BackfillSeriesSlug, &value.Window.BackfillChunkIndex
	}
	return api.ProofOperationalSelection{
		Candidate: toAPIProofCandidateIdentity(value.Candidate), CandidateSlug: value.CandidateSlug.String(), Pipeline: value.Pipeline.String(), Run: value.Run.String(), Step: value.Step.String(),
		VersionCommit: value.VersionCommit, CompiledPlanDigest: value.CompiledPlanDigest, DeploymentResolutionDigest: value.DeploymentResolutionDigest, Attempt: value.Attempt, CheckId: value.CheckID,
		Input: value.Input, Origin: api.ProofOperationalSelectionOrigin(value.Origin), Phase: api.ProofOperationalSelectionPhase(value.Phase), Declaration: value.Declaration, ProjectionDigest: value.ProjectionDigest, Target: value.Target.String(), Window: window,
		Evaluator: value.Evaluator, EvaluatorPolicy: value.EvaluatorPolicy, StartedAt: value.StartedAt, CompletedAt: value.CompletedAt, EvidenceDigest: value.EvidenceDigest,
	}
}
