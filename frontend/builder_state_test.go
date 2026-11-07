package frontend

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelineformat "github.com/Braidplane/braidplane-core/pkg/pipeline/format"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func mustReadPipelineFixture(t *testing.T, name string) []byte {
	t.Helper()

	path := filepath.Join("testdata", "pipelines", name)
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	return content
}

func TestBuilderRoundTripsCurrentPipelineFixturesWithoutSemanticLoss(t *testing.T) {
	t.Parallel()

	patterns := []string{
		filepath.Join("testdata", "pipelines", "*.pipeline.yaml"),
		filepath.Join("testdata", "pipelines", "*.pipeline.yml"),
		filepath.Join("testdata", "pipelines", "*.pipeline.json"),
		filepath.Join("..", "service", "pipelinesvc", "testdata", "*.pipeline.yaml"),
		filepath.Join("..", "service", "pipelinesvc", "testdata", "*.pipeline.yml"),
		filepath.Join("..", "service", "pipelinesvc", "testdata", "*.pipeline.json"),
		filepath.Join("..", "examples", "pipelines", "json", "*.pipeline.json"),
		filepath.Join("..", "examples", "pipelines", "yaml", "*.pipeline.yaml"),
		filepath.Join("..", "examples", "pipelines", "yaml", "*.pipeline.yml"),
		filepath.Join("..", "examples", "golden-path", "pipeline", "*.pipeline.yaml"),
		filepath.Join("..", "examples", "golden-path", "pipeline", "*.pipeline.yml"),
		filepath.Join("..", "examples", "golden-path", "pipeline", "*.pipeline.json"),
	}
	var paths []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		require.NoError(t, err)
		paths = append(paths, matches...)
	}
	require.NotEmpty(t, paths)

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			payload, err := os.ReadFile(path)
			require.NoError(t, err)
			original, err := pipelinevalidate.Load(path, payload)
			require.NoError(t, err)
			state, err := builderStateFromPipelineSpec(path, payload)
			require.NoError(t, err)
			format, err := builderPipelineFormatFromPath(path)
			require.NoError(t, err)
			roundTrippedPayload, err := serializeBuilderPipelineSpec(state, format)
			require.NoError(t, err)
			roundTripped, err := pipelinevalidate.Load(path, roundTrippedPayload)
			require.NoError(t, err)

			originalJSON, _, err := pipelineformat.FormatJSON(original)
			require.NoError(t, err)
			roundTrippedJSON, _, err := pipelineformat.FormatJSON(roundTripped)
			require.NoError(t, err)
			require.JSONEq(t, string(originalJSON), string(roundTrippedJSON))
		})
	}
}

func testQueryStep(stepSlug, dataSourceSlug, operationRef string) pipelinespec.Step {
	return pipelinespec.Step{
		Kind: builderKindQuery,
		Slug: slug.Slug(stepSlug),
		Query: &pipelinespec.QueryRef{
			DataSourceSlug: slug.Slug(dataSourceSlug),
			OperationType:  builderDefaultOperationType,
			OperationRef:   slug.Slug(operationRef),
		},
	}
}

func testSinkStep(stepSlug, fromStep string) pipelinespec.Step {
	step := pipelinespec.Step{
		Kind: builderKindSink,
		Slug: slug.Slug(stepSlug),
		Sink: &pipelinespec.SinkSpec{
			IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
			DataSourceSlug:   slug.Slug("acme-postgres"),
			OperationType:    builderDefaultOperationType,
			OperationRef:     slug.Slug("upsert-users"),
		},
	}
	if fromStep != "" {
		step.Sink.Input = &pipelinespec.StepInput{FromStep: slug.Slug(fromStep)}
	}
	return step
}

