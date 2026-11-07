package odcs

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"sync"

	internalmodel "github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const PinnedVersion = "v3.2.0"

const CompilerID = "braidplane/odcs-flat-port/v2"

const DecimalCompilerID = "braidplane/odcs-flat-port-decimal/v1"

//go:embed schema/odcs-v3.2.0.json
var pinnedSchema []byte

// Diagnostic never includes submitted values or arbitrary property names.
type Diagnostic struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type PropertyIdentity struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

type ConversionResult struct {
	CompilerID         string                                  `json:"compiler_id"`
	Coverage           []internalmodel.ODCSDeclarationCoverage `json:"coverage,omitempty"`
	CoverageComplete   bool                                    `json:"coverage_complete"`
	Properties         []PropertyIdentity                      `json:"properties,omitempty"`
	ODCSVersion        string                                  `json:"odcs_version"`
	Digest             string                                  `json:"digest,omitempty"`
	SchemaObjectID     string                                  `json:"schema_object_id"`
	DocumentValid      bool                                    `json:"document_valid"`
	Shape              *payload.Contract                       `json:"shape,omitempty"`
	Diagnostics        []Diagnostic                            `json:"diagnostics,omitempty"`
	decimalMappings    map[string]decimalTypeMapping
	mappedPhysical     map[string]bool
	decimalMappingPath string
}

var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	var document any
	if err := jsonvalue.Unmarshal(pinnedSchema, &document); err != nil {
		return nil, err
	}
	correctNormativeSchema(document.(map[string]any))
	applyNormativeSchemaRules(document)
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	// No URL loader: validation and compilation cannot fetch remote resources.
	if err := compiler.AddResource("urn:braidplane:odcs:v3.2.0", document); err != nil {
		return nil, xerror.Unexpected().With(err).Detail("register pinned ODCS schema")
	}
	schema, err := compiler.Compile("urn:braidplane:odcs:v3.2.0")
	if err != nil {
		return nil, xerror.Unexpected().With(err).Detail("compile pinned ODCS schema")
	}
	return schema, nil
})

