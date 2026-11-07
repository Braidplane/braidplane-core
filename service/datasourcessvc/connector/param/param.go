// Package param provides utilities for template parameter substitution in query strings.
package param

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// BindParams replaces template parameters with a positional placeholder and returns values in
// textual occurrence order. Repeated parameters therefore produce repeated arguments.
func BindParams(template string, params map[string]any, placeholder string) (string, []any, error) {
	matches := templateParamRegex.FindAllStringSubmatchIndex(template, -1)
	if len(matches) == 0 {
		return template, []any{}, nil
	}

	var result strings.Builder
	result.Grow(len(template))
	args := make([]any, 0, len(matches))
	last := 0
	for _, match := range matches {
		name := template[match[2]:match[3]]
		value, ok := params[name]
		if !ok {
			return "", nil, xerror.Unexpected().Detailf("missing required parameter %q", name)
		}
		result.WriteString(template[last:match[0]])
		result.WriteString(placeholder)
		args = append(args, value)
		last = match[1]
	}
	result.WriteString(template[last:])
	return result.String(), args, nil
}

var (
	templateParamRegex = regexp.MustCompile(
		`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)*)\s*\}\}`,
	)
	exactTemplateParamRegex = regexp.MustCompile(
		`^\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)*)\s*\}\}$`,
	)
)

// ExtractParamNames returns the ordered list of parameter names found in the template string.
// Parameter names are extracted from {{name}} patterns.
// Returns the names in the order they first appear, with duplicates removed.
func ExtractParamNames(template string) []string {
	seen := make(map[string]bool)
	ordered := make([]string, 0) // Initialize to empty slice, not nil

	matches := templateParamRegex.FindAllStringSubmatch(template, -1)
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		name := match[1]
		if !seen[name] {
			seen[name] = true
			ordered = append(ordered, name)
		}
	}

	return ordered
}

// SubstituteParams replaces all {{paramName}} patterns with values from the params map.
// Returns the substituted string and an ordered slice of values matching parameter order.
// Returns error if a required parameter (referenced in template) is missing from params.
func SubstituteParams(template string, params map[string]any) (string, []any, error) {
	paramNames := ExtractParamNames(template)
	args := make([]any, 0, len(paramNames))

	// Validate all referenced parameters exist
	for _, name := range paramNames {
		if _, ok := params[name]; !ok {
			return "", nil, xerror.Unexpected().Detailf("missing required parameter %q", name)
		}
	}

	// Build ordered args slice
	for _, name := range paramNames {
		args = append(args, params[name])
	}

	// Return template and args (caller will transform based on target system)
	return template, args, nil
}

// SubstituteString replaces {{paramName}} patterns in a string with provided values.
// Useful for path strings where we want the actual values, not positional markers.
// Returns error if a required parameter is missing.
func SubstituteString(template string, params map[string]any) (string, error) {
	matches := templateParamRegex.FindAllStringSubmatchIndex(template, -1)
	if len(matches) == 0 {
		return template, nil
	}
	var result strings.Builder
	result.Grow(len(template))
	last := 0
	for _, match := range matches {
		name := template[match[2]:match[3]]
		value, ok := params[name]
		if !ok {
			return "", xerror.Unexpected().Detailf("missing required parameter %q", name)
		}
		if value == nil {
			return "", xerror.InvalidInput(xerror.Query).Detailf("parameter %q is null; use a whole-value binding instead of string interpolation", name)
		}
		result.WriteString(template[last:match[0]])
		_, _ = fmt.Fprintf(&result, "%v", value)
		last = match[1]
	}
	result.WriteString(template[last:])
	return result.String(), nil
}

// SubstituteInMap recursively substitutes {{paramName}} patterns in all string values
// throughout a map structure with any nesting depth. Arrays and nested maps are handled.
// Non-string values are left unchanged.
func SubstituteInMap(data, params map[string]any) (map[string]any, error) {
	// Validate all referenced parameters exist in any string values
	if err := validateMapParams(data, params); err != nil {
		return nil, err
	}

	result := make(map[string]any)
	for key, value := range data {
		substituted, err := substituteValue(value, params)
		if err != nil {
			return nil, err
		}
		result[key] = substituted
	}
	return result, nil
}

// SubstituteInArray recursively substitutes {{paramName}} patterns in all string values
// throughout an array structure with any nesting depth.
func SubstituteInArray(data []any, params map[string]any) ([]any, error) {
	// Validate referenced parameters
	if err := validateArrayParams(data, params); err != nil {
		return nil, err
	}

	result := make([]any, len(data))
	for i, value := range data {
		substituted, err := substituteValue(value, params)
		if err != nil {
			return nil, err
		}
		result[i] = substituted
	}
	return result, nil
}

// SubstituteValue recursively substitutes {{paramName}} in a value that may be a string, map,
// array, or primitive JSON value.
func SubstituteValue(value any, params map[string]any) (any, error) {
	return substituteValue(value, params)
}

// substituteValue recursively substitutes {{paramName}} in a value (string, map, array, or
// primitive).
func substituteValue(value any, params map[string]any) (any, error) {
	switch v := value.(type) {
	case string:
		return substituteStringValue(v, params)
	case map[string]any:
		return SubstituteInMap(v, params)
	case []any:
		return SubstituteInArray(v, params)
	default:
		// Primitives: int, bool, float, nil, etc. are left unchanged
		return value, nil
	}
}

func substituteStringValue(template string, params map[string]any) (any, error) {
	if match := exactTemplateParamRegex.FindStringSubmatch(template); len(match) == 2 {
		name := match[1]
		value, ok := params[name]
		if !ok {
			return nil, xerror.Unexpected().Detailf("missing required parameter %q", name)
		}
		return value, nil
	}

	return SubstituteString(template, params)
}

// validateMapParams checks that all referenced {{paramName}} patterns in string values exist in
// params.
func validateMapParams(data, params map[string]any) error {
	return validateCollectedParams(data, params, collectStringParams)
}

// validateArrayParams checks that all referenced {{paramName}} patterns in string values exist in
// params.
func validateArrayParams(data []any, params map[string]any) error {
	return validateCollectedParams(data, params, collectArrayStringParams)
}

func validateCollectedParams[T any](
	data T,
	params map[string]any,
	collector func(T, map[string]bool),
) error {
	requiredParams := make(map[string]bool)
	collector(data, requiredParams)

	for paramName := range requiredParams {
		if _, ok := params[paramName]; !ok {
			return xerror.Unexpected().Detailf("missing required parameter %q", paramName)
		}
	}
	return nil
}

// collectStringParams recursively gathers all {{paramName}} references in string values within a
// map.
func collectStringParams(data map[string]any, collected map[string]bool) {
	for _, value := range data {
		collectValueParams(value, collected)
	}
}

// collectArrayStringParams recursively gathers all {{paramName}} references in string values within
// an array.
func collectArrayStringParams(data []any, collected map[string]bool) {
	for _, value := range data {
		collectValueParams(value, collected)
	}
}

// collectValueParams recursively gathers all {{paramName}} references in any value.
func collectValueParams(value any, collected map[string]bool) {
	switch v := value.(type) {
	case string:
		for _, name := range ExtractParamNames(v) {
			collected[name] = true
		}
	case map[string]any:
		collectStringParams(v, collected)
	case []any:
		collectArrayStringParams(v, collected)
	}
}
