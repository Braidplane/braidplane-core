package odcs

import "strings"

const variablePattern = `\$\{[A-Za-z_][A-Za-z0-9_]*(?::-[^{}]*)?\}`

// The normative v3.2.0 text permits variables in strings and file references.
// Keep the vendored schema intact; apply these documented corrections in memory.
func applyNormativeSchemaRules(value any) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			switch key {
			case "enum", "const", "default", "examples":
				continue
			}
			applyNormativeSchemaRules(child)
		}
		if value["type"] != "string" {
			return
		}
		_, pattern := value["pattern"]
		_, format := value["format"]
		_, enum := value["enum"]
		if !pattern && !format && !enum {
			return
		}
		original := make(map[string]any, len(value))
		for key, child := range value {
			original[key] = child
			delete(value, key)
		}
		value["anyOf"] = []any{original, map[string]any{"type": "string", "pattern": variablePattern}}
	case []any:
		for _, child := range value {
			applyNormativeSchemaRules(child)
		}
	}
}

func correctNormativeSchema(document map[string]any) {
	definitions := document["$defs"].(map[string]any)
	stableID := definitions["StableId"].(map[string]any)
	stableID["pattern"] = strings.ReplaceAll(stableID["pattern"].(string), `\s`, `\s\p{Z}\x{0085}`)
	object := definitions["SchemaObject"].(map[string]any)["properties"].(map[string]any)
	object["logicalType"].(map[string]any)["enum"] = []any{"object", "array"}
	reference := definitions["FullyQualifiedReference"].(map[string]any)
	reference["pattern"] = strings.ReplaceAll(reference["pattern"].(string), `\s`, `\s\p{Z}\x{0085}`)
	reference["pattern"] = strings.Replace(reference["pattern"].(string), `(?:https?:\/\/)?`, `(?:(?:https?:\/\/)|(?:file:\/\/\/))?`, 1)
}
