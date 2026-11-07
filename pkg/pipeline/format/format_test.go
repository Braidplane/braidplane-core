package format

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestFormatOrdersStepsByVisualGraphLayers(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "orders-sync", Name: "Orders Sync"},
		Steps: []pipelinespec.Step{
			testSinkStep("load-audit", "shape-audit"),
			testTransformStep("shape-audit", map[string]pipelinespec.StepInput{
				"audit": {FromStep: "fetch-audit"},
			}),
			testQueryStep("fetch-orders"),
			testTransformStep("join-orders", map[string]pipelinespec.StepInput{
				"orders":    {FromStep: "fetch-orders"},
				"customers": {FromStep: "fetch-customers"},
			}),
			testSinkStep("load-orders", "join-orders"),
			testQueryStep("fetch-customers"),
			testQueryStep("fetch-audit"),
		},
	}

	result, err := Format(pipeline)
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)

	require.Equal(t, []string{
		"fetch-orders",
		"fetch-customers",
		"fetch-audit",
		"join-orders",
		"shape-audit",
		"load-orders",
		"load-audit",
	}, stepSlugs(result.Spec.Steps))
}

func TestFormatPreservesOriginalOrderWhenEdgesDoNotDistinguishSteps(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "independent-loads", Name: "Independent Loads"},
		Steps: []pipelinespec.Step{
			testQueryStep("fetch-zulu"),
			testQueryStep("fetch-alpha"),
			testQueryStep("fetch-middle"),
		},
	}

	result, err := Format(pipeline)
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.Equal(t, []string{"fetch-zulu", "fetch-alpha", "fetch-middle"}, stepSlugs(result.Spec.Steps))
}

func TestFormatOrdersSameKindDependenciesBeforeConsumers(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "same-kind-deps", Name: "Same Kind Deps"},
		Steps: []pipelinespec.Step{
			testSinkStep("load-users", "project-users"),
			testTransformStep("project-users", map[string]pipelinespec.StepInput{
				"normalized": {FromStep: "normalize-users"},
			}),
			testQueryStepWithParams("fetch-profiles", map[string]any{
				"user_ids": "$steps.fetch-users.rows",
			}),
			testTransformStep("normalize-users", map[string]pipelinespec.StepInput{
				"users":    {FromStep: "fetch-users"},
				"profiles": {FromStep: "fetch-profiles"},
			}),
			testQueryStep("fetch-users"),
		},
	}

	result, err := Format(pipeline)
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.Equal(t, []string{
		"fetch-users",
		"fetch-profiles",
		"normalize-users",
		"project-users",
		"load-users",
	}, stepSlugs(result.Spec.Steps))
}

func TestFormatPreservesTransformToQueryDependencies(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "query-after-transform", Name: "Query After Transform"},
		Steps: []pipelinespec.Step{
			testQueryStepWithParams("fetch-profiles", map[string]any{
				"user_ids": "$steps.project-user-ids.rows",
			}),
			testTransformStep("project-user-ids", map[string]pipelinespec.StepInput{
				"users": {FromStep: "fetch-users"},
			}),
			testQueryStep("fetch-users"),
		},
	}

	result, err := Format(pipeline)
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.Equal(t, []string{
		"fetch-users",
		"project-user-ids",
		"fetch-profiles",
	}, stepSlugs(result.Spec.Steps))
}

func TestFormatFallsBackToStableOrderForCycles(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "cyclic-query-refs", Name: "Cyclic Query Refs"},
		Steps: []pipelinespec.Step{
			testQueryStepWithParams("fetch-a", map[string]any{
				"source": "$steps.fetch-b.rows",
			}),
			testQueryStepWithParams("fetch-b", map[string]any{
				"source": "$steps.fetch-a.rows",
			}),
		},
	}

	result, err := Format(pipeline)
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.Equal(t, []string{"fetch-a", "fetch-b"}, stepSlugs(result.Spec.Steps))
}

func TestFormatReportsUnresolvedReferencesAsDiagnosticsOnly(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "draft-load", Name: "Draft Load"},
		Steps: []pipelinespec.Step{
			testSinkStep("load-users", "missing-normalize-users"),
			testQueryStep("fetch-users"),
		},
	}

	result, err := Format(pipeline)
	require.NoError(t, err)
	require.Equal(t, []string{"fetch-users", "load-users"}, stepSlugs(result.Spec.Steps))
	require.Len(t, result.Diagnostics, 1)
	require.Equal(t, "unresolved_step_reference", result.Diagnostics[0].Code)
	require.Equal(t, "load-users", result.Diagnostics[0].StepSlug)
	require.Equal(t, "missing-normalize-users", result.Diagnostics[0].ReferencedStepSlug)
}

