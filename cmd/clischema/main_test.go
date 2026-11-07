package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/apischema"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestProjectKeepsOnlyReachableWireContract(t *testing.T) {
	var source map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
  "openapi":"3.0.4",
  "paths":{"/public":{"get":{"operationId":"getPublic","responses":{"200":{"description":"Internal detail","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Public"}}}}}}},"/private":{"get":{"operationId":"getPrivate"}}},
  "components":{"schemas":{"Public":{"type":"object","properties":{"description":{"type":"string"},"example":{"type":"string"},"child":{"$ref":"#/components/schemas/Child"}}},"Child":{"type":"object","properties":{"parent":{"$ref":"#/components/schemas/Public"}}},"Private":{"type":"string"}}}
 }`), &source))
	got, err := project(source, map[string]bool{"getPublic": true})
	require.NoError(t, err)
	data, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(data), "getPrivate")
	require.NotContains(t, string(data), "Internal detail")
	require.NotContains(t, string(data), `"Private"`)
	require.Contains(t, string(data), `"description":{"type":"string"}`)
	require.Contains(t, string(data), `"example":{"type":"string"}`)
	require.Contains(t, string(data), `"Child"`)
	_, err = project(source, map[string]bool{"missing": true})
	require.ErrorContains(t, err, "unknown CLI operation")
	source["paths"] = map[string]any{"/public": map[string]any{"get": map[string]any{"operationId": "getPublic", "schema": map[string]any{"$ref": "https://private.invalid/schema"}}}}
	_, err = project(source, map[string]bool{"getPublic": true})
	require.ErrorContains(t, err, "non-component reference")
	require.NotContains(t, string(data), "private.invalid")
}

func TestProjectRejectsMalformedShapes(t *testing.T) {
	for _, input := range []string{
		`{}`,
		`{"paths":{"/public":false}}`,
		`{"paths":{"/public":{"get":{"operationId":"getPublic","security":[false]}}}}`,
		`{"paths":{"/public":{"get":{"operationId":"getPublic","discriminator":{"mapping":{"kind":false}}}}}}`,
	} {
		t.Run(input, func(t *testing.T) {
			var source map[string]any
			require.NoError(t, json.Unmarshal([]byte(input), &source))
			_, err := project(source, map[string]bool{"getPublic": true})
			require.Error(t, err)
		})
	}
}

func TestMergeManagedInputProjectsPrivateOperations(t *testing.T) {
	source := map[string]any{
		"openapi": "3.0.4",
		"paths": map[string]any{
			"/public": map[string]any{"get": map[string]any{"operationId": "getPublic"}},
		},
		"components": map[string]any{
			"schemas": map[string]any{"Shared": map[string]any{"type": "string"}},
		},
	}
	managed := map[string]any{
		"paths": map[string]any{
			"/private": map[string]any{
				"x-internal": true,
				"get":        map[string]any{"operationId": "getPrivate", "responses": map[string]any{"200": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Private"}}}}}},
			},
		},
		"components": map[string]any{
			"schemas": map[string]any{"Private": map[string]any{"x-internal": true, "allOf": []any{map[string]any{"$ref": "#/components/schemas/Shared"}}}},
		},
	}
	require.NoError(t, apischema.MergeManaged(source, managed))
	projected, err := project(source, map[string]bool{"getPrivate": true})
	require.NoError(t, err)
	data, err := json.Marshal(projected)
	require.NoError(t, err)
	require.Contains(t, string(data), "getPrivate")
	require.Contains(t, string(data), "Private")
	require.Contains(t, string(data), "Shared")

	public, err := project(map[string]any{
		"openapi": "3.0.4",
		"paths":   map[string]any{"/public": map[string]any{"get": map[string]any{"operationId": "getPublic"}}},
		"components": map[string]any{
			"schemas": map[string]any{},
		},
	}, map[string]bool{"getPublic": true})
	require.NoError(t, err)
	data, err = json.Marshal(public)
	require.NoError(t, err)
	require.NotContains(t, string(data), "getPrivate")
}

func TestMergeManagedInputRejectsInvalidExtensions(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"paths":      map[string]any{"/public": map[string]any{}},
			"components": map[string]any{"schemas": map[string]any{"Public": map[string]any{}}},
		}
	}
	for name, managed := range map[string]map[string]any{
		"empty paths":        {"paths": map[string]any{}},
		"unknown field":      {"paths": map[string]any{"/private": map[string]any{"x-internal": true}}, "openapi": "3.0.4"},
		"non-object paths":   {"paths": []any{}},
		"public path":        {"paths": map[string]any{"/private": map[string]any{}}},
		"path override":      {"paths": map[string]any{"/public": map[string]any{"x-internal": true}}},
		"invalid components": {"paths": map[string]any{"/private": map[string]any{"x-internal": true}}, "components": []any{}},
		"unsupported group":  {"paths": map[string]any{"/private": map[string]any{"x-internal": true}}, "components": map[string]any{"parameters": map[string]any{}}},
		"non-object schemas": {"paths": map[string]any{"/private": map[string]any{"x-internal": true}}, "components": map[string]any{"schemas": []any{}}},
		"public schema":      {"paths": map[string]any{"/private": map[string]any{"x-internal": true}}, "components": map[string]any{"schemas": map[string]any{"Private": map[string]any{}}}},
		"schema override":    {"paths": map[string]any{"/private": map[string]any{"x-internal": true}}, "components": map[string]any{"schemas": map[string]any{"Public": map[string]any{"x-internal": true}}}},
	} {
		t.Run(name, func(t *testing.T) {
			source := base()
			require.Error(t, apischema.MergeManaged(source, managed))
			require.NotContains(t, source["paths"].(map[string]any), "/private")
			require.NotContains(t, source["components"].(map[string]any)["schemas"].(map[string]any), "Private")
		})
	}
}

func TestPublicProjectionExcludesPlatformAdministration(t *testing.T) {
	data, err := os.ReadFile("../../api-schema/schema.yml")
	require.NoError(t, err)
	var source map[string]any
	require.NoError(t, yaml.Unmarshal(data, &source))
	data, err = os.ReadFile("../../cli/api/operations.txt")
	require.NoError(t, err)
	allowed := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			allowed[line] = true
		}
	}
	projected, err := project(source, allowed)
	require.NoError(t, err)
	output, err := json.Marshal(projected)
	require.NoError(t, err)
	require.NotContains(t, string(output), "getAdminIdentity")
	require.NotContains(t, string(output), "/admin/identity")
	require.NotContains(t, string(output), "AdminIdentity")
	require.NotContains(t, string(output), "AdminDemo")
	require.NotContains(t, string(output), "/admin/")
}
