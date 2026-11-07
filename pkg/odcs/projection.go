package odcs

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/decimal"
	internalmodel "github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
)

const (
	ProjectionCompilerID = "braidplane/odcs-structure/v1"
	MaxProjectionInputs  = 64
)

// ProjectionMapping is a connector-owned capability snapshot, not an inferred
// provider conversion. Its identity and contents participate in the plan digest.
type ProjectionMapping struct {
	ID             string
	Representation model.ContractRepresentation
	RootShape      model.ContractRootShape
	Types          map[payload.StructuralKind]bool
	Physical       map[string]PhysicalTypeMapping
}

type PhysicalTypeMapping struct {
	LogicalType            string
	Kind                   payload.StructuralKind
	Bits, Precision, Scale int
}

// ProjectionInput must already be authorized in one project by the caller.
// No resolver, catalog, provider or credentials capability enters this compiler.
type ProjectionInput struct {
	Contract, Revision, ObjectID, Digest string
	Document                             json.RawMessage
	Mapping                              ProjectionMapping
	Variables                            map[string]ResolutionValue
}

type ProjectionRequest struct {
	Inputs        []ProjectionInput
	Relationships []RelationshipSelection
}

type ProjectionDiagnostic struct {
	Input int
	Phase string
	Diagnostic
}

type ProjectionDefault struct {
	Path, Source string
	Value        any
}

type ProjectionDeclaration struct {
	Path, LogicalType, PhysicalType string
	Options                         map[string]any
}

type ProjectedInput struct {
	Contract, Revision, ObjectID, Digest, ResolvedDigest string
	ObjectName                                           string
	Mapping                                              ProjectionMapping
	Structure                                            *payload.Structure
	Defaults                                             []ProjectionDefault
	Resolutions                                          []ProjectionResolution
	Declarations                                         []ProjectionDeclaration
	Coverage                                             []internalmodel.ODCSDeclarationCoverage
}

type ProjectionPlan struct {
	CompilerID    string
	Inputs        []ProjectedInput
	Relationships []ProjectedRelationship
}

type ProjectionResult struct {
	Plan        *ProjectionPlan
	Digest      string
	Coverage    [][]internalmodel.ODCSDeclarationCoverage
	Diagnostics []ProjectionDiagnostic
}

type projectionCompiler struct {
	input                 *ProjectionInput
	output                *ProjectedInput
	result                *ProjectionResult
	index                 int
	nodes, size           int
	mapped                map[string]bool
	relations             map[string]projectionRelationship
	selectedRelationships map[string]bool
	work                  *int
}