func TestBuilderConnectorDescriptorsJSONMirrorsPipelineCapabilities(t *testing.T) {
	t.Parallel()

	var descriptors []builderConnectorDescriptor
	require.NoError(t, json.Unmarshal([]byte(builderConnectorDescriptorsJSON()), &descriptors))

	descriptorsByType := make(map[string]builderConnectorDescriptor)
	for _, descriptor := range descriptors {
		descriptorsByType[descriptor.Type] = descriptor
	}

	for _, capability := range pipelinespec.AllConnectorCapabilities() {
		descriptor, ok := descriptorsByType[capability.DataSourceType.String()]
		require.Truef(t, ok, "missing descriptor for %s", capability.DataSourceType)
		require.Equal(t, capability.SchemaFamily, descriptor.SchemaFamily)
		require.Equal(t, capability.QueryOperationType, descriptor.QueryOperationType)
		require.Equal(t, capability.SinkBackend.String(), descriptor.SinkBackend)
		require.Equal(t, capability.SupportsPipelineSource, descriptor.SupportsPipelineSource)
		require.Equal(t, capability.SupportsPipelineSink, descriptor.SupportsPipelineSink)
		require.Equal(t, capability.ForcedReadOnly, descriptor.ForcedReadOnly)

		wantKinds := make([]string, 0, len(capability.IncrementalSourceCheckpointKinds))
		for _, kind := range capability.IncrementalSourceCheckpointKinds {
			wantKinds = append(wantKinds, kind.String())
		}
		require.ElementsMatch(t, wantKinds, descriptor.IncrementalSourceCheckpointKinds)
		wantStrategies := make([]string, 0, len(capability.SupportedSinkReplayStrategies))
		for _, strategy := range capability.SupportedSinkReplayStrategies {
			wantStrategies = append(wantStrategies, strategy.String())
		}
		require.ElementsMatch(t, wantStrategies, descriptor.SupportedSinkReplayStrategies)
	}

	require.Len(t, descriptorsByType, len(pipelinespec.AllConnectorCapabilities()))
	require.False(t, descriptorsByType[model.DataSourceTypeHubSpot.String()].ForcedReadOnly)
	require.True(t, descriptorsByType[model.DataSourceTypeHubSpot.String()].SupportsPipelineSink)
	require.Equal(t, model.SinkBackendHTTP.String(), descriptorsByType[model.DataSourceTypeHubSpot.String()].SinkBackend)
	require.Equal(t, model.SinkBackendGit.String(), descriptorsByType[model.DataSourceTypeGithub.String()].SinkBackend)
}

func TestBuilderTransformInputsForSources(t *testing.T) {
	t.Parallel()

	t.Run("preserves explicit input for single source", func(t *testing.T) {
		t.Parallel()

		inputs, err := builderTransformInputsForSources(
			map[string]pipelinespec.StepInput{
				"input": {FromStep: slug.Slug("fetch-users")},
			},
			[]slug.Slug{slug.Slug("fetch-users")},
		)
		require.NoError(t, err)

		require.Equal(t, pipelinespec.StepInput{FromStep: slug.Slug("fetch-users")}, inputs["input"])
	})

	t.Run("requires explicit named inputs for single source", func(t *testing.T) {
		t.Parallel()

		_, err := builderTransformInputsForSources(nil, []slug.Slug{slug.Slug("fetch-users")})

		require.Error(t, err)
		require.ErrorContains(t, err, "upstream sources require explicit transform input names")
	})

	t.Run("requires explicit named inputs for multiple sources", func(t *testing.T) {
		t.Parallel()

		_, err := builderTransformInputsForSources(nil, []slug.Slug{
			slug.Slug("fetch-users"),
			slug.Slug("fetch-accounts"),
		})

		require.Error(t, err)
		require.ErrorContains(t, err, "upstream sources require explicit transform input names")
	})
}

