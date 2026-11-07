package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
)

func expansionOpenAPISpec(t *testing.T, prefix string, depth int, unused bool) []byte {
	t.Helper()
	definitions := map[string]any{"L0": map[string]any{"type": "string", "description": strings.Repeat("value", 16)}}
	for level := 1; level <= depth; level++ {
		ref := map[string]any{"$ref": prefix + fmt.Sprintf("L%d", level-1)}
		definitions[fmt.Sprintf("L%d", level)] = map[string]any{"type": "object", "properties": map[string]any{"left": ref, "right": ref}}
	}
	document := map[string]any{
		"openapi": "3.0.3", "info": map[string]any{"title": "Synthetic", "version": "1"},
		"paths": map[string]any{},
	}
	if strings.Contains(prefix, "components") {
		document["components"] = map[string]any{"schemas": definitions}
	} else {
		document["x-schemas"] = definitions
	}
	if !unused {
		document["paths"] = map[string]any{"/items": map[string]any{"post": map[string]any{
			"requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": prefix + fmt.Sprintf("L%d", depth)}}}},
			"responses":   map[string]any{"200": map[string]any{"description": "OK"}},
		}}}
	}
	data, err := json.Marshal(document)
	require.NoError(t, err)
	return data
}

func TestParseOpenAPIRejectsExpansionBeforeOutput(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"#/components/schemas/", "#/x-schemas/", "#/x%2Dschemas/", "#//x-schemas/"} {
		t.Run(prefix, func(t *testing.T) {
			ctx := schemaquota.WithQuota(context.Background(), 100, 8192)
			schema, err := parseOpenAPIWithQuota(ctx, expansionOpenAPISpec(t, prefix, 6, false))
			require.ErrorContains(t, err, "OpenAPI expansion")
			require.Nil(t, schema)
		})
	}
}

func TestParseOpenAPILimitsUnusedReferenceExpansion(t *testing.T) {
	t.Parallel()
	ctx := schemaquota.WithQuota(context.Background(), 100, 8192)
	schema, err := parseOpenAPIWithQuota(ctx, expansionOpenAPISpec(t, "#/x-schemas/", 6, true))
	require.ErrorContains(t, err, "OpenAPI expansion")
	require.Nil(t, schema)
}

func TestParseOpenAPIAdmitsSmallReferencesAndCycles(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"#/components/schemas/", "#/x-schemas/"} {
		schema, err := parseOpenAPI(expansionOpenAPISpec(t, prefix, 2, false))
		require.NoError(t, err)
		require.Len(t, schema.Operations, 1)
		require.NotEmpty(t, schema.Operations[0].RequestBody)
	}
	data := []byte(`{"openapi":"3.0.3","info":{"title":"Synthetic","version":"1"},"paths":{"/items":{"get":{"responses":{"200":{"description":"OK","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Node"}}}}}}}},"components":{"schemas":{"Node":{"type":"object","properties":{"name":{"type":"string"},"child":{"$ref":"#/components/schemas/Node"}}}}}}`)
	schema, err := parseOpenAPI(data)
	require.NoError(t, err)
	require.Len(t, schema.Operations, 1)
	require.Contains(t, string(schema.Operations[0].Responses), `"name"`)
}

func TestOpenAPIPreflightBoundsYAMLAliasesAndMerges(t *testing.T) {
	t.Parallel()
	data := []byte("openapi: 3.0.3\ninfo: {title: Synthetic, version: '1'}\npaths: {}\nx-leaf: &leaf {description: '" + strings.Repeat("x", 100) + "'}\nx-first: &first [*leaf, *leaf, *leaf, *leaf]\nx-second: &second [*first, *first, *first, *first]\nx-third: [*second, *second, *second, *second]\n")
	ctx := schemaquota.WithQuota(context.Background(), 100, 2048)
	_, err := parseOpenAPIWithQuota(ctx, data)
	require.ErrorContains(t, err, "OpenAPI expansion")

	control := []byte("openapi: 3.0.3\ninfo: {title: Synthetic, version: '1'}\nx-schema: &schema {type: string}\ncomponents:\n  schemas:\n    Value: {<<: *schema, description: ordinary}\npaths:\n  /items:\n    get:\n      responses:\n        '200':\n          description: OK\n          content:\n            application/json:\n              schema: {$ref: '#/components/schemas/Value'}\n")
	schema, err := parseOpenAPI(control)
	require.NoError(t, err)
	require.Contains(t, string(schema.Operations[0].Responses), `"type":"string"`)
}

func TestOpenAPIPreflightYAMLScalarKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		key, pointer, prefix string
	}{
		{key: "0x20", pointer: "32"},
		{key: "2e2", pointer: "200"},
		{key: "true", pointer: "true"},
		{key: "2001-01-01", pointer: "2001-01-01"},
		{key: "*key", pointer: "32", prefix: "x-key: &key 0x20\n"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			for _, depth := range []int{2, 6} {
				var document any
				require.NoError(t, json.Unmarshal(expansionOpenAPISpec(t, "#/x-schemas/", depth, false), &document))
				data, err := yaml.Marshal(document)
				require.NoError(t, err)
				require.Contains(t, string(data), "L0:")
				data = bytes.ReplaceAll(data, []byte("L0:"), []byte(tc.key+":"))
				data = bytes.ReplaceAll(data, []byte("#/x-schemas/L0"), []byte("#/x-schemas/"+tc.pointer))
				data = append([]byte(tc.prefix), data...)
				if depth == 2 {
					schema, err := parseOpenAPI(data)
					require.NoError(t, err)
					require.Len(t, schema.Operations, 1)
					require.NotEmpty(t, schema.Operations[0].RequestBody)
				} else {
					_, err := preflightOpenAPI(schemaquota.WithQuota(context.Background(), 100, 8192), data)
					require.ErrorContains(t, err, "OpenAPI expansion")
				}
			}
		})
	}
}

func TestOpenAPIPreflightRejectsLoaderUnsupportedYAMLKeys(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"null", "~", "0xFFFFFFFFFFFFFFFF", "!!timestamp 2001-01-01"} {
		t.Run(key, func(t *testing.T) {
			data := []byte("openapi: 3.0.3\ninfo: {title: Synthetic, version: '1'}\npaths: {}\nx-values: {" + key + ": value}\n")
			loader := openapi3.NewLoader()
			loader.IsExternalRefsAllowed = false
			_, err := loader.LoadFromData(data)
			require.Error(t, err)
			_, err = preflightOpenAPI(context.Background(), data)
			require.Error(t, err)
		})
	}
}

func TestOpenAPIPreflightBoundsOneOfDuplicationAndNesting(t *testing.T) {
	t.Parallel()
	schema := `{"type":"object","properties":{"value":{"type":"string"}}}`
	for range 8 {
		schema = `{"oneOf":[` + schema + `]}`
	}
	data := []byte(`{"openapi":"3.0.3","info":{"title":"Synthetic","version":"1"},"paths":{"/items":{"get":{"responses":{"200":{"description":"OK","content":{"application/json":{"schema":` + schema + `}}}}}}}}`)
	_, err := parseOpenAPIWithQuota(schemaquota.WithQuota(context.Background(), 100, 4096), data)
	require.ErrorContains(t, err, "OpenAPI expansion")
	deep := []byte(`{"openapi":"3.0.3","info":{"title":"Synthetic","version":"1"},"paths":{},"x-deep":` + strings.Repeat("[", 129) + `0` + strings.Repeat("]", 129) + `}`)
	_, err = parseOpenAPI(deep)
	require.ErrorContains(t, err, "128-level")
}

func TestOpenAPILiteralRefAndCanceledContext(t *testing.T) {
	t.Parallel()
	data := []byte(`{"openapi":"3.0.3","info":{"title":"Synthetic","version":"1"},"paths":{},"x-example":{"$ref":"#/missing-literal-example"}}`)
	_, err := parseOpenAPI(data)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = parseOpenAPIWithQuota(ctx, data)
	require.ErrorIs(t, err, context.Canceled)
}

func TestDraftParameterCollisionSequence(t *testing.T) {
	t.Parallel()
	used := map[string]int{}
	require.Equal(t, "name_2", draftParameterName("name_2", used))
	require.Equal(t, "name", draftParameterName("name", used))
	for i := 3; i <= 1002; i++ {
		require.Equal(t, fmt.Sprintf("name_%d", i), draftParameterName("name", used))
	}
	require.Equal(t, "name_2_2", draftParameterName("name_2", used))
}

