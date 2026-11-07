package odcs

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const MaxCoverageEntries = 16384

// MaxProjectionBytes also bounds aggregate pins and ODCS Proof inputs/claims.
const MaxProjectionBytes = 8 << 20

var coverageVocabulary = sync.OnceValues(func() (map[string]bool, error) {
	var schema map[string]any
	if err := jsonvalue.Unmarshal(pinnedSchema, &schema); err != nil {
		return nil, xerror.Unexpected().With(err).Detail("read pinned ODCS declaration vocabulary")
	}
	keys := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if properties, ok := value["properties"].(map[string]any); ok {
				for key := range properties {
					keys[key] = true
				}
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(schema)
	return keys, nil
})

// Coverage includes containers, leaves and opaque extension contents. Positional
// paths are stable within the immutable digest and omit arbitrary authored keys.
func (r *ConversionResult) coverage(document map[string]any, vocabulary map[string]bool, richProjection bool) {
	mapped := map[string]string{}
	blocking := map[string]Diagnostic{}
	for _, diagnostic := range r.Diagnostics {
		blocking[diagnostic.Path] = diagnostic
	}
	objects, _ := document["schema"].([]any)
	selected := ""
	for i, value := range objects {
		object := value.(map[string]any)
		id, _ := object["id"].(string)
		if id == "" || id != r.SchemaObjectID || variableToken.MatchString(id) {
			continue
		}
		selected = fmt.Sprintf("$.schema[%d]", i)
		properties, _ := object["properties"].([]any)
		for j, value := range properties {
			property := value.(map[string]any)
			path := fmt.Sprintf("%s.properties[%d]", selected, j)
			scope := fmt.Sprintf("port.fields[%d]", j)
			for _, key := range []string{"name", "logicalType", "required", "enum"} {
				mapped[path+"."+key] = scope
			}
			if r.mappedPhysical[path] {
				mapped[path+".physicalType"] = scope
			}
			options, _ := property["logicalTypeOptions"].(map[string]any)
			for key := range options {
				mapped[path+".logicalTypeOptions."+key] = scope
			}
			// Enum annotations describe literals; only their values are enforced.
			entries, _ := property["enum"].([]any)
			for k := range entries {
				mapped[fmt.Sprintf("%s.enum[%d].value", path, k)] = scope
			}
		}
	}
	for i, physical := range slices.Sorted(maps.Keys(r.decimalMappings)) {
		used := false
		for _, value := range objects {
			object := value.(map[string]any)
			if object["id"] != r.SchemaObjectID {
				continue
			}
			properties, _ := object["properties"].([]any)
			for _, value := range properties {
				if value.(map[string]any)["physicalType"] == physical {
					used = true
				}
			}
		}
		if used {
			segment := physical
			if !vocabulary[physical] {
				segment = fmt.Sprintf("*[%d]", i)
			}
			mapped[r.decimalMappingPath+"."+segment] = "port.decimal_types"
		}
	}
	var walk func(any, string, model.ODCSDeclarationCoverage, bool)
	complete, size := true, 0
	walk = func(value any, path string, inherited model.ODCSDeclarationCoverage, opaque bool) {
		if !complete {
			return
		}
		entry := inherited
		entry.Path = path
		if strings.HasPrefix(path, "$.schema[") {
			if selected != "" && (path == selected || strings.HasPrefix(path, selected+".") || strings.HasPrefix(path, selected+"[")) {
				entry.Applicability = "selected_port"
			} else {
				entry.Applicability = "not_selected"
				if entry.State == "descriptive" {
					entry.Reason = "outside_selected_port"
				}
			}
		}
		if scope, ok := mapped[path]; ok && !richProjection {
			entry.State, entry.Reason, entry.RuntimeScope = "enforced", "compiled_runtime_constraint", scope
			if r.Shape == nil {
				entry.State, entry.Reason, entry.RuntimeScope = "unresolved", "projection_unavailable", ""
			}
		}
		if diagnostic, ok := blocking[path]; ok {
			entry.State, entry.Reason, entry.RuntimeScope = "unsupported", diagnostic.Code, ""
			if diagnostic.Code == "variable_unresolved" || diagnostic.Code == "object_not_found" {
				entry.State = "unresolved"
			}
		}
		if text, ok := value.(string); ok && variableToken.MatchString(text) {
			entry.State, entry.Reason, entry.RuntimeScope = "unresolved", "variable_not_resolved", ""
		}
		// Fixed ASCII vocabulary and positional paths need no JSON escaping;
		// 256 bytes conservatively covers field names and punctuation.
		size += len(entry.Path) + len(entry.State) + len(entry.Reason) + len(entry.Applicability) + len(entry.RuntimeScope) + 256
		if len(r.Coverage) == MaxCoverageEntries || size > MaxProjectionBytes {
			complete = false
			return
		}
		r.Coverage = append(r.Coverage, entry)
		switch value := value.(type) {
		case map[string]any:
			for i, key := range sortedKeys(value) {
				child := entry
				segment := key
				if !vocabulary[key] {
					segment = fmt.Sprintf("*[%d]", i)
				}
				childPath := path + "." + segment
				if !opaque && entry.State == "descriptive" {
					switch key {
					case "items", "map":
						if !richProjection {
							child.State, child.Reason = "unsupported", "no_runtime_evaluator"
						}
					case "quality", "slaProperties", "slaDefaultElement", "relationships", "servers", "roles", "context", "primaryKey", "primaryKeyPosition", "unique", "partitioned", "partitionKeyPosition", "transformLogic", "transformSourceObjects", "transformDescription":
						child.State, child.Reason = "unsupported", "no_runtime_evaluator"
					}
				}
				// Labels and descriptions attached to enum values do not constrain them.
				enumPath, _, isEnum := strings.Cut(path, ".enum[")
				_, mappedEnum := mapped[enumPath+".enum"]
				if isEnum && mappedEnum && key != "value" {
					child.State, child.Reason, child.RuntimeScope = "descriptive", "retained_metadata", ""
				}
				childOpaque := opaque || key == "implementation" || key == "arguments" || key == "examples" || key == "value" && strings.Contains(path, ".customProperties[")
				walk(value[key], childPath, child, childOpaque)
			}
		case []any:
			for i, item := range value {
				walk(item, fmt.Sprintf("%s[%d]", path, i), entry, opaque)
			}
		}
	}
	walk(document, "$", model.ODCSDeclarationCoverage{State: "descriptive", Reason: "retained_metadata", Applicability: "document"}, false)
	r.CoverageComplete = complete
	if !complete {
		r.Coverage, r.Shape = nil, nil
		r.add("coverage_limit_exceeded", "$", "Declaration coverage exceeds the compiler's 16384-entry or 8 MiB resource budget; no partial coverage or executable shape is returned.")
		r.sortDiagnostics()
	}
}