func TestFormatReportsMalformedUnresolvedReferenceText(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "draft-load", Name: "Draft Load"},
		Steps: []pipelinespec.Step{
			testQueryStepWithParams("fetch-users", map[string]any{
				"source": "$steps.fetch_users.rows",
			}),
		},
	}

	result, err := Format(pipeline)
	require.NoError(t, err)
	require.Len(t, result.Diagnostics, 1)
	require.Equal(t, "fetch-users", result.Diagnostics[0].StepSlug)
	require.Equal(t, "fetch_users", result.Diagnostics[0].ReferencedStepSlug)
}

func TestFormatReportsMalformedSourceStepSlugAsRawDiagnosticText(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "draft-load", Name: "Draft Load"},
		Steps: []pipelinespec.Step{
			testQueryStepWithParams("fetch_users", map[string]any{
				"source": "$steps.missing-users.rows",
			}),
		},
	}

	result, err := Format(pipeline)
	require.NoError(t, err)
	require.Len(t, result.Diagnostics, 1)
	require.Equal(t, "fetch_users", result.Diagnostics[0].StepSlug)
	require.Equal(t, "missing-users", result.Diagnostics[0].ReferencedStepSlug)
}

func TestFormatPreservesDynamicNumericValues(t *testing.T) {
	t.Parallel()

	largeID := int64(9_007_199_254_740_993)
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "numeric-values", Name: "Numeric Values"},
		Steps: []pipelinespec.Step{
			{
				Kind: "sink",
				Slug: slug.FromString("load-users"),
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
					Input:            &pipelinespec.StepInput{FromStep: "fetch-users"},
					Config: map[string]any{
						"batch_id": largeID,
					},
				},
			},
			testQueryStepWithParams("fetch-users", map[string]any{
				"tenant_id": largeID,
			}),
		},
	}
	pipeline.Steps[1].Query.OperationInline = map[string]any{
		"statement_id": largeID,
	}

	result, err := Format(pipeline)
	require.NoError(t, err)

	require.Equal(t, []string{"fetch-users", "load-users"}, stepSlugs(result.Spec.Steps))
	require.Equal(t, largeID, result.Spec.Steps[0].Query.Parameters["tenant_id"])
	require.Equal(t, largeID, result.Spec.Steps[0].Query.OperationInline["statement_id"])
	require.Equal(t, largeID, result.Spec.Steps[1].Sink.Config["batch_id"])
}

func TestFormatJSONReturnsCanonicalIndentedJSON(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "json-orders", Name: "JSON Orders"},
		Steps: []pipelinespec.Step{
			testSinkStep("load-orders", "shape-orders"),
			testTransformStep("shape-orders", map[string]pipelinespec.StepInput{
				"orders": {FromStep: "fetch-orders"},
			}),
			testQueryStep("fetch-orders"),
		},
	}

	payload, diagnostics, err := FormatJSON(pipeline)
	require.NoError(t, err)
	require.Empty(t, diagnostics)

	require.JSONEq(t, `{
		"metadata": {
			"slug": "json-orders",
			"name": "JSON Orders"
		},
		"steps": [
			{
				"kind": "query",
				"slug": "fetch-orders",
				"query": {
					"data_source_ref": "acme-postgres",
					"operation_type": "sql",
					"operation_ref": "fetch-orders-operation"
				}
			},
			{
				"kind": "transform",
				"slug": "shape-orders",
				"transform": {
					"sql": "SELECT * FROM input",
					"output_kind": "rows",
					"inputs": {
						"orders": "fetch-orders"
					}
				}
			},
			{
				"kind": "sink",
				"slug": "load-orders",
				"sink": {
                    "idempotency_class": "idempotent_with_key",
					"input": "shape-orders",
					"data_source_ref": "acme-warehouse",
					"operation_type": "sql",
					"operation_ref": "load-orders-operation"
				}
			}
		]
	}`, string(payload))

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
}