// CompileProjection is hermetic. Executable admission binds selected structures
// to exact ports in versioned immutable contract plans.
// Any selected failure returns coverage/diagnostics but no partial plan/digest.
func CompileProjection(request ProjectionRequest) (*ProjectionResult, error) {
	result := &ProjectionResult{}
	plan := &ProjectionPlan{CompilerID: ProjectionCompilerID}
	if len(request.Inputs) == 0 || len(request.Inputs) > MaxProjectionInputs || len(request.Relationships) > MaxCoverageEntries {
		result.Diagnostics = append(result.Diagnostics, projectionDiagnostic(-1, "budget", "projection_limit", "$"))
		return result, nil
	}
	size := 0
	for _, input := range request.Inputs {
		size += len(input.Document) + len(input.Contract) + len(input.Revision) + len(input.ObjectID) + len(input.Digest) + len(input.Mapping.ID)
		if len(input.Variables) > MaxCoverageEntries || len(input.Mapping.Physical) > MaxCoverageEntries || len(input.Mapping.Types) > len(payload.StructuralKindValues()) {
			size = MaxProjectionBytes + 1
			break
		}
		for name, value := range input.Variables {
			size += len(name) + len(value.Value) + len(value.Revision) + 128
		}
		for name, mapping := range input.Mapping.Physical {
			size += len(name) + len(mapping.LogicalType) + 128
		}
	}
	for _, selection := range request.Relationships {
		size += len(selection.Path) + 64
	}
	if size > MaxProjectionBytes {
		result.Diagnostics = append(result.Diagnostics, projectionDiagnostic(-1, "budget", "projection_limit", "$"))
		return result, nil
	}
	for i, input := range request.Inputs {
		for kind := range input.Mapping.Types {
			if !kind.IsAStructuralKind() {
				result.Diagnostics = append(result.Diagnostics, projectionDiagnostic(i, "projection", "capability_unsupported", "$"))
				return result, nil
			}
		}
	}
	// Bound all supplied material before allocating decoded documents or trees.
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > MaxProjectionBytes {
		return projectionOverflow(result)
	}
	vocabulary, err := coverageVocabulary()
	if err != nil {
		return nil, err
	}
	compilers := make([]*projectionCompiler, len(request.Inputs))
	work := 0
	outputBytes := 0
	for i := range request.Inputs {
		input := &request.Inputs[i]
		output := ProjectedInput{Contract: input.Contract, Revision: input.Revision, ObjectID: input.ObjectID, Digest: input.Digest, Mapping: input.Mapping}
		output.Mapping.Types, output.Mapping.Physical = maps.Clone(input.Mapping.Types), maps.Clone(input.Mapping.Physical)
		c := &projectionCompiler{input: input, output: &output, result: result, index: i, mapped: map[string]bool{}, relations: map[string]projectionRelationship{}}
		c.work = &work
		c.selectedRelationships = map[string]bool{}
		for _, selection := range request.Relationships {
			if selection.Input == i {
				c.selectedRelationships[selection.Path] = true
			}
		}
		compilers[i] = c
		c.compile(vocabulary)
		encoded, encodeErr := json.Marshal(output)
		outputBytes += len(encoded)
		if encodeErr != nil || outputBytes > MaxProjectionBytes {
			return projectionOverflow(result)
		}
		plan.Inputs = append(plan.Inputs, output)
	}
	compileRelationships(request.Relationships, compilers, plan)
	entries := 0
	for i, c := range compilers {
		c.finishCoverage()
		plan.Inputs[i] = *c.output
		result.Coverage = append(result.Coverage, c.output.Coverage)
		entries += len(c.output.Coverage)
	}
	if entries > MaxCoverageEntries {
		result.Coverage = nil
		result.Diagnostics = append(result.Diagnostics, projectionDiagnostic(-1, "budget", "coverage_limit_exceeded", "$"))
	}
	slices.SortFunc(result.Diagnostics, func(a, b ProjectionDiagnostic) int {
		if a.Input != b.Input {
			return a.Input - b.Input
		}
		if n := strings.Compare(a.Path, b.Path); n != 0 {
			return n
		}
		return strings.Compare(a.Code, b.Code)
	})
	result.Diagnostics = slices.Compact(result.Diagnostics)
	if len(result.Diagnostics) != 0 {
		return result, nil
	}
	raw, err = json.Marshal(plan)
	if err != nil || len(raw) > MaxProjectionBytes {
		return projectionOverflow(result)
	}
	result.Plan, result.Digest = plan, projectionDigest(raw)
	return result, nil
}

func projectionOverflow(result *ProjectionResult) (*ProjectionResult, error) {
	result.Coverage = nil
	result.Diagnostics = append(result.Diagnostics, projectionDiagnostic(-1, "budget", "projection_limit", "$"))
	return result, nil
}

func projectionDigest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }

func projectionDiagnostic(input int, phase, code, path string) ProjectionDiagnostic {
	return ProjectionDiagnostic{Input: input, Phase: phase, Diagnostic: Diagnostic{
		Code: code, Path: path,
		Message: "Supply an explicit supported declaration and resolution within compiler budgets; submitted values are omitted.",
	}}
}

func (c *projectionCompiler) fail(code, path, phase string) {
	if len(c.result.Diagnostics) < MaxCoverageEntries {
		c.result.Diagnostics = append(c.result.Diagnostics, projectionDiagnostic(c.index, phase, code, path))
	}
}

func (c *projectionCompiler) spend(work int, path string) bool {
	if work > payload.DefaultStructuralLimits().Work-*c.work {
		c.fail("projection_limit", path, "budget")
		return false
	}
	*c.work += work
	return true
}

