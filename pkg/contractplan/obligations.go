package contractplan

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// OperationContracts uses only the captured declarations and their exact pins.
// The step's promise/requirement is deliberately not part of this result.
func OperationContracts(op Operation) (input, output *payload.Contract, err error) {
	var content OperationContent
	if err := json.Unmarshal(op.Content, &content); err != nil {
		return nil, nil, ErrInvalid.With(err)
	}
	decode := func(raw json.RawMessage) (*payload.Contract, bool, error) {
		if len(raw) == 0 {
			return nil, false, nil
		}
		var contract *payload.Contract
		if err := json.Unmarshal(raw, &contract); err != nil {
			return nil, false, ErrInvalid.With(err)
		}
		return contract, true, nil
	}
	input, _, err = decode(content.ExpectedInputContract)
	if err != nil {
		return nil, nil, err
	}
	output, _, err = decode(content.OutputContract)
	if err != nil {
		return nil, nil, err
	}
	remaining := append([]model.PinnedODCSPort(nil), content.ContractPins...)
	resolve := func(shape **payload.Contract, ref *pipelinespec.ODCSContractRef, stepName, portName string) error {
		if err := pipelinevalidate.ValidateContractAttachment(*shape, ref); err != nil {
			return ErrInvalid.With(err)
		}
		if ref == nil {
			return nil
		}

		for i, pin := range remaining {
			if pin.StepSlug.String() != stepName || pin.Port != portName {
				continue
			}

			if !pinMatchesReference(pin, ref) {
				return ErrInvalid
			}
			if err := pipelinevalidate.ValidateContractAttachment(pin.Shape, nil); err != nil {
				return ErrInvalid.With(err)
			}
			*shape = payload.CloneContract(pin.Shape)
			remaining = append(remaining[:i], remaining[i+1:]...)
			return nil
		}
		return ErrInvalid
	}
	if err := resolve(&input, content.ExpectedInputContractRef, op.Step.String(), "input"); err != nil {
		return nil, nil, err
	}
	if err := resolve(&output, content.OutputContractRef, op.Step.String(), "output"); err != nil {
		return nil, nil, err
	}
	if len(remaining) != 0 {
		return nil, nil, ErrInvalid
	}
	return input, output, nil
}

// ApplyOperationDefaults preserves both origins. Defaults affect only omitted
// directions; an explicit step declaration remains an independent obligation.
func ApplyOperationDefaults(p *pipelinespec.Pipeline, operations []Operation) error {
	for _, op := range operations {
		input, output, err := OperationContracts(op)
		if err != nil {
			return err
		}
		for i := range p.Steps {
			step := &p.Steps[i]
			if step.Slug != op.Step {
				continue
			}
			var declared, saved *payload.Contract
			phase := "post_query"
			switch {
			case op.Role == "query" && step.Query != nil:
				declared, saved = step.Query.OutputContract, output
				if declared == nil && step.Query.OutputContractRef == nil {
					step.Query.OutputContract = payload.CloneContract(output)
				}
			case op.Role == "sink" && step.Sink != nil:
				phase = "pre_sink"
				declared, saved = step.Sink.InputContract, input
				if declared == nil && step.Sink.InputContractRef == nil {
					step.Sink.InputContract = payload.CloneContract(input)
				}
			default:
				return ErrInvalid
			}

			if err := payload.ValidateContractConjunction(saved, declared); err != nil {
				return xerror.Wrapf("step %q operation and step obligations (%s): %w", op.Step, phase, err)
			}
		}
	}
	// Check each saved sink requirement against the upstream promise only after
	// all operation defaults are installed, independently of operation sort order.
	for _, op := range operations {
		if op.Role != "sink" {
			continue
		}
		input, _, err := OperationContracts(op)
		if err != nil {
			return err
		}
		if input == nil {
			continue
		}
		check := *p
		check.Steps = append([]pipelinespec.Step(nil), p.Steps...)
		for i, step := range check.Steps {
			if step.Slug != op.Step || step.Sink == nil {
				continue
			}
			sink := *step.Sink
			sink.InputContract, sink.InputContractRef = input, nil
			check.Steps[i].Sink = &sink
		}
		if err := pipelinevalidate.ValidateStepInputCompatibility(&check); err != nil {
			return xerror.Wrapf("step %q operation input obligation (pre_sink): %w", op.Step, err)
		}
	}
	return nil
}

// RuntimePipeline reconstructs executable declarations from stored material only.
func (p *Plan) RuntimePipeline() (*pipelinespec.Pipeline, error) {
	if !p.supportsOperations() {
		return nil, ErrInvalid
	}
	pipeline, _, err := authoredIdentity(p.Authored)
	if err != nil {
		return nil, err
	}
	if err := validateOperations(pipeline, p.Operations); err != nil {
		return nil, err
	}
	if err := p.validateResourceScopes(); err != nil {
		return nil, err
	}
	if err := resolvePins(pipeline, p.Pins); err != nil {
		return nil, err
	}
	if err := ApplyOperationDefaults(pipeline, p.Operations); err != nil {
		return nil, err
	}
	return pipeline, nil
}