// Convert validates the entire document and converts exactly one stable object ID.
// Unsupported selected structure returns diagnostics without a partial shape.
// Preserved metadata and unevaluated checks are reported separately as coverage.
func Convert(raw json.RawMessage, objectID string) (*ConversionResult, error) {
	result := &ConversionResult{CompilerID: CompilerID, ODCSVersion: PinnedVersion, SchemaObjectID: objectID, mappedPhysical: map[string]bool{}}
	if len(raw) > MaxDocumentBytes {
		result.add("document_invalid", "$", "Reduce the document to at most 1 MiB.")
		return result, nil
	}
	if err := checkJSON(raw, 0); err != nil {
		result.add("document_invalid", "$", xerror.PublicDetail(err))
		return result, nil
	}
	var document map[string]any
	if err := jsonvalue.Unmarshal(raw, &document); err != nil || document == nil {
		result.add("document_invalid", "$", "Supply one JSON object.")
		return result, nil
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return nil, xerror.Unexpected().With(err).Detail("canonicalize ODCS document")
	}
	// Legacy recorded revisions may fail today's admission but still need exact Proof identity.
	result.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))
	if document["apiVersion"] != PinnedVersion {
		result.add("version_unsupported", "$.apiVersion", "Use the pinned ODCS v3.2.0 version; no version upgrade is inferred.")
	}
	schema, err := compiledSchema()
	if err != nil {
		return nil, xerror.Unexpected().With(err).Detail("compile pinned ODCS schema")
	}
	if err := schema.Validate(document); err != nil {
		var invalid *jsonschema.ValidationError
		if !xerror.As(err, &invalid) {
			return nil, xerror.Unexpected().With(err).Detail("validate pinned ODCS schema")
		}
		result.schemaDiagnostics(invalid, document)
	}
	id, _ := document["id"].(string)
	revision, err := Record(raw, id)
	if err != nil {
		if len(result.Diagnostics) == 0 {
			result.add("document_invalid", "$", xerror.PublicDetail(err))
		}
		result.sortDiagnostics()
		return result, nil
	}
	result.Digest = revision.Digest
	if len(result.Diagnostics) > 0 {
		result.sortDiagnostics()
		return result, nil
	}
	result.DocumentValid = true
	vocabulary, err := coverageVocabulary()
	if err != nil {
		return nil, err
	}
	defer func() { result.coverage(document, vocabulary, false) }()
	objects, _ := document["schema"].([]any)
	selected := -1
	for i, value := range objects {
		if objectID != "" && !variableToken.MatchString(objectID) && value.(map[string]any)["id"] == objectID {
			selected = i
		}
	}
	if selected < 0 {
		result.add("object_not_found", "$.schema", "Select an explicit schema-object ID present in this revision; names and latest are not references.")
		return result, nil
	}
	if !result.readDecimalMappings(document) {
		result.sortDiagnostics()
		return result, nil
	}
	object := objects[selected].(map[string]any)
	path := fmt.Sprintf("$.schema[%d]", selected)
	if containsVariable(object["logicalType"]) {
		result.add("variable_unresolved", path+".logicalType", "Resolve the selected object's logical type explicitly before compilation.")
	} else if logicalType, _ := object["logicalType"].(string); logicalType != "" && logicalType != "object" {
		result.add("type_unsupported", path+".logicalType", "Only object-shaped schema objects have an executable port mapping.")
	}
	properties, _ := object["properties"].([]any)
	if len(properties) == 0 {
		result.add("shape_ambiguous", path+".properties", "Declare at least one property to define the selected port shape.")
	}
	shape := &payload.Contract{Name: objectID}
	seen := map[string]bool{}
	seenIDs := map[string]bool{}
	for i, value := range properties {
		property := value.(map[string]any)
		propertyPath := fmt.Sprintf("%s.properties[%d]", path, i)
		id, _ := property["id"].(string)
		if id != "" && seenIDs[id] {
			result.add("shape_ambiguous", propertyPath+".id", "Use unique property IDs within the selected schema object.")
		}
		seenIDs[id] = true
		name, _ := property["name"].(string)
		result.Properties = append(result.Properties, PropertyIdentity{ID: id, Name: name})
		field := result.field(property, propertyPath)
		if seen[field.Name] {
			result.add("shape_ambiguous", propertyPath+".name", "Use unique property names within the selected schema object.")
		}
		seen[field.Name] = true
		shape.Fields = append(shape.Fields, field)
	}
	if len(result.Diagnostics) == 0 {
		result.Shape = shape
	}
	result.sortDiagnostics()
	return result, nil
}

