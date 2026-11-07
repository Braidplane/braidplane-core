package views

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

const (
	pipelineSpecStepKindSource      = "source"
	pipelineSpecStepKindTransform   = "transform"
	pipelineSpecStepKindSink        = "sink"
	pipelineSpecOperationModeInline = "inline"
)

// PipelineSpecProjection is the read-only display model for a canonical pipeline spec.
type PipelineSpecProjection struct {
	SourceSteps    []PipelineSpecStepProjection
	TransformSteps []PipelineSpecStepProjection
	SinkSteps      []PipelineSpecStepProjection
	DecodeIssues   []string
	TotalSteps     int
}

// PipelineSpecStepProjection contains the operator-facing fields for one canonical step.
type PipelineSpecStepProjection struct {
	SpecPosition     int
	Kind             string
	KindLabel        string
	Slug             string
	Name             string
	DataSourceSlug   string
	OperationType    string
	OperationRef     string
	OperationMode    string
	OperationLabel   string
	OperationInline  string
	Parameters       string
	SQL              string
	OutputKind       string
	IdempotencyClass string
	Inputs           []PipelineSpecInputProjection
	ContractSlots    []PipelineSpecContractSlot
}

// PipelineSpecInputProjection describes one explicit upstream step reference.
type PipelineSpecInputProjection struct {
	Name     string
	FromStep string
}

// PipelineSpecContractSlot keeps the declared role of a contract on a step.
type PipelineSpecContractSlot struct {
	Label    string
	Contract *api.PipelineSchemaContract
	Ref      *api.PipelineODCSContractRef
}

// PipelineSpecProjectionFromPipeline projects generated API step unions into stable view data.
func PipelineSpecProjectionFromPipeline(pipeline *api.Pipeline) PipelineSpecProjection {
	projection := PipelineSpecProjection{}
	if pipeline == nil {
		return projection
	}

	projection.TotalSteps = len(pipeline.Spec.Steps)
	for index, step := range pipeline.Spec.Steps {
		kind, err := step.Discriminator()
		if err != nil {
			projection.DecodeIssues = append(projection.DecodeIssues, pipelineSpecDecodeIssue(index))
			continue
		}
		switch kind {
		case string(api.PipelineStepKindInput):
			inputStep, err := step.AsPipelineInputStep()
			if err != nil {
				projection.DecodeIssues = append(projection.DecodeIssues, pipelineSpecDecodeIssue(index))
				continue
			}
			projection.SourceSteps = append(projection.SourceSteps, pipelineSpecInputStepProjection(index+1, inputStep))
		case string(api.PipelineStepKindQuery):
			queryStep, err := step.AsPipelineQueryStep()
			if err != nil {
				projection.DecodeIssues = append(projection.DecodeIssues, pipelineSpecDecodeIssue(index))
				continue
			}
			projection.SourceSteps = append(projection.SourceSteps, pipelineSpecQueryStepProjection(index+1, queryStep))
		case string(api.PipelineStepKindTransform):
			transformStep, err := step.AsPipelineTransformStep()
			if err != nil {
				projection.DecodeIssues = append(projection.DecodeIssues, pipelineSpecDecodeIssue(index))
				continue
			}
			projection.TransformSteps = append(projection.TransformSteps, pipelineSpecTransformStepProjection(index+1, transformStep))
		case string(api.PipelineStepKindSink):
			sinkStep, err := step.AsPipelineSinkStep()
			if err != nil {
				projection.DecodeIssues = append(projection.DecodeIssues, pipelineSpecDecodeIssue(index))
				continue
			}
			projection.SinkSteps = append(projection.SinkSteps, pipelineSpecSinkStepProjection(index+1, sinkStep))
		default:
			projection.DecodeIssues = append(projection.DecodeIssues, pipelineSpecDecodeIssue(index))
		}
	}
	return projection
}