func TestFormatDataFormatsJSONAndYAMLByExplicitFormat(t *testing.T) {
	t.Parallel()

	jsonPayload := []byte(`{
		"metadata": {"slug": "typed-format", "name": "Typed Format"},
		"steps": [
			{"kind": "sink", "slug": "load-users", "sink": {"input": "shape-users"}},
			{"kind": "transform", "slug": "shape-users", "transform": {"sql": "SELECT * FROM input", "output_kind": "rows", "inputs": {"input": "fetch-users"}}},
			{"kind": "query", "slug": "fetch-users", "query": {"data_source_ref": "acme-postgres", "operation_type": "sql", "operation_ref": "fetch-users-operation"}}
		]
	}`)
	yamlPayload := []byte(`
metadata:
  slug: typed-format
  name: Typed Format
steps:
  - kind: sink
    slug: load-users
    sink:
      idempotency_class: idempotent_with_key
      input: shape-users
  - kind: transform
    slug: shape-users
    transform:
      sql: SELECT * FROM input
      output_kind: rows
      inputs:
        input: fetch-users
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: fetch-users-operation
`)

	formattedJSON, jsonDiagnostics, err := FormatData("json", jsonPayload)
	require.NoError(t, err)
	require.Empty(t, jsonDiagnostics)
	require.JSONEq(t, `{
		"metadata": {"slug": "typed-format", "name": "Typed Format"},
		"steps": [
			{"kind": "query", "slug": "fetch-users", "query": {"data_source_ref": "acme-postgres", "operation_type": "sql", "operation_ref": "fetch-users-operation"}},
			{"kind": "transform", "slug": "shape-users", "transform": {"sql": "SELECT * FROM input", "output_kind": "rows", "inputs": {"input": "fetch-users"}}},
			{"kind": "sink", "slug": "load-users", "sink": {"input": "shape-users"}}
		]
	}`, string(formattedJSON))

	formattedYAML, yamlDiagnostics, err := FormatData("yaml", yamlPayload)
	require.NoError(t, err)
	require.Empty(t, yamlDiagnostics)
	require.Contains(t, string(formattedYAML), "slug: fetch-users")
	require.Less(t,
		strings.Index(string(formattedYAML), "slug: fetch-users"),
		strings.Index(string(formattedYAML), "slug: shape-users"),
	)
	require.Less(t,
		strings.Index(string(formattedYAML), "slug: shape-users"),
		strings.Index(string(formattedYAML), "slug: load-users"),
	)
}

func TestFormatOrdersInputBeforeOtherSourceSteps(t *testing.T) {
	t.Parallel()

	result, err := Format(&pipelinespec.Pipeline{Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "users"},
		{Kind: "input", Slug: "event"},
	}})

	require.NoError(t, err)
	require.Equal(t, []slug.Slug{"event", "users"}, []slug.Slug{result.Spec.Steps[0].Slug, result.Spec.Steps[1].Slug})
}

func TestFormatDataPreservesJSONIntegerPrecision(t *testing.T) {
	t.Parallel()

	const largeID = "9007199254740993"
	jsonPayload := []byte(`{
		"metadata": {"slug": "json-numbers", "name": "JSON Numbers"},
		"steps": [
			{
				"kind": "sink",
				"slug": "load-users",
				"sink": {
                    "idempotency_class": "idempotent_with_key",
					"input": "fetch-users",
					"config": {
						"batch_id": ` + largeID + `
					}
				}
			},
			{
				"kind": "query",
				"slug": "fetch-users",
				"query": {
					"data_source_ref": "acme-postgres",
					"operation_type": "sql",
					"operation_inline": {
						"statement_id": ` + largeID + `
					},
					"parameters": {
						"tenant_id": ` + largeID + `,
						"nested": {
							"ids": [` + largeID + `]
						}
					}
				}
			}
		]
	}`)

	formattedJSON, diagnostics, err := FormatData("json", jsonPayload)
	require.NoError(t, err)
	require.Empty(t, diagnostics)

	var decoded struct {
		Steps []struct {
			Query *struct {
				OperationInline map[string]any `json:"operation_inline"`
				Parameters      map[string]any `json:"parameters"`
			} `json:"query"`
			Sink *struct {
				Config map[string]any `json:"config"`
			} `json:"sink"`
		} `json:"steps"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(formattedJSON)))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&decoded))

	require.Equal(t, largeID, decoded.Steps[0].Query.OperationInline["statement_id"].(json.Number).String())
	require.Equal(t, largeID, decoded.Steps[0].Query.Parameters["tenant_id"].(json.Number).String())
	nested := decoded.Steps[0].Query.Parameters["nested"].(map[string]any)
	ids := nested["ids"].([]any)
	require.Equal(t, largeID, ids[0].(json.Number).String())
	require.Equal(t, largeID, decoded.Steps[1].Sink.Config["batch_id"].(json.Number).String())
}

func TestFormatDataWrapsDecodeFailuresAsInvalidInput(t *testing.T) {
	t.Parallel()

	_, _, err := FormatData("json", []byte(`{"metadata":`))

	require.Error(t, err)
	var xe *xerror.Error
	require.ErrorAs(t, err, &xe)
	require.Equal(t, xerror.KindInvalidInput, xe.Kind())
	require.Equal(t, "PIPELINE_INVALID_INPUT", xe.Code())
}

func TestFormatDataRejectsTrailingJSONData(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		data string
	}{
		{
			name: "Garbage",
			data: `{"metadata":{"slug":"trailing-data","name":"Trailing Data"},"steps":[]} garbage`,
		},
		{
			name: "SecondObject",
			data: `{"metadata":{"slug":"trailing-data","name":"Trailing Data"},"steps":[]} {"metadata":{"slug":"other","name":"Other"},"steps":[]}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := FormatData("json", []byte(tc.data))

			require.Error(t, err)
			var xe *xerror.Error
			require.ErrorAs(t, err, &xe)
			require.Equal(t, xerror.KindInvalidInput, xe.Kind())
			require.Equal(t, "PIPELINE_INVALID_INPUT", xe.Code())
		})
	}
}

