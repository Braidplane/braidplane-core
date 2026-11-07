// Command clischema projects only reviewed CLI operations and their wire types.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/apischema"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"gopkg.in/yaml.v3"
)

func project(source map[string]any, allowed map[string]bool) (map[string]any, error) {
	paths := map[string]any{}
	found := map[string]bool{}
	sourcePaths, ok := source["paths"].(map[string]any)
	if !ok {
		return nil, xerror.Unexpected().Detailf("paths must be an object")
	}
	for path, raw := range sourcePaths {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, xerror.Unexpected().Detailf("path %q must be an object", path)
		}
		selected := map[string]any{}
		for method, rawOperation := range item {
			operation, ok := rawOperation.(map[string]any)
			if !ok {
				continue
			}
			id, _ := operation["operationId"].(string)
			if allowed[id] {
				selected[method] = operation
				found[id] = true
			}
		}
		if len(selected) > 0 {
			if parameters, ok := item["parameters"]; ok {
				selected["parameters"] = parameters
			}
			paths[path] = selected
		}
	}
	for id := range allowed {
		if !found[id] {
			return nil, xerror.Unexpected().Detailf("unknown CLI operation %q", id)
		}
	}
	output := map[string]any{"openapi": source["openapi"], "info": map[string]any{"title": "Braidplane CLI API", "version": "0.0.1"}, "paths": paths}
	if security, ok := source["security"]; ok {
		output["security"] = security
	}
	components := map[string]any{}
	output["components"] = components
	var visit func(any, string) error
	include := func(ref string) error {
		parts := strings.Split(ref, "/")
		if len(parts) != 4 || parts[0] != "#" || parts[1] != "components" {
			return xerror.Unexpected().Detailf("non-component reference %q", ref)
		}
		kind, name := parts[2], parts[3]
		group, ok := components[kind].(map[string]any)
		if !ok {
			group = map[string]any{}
			components[kind] = group
		}
		if _, exists := group[name]; exists {
			return nil
		}
		all, ok := source["components"].(map[string]any)
		if !ok {
			return xerror.Unexpected().Detailf("missing components")
		}
		original, ok := all[kind].(map[string]any)
		if !ok {
			return xerror.Unexpected().Detailf("missing component group %q", kind)
		}
		value, ok := original[name]
		if !ok {
			return xerror.Unexpected().Detailf("missing component %q", ref)
		}
		group[name] = value
		return visit(value, "")
	}
	visit = func(value any, parent string) error {
		switch node := value.(type) {
		case []any:
			for _, child := range node {
				if err := visit(child, parent); err != nil {
					return xerror.Unexpected().With(err)
				}
			}
		case map[string]any:
			// Descriptive metadata and examples are not required to reproduce wire types.
			if parent != "properties" && parent != "headers" && parent != "mapping" && parent != "paths" && parent != "responses" && parent != "content" {
				if _, ok := node["description"].(string); ok {
					node["description"] = ""
				}
				for _, key := range []string{"summary", "example", "examples", "externalDocs", "tags"} {
					delete(node, key)
				}
			}
			if ref, ok := node["$ref"].(string); ok {
				if err := include(ref); err != nil {
					return xerror.Unexpected().With(err)
				}
			}
			if security, ok := node["security"].([]any); ok {
				for _, raw := range security {
					requirement, ok := raw.(map[string]any)
					if !ok {
						return xerror.Unexpected().Detailf("security requirement must be an object")
					}
					for name := range requirement {
						if err := include("#/components/securitySchemes/" + name); err != nil {
							return xerror.Unexpected().With(err)
						}
					}
				}
			}
			if discriminator, ok := node["discriminator"].(map[string]any); ok {
				if mapping, ok := discriminator["mapping"].(map[string]any); ok {
					for _, raw := range mapping {
						ref, ok := raw.(string)
						if !ok {
							return xerror.Unexpected().Detailf("discriminator mapping must contain string references")
						}
						if !strings.HasPrefix(ref, "#/") {
							ref = "#/components/schemas/" + ref
						}
						if err := include(ref); err != nil {
							return xerror.Unexpected().With(err)
						}
					}
				}
			}
			for key, child := range node {
				if err := visit(child, key); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(map[string]any{"paths": paths, "security": output["security"]}, ""); err != nil {
		return nil, err
	}
	return output, nil
}

func run() error {
	input := flag.String("input", "api-schema/schema.yml", "canonical API schema")
	managedInput := flag.String("managed-input", "", "private API schema extension")
	allow := flag.String("allow", "cli/api/operations.txt", "reviewed operation IDs")
	output := flag.String("output", "cli/api/schema.json", "CLI schema")
	flag.Parse()
	data, err := os.ReadFile(*input)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	var source map[string]any
	if err = yaml.Unmarshal(data, &source); err != nil {
		return xerror.Unexpected().With(err)
	}
	if *managedInput != "" {
		data, err = os.ReadFile(*managedInput)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		var managed map[string]any
		if err = yaml.Unmarshal(data, &managed); err != nil {
			return xerror.Unexpected().With(err)
		}
		if err = apischema.MergeManaged(source, managed); err != nil {
			return xerror.Unexpected().With(err)
		}
	}
	data, err = os.ReadFile(*allow)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	allowed := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if allowed[line] {
			return xerror.Unexpected().Detailf("duplicate operation %q", line)
		}
		allowed[line] = true
	}
	if len(allowed) == 0 {
		return xerror.Unexpected().Detailf("empty operation allowlist")
	}
	result, err := project(source, allowed)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	data, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if err := os.WriteFile(*output, append(data, '\n'), 0o600); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