func (r *ConversionResult) field(property map[string]any, path string) payload.ContractField {
	for _, key := range []string{"properties", "items", "map"} {
		if _, present := property[key]; present {
			r.add("mapping_unsupported", path+"."+key, "Nested structure has no complete executable port mapping; use an explicitly supported structural contract.")
		}
	}
	for _, key := range []string{"name", "logicalType", "logicalTypeOptions"} {
		if containsVariable(property[key]) {
			r.add("variable_unresolved", path+"."+key, "Resolve this structural declaration explicitly before compiling the selected port; no substitution is inferred.")
			return payload.ContractField{}
		}
	}
	name, _ := property["name"].(string)
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, ".[]") {
		r.add("shape_ambiguous", path+".name", "Use a nonempty literal property name without Braidplane path syntax (dots or brackets).")
	}
	required, _ := property["required"].(bool)
	description, _ := property["description"].(string)
	// ODCS required governs null values, while Braidplane also governs presence.
	field := payload.ContractField{Name: name, Required: required, Nullable: !required, Description: description}
	logicalType, _ := property["logicalType"].(string)
	physicalType, _ := property["physicalType"].(string)
	options, _ := property["logicalTypeOptions"].(map[string]any)
	mapping, mapped := r.decimalMappings[physicalType]
	if mapped {
		if logicalType != "number" || options["format"] != nil {
			r.add("decimal_mapping_conflict", path+".physicalType", "Decimal physical mappings require a number field without a format.")
		}
	}
	if len(r.decimalMappings) != 0 && logicalType == "number" && (physicalType == "" || !mapped) {
		r.add("decimal_mapping_missing", path+".physicalType", "Map each physical type used by the selected number fields explicitly.")
	}
	switch logicalType {
	case "string":
		field.Type = model.ContractFieldTypeValueString
	case "integer":
		field.Type = model.ContractFieldTypeValueInteger
	case "number":
		field.Type = model.ContractFieldTypeValueNumber
		if mapped {
			field.Type = model.ContractFieldTypeValueDecimal
			field.Precision, field.Scale = mapping.Precision, mapping.Scale
			r.mappedPhysical[path] = true
		}
	case "boolean":
		field.Type = model.ContractFieldTypeValueBoolean
	case "object":
		field.Type = model.ContractFieldTypeValueObject
	case "array":
		field.Type = model.ContractFieldTypeValueArray
	default:
		r.add("type_unsupported", path+".logicalType", "Declare string, integer, number, boolean, object, or array; temporal, map, vector, and missing logical types have no lossless port mapping.")
	}
	validations := &payload.ContractFieldValidations{}
	for _, key := range sortedKeys(options) {
		value := options[key]
		if logicalType == "integer" || key == "minLength" || key == "maxLength" {
			if integer, ok := portInteger(value); ok {
				value = integer
			}
		}
		switch {
		case logicalType == "string" && (key == "minLength" || key == "maxLength"):
			integer, err := strconv.Atoi(value.(json.Number).String())
			switch {
			case err != nil:
				r.add("mapping_unsupported", path+".logicalTypeOptions."+key, "Use a length bound representable as a Braidplane integer.")
			case key == "minLength":
				validations.MinLength = &integer
			default:
				validations.MaxLength = &integer
			}
		case logicalType == "string" && key == "format" && (value == "email" || value == "uuid"):
			validations.Format = value.(string)
		case (logicalType == "integer" || logicalType == "number") && key == "minimum":
			validations.Min = value
			if logicalType == "number" && !mapped && !exactPortNumber(value) {
				r.add("mapping_unsupported", path+".logicalTypeOptions.minimum", "Use a bound exactly representable by the port's float64 number type; rounding would change the constraint.")
			}
		case (logicalType == "integer" || logicalType == "number") && key == "maximum":
			validations.Max = value
			if logicalType == "number" && !mapped && !exactPortNumber(value) {
				r.add("mapping_unsupported", path+".logicalTypeOptions.maximum", "Use a bound exactly representable by the port's float64 number type; rounding would change the constraint.")
			}
		default:
			r.add("mapping_unsupported", path+".logicalTypeOptions."+safeSegment(key), "This structural constraint cannot be enforced by the port model; select a supported structural contract.")
		}
	}
	if entries, ok := property["enum"].([]any); ok {
		for i, entry := range entries {
			item := entry.(map[string]any)
			if containsVariable(item["value"]) {
				r.add("variable_unresolved", fmt.Sprintf("%s.enum[%d].value", path, i), "Resolve this enum literal explicitly before compiling the selected port; no substitution is inferred.")
				continue
			}
			value := item["value"]
			if logicalType == "integer" {
				if integer, ok := portInteger(value); ok {
					value = integer
				}
			}
			validations.Enum = append(validations.Enum, value)
			if logicalType == "number" && !mapped && !exactPortNumber(item["value"]) {
				r.add("mapping_unsupported", fmt.Sprintf("%s.enum[%d].value", path, i), "Use an enum literal exactly representable by the port's float64 number type; rounding would change the allowed values.")
			}
		}
	}
	if len(options) > 0 || validations.Enum != nil {
		field.Validations = validations
		// Reuse the same declaration validation the executable pipeline consumes.
		if err := payload.ValidateContractFieldDeclarations(&payload.Contract{Fields: []payload.ContractField{field}}); err != nil {
			r.add("mapping_invalid", path, "Use type-compatible enum values and consistent supported bounds; null enum literals are not supported.")
		}
	}
	if mapped {
		for _, value := range append(append([]any{}, validations.Enum...), validations.Min, validations.Max) {
			if value == nil {
				continue
			}
			if _, err := payload.DecimalFieldValue(value, field); err != nil {
				r.add("mapping_invalid", path, "Use exact decimal bounds and enum values that fit the declared precision and scale.")
				break
			}
		}
	}
	return field
}