func TestSerializeBuilderStateCanonical(t *testing.T) {
	t.Parallel()

	state := builderState{
		PipelineSpec: builderPipelineSpec{
			Metadata: pipelinespec.Metadata{
				Slug: slug.Slug("example-pipeline"),
				Name: "Example Pipeline",
			},
			Steps: []pipelinespec.Step{
				testQueryStep("fetch-users", "acme-postgres", "list-users"),
				testSinkStep("sync-users", "fetch-users"),
			},
		},
		Graph: builderGraph{
			Nodes: []builderGraphNode{
				{StepSlug: slug.Slug("fetch-users")},
				{StepSlug: slug.Slug("sync-users")},
			},
			Edges: []builderEdge{
				{FromStep: slug.Slug("fetch-users"), ToStep: slug.Slug("sync-users")},
			},
			Bindings: []builderBinding{
				{
					FromStep: slug.Slug("fetch-users"),
					FromPort: builderPortRows,
					ToStep:   slug.Slug("sync-users"),
					ToPort:   builderPortPayload,
				},
			},
		},
		Context: builderStateContext{
			ExpectedVersionID: stringPtr("spec-hash-12"),
			CurrentVersionID:  stringPtr("spec-hash-15"),
			SourceFormat:      pipelineFormatPtr(model.PipelineFormatYAML),
			ExportFormat:      pipelineFormatPtr(model.PipelineFormatJSON),
		},
	}

	got, err := serializeBuilderStateCanonical(state)
	require.NoError(t, err)

	require.JSONEq(t, `{
	  "pipeline_spec": {
	    "metadata": {
	      "slug": "example-pipeline",
	      "name": "Example Pipeline"
	    },
	    "steps": [
	      {
	        "kind": "query",
	        "slug": "fetch-users",
	        "query": {
	          "data_source_ref": "acme-postgres",
	          "operation_type": "sql",
	          "operation_ref": "list-users"
	        }
	      },
	      {
	        "kind": "sink",
	        "slug": "sync-users",
	        "sink": {
                    "idempotency_class": "idempotent_with_key",
	          "input": "fetch-users",
	          "data_source_ref": "acme-postgres",
	          "operation_type": "sql",
	          "operation_ref": "upsert-users"
	        }
	      }
	    ]
	  },
	  "graph": {
	    "nodes": [
	      {"step_slug": "fetch-users"},
	      {"step_slug": "sync-users"}
	    ],
	    "edges": [
	      {"from_step": "fetch-users", "to_step": "sync-users"}
	    ],
	    "bindings": [
	      {
	        "from_step": "fetch-users",
	        "from_port": "rows",
	        "to_step": "sync-users",
	        "to_port": "payload"
	      }
	    ]
	  },
	  "context": {
	    "expected_spec_hash": "spec-hash-12",
	    "current_version_spec_hash": "spec-hash-15",
	    "source_format": "yaml",
	    "export_format": "json",
	    "deterministic": false,
	    "record_run_metadata": false
	  }
	}`, string(got))
}

func TestSerializeBuilderStateComparable(t *testing.T) {
	t.Parallel()

	state := builderState{
		PipelineSpec: builderPipelineSpec{
			Metadata: pipelinespec.Metadata{
				Slug: slug.Slug("example-pipeline"),
				Name: "Example Pipeline",
			},
			Steps: []pipelinespec.Step{
				testQueryStep("fetch-users", "acme-postgres", "list-users"),
				testSinkStep("sync-users", "fetch-users"),
			},
		},
		Graph: builderGraph{
			Nodes: []builderGraphNode{
				{StepSlug: slug.Slug("fetch-users")},
				{StepSlug: slug.Slug("sync-users")},
			},
			Edges: []builderEdge{
				{FromStep: slug.Slug("fetch-users"), ToStep: slug.Slug("sync-users")},
			},
			Bindings: []builderBinding{
				{
					FromStep: slug.Slug("fetch-users"),
					FromPort: builderPortRows,
					ToStep:   slug.Slug("sync-users"),
					ToPort:   builderPortPayload,
				},
			},
		},
		Context: builderStateContext{
			ExpectedVersionID:     stringPtr("spec-hash-1"),
			CurrentVersionID:      stringPtr("spec-hash-2"),
			DraftBasedOnVersionID: stringPtr("spec-hash-3"),
		},
	}

	got, err := serializeBuilderStateComparable(state)
	require.NoError(t, err)
	require.NotContains(t, string(got), "expected_version_id")
	require.NotContains(t, string(got), "current_version_id")
	require.NotContains(t, string(got), "draft_based_on_version_id")
	require.Contains(t, string(got), `"pipeline_spec"`)
	require.Contains(t, string(got), `"graph"`)
}