func TestCoreOpenAPIDefaultQuota(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "api-schema", "schema.yml"))
	require.NoError(t, err)
	ctx := schemaquota.WithQuota(context.Background(), 10_000, maxOpenAPISize)
	require.NoError(t, schemaquota.Charge(ctx, 0, len(data)))
	schema, err := parseOpenAPIWithQuota(ctx, data)
	require.NoError(t, err)
	require.NotEmpty(t, schema.Operations)
}

func TestOpenAPI31SiblingAndEscapedPointer(t *testing.T) {
	t.Parallel()
	data := []byte(`{"openapi":"3.1.0","info":{"title":"Synthetic","version":"1"},"paths":{"/items":{"get":{"responses":{"200":{"description":"OK","content":{"application/json":{"schema":{"$ref":"#/components/schemas/A~1B","description":"sibling description"}}}}}}}},"components":{"schemas":{"A/B":{"type":"object","properties":{"value":{"type":"string"}}}}}}`)
	schema, err := parseOpenAPI(data)
	require.NoError(t, err)
	require.Contains(t, string(schema.Operations[0].Responses), "sibling description")
	require.Contains(t, string(schema.Operations[0].Responses), `"value"`)
}

func TestOpenAPIRootExtensionReferenceBudget(t *testing.T) {
	t.Parallel()
	for _, pointer := range []string{"#", "#/", "#%2F"} {
		t.Run(pointer, func(t *testing.T) {
			responses := map[string]any{}
			for index := range 20 {
				responses[fmt.Sprint(200+index)] = map[string]any{"$ref": pointer}
			}
			data, err := json.Marshal(map[string]any{
				"openapi": "3.0.3", "info": map[string]any{"title": "Synthetic", "version": "1"},
				"description": strings.Repeat("description", 128),
				"paths":       map[string]any{"/items": map[string]any{"get": map[string]any{"responses": responses}}},
			})
			require.NoError(t, err)
			_, err = parseOpenAPIWithQuota(schemaquota.WithQuota(context.Background(), 100, 8192), data)
			require.ErrorContains(t, err, "OpenAPI expansion")
		})
	}
}

func TestOpenAPIPreflightFollowsLoaderRefCaseVariants(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"$REF", "$Ref", "$rEf"} {
		t.Run(key, func(t *testing.T) {
			data := bytes.ReplaceAll(expansionOpenAPISpec(t, "#/x-schemas/", 6, false), []byte(`"$ref"`), []byte(`"`+key+`"`))
			_, err := preflightOpenAPI(schemaquota.WithQuota(context.Background(), 100, 8192), data)
			require.ErrorContains(t, err, "OpenAPI expansion")
		})
	}
}

func TestOpenAPILoaderResolvesUppercaseGenericReferences(t *testing.T) {
	t.Parallel()
	data := bytes.ReplaceAll(expansionOpenAPISpec(t, "#/x-schemas/", 1, false), []byte(`"$ref"`), []byte(`"$REF"`))
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	document, err := loader.LoadFromData(data)
	require.NoError(t, err)
	schema := document.Paths.Find("/items").Post.RequestBody.Value.Content["application/json"].Schema.Value
	require.NotNil(t, schema)
	require.NotNil(t, schema.Properties["left"].Value)
	require.NotSame(t, schema.Properties["left"].Value, schema.Properties["right"].Value)
}

func TestOpenAPICaseVariantsInComponentFields(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"SCHEMAS", "ſchemas"} {
		t.Run(name, func(t *testing.T) {
			modify := func(data []byte) []byte {
				data = bytes.ReplaceAll(data, []byte(`"components":`), []byte(`"COMPONENTS":`))
				return bytes.ReplaceAll(data, []byte(`"schemas":`), []byte(`"`+name+`":`))
			}
			control := modify(expansionOpenAPISpec(t, "#/components/schemas/", 2, false))
			schema, err := parseOpenAPI(control)
			require.NoError(t, err)
			require.Len(t, schema.Operations, 1)
			data := modify(expansionOpenAPISpec(t, "#/components/schemas/", 6, false))
			_, err = parseOpenAPIWithQuota(schemaquota.WithQuota(context.Background(), 100, 8192), data)
			require.ErrorContains(t, err, "OpenAPI expansion")
		})
	}
}