func pipelineSpecInputStepProjection(specPosition int, step api.PipelineInputStep) PipelineSpecStepProjection {
	var reference *api.PipelineODCSContractRef
	if selected := step.Input.OutputContractRef; selected != nil {
		representation := api.PipelineODCSContractRefRepresentation(selected.Representation)
		root := api.PipelineODCSContractRefRootShape(selected.RootShape)
		reference = &api.PipelineODCSContractRef{ContractSlug: selected.ContractSlug, RevisionSlug: selected.RevisionSlug, SchemaObjectId: selected.SchemaObjectId, Representation: &representation, RootShape: &root}
	}
	return PipelineSpecStepProjection{
		SpecPosition: specPosition,
		Kind:         pipelineSpecStepKindSource,
		KindLabel:    "Input",
		Slug:         step.Slug,
		Name:         pipelineSpecNamePtr(step.Name),
		ContractSlots: []PipelineSpecContractSlot{
			{Label: "Output contract", Contract: step.Input.OutputContract, Ref: reference},
		},
	}
}

// PipelineSpecContractFields returns a stable field list for a rendered contract.
func PipelineSpecContractFields(contract *api.PipelineSchemaContract) []api.PipelineSchemaContractField {
	if contract == nil || contract.Fields == nil {
		return nil
	}
	fields := make([]api.PipelineSchemaContractField, len(*contract.Fields))
	copy(fields, *contract.Fields)
	return fields
}

// PipelineSpecContractSummary renders representation, root shape, and field count for a contract.
func PipelineSpecContractSummary(contract *api.PipelineSchemaContract) string {
	if contract == nil {
		return "not declared"
	}

	parts := []string{}
	if contract.Representation != nil && strings.TrimSpace(string(*contract.Representation)) != "" {
		parts = append(parts, strings.TrimSpace(string(*contract.Representation)))
	}
	if contract.RootShape != nil && strings.TrimSpace(string(*contract.RootShape)) != "" {
		parts = append(parts, "root "+strings.TrimSpace(string(*contract.RootShape)))
	}
	fieldCount := len(PipelineSpecContractFields(contract))
	switch fieldCount {
	case 0:
		parts = append(parts, "no fields")
	case 1:
		parts = append(parts, "1 field")
	default:
		parts = append(parts, strconv.Itoa(fieldCount)+" fields")
	}
	if len(parts) == 0 {
		return "declared"
	}
	return strings.Join(parts, " / ")
}

// PipelineSpecDecodeIssueSummary renders the decode warning summary with correct plural copy.
func PipelineSpecDecodeIssueSummary(count int) string {
	if count == 1 {
		return "1 spec step could not be decoded for display."
	}
	return strconv.Itoa(count) + " spec steps could not be decoded for display."
}

// PipelineSpecContractName returns a contract name or a stable fallback label.
func PipelineSpecContractName(contract *api.PipelineSchemaContract) string {
	if contract == nil {
		return ""
	}
	if name := strings.TrimSpace(contract.Name); name != "" {
		return name
	}
	return "unnamed contract"
}

// PipelineSpecContractRepresentation returns the display value for a contract representation.
func PipelineSpecContractRepresentation(contract *api.PipelineSchemaContract) string {
	if contract == nil || contract.Representation == nil {
		return "-"
	}
	return pipelineSpecDisplayValue(string(*contract.Representation))
}

// PipelineSpecContractRootShape returns the display value for a JSON contract root shape.
func PipelineSpecContractRootShape(contract *api.PipelineSchemaContract) string {
	if contract == nil || contract.RootShape == nil {
		return "-"
	}
	return pipelineSpecDisplayValue(string(*contract.RootShape))
}

// PipelineSpecContractHasRootShape reports whether a JSON contract declares a root shape.
func PipelineSpecContractHasRootShape(contract *api.PipelineSchemaContract) bool {
	if contract == nil || contract.Representation == nil || contract.RootShape == nil {
		return false
	}
	return *contract.Representation == api.PipelineSchemaContractRepresentationJson && strings.TrimSpace(string(*contract.RootShape)) != ""
}