func TestBuilderStateRoundTrip(t *testing.T) {
	t.Parallel()

	initial := builderState{
		PipelineSpec: builderPipelineSpec{
			Metadata: pipelinespec.Metadata{
				Slug: slug.Slug("example-pipeline"),
				Name: "Example Pipeline",
			},
			Steps: []pipelinespec.Step{
				testQueryStep("fetch-users", "acme-postgres", "list-users"),
				{
					Kind: builderKindTransform,
					Slug: slug.Slug("normalize-users"),
					Name: "Normalize Users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: slug.Slug("fetch-users")}}, SQL: "SELECT * FROM input",
						OutputKind: model.TransformOutputKindRows,
					},
				},
				testSinkStep("sync-users", "normalize-users"),
			},
		},
		Graph: builderGraph{
			Nodes: []builderGraphNode{
				{StepSlug: slug.Slug("fetch-users")},
				{StepSlug: slug.Slug("normalize-users")},
				{StepSlug: slug.Slug("sync-users")},
			},
			Edges: []builderEdge{
				{FromStep: slug.Slug("normalize-users"), ToStep: slug.Slug("sync-users")},
				{FromStep: slug.Slug("fetch-users"), ToStep: slug.Slug("normalize-users")},
			},
			Bindings: []builderBinding{
				{
					FromStep: slug.Slug("normalize-users"),
					FromPort: builderPortRows,
					ToStep:   slug.Slug("sync-users"),
					ToPort:   builderPortPayload,
				},
				{
					FromStep: slug.Slug("fetch-users"),
					FromPort: builderPortRows,
					ToStep:   slug.Slug("normalize-users"),
					ToPort:   "input",
				},
			},
		},
	}

	serialized, err := serializeBuilderStateCanonical(initial)
	require.NoError(t, err)

	decoded, err := deserializeBuilderState(serialized)
	require.NoError(t, err)

	reserialized, err := serializeBuilderStateCanonical(decoded)
	require.NoError(t, err)

	require.Equal(t, bytes.TrimSpace(serialized), bytes.TrimSpace(reserialized))
	require.Equal(t, canonicalizeBuilderState(initial), decoded)
}

func TestBuilderStateFromPipeline(t *testing.T) {
	t.Parallel()

	t.Run("rejects nil pipeline", func(t *testing.T) {
		t.Parallel()

		_, err := builderStateFromPipeline(nil)
		require.Error(t, err)
		require.ErrorIs(t, err, errBuilderStateInvalid)
		require.ErrorContains(t, err, "pipeline is required")
	})

	t.Run("maps canonical steps into graph references", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: slug.Slug("users-sync"),
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{
				testQueryStep("fetch-users", "acme-postgres", "list-users"),
				{
					Kind: builderKindTransform,
					Slug: slug.Slug("normalize-users"),
					Name: "Normalize users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: slug.Slug("fetch-users")}}, SQL: "SELECT id FROM input",
						OutputKind: model.TransformOutputKindCSV,
					},
				},
				testSinkStep("users-sync", "normalize-users"),
			},
		}

		state, err := builderStateFromPipeline(pipeline)
		require.NoError(t, err)
		require.Len(t, state.PipelineSpec.Steps, 3)
		require.Len(t, state.Graph.Nodes, 3)
		require.Len(t, state.Graph.Edges, 2)
		require.Len(t, state.Graph.Bindings, 2)
		require.Equal(t, slug.Slug("fetch-users"), state.Graph.Nodes[0].StepSlug)
		require.Equal(t, slug.Slug("normalize-users"), state.Graph.Nodes[1].StepSlug)
		require.Equal(t, slug.Slug("users-sync"), state.Graph.Nodes[2].StepSlug)
	})
}

func TestBuilderStateFromPipelineSpec(t *testing.T) {
	t.Parallel()

	t.Run("loads yaml fixture into split state", func(t *testing.T) {
		t.Parallel()

		payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")

		state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
		require.NoError(t, err)
		require.Equal(t, model.PipelineFormatYAML, *state.Context.SourceFormat)
		require.Equal(t, model.PipelineFormatYAML, *state.Context.ExportFormat)
		require.Len(t, state.PipelineSpec.Steps, 2)
		require.Len(t, state.Graph.Nodes, 2)
		require.Len(t, state.Graph.Edges, 1)
		require.Len(t, state.Graph.Bindings, 1)
	})

	t.Run("loads json fixture into split state", func(t *testing.T) {
		t.Parallel()

		payload := mustReadPipelineFixture(t, "single-query.pipeline.json")

		state, err := builderStateFromPipelineSpec("single-query.pipeline.json", payload)
		require.NoError(t, err)
		require.Equal(t, model.PipelineFormatJSON, *state.Context.SourceFormat)
		require.Equal(t, model.PipelineFormatJSON, *state.Context.ExportFormat)
	})

	t.Run("loads spec with multiple roots converging through named transform inputs", func(t *testing.T) {
		t.Parallel()

		payload := []byte(`metadata:
  slug: source-inspection
  name: Source Inspection
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: list-users
      output_contract:
        representation: rows
        fields:
          - name: id
            type: integer
            required: true
          - name: email
            type: string
            required: true
  - kind: query
    slug: fetch-accounts
    query:
      data_source_ref: acme-crm
      operation_type: http
      operation_inline:
        method: GET
        path: /accounts
      output_contract:
        representation: rows
        fields:
          - name: user_id
            type: integer
            required: true
          - name: name
            type: string
            required: true
  - kind: transform
    slug: enrich-users
    transform:
      inputs:
        users: fetch-users
        accounts: fetch-accounts
      sql: |
        SELECT users.id, users.email, accounts.name AS account_name
        FROM users
        JOIN accounts ON accounts.user_id = users.id
      output_kind: rows
      output_contract:
        representation: rows
        fields:
          - name: id
            type: integer
            required: true
          - name: email
            type: string
            required: true
          - name: account_name
            type: string
            required: true
  - kind: sink
    slug: write-enriched-users
    sink:
      idempotency_class: idempotent_with_key
      input: enrich-users
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: upsert-users
`)

		state, err := builderStateFromPipelineSpec("source-inspection.pipeline.yaml", payload)
		require.NoError(t, err)
		require.Len(t, state.PipelineSpec.Steps, 4)
		require.Len(t, state.Graph.Nodes, 4)
		require.Len(t, state.Graph.Edges, 3)
		require.Len(t, state.Graph.Bindings, 3)
	})
}