func TestFormatDataRejectsExtraYAMLDocuments(t *testing.T) {
	t.Parallel()

	_, _, err := FormatData("yaml", []byte(`
metadata:
  slug: trailing-docs
  name: Trailing Docs
steps: []
---
metadata:
  slug: other
  name: Other
steps: []
`))

	require.Error(t, err)
	var xe *xerror.Error
	require.ErrorAs(t, err, &xe)
	require.Equal(t, xerror.KindInvalidInput, xe.Kind())
	require.Equal(t, "PIPELINE_INVALID_INPUT", xe.Code())
}

func testQueryStep(stepSlug string) pipelinespec.Step {
	return testQueryStepWithParams(stepSlug, nil)
}

func testQueryStepWithParams(stepSlug string, params map[string]any) pipelinespec.Step {
	return pipelinespec.Step{
		Kind: "query",
		Slug: slug.FromString(stepSlug),
		Query: &pipelinespec.QueryRef{
			DataSourceSlug: "acme-postgres",
			OperationType:  "sql",
			OperationRef:   slug.FromString(stepSlug + "-operation"),
			Parameters:     params,
		},
	}
}

func testTransformStep(stepSlug string, inputs map[string]pipelinespec.StepInput) pipelinespec.Step {
	return pipelinespec.Step{
		Kind: "transform",
		Slug: slug.FromString(stepSlug),
		Transform: &pipelinespec.TransformSpec{
			SQL:        "SELECT * FROM input",
			OutputKind: model.TransformOutputKindRows,
			Inputs:     inputs,
		},
	}
}

func testSinkStep(stepSlug, inputStepSlug string) pipelinespec.Step {
	return pipelinespec.Step{
		Kind: "sink",
		Slug: slug.FromString(stepSlug),
		Sink: &pipelinespec.SinkSpec{
			IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
			Input:            &pipelinespec.StepInput{FromStep: slug.FromString(inputStepSlug)},
			DataSourceSlug:   "acme-warehouse",
			OperationType:    "sql",
			OperationRef:     slug.FromString(stepSlug + "-operation"),
		},
	}
}

func stepSlugs(steps []pipelinespec.Step) []string {
	slugs := make([]string, 0, len(steps))
	for _, step := range steps {
		slugs = append(slugs, step.Slug.String())
	}
	return slugs
}

func TestFormatPreservesDisconnectedAndIncompleteDraft(t *testing.T) {
	t.Parallel()
	draft := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "draft", Name: "Draft"},
		Steps: []pipelinespec.Step{
			testQueryStep("a"), testSinkStep("x", "a"),
			testQueryStep("b"), testSinkStep("y", "b"),
			{Kind: "query", Slug: "unfinished"},
		},
	}
	formatted, err := Format(draft)
	require.NoError(t, err)
	require.ElementsMatch(t, draft.Steps, formatted.Spec.Steps)
	require.Empty(t, formatted.Diagnostics)
	require.Equal(t, "x", draft.Steps[1].Slug.String())
}
