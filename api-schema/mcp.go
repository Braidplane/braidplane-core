package apischema

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"gopkg.in/yaml.v3"
)

var coreComponents = sync.OnceValues(func() (map[string]map[string]any, error) {
	var doc struct {
		OpenAPI    string
		Components struct{ Schemas map[string]map[string]any }
	}
	if err := yaml.Unmarshal(Source(), &doc); err != nil {
		return nil, xerror.Wrapf("read Core schemas: %w", err)
	}
	if doc.OpenAPI != "3.0.4" {
		return nil, xerror.Unexpected().Detailf("unsupported Core OpenAPI dialect %q", doc.OpenAPI)
	}
	return doc.Components.Schemas, nil
})

// MCPObjectSchema derives a standalone JSON Schema for a catalog-selected Core
// component. The catalog owns operation selection and sensitive-field mapping.
func MCPObjectSchema(name string) (json.RawMessage, error) {
	components, err := coreComponents()
	if err != nil {
		return nil, err
	}
	return exportObject(components, name)
}

func exportObject(components map[string]map[string]any, name string) (json.RawMessage, error) {
	root := components[name]
	if root["type"] != "object" || root["nullable"] == true {
		return nil, xerror.Unexpected().Detailf("expected Core object component %q", name)
	}
	export := schemaExport{components: components, defs: map[string]any{}}
	object, err := export.convert(root)
	if err != nil {
		return nil, xerror.Wrapf("export component %s: %w", name, err)
	}
	object["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	if len(export.defs) > 0 {
		object["$defs"] = export.defs
	}
	data, err := json.Marshal(object)
	if err != nil {
		return nil, xerror.Wrapf("encode MCP schema: %w", err)
	}
	return data, nil
}

type schemaExport struct {
	components map[string]map[string]any
	defs       map[string]any
}

func (e schemaExport) convert(raw any) (map[string]any, error) {
	src, ok := raw.(map[string]any)
	if !ok {
		return nil, xerror.Unexpected().Detailf("expected schema object")
	}
	out := map[string]any{}
	for key, value := range src {
		switch key {
		case "$ref":
			ref, _ := value.(string)
			name, local := strings.CutPrefix(ref, "#/components/schemas/")
			target := e.components[name]
			if !local || target == nil || len(src) != 1 || strings.ContainsAny(name, "/~") {
				return nil, xerror.Unexpected().Detailf("unsupported Core schema reference %q", ref)
			}
			out["$ref"] = "#/$defs/" + name
			if _, seen := e.defs[name]; !seen {
				e.defs[name] = nil // Reserve before descending into recursive types.
				var err error
				e.defs[name], err = e.convert(target)
				if err != nil {
					return nil, err
				}
			}
		case "properties":
			props, ok := value.(map[string]any)
			if !ok {
				return nil, xerror.Unexpected().Detailf("invalid properties")
			}
			fields := map[string]any{}
			for field, schema := range props {
				converted, err := e.convert(schema)
				if err != nil {
					return nil, xerror.Wrapf("property %s: %w", field, err)
				}
				fields[field] = converted
			}
			out[key] = fields
		case "additionalProperties", "items", "not":
			if _, ok := value.(bool); ok && key == "additionalProperties" {
				out[key] = value
				continue
			}
			child, err := e.convert(value)
			if err != nil {
				return nil, err
			}
			out[key] = child
		case "oneOf", "allOf", "anyOf":
			items, ok := value.([]any)
			if !ok {
				return nil, xerror.Unexpected().Detailf("invalid %s", key)
			}
			variants := make([]any, 0, len(items))
			for _, item := range items {
				child, err := e.convert(item)
				if err != nil {
					return nil, err
				}
				variants = append(variants, child)
			}
			out[key] = variants
		case "exclusiveMinimum", "exclusiveMaximum":
			// In 3.0 the boolean modifies a separate inclusive bound.
			if value == true {
				bound := strings.ToLower(strings.TrimPrefix(key, "exclusive"))
				if src[bound] == nil {
					return nil, xerror.Unexpected().Detailf("missing %s", bound)
				}
				out[key] = src[bound]
			}
		case "format":
			switch value {
			case "date-time", "email", "uuid", "uri":
				out[key] = value
			default:
				return nil, xerror.Unexpected().Detailf("unsupported schema format %v", value)
			}
		case "example":
			out["examples"] = []any{value}
		case "type", "description", "title", "default", "enum", "required", "minimum", "maximum", "multipleOf", "minLength", "maxLength", "pattern", "minItems", "maxItems", "uniqueItems", "minProperties", "maxProperties":
			out[key] = value
		case "nullable", "x-enum-varnames":
		default:
			return nil, xerror.Unexpected().Detailf("unsupported Core schema keyword %q", key)
		}
	}
	// Core rejects null for an untyped 3.0 schema unless a composition admits it.
	if src["type"] == nil && src["$ref"] == nil && src["allOf"] == nil && src["oneOf"] == nil && src["anyOf"] == nil {
		out["type"] = []string{"object", "array", "string", "number", "boolean"}
	}
	if src["nullable"] == true {
		return map[string]any{"anyOf": []any{out, map[string]any{"type": "null"}}}, nil
	}
	return out, nil
}