func TestValidateBuilderState(t *testing.T) {
	t.Parallel()

	t.Run("rejects duplicate step slug", func(t *testing.T) {
		t.Parallel()

		state := builderState{
			PipelineSpec: builderPipelineSpec{
				Metadata: pipelinespec.Metadata{
					Slug: slug.Slug("test-pipeline"),
					Name: "Test Pipeline",
				},
				Steps: []pipelinespec.Step{
					testQueryStep("fetch-users", "acme-postgres", "list-users"),
					testQueryStep("fetch-users", "acme-postgres", "list-teams"),
				},
			},
			Graph: builderGraph{
				Nodes: []builderGraphNode{
					{StepSlug: slug.Slug("fetch-users")},
				},
			},
		}

		err := validateBuilderState(state)
		require.Error(t, err)
		require.ErrorContains(t, err, "duplicate step slug")
	})

	t.Run("rejects sink without graph input", func(t *testing.T) {
		t.Parallel()

		state := builderState{
			PipelineSpec: builderPipelineSpec{
				Metadata: pipelinespec.Metadata{
					Slug: slug.Slug("test-pipeline"),
					Name: "Test Pipeline",
				},
				Steps: []pipelinespec.Step{
					testQueryStep("fetch-users", "acme-postgres", "list-users"),
					testSinkStep("test-pipeline", ""),
				},
			},
			Graph: builderGraph{
				Nodes: []builderGraphNode{
					{StepSlug: slug.Slug("fetch-users")},
					{StepSlug: slug.Slug("test-pipeline")},
				},
			},
		}

		err := validateBuilderState(state)
		require.Error(t, err)
		require.ErrorContains(t, err, "must have at least one incoming step dependency")
	})

	t.Run("rejects graph edge into query step", func(t *testing.T) {
		t.Parallel()

		state := builderState{
			PipelineSpec: builderPipelineSpec{
				Metadata: pipelinespec.Metadata{
					Slug: slug.Slug("test-pipeline"),
					Name: "Test Pipeline",
				},
				Steps: []pipelinespec.Step{
					testQueryStep("fetch-users", "acme-postgres", "list-users"),
					testQueryStep("fetch-accounts", "acme-crm", "list-accounts"),
				},
			},
			Graph: builderGraph{
				Nodes: []builderGraphNode{
					{StepSlug: slug.Slug("fetch-users")},
					{StepSlug: slug.Slug("fetch-accounts")},
				},
				Edges: []builderEdge{
					{FromStep: slug.Slug("fetch-users"), ToStep: slug.Slug("fetch-accounts")},
				},
			},
		}

		err := validateBuilderState(state)
		require.Error(t, err)
		require.ErrorContains(t, err, "query step \"fetch-accounts\" cannot have incoming dependencies")
	})
}

