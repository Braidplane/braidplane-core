package apischema

import "github.com/Braidplane/braidplane-core/pkg/xerror"

// MergeManaged adds private API definitions without replacing public definitions.
func MergeManaged(source, managed map[string]any) error {
	paths, ok := source["paths"].(map[string]any)
	if !ok {
		return xerror.Unexpected().Detailf("canonical paths must be an object")
	}
	components, ok := source["components"].(map[string]any)
	if !ok {
		return xerror.Unexpected().Detailf("canonical components must be an object")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		return xerror.Unexpected().Detailf("canonical schema components must be an object")
	}
	for key := range managed {
		if key != "paths" && key != "components" {
			return xerror.Unexpected().Detailf("unknown managed input field %q", key)
		}
	}
	managedPaths, ok := managed["paths"].(map[string]any)
	if !ok || len(managedPaths) == 0 {
		return xerror.Unexpected().Detailf("managed paths must be a non-empty object")
	}
	var managedSchemas map[string]any
	if rawComponents, exists := managed["components"]; exists {
		managedComponents, ok := rawComponents.(map[string]any)
		if !ok {
			return xerror.Unexpected().Detailf("managed components must be an object")
		}
		for group := range managedComponents {
			if group != "schemas" {
				return xerror.Unexpected().Detailf("unsupported managed component group %q", group)
			}
		}
		if rawSchemas, exists := managedComponents["schemas"]; exists {
			var ok bool
			managedSchemas, ok = rawSchemas.(map[string]any)
			if !ok {
				return xerror.Unexpected().Detailf("managed schema components must be an object")
			}
		}
	}
	for path, raw := range managedPaths {
		item, ok := raw.(map[string]any)
		if !ok {
			return xerror.Unexpected().Detailf("managed path %q must be an object", path)
		}
		internal, ok := item["x-internal"].(bool)
		if !ok || !internal {
			return xerror.Unexpected().Detailf("managed path %q must be internal", path)
		}
		if _, exists := paths[path]; exists {
			return xerror.Unexpected().Detailf("managed path %q overrides canonical path", path)
		}
	}
	for name, raw := range managedSchemas {
		schema, ok := raw.(map[string]any)
		if !ok {
			return xerror.Unexpected().Detailf("managed schema %q must be an object", name)
		}
		internal, ok := schema["x-internal"].(bool)
		if !ok || !internal {
			return xerror.Unexpected().Detailf("managed schema %q must be internal", name)
		}
		if _, exists := schemas[name]; exists {
			return xerror.Unexpected().Detailf("managed schema %q overrides canonical schema", name)
		}
	}
	for path, item := range managedPaths {
		paths[path] = item
	}
	for name, schema := range managedSchemas {
		schemas[name] = schema
	}
	return nil
}