// PipelineSpecContractFieldLabels renders field requiredness, nullability, and validation badges.
func PipelineSpecContractFieldLabels(field api.PipelineSchemaContractField) []string {
	labels := []string{}
	if field.Required {
		labels = append(labels, "required")
	} else {
		labels = append(labels, "optional")
	}
	if field.Nullable != nil {
		if *field.Nullable {
			labels = append(labels, "nullable")
		} else {
			labels = append(labels, "non-null")
		}
	}
	labels = append(labels, PipelineSpecContractValidationLabels(field.Validations)...)
	return labels
}

// PipelineSpecContractValidationLabels renders supported field validation rules compactly.
func PipelineSpecContractValidationLabels(validations *api.PipelineSchemaContractFieldValidations) []string {
	if validations == nil {
		return nil
	}

	labels := []string{}
	if validations.Format != nil && strings.TrimSpace(string(*validations.Format)) != "" {
		labels = append(labels, "format "+strings.TrimSpace(string(*validations.Format)))
	}
	if validations.Min != nil {
		labels = append(labels, "min "+pipelineSpecUnionJSONLabel(*validations.Min))
	}
	if validations.Max != nil {
		labels = append(labels, "max "+pipelineSpecUnionJSONLabel(*validations.Max))
	}
	if validations.MinLength != nil {
		labels = append(labels, "min_length "+strconv.Itoa(*validations.MinLength))
	}
	if validations.MaxLength != nil {
		labels = append(labels, "max_length "+strconv.Itoa(*validations.MaxLength))
	}
	if validations.Unique != nil && *validations.Unique {
		labels = append(labels, "unique")
	}
	if validations.Enum != nil {
		labels = append(labels, "enum "+pipelineSpecEnumLabel(*validations.Enum))
	}
	return labels
}

// PipelineSpecStepKindBadgeClass maps canonical step groups to compact badge treatments.
func PipelineSpecStepKindBadgeClass(kind string) string {
	switch strings.TrimSpace(kind) {
	case pipelineSpecStepKindSource:
		return badgeInfoOutline
	case pipelineSpecStepKindTransform:
		return badgePrimaryOutline
	case pipelineSpecStepKindSink:
		return badgeSuccessOutline
	default:
		return badgeGhostOutline
	}
}

// PipelineSpecStepDisplayName returns a human label without duplicating a slug-only name.
func PipelineSpecStepDisplayName(step PipelineSpecStepProjection) string {
	if strings.TrimSpace(step.Name) != "" {
		return strings.TrimSpace(step.Name)
	}
	return pipelineSpecDisplayValue(step.Slug)
}

// PipelineSpecStepSlugDisplay returns the slug only when it adds information beside the name.
func PipelineSpecStepSlugDisplay(step PipelineSpecStepProjection) string {
	name := strings.TrimSpace(step.Name)
	slug := strings.TrimSpace(step.Slug)
	if name == "" || slug == "" || strings.EqualFold(name, slug) {
		return ""
	}
	return slug
}

// PipelineSpecInputLabel renders named transform inputs and sink payload inputs.
func PipelineSpecInputLabel(input PipelineSpecInputProjection) string {
	if strings.TrimSpace(input.Name) == "" {
		return pipelineSpecDisplayValue(input.FromStep)
	}
	return strings.TrimSpace(input.Name) + " <- " + pipelineSpecDisplayValue(input.FromStep)
}

// pipelineSpecQueryStepProjection maps one query/source step into view data.
func pipelineSpecQueryStepProjection(specPosition int, step api.PipelineQueryStep) PipelineSpecStepProjection {
	operationRef := pipelineSpecSlugPtr(step.Query.OperationRef)
	operationInline := pipelineSpecOperationInline(step.Query.OperationInline)
	return PipelineSpecStepProjection{
		SpecPosition:    specPosition,
		Kind:            pipelineSpecStepKindSource,
		KindLabel:       "Source",
		Slug:            step.Slug,
		Name:            pipelineSpecNamePtr(step.Name),
		DataSourceSlug:  step.Query.DataSourceRef,
		OperationType:   string(step.Query.OperationType),
		OperationRef:    operationRef,
		OperationMode:   pipelineSpecOperationMode(operationRef, operationInline),
		OperationLabel:  pipelineSpecOperationLabel(operationRef, operationInline),
		OperationInline: operationInline,
		Parameters:      pipelineSpecParameters(step.Query.Parameters),
		ContractSlots: []PipelineSpecContractSlot{
			{Label: "Output contract", Contract: step.Query.OutputContract, Ref: step.Query.OutputContractRef},
		},
	}
}