func TestBuilderStateToPipeline(t *testing.T) {
	t.Parallel()

	t.Run("round trips spec through builder state", func(t *testing.T) {
		t.Parallel()

		payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
		state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
		require.NoError(t, err)

		got, err := builderStateToPipeline(state)
		require.NoError(t, err)

		require.Equal(t, slug.Slug("acme-users-sync"), got.Metadata.Slug)
		require.Equal(t, "ACME Users Sync", got.Metadata.Name)
		require.Len(t, got.Steps, 2)
		require.Equal(t, slug.Slug("fetch-users"), got.Steps[0].Slug)
		require.Equal(t, builderKindSink, got.Steps[1].Kind)
		require.Equal(t, slug.Slug("fetch-users"), got.Steps[1].Sink.Input.FromStep)
	})

	t.Run("orders steps from spec order", func(t *testing.T) {
		t.Parallel()

		state := builderState{
			PipelineSpec: builderPipelineSpec{
				Metadata: pipelinespec.Metadata{
					Slug: slug.Slug("two-step"),
					Name: "Two Step",
				},
				Steps: []pipelinespec.Step{
					testQueryStep("fetch-users", "db", "list-users"),
					testQueryStep("send-emails", "email", "send"),
					testSinkStep("two-step", "send-emails"),
				},
			},
			Graph: builderGraph{
				Nodes: []builderGraphNode{
					{StepSlug: slug.Slug("fetch-users")},
					{StepSlug: slug.Slug("send-emails")},
					{StepSlug: slug.Slug("two-step")},
				},
				Edges: []builderEdge{
					{FromStep: slug.Slug("send-emails"), ToStep: slug.Slug("two-step")},
				},
				Bindings: []builderBinding{
					{
						FromStep: slug.Slug("send-emails"),
						FromPort: builderPortRows,
						ToStep:   slug.Slug("two-step"),
						ToPort:   builderPortPayload,
					},
				},
			},
		}
		state.PipelineSpec.Steps[1].Query.Parameters = map[string]any{
			"recipients": "$steps.fetch-users.rows",
		}

		pipeline, err := builderStateToPipeline(state)
		require.NoError(t, err)
		require.Len(t, pipeline.Steps, 3)
		// Steps should be in spec order: fetch-users, send-emails, two-step
		require.Equal(t, slug.Slug("fetch-users"), pipeline.Steps[0].Slug)
		require.Equal(t, slug.Slug("send-emails"), pipeline.Steps[1].Slug)
		require.Equal(t, slug.Slug("two-step"), pipeline.Steps[2].Slug)
	})
}

func TestSerializeBuilderPipelineSpec(t *testing.T) {
	t.Parallel()

	payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
	state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
	require.NoError(t, err)

	jsonOutput, err := serializeBuilderPipelineSpec(state, model.PipelineFormatJSON)
	require.NoError(t, err)
	require.NoError(t, validateBuilderPipelineSpec(model.PipelineFormatJSON, jsonOutput))

	yamlOutput, err := serializeBuilderPipelineSpec(state, model.PipelineFormatYAML)
	require.NoError(t, err)
	require.NoError(t, validateBuilderPipelineSpec(model.PipelineFormatYAML, yamlOutput))

	loadedJSON, err := pipelinevalidate.Load("builder-export.pipeline.json", jsonOutput)
	require.NoError(t, err)
	require.Equal(t, slug.Slug("acme-users-sync"), loadedJSON.Metadata.Slug)

	loadedYAML, err := pipelinevalidate.Load("builder-export.pipeline.yaml", yamlOutput)
	require.NoError(t, err)
	require.Equal(t, slug.Slug("acme-users-sync"), loadedYAML.Metadata.Slug)
}

func stringPtr(value string) *string {
	return &value
}

func pipelineFormatPtr(value model.PipelineFormat) *model.PipelineFormat {
	return &value
}

