package contractplan

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
)

// CapturePipelinePins resolves every authored port through an authorized catalog
// lookup. Selected structures never pass through the flat ODCS converter.
func CapturePipelinePins(pipeline *pipelinespec.Pipeline, projection *odcs.ProjectionPlan, lookup func(*pipelinespec.ODCSContractRef) (*model.ODCSRevision, error)) ([]model.PinnedODCSPort, error) {
	var pins []model.PinnedODCSPort
	pinBytes := 2
	for _, port := range ports(pipeline) {
		ref := *port.ref
		if ref == nil {
			continue
		}
		if *port.shape != nil {
			return nil, ErrInvalid
		}
		revision, err := lookup(ref)
		if err != nil {
			return nil, err
		}
		pin, err := CapturePort(ref, revision, projection)
		if err != nil {
			return nil, err
		}
		pin.StepSlug, pin.Port = port.step, port.name
		encoded, err := json.Marshal(pin)
		if err != nil {
			return nil, ErrInvalid.With(err)
		}
		pinBytes += len(encoded) + 1
		if pinBytes > odcs.MaxProjectionBytes {
			return nil, ErrInvalid
		}
		pins = append(pins, pin)
	}
	return pins, nil
}

func CapturePort(ref *pipelinespec.ODCSContractRef, revision *model.ODCSRevision, projection *odcs.ProjectionPlan) (model.PinnedODCSPort, error) {
	if ref == nil || revision == nil || pipelinevalidate.ValidateContractAttachment(nil, ref) != nil {
		return model.PinnedODCSPort{}, ErrInvalid
	}
	pin := model.PinnedODCSPort{ContractSlug: ref.ContractSlug, RevisionSlug: ref.RevisionSlug, SchemaObjectID: ref.SchemaObjectID, Digest: revision.Digest}
	if revision.ODCSVersion != odcs.PinnedVersion {
		return pin, ErrInvalid
	}
	selected, err := selectExecutableProjection(projection, ref, &payload.Contract{Representation: ref.Representation, RootShape: ref.RootShape})
	if err != nil {
		return pin, err
	}
	if selected != nil {
		if selected.Digest != revision.Digest {
			return pin, ErrInvalid
		}
		pin.Coverage = selected.Coverage
		pin.Shape, err = projectionContract(selected.Structure)
		pin.CompilerID = odcs.ProjectionCompilerID
		return pin, err
	}
	conversion, err := odcs.Convert(revision.Document, ref.SchemaObjectID)
	if err != nil {
		return pin, err
	}
	if conversion.Shape == nil || len(conversion.Diagnostics) != 0 || conversion.Digest != revision.Digest {
		return pin, ErrInvalid
	}
	pin.Shape, pin.CompilerID, pin.Coverage = conversion.Shape, conversion.CompilerID, conversion.Coverage
	pin.Shape.Representation, pin.Shape.RootShape = ref.Representation, ref.RootShape
	return pin, nil
}

