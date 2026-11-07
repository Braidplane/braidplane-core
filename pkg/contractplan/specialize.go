package contractplan

import (
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
)

// Specialize resolves only declared non-secret projection parameters from the
// stored template. It never reads catalog revisions or mutable operations.
func Specialize(raw []byte, id string, values map[string]odcs.ResolutionValue) ([]byte, string, error) {
	plan, err := Read(raw, id)
	if err != nil {
		return nil, "", err
	}
	if len(values) == 0 {
		return raw, id, nil
	}
	if (plan.FormatVersion != 3 && plan.FormatVersion != FormatVersion) || plan.ProjectionInputs == nil || len(values) > 128 {
		return nil, "", ErrInvalid
	}
	for key, value := range values {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value.Revision) == "" || len(value.Revision) > 255 || len(value.Value) > 4000 || strings.Contains(value.Value, "{{") || strings.Contains(value.Value, "${") {
			return nil, "", ErrInvalid
		}
		declared := false
		for i := range plan.ProjectionInputs.Inputs {
			input := &plan.ProjectionInputs.Inputs[i]
			if _, ok := input.Variables[key]; ok {
				input.Variables[key] = value
				declared = true
			}
		}
		if !declared {
			return nil, "", ErrInvalid
		}
	}
	if plan.FormatVersion >= 4 {
		projection, err := odcs.CompileProjection(*plan.ProjectionInputs)
		if err != nil {
			return nil, "", err
		}
		if projection.Plan == nil || len(projection.Diagnostics) != 0 {
			return nil, "", ErrInvalid
		}
		refresh := func(pins []model.PinnedODCSPort) error {
			for i, pin := range pins {
				if pin.CompilerID != odcs.ProjectionCompilerID {
					continue
				}
				ref := &pipelinespec.ODCSContractRef{ContractSlug: pin.ContractSlug, RevisionSlug: pin.RevisionSlug, SchemaObjectID: pin.SchemaObjectID, Representation: pin.Shape.Representation, RootShape: pin.Shape.RootShape}
				updated, err := CapturePort(ref, &model.ODCSRevision{Digest: pin.Digest, ODCSVersion: odcs.PinnedVersion}, projection.Plan)
				if err != nil {
					return err
				}
				updated.StepSlug, updated.Port = pin.StepSlug, pin.Port
				pins[i] = updated
			}
			return nil
		}
		if err := refresh(plan.Pins); err != nil {
			return nil, "", err
		}
		for i := range plan.Operations {
			var content OperationContent
			if err := json.Unmarshal(plan.Operations[i].Content, &content); err != nil {
				return nil, "", ErrInvalid.With(err)
			}
			if err := refresh(content.ContractPins); err != nil {
				return nil, "", ErrInvalid.With(err)
			}
			plan.Operations[i].Content, err = json.Marshal(content)
			if err != nil {
				return nil, "", ErrInvalid.With(err)
			}
		}
	}
	return Compile(Input{Source: plan.Source, Authored: plan.Authored, Operations: plan.Operations, Pins: plan.Pins, Projection: plan.ProjectionInputs})
}
