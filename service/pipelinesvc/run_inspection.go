package pipelinesvc

import (
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"
)

// RunCompositionInspection deliberately excludes document, operation and input values.
type RunCompositionInspection struct {
	AdmissionEnabled                                                           bool
	Status, TemplateDigest, CompiledPlanDigest, ResolutionDigest, SourceCommit string
	Pins                                                                       []RunContractPinInspection
	Operations                                                                 []RunOperationInspection
	Targets                                                                    []RunTargetInspection
}

type RunContractPinInspection struct {
	Step                                                slug.Slug
	Port, Origin                                        string
	Contract, Revision                                  slug.Slug
	Object, Digest, Compiler, Representation, RootShape string
}

type RunOperationInspection struct {
	Step, Operation        slug.Slug
	Role, Revision, Digest string
	ResourceScope          *resourcescope.Identity
}

type RunTargetInspection struct {
	Step, Reference, DataSource slug.Slug
	Role, BindingRevision       string
}

func (s *service) inspectRunComposition(ctx context.Context, org, project, environment slug.Slug, detail *PipelineRunDetail, digest *string) (*RunCompositionInspection, error) {
	if digest == nil || *digest == "" {
		return &RunCompositionInspection{Status: "not_recorded"}, nil
	}
	pipeline, err := slug.New(detail.Metadata.PipelineSlug)
	if err != nil {
		return nil, ErrPipelineRunNotFound.With(err)
	}
	// This is a historical reader; new-admission controls must not hide retained evidence.
	raw, err := s.resolutions.Find(ctx, s.dbtx, org, project, environment, pipeline, *digest)
	if xerror.Is(err, deploymentresolutions.ErrNotFound) {
		return &RunCompositionInspection{Status: "unavailable", ResolutionDigest: *digest}, nil
	}
	if err != nil {
		return nil, xerror.Wrapf("read recorded run resolution: %w", err)
	}
	return projectRunComposition(raw, *digest, org, project, environment, pipeline, detail.Metadata.PipelineVersionSpecHash), nil
}

func projectRunComposition(raw []byte, digest string, org, project, environment, pipeline slug.Slug, specHash string) *RunCompositionInspection {
	result := &RunCompositionInspection{Status: "unsupported", ResolutionDigest: digest}
	resolution, err := deploymentplan.Read(raw, digest)
	if err != nil || !resolution.Matches(org, project, environment, pipeline) {
		return result
	}
	compiled, err := contractplan.Read(resolution.CompiledPlan, resolution.CompiledDigest)
	if err != nil || compiled.AuthoredSpecHash != specHash {
		return result
	}
	result.Status, result.TemplateDigest, result.CompiledPlanDigest, result.SourceCommit = "available", resolution.TemplateDigest, resolution.CompiledDigest, compiled.Source.Commit
	for _, pin := range compiled.Pins {
		result.Pins = append(result.Pins, inspectRunPin(pin, "step", pin.StepSlug))
	}
	for _, operation := range compiled.Operations {
		result.Operations = append(result.Operations, RunOperationInspection{Step: operation.Step, Role: operation.Role, Operation: operation.Slug, Revision: operation.Revision, Digest: operation.Digest})
		var content contractplan.OperationContent
		if json.Unmarshal(operation.Content, &content) != nil {
			return &RunCompositionInspection{Status: "unsupported", ResolutionDigest: digest}
		}
		result.Operations[len(result.Operations)-1].ResourceScope = content.ResourceScope.Identity()
		for _, pin := range content.ContractPins {
			result.Pins = append(result.Pins, inspectRunPin(pin, "operation", operation.Step))
		}
	}
	for _, target := range resolution.Targets {
		result.Targets = append(result.Targets, RunTargetInspection{Step: target.Step, Role: target.Role, Reference: target.Reference, DataSource: target.DataSource, BindingRevision: target.BindingRevision})
	}
	return result
}

func inspectRunPin(pin model.PinnedODCSPort, origin string, step slug.Slug) RunContractPinInspection {
	result := RunContractPinInspection{Step: step, Port: pin.Port, Origin: origin, Contract: pin.ContractSlug, Revision: pin.RevisionSlug, Object: pin.SchemaObjectID, Digest: pin.Digest, Compiler: pin.CompilerID}
	if pin.Shape != nil {
		result.Representation, result.RootShape = pin.Shape.Representation.String(), pin.Shape.RootShape.String()
	}
	return result
}
