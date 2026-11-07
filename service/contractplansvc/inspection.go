package contractplansvc

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
)

// Summary contains identity metadata only, never authored or resolved values.
type Summary struct {
	PipelineSlug                                             slug.Slug
	Digest, SpecHash, Status, CompilerID, StructuralPolicyID string
	AdmissionEnabled                                         bool
	Source                                                   contractplan.Source
	Pins                                                     []ContractPin
	Operations                                               []OperationIdentity
	Projections                                              []ProjectionIdentity
}

type ProjectionIdentity struct {
	Input                                                                                    int
	Contract, Revision, Object, Digest, ResolvedDigest, MappingID, Representation, RootShape string
}

type ContractPin struct {
	Step, Contract, Revision                                          slug.Slug
	Port, Origin, Object, Digest, Compiler, Representation, RootShape string
}

type OperationIdentity struct {
	Step, Operation        slug.Slug
	Role, Revision, Digest string
	ResourceScope          *resourcescope.Identity
}

func summarize(stored *compiledplans.Snapshot) *Summary {
	result := &Summary{PipelineSlug: stored.PipelineSlug, Digest: stored.Digest, SpecHash: stored.AuthoredSpecHash, Status: "unsupported", AdmissionEnabled: true}
	plan, err := validateSnapshot(stored, stored.PipelineSlug, stored.Digest)
	if err != nil {
		return result
	}
	result.Status, result.Source, result.CompilerID, result.StructuralPolicyID = "available", plan.Source, plan.CompilerID, plan.StructuralPolicyID
	if plan.Projection != nil {
		for i, input := range plan.Projection.Inputs {
			result.Projections = append(result.Projections, ProjectionIdentity{Input: i, Contract: input.Contract, Revision: input.Revision, Object: input.ObjectID, Digest: input.Digest, ResolvedDigest: input.ResolvedDigest, MappingID: input.Mapping.ID, Representation: input.Mapping.Representation.String(), RootShape: input.Mapping.RootShape.String()})
		}
	}
	appendPin := func(pin model.PinnedODCSPort, origin string) {
		identity := ContractPin{Step: pin.StepSlug, Port: pin.Port, Origin: origin, Contract: pin.ContractSlug, Revision: pin.RevisionSlug, Object: pin.SchemaObjectID, Digest: pin.Digest, Compiler: pin.CompilerID}
		if pin.Shape != nil {
			identity.Representation, identity.RootShape = pin.Shape.Representation.String(), pin.Shape.RootShape.String()
		}
		result.Pins = append(result.Pins, identity)
	}
	for _, pin := range plan.Pins {
		appendPin(pin, "step")
	}
	for _, operation := range plan.Operations {
		result.Operations = append(result.Operations, OperationIdentity{Step: operation.Step, Role: operation.Role, Operation: operation.Slug, Revision: operation.Revision, Digest: operation.Digest})
		var content contractplan.OperationContent
		if json.Unmarshal(operation.Content, &content) == nil {
			result.Operations[len(result.Operations)-1].ResourceScope = content.ResourceScope.Identity()
			for _, pin := range content.ContractPins {
				appendPin(pin, "operation")
			}
		}
	}
	return result
}