// pipelineSpecTransformStepProjection maps one SQL transform step into view data.
func pipelineSpecTransformStepProjection(specPosition int, step api.PipelineTransformStep) PipelineSpecStepProjection {
	slots := make([]PipelineSpecContractSlot, 0, 2)
	if step.Transform.InputContracts == nil {
		slots = append(slots, PipelineSpecContractSlot{Label: "Input contract", Contract: step.Transform.InputContract, Ref: step.Transform.InputContractRef})
	}
	slots = append(slots, PipelineSpecContractSlot{Label: "Output contract", Contract: step.Transform.OutputContract, Ref: step.Transform.OutputContractRef})
	if step.Transform.InputContracts != nil {
		names := make([]string, 0, len(*step.Transform.InputContracts))
		for name := range *step.Transform.InputContracts {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			attachment := (*step.Transform.InputContracts)[name]
			slot := PipelineSpecContractSlot{Label: "Input requirement: " + name}
			if contract := attachment.Contract; contract != nil {
				slot.Contract = &api.PipelineSchemaContract{Name: contract.Name, Fields: contract.Fields}
				if contract.Representation != nil {
					slot.Contract.Representation = new(api.PipelineSchemaContractRepresentation(*contract.Representation))
				}
				if contract.RootShape != nil {
					slot.Contract.RootShape = new(api.PipelineSchemaContractRootShape(*contract.RootShape))
				}
			}
			if ref := attachment.ContractRef; ref != nil {
				slot.Ref = &api.PipelineODCSContractRef{ContractSlug: ref.ContractSlug, RevisionSlug: ref.RevisionSlug, SchemaObjectId: ref.SchemaObjectId}
				if ref.Representation != nil {
					slot.Ref.Representation = new(api.PipelineODCSContractRefRepresentation(*ref.Representation))
				}
				if ref.RootShape != nil {
					slot.Ref.RootShape = new(api.PipelineODCSContractRefRootShape(*ref.RootShape))
				}
			}
			slots = append(slots, slot)
		}
	}
	return PipelineSpecStepProjection{
		SpecPosition:  specPosition,
		Kind:          pipelineSpecStepKindTransform,
		KindLabel:     "Transform",
		Slug:          step.Slug,
		Name:          pipelineSpecNamePtr(step.Name),
		SQL:           strings.TrimSpace(step.Transform.Sql),
		OutputKind:    string(step.Transform.OutputKind),
		Inputs:        pipelineSpecTransformInputs(step.Transform.Inputs),
		ContractSlots: slots,
	}
}

// pipelineSpecSinkStepProjection maps one terminal sink step into view data.
func pipelineSpecSinkStepProjection(specPosition int, step api.PipelineSinkStep) PipelineSpecStepProjection {
	operationRef := pipelineSpecSlugPtr(step.Sink.OperationRef)
	operationInline := pipelineSpecOperationInline(step.Sink.OperationInline)
	return PipelineSpecStepProjection{
		SpecPosition:     specPosition,
		Kind:             pipelineSpecStepKindSink,
		KindLabel:        "Sink",
		Slug:             step.Slug,
		Name:             pipelineSpecNamePtr(step.Name),
		DataSourceSlug:   step.Sink.DataSourceRef,
		OperationType:    string(step.Sink.OperationType),
		OperationRef:     operationRef,
		OperationMode:    pipelineSpecOperationMode(operationRef, operationInline),
		OperationLabel:   pipelineSpecOperationLabel(operationRef, operationInline),
		OperationInline:  operationInline,
		IdempotencyClass: pipelineSpecSinkIdempotencyClass(step.Sink.IdempotencyClass),
		Inputs: []PipelineSpecInputProjection{
			{Name: "payload", FromStep: step.Sink.Input},
		},
		ContractSlots: []PipelineSpecContractSlot{
			{Label: "Input contract", Contract: step.Sink.InputContract, Ref: step.Sink.InputContractRef},
		},
	}
}