func TestDeserializeBuilderState(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
	  "pipeline_spec": {
	    "metadata": {
	      "slug": "example-pipeline",
	      "name": "Example Pipeline"
	    },
	    "steps": [
	      {
	        "kind": "query",
	        "slug": "fetch-users",
	        "query": {
	          "data_source_ref": "acme-postgres",
	          "operation_type": "sql",
	          "operation_ref": "list-users"
	        }
	      },
	      {
	        "kind": "sink",
	        "slug": "sync-users",
	        "sink": {
	          "idempotency_class": "idempotent_with_key"
	        }
	      }
	    ]
	  },
	  "graph": {
	    "nodes": [
	      {"step_slug": "fetch-users"},
	      {"step_slug": "sync-users"}
	    ],
	    "edges": [
	      {"from_step": "fetch-users", "to_step": "sync-users"}
	    ],
	    "bindings": [
	      {
	        "from_step": "fetch-users",
	        "from_port": "rows",
	        "to_step": "sync-users",
	        "to_port": "payload"
	      }
	    ]
	  }
	}`)

	state, err := deserializeBuilderState(payload)
	require.NoError(t, err)
	require.Len(t, state.PipelineSpec.Steps, 2)
	require.Equal(t, slug.Slug("fetch-users"), state.PipelineSpec.Steps[0].Slug)

	rawJSON, err := json.Marshal(state)
	require.NoError(t, err)
	require.Contains(t, string(rawJSON), `"pipeline_spec"`)
	require.Contains(t, string(rawJSON), `"graph"`)
}

func TestLayoutGraphLinearPipeline(t *testing.T) {
	t.Parallel()

	spec := &builderPipelineSpec{
		Metadata: pipelinespec.Metadata{
			Slug: slug.Slug("test-pipeline"),
			Name: "Test",
		},
		Steps: []pipelinespec.Step{
			testQueryStep("fetch-users", "db", "list-users"),
			{
				Kind: builderKindTransform,
				Slug: slug.Slug("normalize"),
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: slug.Slug("fetch-users")}}, SQL: "SELECT * FROM input",
					OutputKind: model.TransformOutputKindRows,
				},
			},
			testSinkStep("sync", "normalize"),
		},
	}

	positions := layoutGraph(spec)

	require.Len(t, positions, 3)
	// Query at level 0, column 2
	require.Equal(t, position{X: 2, Y: 1}, positions["fetch-users"])
	// Transform at level 1, column 9 (2 + 1*7)
	require.Equal(t, position{X: 9, Y: 1}, positions["normalize"])
	// Sink at level 2, column 16 (2 + 2*7)
	require.Equal(t, position{X: 16, Y: 1}, positions["sync"])
}

func TestLayoutGraphMultipleSourcesAtSameLevel(t *testing.T) {
	t.Parallel()

	spec := &builderPipelineSpec{
		Metadata: pipelinespec.Metadata{
			Slug: slug.Slug("multi-source"),
			Name: "Multi-Source",
		},
		Steps: []pipelinespec.Step{
			testQueryStep("fetch-users", "db", "list-users"),
			testQueryStep("fetch-teams", "db", "list-teams"),
			testSinkStep("sync", "fetch-users"),
		},
	}

	positions := layoutGraph(spec)

	require.Len(t, positions, 3)
	// Both queries are in the query column in spec order.
	require.Equal(t, 2, positions["fetch-users"].X)
	require.Equal(t, 1, positions["fetch-users"].Y)
	require.Equal(t, 2, positions["fetch-teams"].X)
	require.Equal(t, 4, positions["fetch-teams"].Y) // Y = 1 + 1*3 (second in level, gap of 3)
	// Sink is always in sink column.
	require.Equal(t, 16, positions["sync"].X)
}

func TestLayoutGraphQuerySinkPipeline_UsesSinkColumn(t *testing.T) {
	t.Parallel()

	spec := &builderPipelineSpec{
		Metadata: pipelinespec.Metadata{
			Slug: slug.Slug("query-sink"),
			Name: "Query Sink",
		},
		Steps: []pipelinespec.Step{
			testQueryStep("fetch-users", "db", "list-users"),
			testSinkStep("sync", "fetch-users"),
		},
	}

	positions := layoutGraph(spec)

	require.Len(t, positions, 2)
	require.Equal(t, position{X: 2, Y: 1}, positions["fetch-users"])
	require.Equal(t, position{X: 16, Y: 1}, positions["sync"])
}

func TestLayoutGraphEmptySpec(t *testing.T) {
	t.Parallel()

	spec := &builderPipelineSpec{}

	positions := layoutGraph(spec)

	require.Empty(t, positions)
}

func TestLayoutGraphSingleNode(t *testing.T) {
	t.Parallel()

	spec := &builderPipelineSpec{
		Metadata: pipelinespec.Metadata{
			Slug: slug.Slug("single"),
			Name: "Single",
		},
		Steps: []pipelinespec.Step{
			testQueryStep("fetch", "db", "list"),
		},
	}

	positions := layoutGraph(spec)

	require.Len(t, positions, 1)
	require.Equal(t, position{X: 2, Y: 1}, positions["fetch"])
}
