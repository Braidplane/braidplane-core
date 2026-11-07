package contractplan

import (
	"bytes"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

func (p *Plan) validateResourceScopes() error {
	pipeline, _, err := authoredIdentity(p.Authored)
	if err != nil {
		return err
	}
	for _, op := range p.Operations {
		var content OperationContent
		if err := json.Unmarshal(op.Content, &content); err != nil {
			return ErrInvalid.With(err)
		}
		scope := content.ResourceScope
		if scope == nil {
			continue
		}
		if p.FormatVersion < 3 {
			return ErrInvalid
		}
		descriptor, supported := catalog.DescriptorFor(op.DataSourceType)
		if !supported || descriptor.ResourceScopeCapability().PolicyID != scope.PolicyID {
			return ErrInvalid
		}
		for _, step := range pipeline.Steps {
			if step.Slug != op.Step {
				continue
			}
			if op.Role == "query" && (step.Query == nil || step.Query.OperationType != "s3") || op.Role == "sink" && (step.Sink == nil || step.Sink.OperationType != "s3") {
				return ErrInvalid
			}
		}
		// Unknown scope fields cannot acquire a confinement claim by being ignored.
		decoder := json.NewDecoder(bytes.NewReader(op.Content))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&content); err != nil {
			return ErrInvalid.With(err)
		}
		if err := scope.Validate(); err != nil || scope.DataSource != op.DataSource || scope.Operation != op.Slug {
			return ErrInvalid.With(err)
		}
		var data struct {
			Statement string `json:"statement"`
		}
		if err := json.Unmarshal(content.Data, &data); err != nil {
			return ErrInvalid.With(err)
		}
		if err := scope.ValidateStatement([]byte(data.Statement)); err != nil {
			return ErrInvalid.With(err)
		}
		matched := false
		port := "output"
		if op.Role == "sink" {
			port = "input"
		}
		for _, pins := range [][]model.PinnedODCSPort{p.Pins, content.ContractPins} {
			for _, pin := range pins {
				if pin.StepSlug == op.Step && pin.Port == port && pin.ContractSlug == scope.Contract && pin.RevisionSlug == scope.Revision && pin.SchemaObjectID == scope.ObjectID && pin.Shape != nil && pin.Shape.Representation == scope.Representation {
					matched = true
				}
			}
		}
		if !matched {
			return ErrInvalid
		}
	}
	return nil
}