func (c *projectionCompiler) compile(vocabulary map[string]bool) {
	input := c.input
	var document map[string]any
	if len(input.Document) > MaxDocumentBytes || jsonvalue.Unmarshal(input.Document, &document) != nil || document == nil {
		c.fail("document_invalid", "$", "admission")
		return
	}
	id, _ := document["id"].(string)
	record, err := Record(input.Document, id)
	if err != nil {
		c.fail("document_invalid", "$", "admission")
		return
	}
	if input.Contract == "" || input.Revision == "" || input.Revision == "latest" || input.Digest != record.Digest || input.Mapping.ID == "" {
		c.fail("identity_invalid", "$", "resolution")
	}
	coverage := &ConversionResult{SchemaObjectID: input.ObjectID}
	coverage.coverage(document, vocabulary, true)
	c.output.Coverage = coverage.Coverage
	if !coverage.CoverageComplete {
		c.fail("coverage_limit_exceeded", "$", "budget")
		return
	}
	var object map[string]any
	path := "$.schema"
	for i, entry := range document["schema"].([]any) {
		candidate := entry.(map[string]any)
		if input.ObjectID != "" && !strings.Contains(input.ObjectID, "${") && candidate["id"] == input.ObjectID {
			object, path = candidate, fmt.Sprintf("$.schema[%d]", i)
		}
	}
	if object == nil {
		c.fail("object_not_found", path, "resolution")
		return
	}
	// Coverage was built from the admitted original; resolve only the copied tree.
	c.resolve(object, path)
	c.output.ObjectName, _ = object["name"].(string)
	c.collectRelationships(object, path, nil)
	resolved, err := json.Marshal(document)
	if err != nil || len(resolved) > MaxDocumentBytes {
		c.fail("projection_limit", path, "budget")
		return
	}
	// Revalidate the complete document, including normative constraints, once.
	if _, err := Record(resolved, id); err != nil {
		c.fail("resolution_invalid", path, "resolution")
		return
	}
	c.output.ResolvedDigest = projectionDigest(resolved)
	root := c.node(object, path, 0, true)
	s := &payload.Structure{
		Contract: input.Contract, Revision: input.Revision, Object: input.ObjectID,
		Representation: input.Mapping.Representation, RootShape: input.Mapping.RootShape, Root: root,
	}
	if err := payload.ValidateStructure(s, payload.DefaultStructuralLimits()); err != nil {
		c.fail("mapping_invalid", path, "projection")
	}
	if input.Mapping.Representation == model.ContractRepresentationJSON &&
		(input.Mapping.RootShape == model.ContractRootShapeObject && root != nil && root.Kind != payload.StructuralObject ||
			input.Mapping.RootShape == model.ContractRootShapeArray && root != nil && root.Kind != payload.StructuralArray) {
		c.fail("representation_conflict", path, "projection")
	}
	c.output.Structure = s
}

func (c *projectionCompiler) defaultValue(path string, options map[string]any, key string, value any) any {
	if supplied, exists := options[key]; exists {
		return supplied
	}
	c.output.Defaults = append(c.output.Defaults, ProjectionDefault{
		Path:   path + ".logicalTypeOptions." + key,
		Source: "ODCS/v3.2.0@f0bdad95346905d500be5ef4b2c2d9b1d95223b7", Value: value,
	})
	return value
}

