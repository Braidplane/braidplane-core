package contractplan

import (
	"encoding/json"
	"slices"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type operationRef struct {
	step              slug.Slug
	role              string
	source, reference slug.Slug
	inline            map[string]any
}

func operationRefs(p *pipelinespec.Pipeline) []operationRef {
	var refs []operationRef
	for _, step := range p.Steps {
		if q := step.Query; q != nil {
			refs = append(refs, operationRef{step.Slug, "query", q.DataSourceSlug, q.OperationRef, q.OperationInline})
		}
		if s := step.Sink; s != nil {
			refs = append(refs, operationRef{step.Slug, "sink", s.DataSourceSlug, s.OperationRef, s.OperationInline})
		}
	}
	return refs
}

func validateOperations(p *pipelinespec.Pipeline, operations []Operation) error {
	remaining := slices.Clone(operations)
	for _, ref := range operationRefs(p) {
		if ref.reference == "" {
			continue
		}
		i := slices.IndexFunc(remaining, func(op Operation) bool { return op.Step == ref.step && op.Role == ref.role })
		if i < 0 || len(ref.inline) != 0 {
			return ErrInvalid
		}
		op := remaining[i]
		content, err := canonical(op.Content)
		if err != nil || op.Slug != ref.reference || op.DataSource != ref.source || op.Revision == "" || digest(content) != op.Digest {
			return ErrInvalid.With(err)
		}
		var object OperationContent
		if err := json.Unmarshal(content, &object); err != nil || len(object.Data) == 0 {
			return ErrInvalid.With(err)
		}
		var data struct {
			Statement string `json:"statement"`
		}
		if err := json.Unmarshal(object.Data, &data); err != nil || data.Statement == "" {
			return ErrInvalid.With(err)
		}
		remaining = slices.Delete(remaining, i, i+1)
	}
	if len(remaining) != 0 {
		return ErrInvalid
	}
	return nil
}

type port struct {
	step  slug.Slug
	name  string
	ref   **pipelinespec.ODCSContractRef
	shape **payload.Contract
}

func ports(p *pipelinespec.Pipeline) []port {
	var all []port
	for i := range p.Steps {
		s := &p.Steps[i]
		if s.Input != nil {
			all = append(all, port{s.Slug, "output", &s.Input.OutputContractRef, &s.Input.OutputContract})
		}
		if s.Query != nil {
			all = append(all, port{s.Slug, "output", &s.Query.OutputContractRef, &s.Query.OutputContract})
		}
		if s.Transform != nil {
			all = append(all, port{s.Slug, "input", &s.Transform.InputContractRef, &s.Transform.InputContract}, port{s.Slug, "output", &s.Transform.OutputContractRef, &s.Transform.OutputContract})
		}
		if s.Transform != nil {
			for name, attachment := range s.Transform.InputContracts {
				if attachment != nil {
					all = append(all, port{s.Slug, "input:" + name, &attachment.ContractRef, &attachment.Contract})
				}
			}
		}
		if s.Sink != nil {
			all = append(all, port{s.Slug, "input", &s.Sink.InputContractRef, &s.Sink.InputContract})
		}
	}
	return all
}

func resolvePins(p *pipelinespec.Pipeline, pins []model.PinnedODCSPort) error {
	remaining := slices.Clone(pins)
	for _, port := range ports(p) {
		ref := *port.ref
		if ref == nil {
			continue
		}
		i := slices.IndexFunc(remaining, func(pin model.PinnedODCSPort) bool { return pin.StepSlug == port.step && pin.Port == port.name })
		if i < 0 || *port.shape != nil {
			return ErrInvalid
		}
		pin := remaining[i]
		if !pinMatchesReference(pin, ref) {
			return ErrInvalid
		}
		*port.ref, *port.shape = nil, pin.Shape
		remaining = slices.Delete(remaining, i, i+1)
	}
	if len(remaining) != 0 {
		return ErrInvalid
	}
	return nil
}

func validatePins(p *pipelinespec.Pipeline, pins []model.PinnedODCSPort) error {
	if err := resolvePins(p, pins); err != nil {
		return err
	}
	if pipelinevalidate.ValidateArtifactStructure(p) != nil || pipelinevalidate.ValidateStepInputCompatibility(p) != nil {
		return ErrInvalid
	}
	return nil
}
