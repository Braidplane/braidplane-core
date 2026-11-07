package contractplan

import (
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// SelectStructuralProjection identifies one exact executable check shape
// when a contract object is reused through several mappings.
func SelectStructuralProjection(plan *odcs.ProjectionPlan, ref *pipelinespec.ODCSContractRef, contract *payload.Contract) (*payload.Structure, string, error) {
	if plan == nil {
		return nil, "", nil
	}
	var selected *payload.Structure
	selectedDigest := ""
	matched := false
	for _, projected := range plan.Inputs {
		if projected.Contract != ref.ContractSlug.String() || projected.Revision != ref.RevisionSlug.String() || projected.ObjectID != ref.SchemaObjectID {
			continue
		}
		matched = true
		structure := projected.Structure
		if structure == nil {
			return nil, "", ErrInvalid
		}
		if contract.Representation != model.ContractRepresentationUnknown && structure.Representation != contract.Representation {
			continue
		}
		if contract.RootShape != model.ContractRootShapeUnknown && structure.RootShape != contract.RootShape {
			continue
		}
		encoded, err := json.Marshal(structure)
		if err != nil {
			return nil, "", xerror.Unexpected().With(err)
		}
		candidate := strings.TrimPrefix(digest(encoded), "sha256:")
		if selected != nil && candidate != selectedDigest {
			return nil, "", ErrInvalid
		}
		selected, selectedDigest = structure, candidate
	}
	if matched && selected == nil {
		return nil, "", ErrInvalid
	}
	return selected, selectedDigest, nil
}

// ValidateProjectionBindings rejects new public compilations whose selected
// projections cannot supply the exact port checks. Historical readers stay unchanged.
func (p *Plan) ValidateProjectionBindings() error {
	pins := append([]model.PinnedODCSPort(nil), p.Pins...)
	for _, operation := range p.Operations {
		var content OperationContent
		if err := json.Unmarshal(operation.Content, &content); err != nil {
			return ErrInvalid.With(err)
		}
		pins = append(pins, content.ContractPins...)
	}
	for _, pin := range pins {
		if pin.Shape == nil {
			return ErrInvalid
		}
		ref := &pipelinespec.ODCSContractRef{ContractSlug: pin.ContractSlug, RevisionSlug: pin.RevisionSlug, SchemaObjectID: pin.SchemaObjectID}
		if _, _, err := SelectStructuralProjection(p.Projection, ref, pin.Shape); err != nil {
			return err
		}
	}
	return nil
}