func (c *projectionCompiler) node(p map[string]any, path string, depth int, root bool) *payload.StructuralType {
	if !c.spend(1, path) {
		return nil
	}
	c.nodes++
	if depth >= payload.DefaultStructuralLimits().Depth || c.nodes > payload.DefaultStructuralLimits().Nodes {
		c.fail("projection_limit", path, "budget")
		return nil
	}
	if p == nil {
		c.fail("shape_ambiguous", path, "projection")
		return nil
	}
	logical, _ := p["logicalType"].(string)
	if root && logical == "" {
		logical = "object"
	}
	physical, _ := p["physicalType"].(string)
	options, _ := p["logicalTypeOptions"].(map[string]any)
	c.output.Declarations = append(c.output.Declarations, ProjectionDeclaration{Path: path, LogicalType: logical, PhysicalType: physical, Options: options})
	required, _ := p["required"].(bool)
	t := &payload.StructuralType{Nullable: !required && !root}
	allowed := map[string]bool{}
	switch logical {
	case "object":
		t.Kind, t.Additional = payload.StructuralObject, payload.StructuralOpen
		children, _ := p["properties"].([]any)
		if len(children) > payload.DefaultStructuralLimits().Nodes-c.nodes {
			c.fail("projection_limit", path+".properties", "budget")
			return nil
		}
		positions := map[string]int{}
		for i, child := range children {
			property := child.(map[string]any)
			name, _ := property["name"].(string)
			id, _ := property["id"].(string)
			required, _ := property["required"].(bool)
			positions[name] = i
			t.Properties = append(t.Properties, payload.StructuralProperty{
				Name: name, ID: id, Required: required,
				Type: c.node(property, fmt.Sprintf("%s.properties[%d]", path, i), depth+1, false),
			})
		}
		if _, exists := options["minProperties"]; !exists {
			c.defaultValue(path, options, "minProperties", json.Number("0"))
		} else if options["minProperties"] == json.Number("0") {
			allowed["minProperties"] = true
		}
		// An explicit required-name list is an obligation on the same children.
		if names, ok := options["required"].([]any); ok {
			allowed["required"] = true
			for _, name := range names {
				index, found := positions[name.(string)]
				if found {
					t.Properties[index].Required = true
				}
				if !found {
					c.fail("mapping_invalid", path+".logicalTypeOptions.required", "projection")
				}
			}
		}
	case "array":
		t.Kind = payload.StructuralArray
		items, _ := p["items"].(map[string]any)
		t.Items = c.node(items, path+".items", depth+1, false)
		allowed["minItems"], allowed["maxItems"], allowed["uniqueItems"] = true, true, true
		t.MinLength, t.MaxLength = projectionInt(options["minItems"]), projectionInt(options["maxItems"])
		c.defaultValue(path, options, "minItems", json.Number("0"))
		t.Unique, _ = c.defaultValue(path, options, "uniqueItems", false).(bool)
	case "map":
		t.Kind = payload.StructuralMap
		mapping, _ := p["map"].(map[string]any)
		key, _ := mapping["key"].(map[string]any)
		value, _ := mapping["value"].(map[string]any)
		t.Keys = c.node(key, path+".map.key", depth+1, false)
		if t.Keys != nil {
			t.Keys.Nullable = false
		}
		t.Values = c.node(value, path+".map.value", depth+1, false)
	case "vector":
		t.Kind = payload.StructuralVector
		allowed["dimensions"], allowed["elementType"] = true, true
		if dimension := projectionInt(options["dimensions"]); dimension != nil {
			t.Dimension = *dimension
		}
		element := c.defaultValue(path, options, "elementType", "float32")
		t.Items = &payload.StructuralType{Kind: payload.StructuralNumber, Bits: 32}
		if element == "float64" {
			t.Items.Bits = 64
		} else if element != "float32" {
			c.fail("type_unsupported", path+".logicalTypeOptions.elementType", "projection")
		}
		// Similarity/model metadata is advisory; normalization is an obligation.
		for _, key := range []string{"distanceMetric", "embeddingModel", "embeddingModelVersion"} {
			allowed[key] = true
		}
		allowed["normalized"] = c.defaultValue(path, options, "normalized", false) == false
	case "integer", "number":
		allowed["format"], allowed["minimum"], allowed["maximum"] = true, true, true
		if logical == "integer" {
			t.Kind = payload.StructuralInteger
			format := c.defaultValue(path, options, "format", "i32")
			t.Bits = map[any]int{"i8": 8, "i16": 16, "i32": 32, "i64": 64}[format]
		} else {
			t.Kind = payload.StructuralNumber
			// The vendored number-format default is i32, outside its f32/f64
			// vocabulary. Require an explicit format or physical mapping.
			if format, exists := options["format"]; exists {
				t.Bits = map[any]int{"f32": 32, "f64": 64}[format]
			} else if mapping, exists := c.input.Mapping.Physical[physical]; exists {
				t.Bits = mapping.Bits
			} else {
				c.fail("mapping_ambiguous", path+".logicalTypeOptions.format", "projection")
			}
		}
		for _, key := range []string{"minimum", "maximum"} {
			if number, exists := options[key]; exists {
				bound, err := decimal.Parse(fmt.Sprint(number))
				switch {
				case err != nil:
					c.fail("mapping_invalid", path+".logicalTypeOptions."+key, "projection")
				case key == "minimum":
					t.Min = &bound
				default:
					t.Max = &bound
				}
			}
		}
	case "string":
		t.Kind = payload.StructuralString
		allowed["format"], allowed["minLength"], allowed["maxLength"] = true, true, true
		t.Format, _ = options["format"].(string)
		t.MinLength, t.MaxLength = projectionInt(options["minLength"]), projectionInt(options["maxLength"])
	case "boolean":
		t.Kind = payload.StructuralBoolean
	case "date", "time", "timestamp":
		c.temporal(t, logical, options, path, allowed)
	default:
		c.fail("type_unsupported", path+".logicalType", "projection")
	}
	if physical != "" {
		mapping, ok := c.input.Mapping.Physical[physical]
		exactDecimal := logical == "number" && mapping.Kind == payload.StructuralDecimal && options["format"] == nil
		switch {
		case !ok || mapping.LogicalType != logical || mapping.Kind != t.Kind && !exactDecimal:
			c.fail("physical_mapping_conflict", path+".physicalType", "projection")
		case mapping.Kind == payload.StructuralDecimal:
			t.Kind, t.Bits, t.Precision, t.Scale = mapping.Kind, 0, mapping.Precision, mapping.Scale
		case mapping.Bits != 0 && mapping.Bits != t.Bits:
			c.fail("physical_mapping_conflict", path+".physicalType", "projection")
		}
	}
	if !c.input.Mapping.Types[t.Kind] || t.Kind == payload.StructuralVector && !c.input.Mapping.Types[payload.StructuralNumber] {
		c.fail("capability_unsupported", path+".logicalType", "projection")
	}
	for _, key := range sortedKeys(options) {
		if slices.Contains([]string{"minLength", "maxLength", "minItems", "maxItems", "dimensions"}, key) && projectionInt(options[key]) == nil {
			c.fail("mapping_unsupported", path+".logicalTypeOptions."+key, "projection")
		}
		if !allowed[key] {
			c.fail("mapping_unsupported", path+".logicalTypeOptions."+safeSegment(key), "projection")
		} else if key != "distanceMetric" && key != "embeddingModel" && key != "embeddingModelVersion" {
			c.mapped[path+".logicalTypeOptions."+key] = true
		}
	}
	for _, key := range []string{"properties", "items", "map"} {
		_, exists := p[key]
		expected := map[string]string{"properties": "object", "items": "array", "map": "map"}[key]
		if exists && logical != expected {
			c.fail("mapping_unsupported", path+"."+key, "projection")
		}
	}
	if entries, ok := p["enum"].([]any); ok {
		for i, entry := range entries {
			t.Enum = append(t.Enum, entry.(map[string]any)["value"])
			c.mapped[fmt.Sprintf("%s.enum[%d].value", path, i)] = true
		}
	}
	c.mapped[path] = true
	for _, key := range []string{"name", "id", "logicalType", "physicalType", "required", "logicalTypeOptions", "enum"} {
		c.mapped[path+"."+key] = true
	}
	for _, key := range []string{"properties", "items", "map"} {
		if _, exists := p[key]; exists {
			c.mapped[path+"."+key] = true
		}
	}
	return t
}

