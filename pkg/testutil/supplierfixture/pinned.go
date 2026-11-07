package supplierfixture

import (
	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// PinnedPipeline replaces normalized-offer declarations with the same immutable
// catalog reference. Raw supplier contracts retain their provider-specific shape.
func PinnedPipeline() (*spec.Pipeline, *spec.ODCSContractRef, error) {
	raw, err := Files.ReadFile("data/supplier-price-desk.pipeline.yaml")
	if err != nil {
		return nil, nil, xerror.Wrapf("read supplier pipeline fixture: %w", err)
	}
	pipeline, err := validate.Load("supplier-price-desk.pipeline.yaml", raw)
	if err != nil {
		return nil, nil, err
	}
	ref := &spec.ODCSContractRef{ContractSlug: "supplier-offer", RevisionSlug: "v1", SchemaObjectID: "offer", Representation: model.ContractRepresentationRows}
	for _, step := range pipeline.Steps {
		if transform := step.Transform; transform != nil {
			transform.OutputContract, transform.OutputContractRef = nil, ref
			for _, attachment := range transform.InputContracts {
				attachment.Contract, attachment.ContractRef = nil, ref
			}
		}
		if sink := step.Sink; sink != nil {
			sink.InputContract, sink.InputContractRef = nil, ref
		}
	}
	return pipeline, ref, nil
}
