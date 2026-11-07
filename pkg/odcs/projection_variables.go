package odcs

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ResolutionValue is explicitly supplied non-secret string data. Revision names
// its authoring snapshot; this compiler never consults environment or secrets.
type ResolutionValue struct {
	Value    string
	Revision string
}

type ProjectionResolution struct {
	Path, Token, Value, Revision string
	Default                      bool
}

func expandProjectionString(text, path string, values map[string]ResolutionValue) (string, []ProjectionResolution, bool) {
	var out strings.Builder
	var resolutions []ProjectionResolution
	for {
		start := strings.Index(text, "${")
		if start < 0 {
			if out.Len()+len(text) > MaxDocumentBytes {
				return "", nil, false
			}
			out.WriteString(text)
			return out.String(), resolutions, true
		}
		end := strings.IndexByte(text[start:], '}')
		if end < 0 {
			return "", nil, false
		}
		end += start + 1
		token := text[start:end]
		if variableToken.FindString(token) != token {
			return "", nil, false
		}
		name, fallback, hasDefault := strings.Cut(token[2:len(token)-1], ":-")
		value, supplied := values[name]
		useDefault := !supplied || value.Value == ""
		if useDefault {
			if !hasDefault {
				return "", nil, false
			}
			value = ResolutionValue{Value: fallback, Revision: PinnedVersion}
		} else if value.Revision == "" {
			return "", nil, false
		}
		// No second pass, runtime substitution, or expansion through another input.
		if !utf8.ValidString(value.Value) || strings.Contains(value.Value, "${") || strings.Contains(value.Value, "{{") ||
			out.Len()+start+len(value.Value) > MaxDocumentBytes {
			return "", nil, false
		}
		out.WriteString(text[:start])
		out.WriteString(value.Value)
		resolutions = append(resolutions, ProjectionResolution{Path: path, Token: token, Value: value.Value, Revision: value.Revision, Default: useDefault})
		text = text[end:]
	}
}

// Only executable declarations are visited. Opaque metadata and unselected
// relationships retain their original tokens and never become compile inputs.
func (c *projectionCompiler) resolve(value any, path string) any {
	if !c.spend(1, path) {
		return value
	}
	if c.size > MaxProjectionBytes {
		return value
	}
	switch value := value.(type) {
	case string:
		if strings.Contains(value, "{{") {
			c.fail("variable_invalid", path, "resolution")
			return value
		}
		expanded, records, ok := expandProjectionString(value, path, c.input.Variables)
		if !ok {
			c.fail("variable_unresolved", path, "resolution")
			return value
		}
		c.size += len(expanded)
		for _, record := range records {
			c.size += len(record.Path) + len(record.Token) + len(record.Value) + len(record.Revision) + 128
		}
		if c.size > MaxProjectionBytes {
			c.fail("projection_limit", path, "budget")
			return value
		}
		c.output.Resolutions = append(c.output.Resolutions, records...)
		return expanded
	case []any:
		for i, child := range value {
			value[i] = c.resolve(child, fmt.Sprintf("%s[%d]", path, i))
		}
	case map[string]any:
		for _, key := range sortedKeys(value) {
			if key == "enum" {
				entries, _ := value[key].([]any)
				for i, entry := range entries {
					item := entry.(map[string]any)
					item["value"] = c.resolve(item["value"], fmt.Sprintf("%s.enum[%d].value", path, i))
				}
			} else if projectionKey(key) {
				value[key] = c.resolve(value[key], path+"."+safeSegment(key))
			}
		}
	}
	return value
}

func projectionKey(key string) bool {
	switch key {
	case "name", "id", "logicalType", "physicalType", "logicalTypeOptions", "properties", "items", "map", "key", "value", "required", "enum",
		"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "minLength", "maxLength", "format", "pattern",
		"minItems", "maxItems", "uniqueItems", "minProperties", "maxProperties", "dimensions", "elementType", "timezone", "defaultTimezone", "normalized":
		return true
	}
	return false
}