func (c *projectionCompiler) temporal(t *payload.StructuralType, logical string, options map[string]any, path string, allowed map[string]bool) {
	allowed["format"] = true
	format := c.defaultValue(path, options, "format", "YYYY-MM-DDTHH:mm:ss.SSSZ")
	t.Format, _ = format.(string)
	switch {
	case logical == "date" && format == "yyyy-MM-dd":
		t.Kind = payload.StructuralDate
	case logical == "time" && format == "HH:mm:ss" && options["timezone"] != true:
		t.Kind = payload.StructuralLocalTime
	case logical == "timestamp" && format == "yyyy-MM-ddTHH:mm:ss" && options["timezone"] == false:
		t.Kind = payload.StructuralLocalDatetime
	case logical == "timestamp" && (format == "YYYY-MM-DDTHH:mm:ss.SSSZ" || format == "yyyy-MM-ddTHH:mm:ssZ") && options["timezone"] != false:
		t.Kind = payload.StructuralInstant
	default:
		c.fail("temporal_mapping_unsupported", path+".logicalTypeOptions", "projection")
	}
	if logical != "date" {
		allowed["timezone"], allowed["defaultTimezone"] = true, true
		zone := c.defaultValue(path, options, "defaultTimezone", "Etc/UTC")
		if zone != "Etc/UTC" && zone != "UTC" {
			c.fail("temporal_mapping_unsupported", path+".logicalTypeOptions.defaultTimezone", "projection")
		}
	}
}

func projectionInt(value any) *int {
	number, ok := portInteger(value)
	if !ok {
		return nil
	}
	integer, err := strconv.Atoi(number.String())
	if err != nil {
		return nil
	}
	return &integer
}

func (c *projectionCompiler) finishCoverage() {
	failed := len(c.result.Diagnostics) != 0
	blocking := map[string]string{}
	for _, diagnostic := range c.result.Diagnostics {
		if diagnostic.Input == c.index {
			blocking[diagnostic.Path] = diagnostic.Code
		}
	}
	for i := range c.output.Coverage {
		entry := &c.output.Coverage[i]
		if c.mapped[entry.Path] {
			entry.State, entry.Reason, entry.RuntimeScope = "validated", "compiled_structural_constraint", ""
			if failed {
				entry.State, entry.Reason = "unresolved", "projection_unavailable"
			}
		}
		if code, exists := blocking[entry.Path]; exists {
			entry.State, entry.Reason, entry.RuntimeScope = "unsupported", code, ""
		}
	}
	slices.SortFunc(c.output.Defaults, func(a, b ProjectionDefault) int { return strings.Compare(a.Path, b.Path) })
}