// projectionContract supplies the existing top-level compatibility checks. The
// retained structure remains mandatory for complete runtime enforcement.
func projectionContract(s *payload.Structure) (*payload.Contract, error) {
	if payload.ValidateStructure(s, payload.DefaultStructuralLimits()) != nil || s.Root.Kind != payload.StructuralObject || len(s.Root.Properties) == 0 || s.Representation != model.ContractRepresentationRows && s.Representation != model.ContractRepresentationJSON {
		return nil, ErrInvalid
	}
	c := &payload.Contract{Name: s.Object, Representation: s.Representation, RootShape: s.RootShape}
	for _, property := range s.Root.Properties {
		if strings.ContainsAny(property.Name, ".[]") {
			return nil, ErrInvalid
		}
		t := property.Type
		kind := map[payload.StructuralKind]model.ContractFieldType{
			payload.StructuralObject: model.ContractFieldTypeValueObject, payload.StructuralMap: model.ContractFieldTypeValueObject,
			payload.StructuralArray: model.ContractFieldTypeValueArray, payload.StructuralVector: model.ContractFieldTypeValueArray,
			payload.StructuralString: model.ContractFieldTypeValueString, payload.StructuralDate: model.ContractFieldTypeValueString,
			payload.StructuralLocalTime: model.ContractFieldTypeValueString, payload.StructuralLocalDatetime: model.ContractFieldTypeValueString, payload.StructuralInstant: model.ContractFieldTypeValueString,
			payload.StructuralBoolean: model.ContractFieldTypeValueBoolean, payload.StructuralInteger: model.ContractFieldTypeValueInteger,
			payload.StructuralNumber: model.ContractFieldTypeValueNumber, payload.StructuralDecimal: model.ContractFieldTypeValueDecimal,
		}[t.Kind]
		if kind == model.ContractFieldTypeUnknown {
			return nil, ErrInvalid
		}
		field := payload.ContractField{Name: property.Name, Type: kind, Required: property.Required, Nullable: t.Nullable}
		if t.Kind == payload.StructuralDecimal {
			precision, scale := t.Precision, t.Scale
			field.Precision, field.Scale = &precision, &scale
		}
		c.Fields = append(c.Fields, field)
	}
	return c, nil
}

func pinMatchesReference(pin model.PinnedODCSPort, ref *pipelinespec.ODCSContractRef) bool {
	if !validDigest(pin.Digest) || pin.Shape == nil || pin.ContractSlug != ref.ContractSlug || pin.RevisionSlug != ref.RevisionSlug || pin.SchemaObjectID != ref.SchemaObjectID {
		return false
	}
	if pin.CompilerID == odcs.ProjectionCompilerID {
		return (ref.Representation == model.ContractRepresentationUnknown || pin.Shape.Representation == ref.Representation) && (ref.RootShape == model.ContractRootShapeUnknown || pin.Shape.RootShape == ref.RootShape)
	}
	return odcs.IsFlatCompiler(pin.CompilerID) && pin.Shape.Representation == ref.Representation && pin.Shape.RootShape == ref.RootShape
}

func (p *Plan) validateExecutablePins() error {
	pins := append([]model.PinnedODCSPort(nil), p.Pins...)
	for _, op := range p.Operations {
		var content OperationContent
		if err := json.Unmarshal(op.Content, &content); err != nil {
			return ErrInvalid.With(err)
		}
		pins = append(pins, content.ContractPins...)
	}
	for _, pin := range pins {
		if pin.CompilerID != odcs.ProjectionCompilerID {
			continue
		}
		if p.FormatVersion < 4 || pin.Shape == nil {
			return ErrInvalid
		}
		ref := &pipelinespec.ODCSContractRef{ContractSlug: pin.ContractSlug, RevisionSlug: pin.RevisionSlug, SchemaObjectID: pin.SchemaObjectID}
		selected, err := selectExecutableProjection(p.Projection, ref, pin.Shape)
		if err != nil || selected == nil || selected.Digest != pin.Digest {
			return ErrInvalid.With(err)
		}
		shape, err := projectionContract(selected.Structure)
		if err != nil || !reflect.DeepEqual(shape, pin.Shape) {
			return ErrInvalid.With(err)
		}
	}
	return nil
}

// New executable pins require one exact mapping input even when two mappings
// produce identical structures. Historical structural selection stays unchanged.
func selectExecutableProjection(plan *odcs.ProjectionPlan, ref *pipelinespec.ODCSContractRef, contract *payload.Contract) (*odcs.ProjectedInput, error) {
	structure, _, err := SelectStructuralProjection(plan, ref, contract)
	if err != nil || structure == nil {
		return nil, err
	}
	var selected *odcs.ProjectedInput
	for i := range plan.Inputs {
		input := &plan.Inputs[i]
		if input.Contract != ref.ContractSlug.String() || input.Revision != ref.RevisionSlug.String() || input.ObjectID != ref.SchemaObjectID || input.Structure.Representation != structure.Representation || input.Structure.RootShape != structure.RootShape {
			continue
		}
		if selected != nil {
			return nil, ErrInvalid
		}
		selected = input
	}
	return selected, nil
}