// pipelineSpecTransformInputs returns transform input references sorted by relation name.
func pipelineSpecTransformInputs(inputs map[string]api.Slug) []PipelineSpecInputProjection {
	if len(inputs) == 0 {
		return nil
	}
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]PipelineSpecInputProjection, 0, len(names))
	for _, name := range names {
		out = append(out, PipelineSpecInputProjection{Name: name, FromStep: inputs[name]})
	}
	return out
}

// pipelineSpecSinkIdempotencyClass normalizes required sink replay metadata for display.
func pipelineSpecSinkIdempotencyClass(value api.PipelineSinkSpecIdempotencyClass) string {
	return strings.TrimSpace(string(value))
}

// pipelineSpecOperationMode reports whether an operation uses a saved ref or inline payload.
func pipelineSpecOperationMode(operationRef, operationInline string) string {
	switch {
	case strings.TrimSpace(operationRef) != "":
		return "operation ref"
	case strings.TrimSpace(operationInline) != "":
		return pipelineSpecOperationModeInline
	default:
		return "-"
	}
}

// pipelineSpecOperationLabel returns the most useful operator-facing operation identifier.
func pipelineSpecOperationLabel(operationRef, operationInline string) string {
	if strings.TrimSpace(operationRef) != "" {
		return strings.TrimSpace(operationRef)
	}
	if strings.TrimSpace(operationInline) != "" {
		return strings.TrimSpace(operationInline)
	}
	return "-"
}

// pipelineSpecOperationInline renders inline source and sink operations as stable JSON.
func pipelineSpecOperationInline(inline *map[string]interface{}) string {
	if inline == nil || len(*inline) == 0 {
		return ""
	}
	data, err := json.MarshalIndent(*inline, "", "  ")
	if err != nil {
		return pipelineSpecOperationModeInline
	}
	text := strings.TrimSpace(string(data))
	if text == "" || text == "{}" || text == "null" {
		return ""
	}
	return text
}

// pipelineSpecNamePtr converts an optional generated name pointer into a trimmed string.
func pipelineSpecNamePtr(value *api.Name) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// pipelineSpecSlugPtr converts an optional generated slug pointer into a trimmed string.
func pipelineSpecSlugPtr(value *api.Slug) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// pipelineSpecDisplayValue normalizes blank display values to a stable placeholder.
func pipelineSpecDisplayValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "-"
	}
	return trimmed
}

// pipelineSpecEnumLabel renders enum validation literals without exposing raw JSON syntax first.
func pipelineSpecEnumLabel(values []api.PipelineSchemaContractValidationLiteral) string {
	if len(values) == 0 {
		return "0 values"
	}
	labels := make([]string, 0, len(values))
	for i, value := range values {
		if i >= 3 {
			labels = append(labels, "+"+strconv.Itoa(len(values)-i))
			break
		}
		labels = append(labels, pipelineSpecUnionJSONLabel(value))
	}
	return strings.Join(labels, ", ")
}

// pipelineSpecUnionJSONLabel renders generated union values as short scalar labels.
func pipelineSpecUnionJSONLabel(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "?"
	}
	text := strings.TrimSpace(string(data))
	if unquoted, err := strconv.Unquote(text); err == nil {
		return unquoted
	}
	return text
}

// pipelineSpecDecodeIssue returns a safe message for malformed generated step unions.
func pipelineSpecDecodeIssue(index int) string {
	return "step " + strconv.Itoa(index+1) + " could not be decoded"
}

func pipelineSpecParameters(parameters *map[string]interface{}) string {
	if parameters == nil {
		return ""
	}
	raw, err := json.MarshalIndent(parameters, "", "  ")
	if err != nil {
		return "Parameters unavailable"
	}
	return string(raw)
}