func portInteger(value any) (json.Number, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return "", false
	}
	exact, ok := new(big.Rat).SetString(number.String())
	if !ok || !exact.IsInt() || !exact.Num().IsInt64() {
		return "", false
	}
	return json.Number(exact.Num().String()), true
}

func exactPortNumber(value any) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	converted, err := number.Float64()
	if err != nil {
		return false
	}
	exact, ok := new(big.Rat).SetString(number.String())
	return ok && exact.Cmp(new(big.Rat).SetFloat64(converted)) == 0
}

func (r *ConversionResult) schemaDiagnostics(invalid *jsonschema.ValidationError, document any) {
	if len(invalid.Causes) > 0 {
		for _, child := range invalid.Causes {
			r.schemaDiagnostics(child, document)
		}
		return
	}
	path := "$"
	parent := document
	for _, segment := range invalid.InstanceLocation {
		switch value := parent.(type) {
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(value) {
				path += ".*"
				parent = nil
				continue
			}
			path += fmt.Sprintf("[%d]", index)
			parent = value[index]
		case map[string]any:
			path += "." + safeSegment(segment)
			parent = value[segment]
		default:
			path += ".*"
		}
	}
	keyword := strings.Join(invalid.ErrorKind.KeywordPath(), ".")
	if keyword == "" {
		keyword = "declaration not allowed by schema"
	}
	r.add("schema_invalid", path, "Correct this declaration to satisfy pinned ODCS v3.2.0 ("+keyword+"); submitted values are omitted.")
}

func (r *ConversionResult) add(code, path, message string) {
	r.Diagnostics = append(r.Diagnostics, Diagnostic{Code: code, Path: path, Message: message})
}

func (r *ConversionResult) sortDiagnostics() {
	slices.SortFunc(r.Diagnostics, func(a, b Diagnostic) int {
		if n := strings.Compare(a.Path, b.Path); n != 0 {
			return n
		}
		if n := strings.Compare(a.Code, b.Code); n != 0 {
			return n
		}
		return strings.Compare(a.Message, b.Message)
	})
	r.Diagnostics = slices.Compact(r.Diagnostics)
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func safeSegment(key string) string {
	switch key {
	case "credentials", "credential", "password", "token", "secret", "servers", "server", "team", "support", "roles", "customProperties", "kind", "apiVersion", "id", "version", "name", "status", "description", "tags", "contractCreatedTs", "schema", "properties", "logicalType", "logicalTypeOptions", "required", "enum", "value", "label", "items", "map", "key", "minimum", "maximum", "minLength", "maxLength", "format", "pattern", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "maxItems", "minItems", "uniqueItems", "maxProperties", "minProperties", "dimensions", "elementType", "timezone", "defaultTimezone", "quality", "slaProperties", "slaDefaultElement", "relationships", "classification", "tenant", "domain", "dataProduct", "context", "price", "authoritativeDefinitions", "businessName", "physicalName", "physicalType", "primaryKey", "primaryKeyPosition", "unique", "semanticType", "partitioned", "partitionKeyPosition", "encryptedName", "transformSourceObjects", "transformLogic", "transformDescription", "examples", "criticalDataElement", "deprecated", "synonyms":
		return key
	default:
		return "*"
	}
}
