package pipelinebuilder

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestFromPipelineProjectsCanonicalSpecIntoDeterministicGraph(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: slug.Slug("users-sync"),
			Name: "Users Sync",
		},
		Steps: []pipelinespec.Step{
			testQueryStep("fetch-users"),
			testTransformStep("normalize-users", map[string]pipelinespec.StepInput{
				"users": {FromStep: slug.Slug("fetch-users")},
			}),
			testSinkStep("persist-users", "normalize-users"),
		},
	}

	state, err := FromPipeline(pipeline)
	require.NoError(t, err)

	require.Equal(t, slug.Slug("users-sync"), state.PipelineSpec.Metadata.Slug)
	require.Equal(t, []GraphNode{
		{StepSlug: slug.Slug("fetch-users")},
		{StepSlug: slug.Slug("normalize-users")},
		{StepSlug: slug.Slug("persist-users")},
	}, state.Graph.Nodes)
	require.Equal(t, []Edge{
		{FromStep: slug.Slug("fetch-users"), ToStep: slug.Slug("normalize-users")},
		{FromStep: slug.Slug("normalize-users"), ToStep: slug.Slug("persist-users")},
	}, state.Graph.Edges)
	require.Equal(t, []Binding{
		{
			FromStep: slug.Slug("fetch-users"),
			FromPort: "rows",
			ToStep:   slug.Slug("normalize-users"),
			ToPort:   "users",
		},
		{
			FromStep: slug.Slug("normalize-users"),
			FromPort: "rows",
			ToStep:   slug.Slug("persist-users"),
			ToPort:   "payload",
		},
	}, state.Graph.Bindings)
}

func TestInputStepRoundTripsThroughBuilderState(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "event-pipeline", Name: "Event pipeline"},
		Steps: []pipelinespec.Step{
			{
				Kind: "input",
				Slug: "event",
				Input: &pipelinespec.InputSpec{OutputContract: &payload.Contract{
					Name:           "event",
					Representation: model.ContractRepresentationJSON,
					RootShape:      model.ContractRootShapeObject,
					Fields:         []payload.ContractField{},
				}},
			},
			testSinkStep("persist-event", "event"),
		},
	}

	state, err := FromPipeline(pipeline)
	require.NoError(t, err)
	require.Empty(t, ValidateStateIssues(state))

	roundTripped, err := ToPipeline(state)
	require.NoError(t, err)
	require.Len(t, roundTripped.Steps, 2)
	require.NotNil(t, roundTripped.Steps[0].Input)
	require.Equal(t, pipeline.Steps[0].Input.OutputContract, roundTripped.Steps[0].Input.OutputContract)
	require.Equal(t, slug.Slug("event"), roundTripped.Steps[1].Sink.Input.FromStep)
}

func TestFromPipelinePreservesAuthoredStepOrderInGraphNodes(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: slug.Slug("users-sync"), Name: "Users Sync"},
		Steps: []pipelinespec.Step{
			testQueryStep("fetch-users"),
			testSinkStep("acme-users-sync", "fetch-users"),
		},
	}

	state, err := FromPipeline(pipeline)
	require.NoError(t, err)
	require.Equal(t, []GraphNode{
		{StepSlug: slug.Slug("fetch-users")},
		{StepSlug: slug.Slug("acme-users-sync")},
	}, state.Graph.Nodes)
}

func TestToPipelineKeepsCanonicalSpecAsSemanticSourceOfTruth(t *testing.T) {
	t.Parallel()

	state := State{
		PipelineSpec: PipelineSpec{
			Metadata: pipelinespec.Metadata{
				Slug: slug.Slug("users-sync"),
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{
				testSinkStep("persist-users", ""),
				testTransformStep("normalize-users", map[string]pipelinespec.StepInput{
					"users": {FromStep: slug.Slug("fetch-users")},
				}),
				testQueryStep("fetch-users"),
			},
		},
		Graph: Graph{
			Nodes: []GraphNode{
				{StepSlug: slug.Slug("persist-users")},
				{StepSlug: slug.Slug("normalize-users")},
				{StepSlug: slug.Slug("fetch-users")},
			},
			Edges: []Edge{
				{FromStep: slug.Slug("fetch-users"), ToStep: slug.Slug("normalize-users")},
				{FromStep: slug.Slug("normalize-users"), ToStep: slug.Slug("persist-users")},
			},
			Bindings: []Binding{
				{
					FromStep: slug.Slug("fetch-users"),
					FromPort: "rows",
					ToStep:   slug.Slug("normalize-users"),
					ToPort:   "users",
				},
				{
					FromStep: slug.Slug("normalize-users"),
					FromPort: "rows",
					ToStep:   slug.Slug("persist-users"),
					ToPort:   "payload",
				},
			},
		},
	}

	pipeline, err := ToPipeline(state)
	require.NoError(t, err)

	require.Equal(t, []slug.Slug{
		slug.Slug("persist-users"),
		slug.Slug("normalize-users"),
		slug.Slug("fetch-users"),
	}, []slug.Slug{
		pipeline.Steps[0].Slug,
		pipeline.Steps[1].Slug,
		pipeline.Steps[2].Slug,
	})
	require.Equal(t, map[string]pipelinespec.StepInput{
		"users": {FromStep: slug.Slug("fetch-users")},
	}, pipeline.Steps[1].Transform.Inputs)
	require.Equal(t, slug.Slug("normalize-users"), pipeline.Steps[0].Sink.Input.FromStep)
}

func TestFromPipelineProjectsOneProducerToMultipleSinkTargets(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: slug.Slug("fanout-sync"),
			Name: "Fanout Sync",
		},
		Steps: []pipelinespec.Step{
			testQueryStep("fetch-users"),
			testTransformStep("normalize-users", map[string]pipelinespec.StepInput{
				"users": {FromStep: slug.Slug("fetch-users")},
			}),
			testSinkStep("write-warehouse", "normalize-users"),
			testSinkStep("notify-http", "normalize-users"),
		},
	}

	state, err := FromPipeline(pipeline)
	require.NoError(t, err)

	require.Equal(t, []Edge{
		{FromStep: slug.Slug("fetch-users"), ToStep: slug.Slug("normalize-users")},
		{FromStep: slug.Slug("normalize-users"), ToStep: slug.Slug("notify-http")},
		{FromStep: slug.Slug("normalize-users"), ToStep: slug.Slug("write-warehouse")},
	}, state.Graph.Edges)
	require.Equal(t, []Binding{
		{
			FromStep: slug.Slug("fetch-users"),
			FromPort: "rows",
			ToStep:   slug.Slug("normalize-users"),
			ToPort:   "users",
		},
		{
			FromStep: slug.Slug("normalize-users"),
			FromPort: "rows",
			ToStep:   slug.Slug("notify-http"),
			ToPort:   "payload",
		},
		{
			FromStep: slug.Slug("normalize-users"),
			FromPort: "rows",
			ToStep:   slug.Slug("write-warehouse"),
			ToPort:   "payload",
		},
	}, state.Graph.Bindings)

	rebuilt, err := ToPipeline(state)
	require.NoError(t, err)
	require.Len(t, rebuilt.Steps, 4)
	require.Equal(t, slug.Slug("normalize-users"), rebuilt.Steps[2].Sink.Input.FromStep)
	require.Equal(t, slug.Slug("normalize-users"), rebuilt.Steps[3].Sink.Input.FromStep)
}

func TestStateSerializationUsesSpecHashVersionContext(t *testing.T) {
	t.Parallel()

	sourceFormat := model.PipelineFormatYAML
	exportFormat := model.PipelineFormatJSON
	timeoutSecs := 30
	expectedSpecHash := "spec-hash-12"
	currentSpecHash := "spec-hash-13"
	draftBasedOnSpecHash := "spec-hash-11"
	state := State{
		PipelineSpec: PipelineSpec{
			Metadata: pipelinespec.Metadata{
				Slug: slug.Slug("users-sync"),
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{testQueryStep("fetch-users")},
		},
		Graph: Graph{
			Nodes: []GraphNode{{StepSlug: slug.Slug("fetch-users")}},
		},
		Context: Context{
			ExpectedVersionID:     &expectedSpecHash,
			CurrentVersionID:      &currentSpecHash,
			DraftBasedOnVersionID: &draftBasedOnSpecHash,
			SourceFormat:          &sourceFormat,
			ExportFormat:          &exportFormat,
			Deterministic:         true,
			TimeoutSecs:           &timeoutSecs,
			RecordMeta:            true,
		},
	}

	canonical, err := SerializeStateCanonical(state)
	require.NoError(t, err)
	require.Contains(t, string(canonical), `"expected_spec_hash": "spec-hash-12"`)
	require.Contains(t, string(canonical), `"current_version_spec_hash": "spec-hash-13"`)
	require.Contains(t, string(canonical), `"draft_based_on_spec_hash": "spec-hash-11"`)
	require.NotContains(t, string(canonical), "expected_version_id")
	require.NotContains(t, string(canonical), "current_version_id")
	require.NotContains(t, string(canonical), "draft_based_on_version_id")

	var decoded State
	require.NoError(t, json.Unmarshal(canonical, &decoded))
	require.Equal(t, expectedSpecHash, *decoded.Context.ExpectedVersionID)
	require.Equal(t, currentSpecHash, *decoded.Context.CurrentVersionID)
	require.Equal(t, draftBasedOnSpecHash, *decoded.Context.DraftBasedOnVersionID)

	comparableState, err := SerializeStateComparable(decoded)
	require.NoError(t, err)
	require.NotContains(t, string(comparableState), "spec_hash")
}

func TestStateCodecAndPipelineExportRoundTrip(t *testing.T) {
	t.Parallel()

	state := State{
		PipelineSpec: PipelineSpec{
			Metadata: pipelinespec.Metadata{
				Slug: slug.Slug("users-sync"),
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{
				testQueryStep("fetch-users"),
				testTransformStep("normalize-users", map[string]pipelinespec.StepInput{
					"users": {FromStep: slug.Slug("fetch-users")},
				}),
				testSinkStep("persist-users", "normalize-users"),
			},
		},
		Graph: Graph{
			Nodes: []GraphNode{
				{StepSlug: slug.Slug("fetch-users")},
				{StepSlug: slug.Slug("normalize-users")},
				{StepSlug: slug.Slug("persist-users")},
			},
			Edges: []Edge{
				{FromStep: slug.Slug("fetch-users"), ToStep: slug.Slug("normalize-users")},
				{FromStep: slug.Slug("normalize-users"), ToStep: slug.Slug("persist-users")},
			},
		},
	}

	state.PipelineSpec.Steps[2].Sink.EmptyInputPolicy = "reject"
	payload, err := SerializeStateCanonical(state)
	require.NoError(t, err)

	decoded, err := DeserializeState(payload)
	require.NoError(t, err)
	require.Equal(t, slug.Slug("users-sync"), decoded.PipelineSpec.Metadata.Slug)
	require.Equal(t, "reject", decoded.PipelineSpec.Steps[2].Sink.EmptyInputPolicy)

	pipelinePayload, err := SerializePipelineSpec(decoded, model.PipelineFormatJSON)
	require.NoError(t, err)
	require.Contains(t, string(pipelinePayload), `"slug": "users-sync"`)
	require.Contains(t, string(pipelinePayload), `"empty_input_policy": "reject"`)

	require.NoError(t, ValidatePipelineSpec(model.PipelineFormatJSON, pipelinePayload))
	require.NoError(t, ValidateState(decoded))
	require.Empty(t, ValidateStateIssues(decoded))
}

func TestStateSerializationCanonicalizesAndRoundTripsFanOutExport(t *testing.T) {
	t.Parallel()

	sourceFormat := model.PipelineFormatYAML
	exportFormat := model.PipelineFormatJSON
	state := State{
		PipelineSpec: PipelineSpec{
			Metadata: pipelinespec.Metadata{
				Slug: slug.Slug(" users-sync "),
				Name: " Users Sync ",
			},
			Steps: []pipelinespec.Step{
				testSinkStep("persist-users", "normalize-users"),
				testTransformStep("normalize-users", map[string]pipelinespec.StepInput{
					"users": {FromStep: slug.Slug("fetch-users")},
				}),
				testQueryStep("fetch-users"),
			},
		},
		Graph: Graph{
			Nodes: []GraphNode{
				{StepSlug: slug.Slug(" persist-users ")},
				{StepSlug: slug.Slug(" normalize-users ")},
				{StepSlug: slug.Slug(" fetch-users ")},
			},
			Edges: []Edge{
				{FromStep: slug.Slug(" normalize-users "), ToStep: slug.Slug(" persist-users ")},
				{FromStep: slug.Slug(" fetch-users "), ToStep: slug.Slug(" normalize-users ")},
			},
			Bindings: []Binding{
				{
					FromStep: slug.Slug(" normalize-users "),
					FromPort: " rows ",
					ToStep:   slug.Slug(" persist-users "),
					ToPort:   " payload ",
				},
				{
					FromStep: slug.Slug(" fetch-users "),
					FromPort: " rows ",
					ToStep:   slug.Slug(" normalize-users "),
					ToPort:   " users ",
				},
			},
		},
		Context: Context{
			SourceFormat:  &sourceFormat,
			ExportFormat:  &exportFormat,
			Deterministic: true,
			RecordMeta:    true,
		},
	}

	canonicalJSON, err := SerializeStateCanonical(state)
	require.NoError(t, err)
	require.Contains(t, string(canonicalJSON), `"record_run_metadata": true`)
	require.Contains(t, string(canonicalJSON), `"step_slug": "fetch-users"`)

	comparableJSON, err := SerializeStateComparable(state)
	require.NoError(t, err)
	require.NotContains(t, string(comparableJSON), "record_run_metadata")

	decoded, err := DeserializeState(canonicalJSON)
	require.NoError(t, err)
	require.Equal(t, slug.Slug("users-sync"), decoded.PipelineSpec.Metadata.Slug)
	require.Equal(t, "Users Sync", decoded.PipelineSpec.Metadata.Name)

	exportedJSON, err := SerializePipelineSpec(decoded, ResolvePipelineExportFormat(decoded.Context))
	require.NoError(t, err)
	require.True(t, json.Valid(exportedJSON))
	require.NoError(t, ValidatePipelineSpec(model.PipelineFormatJSON, exportedJSON))

	imported, err := FromPipelineSpec("users-sync.pipeline.json", exportedJSON)
	require.NoError(t, err)
	require.NotNil(t, imported.Context.SourceFormat)
	require.Equal(t, model.PipelineFormatJSON, *imported.Context.SourceFormat)
	require.Equal(t, decoded.Graph.Edges, imported.Graph.Edges)

	exportedYAML, err := SerializePipelineSpec(decoded, model.PipelineFormatYAML)
	require.NoError(t, err)
	require.Contains(t, string(exportedYAML), "slug: users-sync")
	require.NoError(t, ValidatePipelineSpec(model.PipelineFormatYAML, exportedYAML))
}

func TestFromPipelineSpecLoadsYAMLAndRecordsSourceFormat(t *testing.T) {
	t.Parallel()

	state, err := FromPipelineSpec("customer-sync.pipeline.yaml", []byte(`
metadata:
  slug: customer-sync
  name: Customer Sync
steps:
  - kind: query
    slug: fetch-users
    query:
      data_source_ref: warehouse
      operation_type: sql
      operation_ref: list-users
      output_contract:
        fields:
          - name: id
            type: integer
            required: true
  - kind: transform
    slug: normalize-users
    transform:
      inputs:
        users: fetch-users
      sql: SELECT id FROM users
      output_kind: rows
      output_contract:
        fields:
          - name: id
            type: integer
            required: true
  - kind: sink
    slug: persist-users
    sink:
      idempotency_class: idempotent_with_key
      input: normalize-users
      data_source_ref: acme-postgres
      operation_type: sql
      operation_ref: upsert-users
`))
	require.NoError(t, err)
	require.Equal(t, model.PipelineFormatYAML, *state.Context.SourceFormat)
	require.Equal(t, model.PipelineFormatYAML, *state.Context.ExportFormat)
	require.Len(t, state.Graph.Edges, 2)
	require.Len(t, state.Graph.Bindings, 2)
}

func TestPipelineBuilderUtilityBranches(t *testing.T) {
	t.Parallel()

	require.Equal(t, model.PipelineFormatYAML, ResolvePipelineExportFormat(Context{}))
	jsonFormat := model.PipelineFormatJSON
	yamlFormat := model.PipelineFormatYAML
	require.Equal(t, model.PipelineFormatJSON, ResolvePipelineExportFormat(Context{
		SourceFormat: &yamlFormat,
		ExportFormat: &jsonFormat,
	}))
	require.Equal(t, model.PipelineFormatYAML, ResolvePipelineExportFormat(Context{
		SourceFormat: &yamlFormat,
	}))

	format, err := PipelineFormatFromPath("pipelines/customer-sync.pipeline.yml")
	require.NoError(t, err)
	require.Equal(t, model.PipelineFormatYAML, format)
	format, err = PipelineFormatFromPath("pipelines/customer-sync.pipeline.json")
	require.NoError(t, err)
	require.Equal(t, model.PipelineFormatJSON, format)
	_, err = PipelineFormatFromPath("pipelines/customer-sync.txt")
	require.Error(t, err)

	require.Equal(t, model.SinkBackendDB, ResolveSinkBackend(" SQL "))
	require.Equal(t, model.SinkBackendMongo, ResolveSinkBackend("mongodb"))
	require.Equal(t, model.SinkBackendHTTP, ResolveSinkBackend("http"))
	require.Equal(t, model.SinkBackendFTP, ResolveSinkBackend("ftp"))
	require.Equal(t, model.SinkBackendGit, ResolveSinkBackend("git"))
	require.Equal(t, model.SinkBackendUnknown, ResolveSinkBackend("unknown"))

	require.Equal(t, "SELECT 1", InlineSQLFrom(map[string]any{"sql": " SELECT 1 "}))
	require.Empty(t, InlineSQLFrom(map[string]any{"sql": 1}))
	require.Empty(t, InlineSQLFrom(map[string]any{"text": "SELECT 1"}))
	require.Empty(t, InlineSQLFrom(nil))
}

func TestPipelineBuilderAdditionalUtilityBranches(t *testing.T) {
	t.Parallel()

	sourceFormat := model.PipelineFormatJSON
	require.Equal(t, model.PipelineFormatJSON, ResolvePipelineExportFormat(Context{
		SourceFormat: &sourceFormat,
	}))

	format, err := PipelineFormatFromPath("pipeline.yaml")
	require.NoError(t, err)
	require.Equal(t, model.PipelineFormatYAML, format)

	require.Empty(t, InlineSQLFrom(map[string]any{"path": "/users"}))
}

func TestTransformInputsForSourcesPreservesExistingNamesAndRejectsUnnamedSources(t *testing.T) {
	t.Parallel()

	inputs, err := TransformInputsForSources(
		map[string]pipelinespec.StepInput{
			"users":       {FromStep: slug.Slug("fetch-users")},
			"users_alias": {FromStep: slug.Slug("fetch-users")},
			"orders":      {FromStep: slug.Slug("fetch-orders")},
		},
		[]slug.Slug{
			slug.Slug("fetch-users"),
			slug.Slug("fetch-users"),
			slug.Slug("fetch-orders"),
		},
	)
	require.NoError(t, err)
	require.Equal(t, map[string]pipelinespec.StepInput{
		"users":       {FromStep: slug.Slug("fetch-users")},
		"users_alias": {FromStep: slug.Slug("fetch-users")},
		"orders":      {FromStep: slug.Slug("fetch-orders")},
	}, inputs)

	_, err = TransformInputsForSources(
		map[string]pipelinespec.StepInput{"users": {FromStep: slug.Slug("fetch-users")}},
		[]slug.Slug{slug.Slug("fetch-missing")},
	)
	require.Error(t, err)
}

func TestDeserializeStateReportsValidationIssues(t *testing.T) {
	t.Parallel()

	_, err := DeserializeState([]byte("   "))
	require.Error(t, err)

	_, err = DeserializeStatePermissive([]byte("   "))
	require.Error(t, err)

	_, err = DeserializeStatePermissive([]byte(`{`))
	require.Error(t, err)

	_, issues, err := DeserializeStateWithIssues([]byte(`{
	  "pipeline_spec": {
	    "metadata": {"slug": "customer-sync", "name": "Customer Sync"},
	    "steps": [
	      {"kind": "query", "slug": "fetch-users", "query": {"data_source_ref": "warehouse", "operation_type": "sql", "operation_ref": "list-users"}}
	    ]
	  },
	  "graph": {
	    "nodes": [{"step_slug": "missing-step"}],
	    "edges": [],
	    "bindings": []
	  }
	}`))
	require.Error(t, err)
	require.Len(t, issues, 1)
	require.Equal(t, "GRAPH_NODE_STEP_NOT_FOUND", issues[0].Code)
}

func TestValidateStateIssuesCoversGraphContractFailures(t *testing.T) {
	t.Parallel()

	base := func() State {
		return State{
			PipelineSpec: PipelineSpec{
				Metadata: pipelinespec.Metadata{Slug: slug.Slug("customer-sync"), Name: "Customer Sync"},
				Steps: []pipelinespec.Step{
					testQueryStep("fetch-users"),
					testTransformStep("normalize-users", map[string]pipelinespec.StepInput{
						"users": {FromStep: slug.Slug("fetch-users")},
					}),
					testSinkStep("persist-users", "normalize-users"),
				},
			},
			Graph: Graph{
				Nodes: []GraphNode{
					{StepSlug: slug.Slug("fetch-users")},
					{StepSlug: slug.Slug("normalize-users")},
					{StepSlug: slug.Slug("persist-users")},
				},
				Edges: []Edge{
					{FromStep: slug.Slug("fetch-users"), ToStep: slug.Slug("normalize-users")},
					{FromStep: slug.Slug("normalize-users"), ToStep: slug.Slug("persist-users")},
				},
				Bindings: []Binding{
					{
						FromStep: slug.Slug("fetch-users"),
						FromPort: builderPortRows,
						ToStep:   slug.Slug("normalize-users"),
						ToPort:   "users",
					},
					{
						FromStep: slug.Slug("normalize-users"),
						FromPort: builderPortRows,
						ToStep:   slug.Slug("persist-users"),
						ToPort:   builderPortPayload,
					},
				},
			},
		}
	}

	tests := []struct {
		name   string
		mutate func(*State)
		code   string
	}{
		{
			name: "duplicate step slug",
			mutate: func(state *State) {
				state.PipelineSpec.Steps[1].Slug = slug.Slug("fetch-users")
			},
			code: "DUPLICATE_STEP_SLUG",
		},
		{
			name: "duplicate graph node",
			mutate: func(state *State) {
				state.Graph.Nodes[1].StepSlug = slug.Slug("fetch-users")
			},
			code: "DUPLICATE_GRAPH_NODE_STEP_SLUG",
		},
		{
			name: "missing graph node for step",
			mutate: func(state *State) {
				state.Graph.Nodes = state.Graph.Nodes[:2]
			},
			code: "STEP_GRAPH_NODE_REQUIRED",
		},
		{
			name: "edge source missing",
			mutate: func(state *State) {
				state.Graph.Edges[0].FromStep = slug.Slug("missing-step")
			},
			code: "EDGE_SOURCE_NOT_FOUND",
		},
		{
			name: "query incoming edge",
			mutate: func(state *State) {
				state.Graph.Edges = []Edge{{FromStep: slug.Slug("normalize-users"), ToStep: slug.Slug("fetch-users")}}
			},
			code: "QUERY_INCOMING_DEPENDENCY_FORBIDDEN",
		},
		{
			name: "input incoming edge",
			mutate: func(state *State) {
				state.PipelineSpec.Steps[0] = testInputStep("fetch-users")
				state.Graph.Edges = []Edge{{FromStep: slug.Slug("normalize-users"), ToStep: slug.Slug("fetch-users")}}
			},
			code: "INPUT_INCOMING_DEPENDENCY_FORBIDDEN",
		},
		{
			name: "sink outgoing edge",
			mutate: func(state *State) {
				state.Graph.Edges = []Edge{{FromStep: slug.Slug("persist-users"), ToStep: slug.Slug("normalize-users")}}
			},
			code: "SINK_OUTGOING_DEPENDENCY_FORBIDDEN",
		},
		{
			name: "binding source missing",
			mutate: func(state *State) {
				state.Graph.Bindings[0].FromStep = slug.Slug("missing-step")
			},
			code: "BINDING_SOURCE_NOT_FOUND",
		},
		{
			name: "binding target query forbidden",
			mutate: func(state *State) {
				state.Graph.Bindings[0].ToStep = slug.Slug("fetch-users")
			},
			code: "BINDING_TARGET_QUERY_FORBIDDEN",
		},
		{
			name: "binding target input forbidden",
			mutate: func(state *State) {
				state.PipelineSpec.Steps[0] = testInputStep("fetch-users")
				state.Graph.Bindings[0].ToStep = slug.Slug("fetch-users")
			},
			code: "BINDING_TARGET_INPUT_FORBIDDEN",
		},
		{
			name: "sink source forbidden",
			mutate: func(state *State) {
				state.Graph.Bindings[0].FromStep = slug.Slug("persist-users")
			},
			code: "BINDING_SOURCE_SINK_FORBIDDEN",
		},
		{
			name: "transform source port invalid",
			mutate: func(state *State) {
				state.Graph.Bindings[0].FromPort = "payload"
			},
			code: "STEP_BINDING_SOURCE_PORT_INVALID",
		},
		{
			name: "sink target port invalid",
			mutate: func(state *State) {
				state.Graph.Bindings[1].ToPort = builderPortRows
			},
			code: "SINK_BINDING_TARGET_PORT_INVALID",
		},
		{
			name: "DuplicateTransformInputPort",
			mutate: func(state *State) {
				state.Graph.Bindings = append(state.Graph.Bindings, Binding{
					FromStep: slug.Slug("fetch-users"),
					FromPort: builderPortRows,
					ToStep:   slug.Slug("normalize-users"),
					ToPort:   "users",
				})
			},
			code: "MULTIPLE_BINDINGS_FOR_TARGET_PORT",
		},
		{
			name: "MissingDeclaredTransformInputBinding",
			mutate: func(state *State) {
				state.PipelineSpec.Steps[1].Transform.Inputs["users_alias"] = pipelinespec.StepInput{
					FromStep: slug.Slug("fetch-users"),
				}
			},
			code: "STEP_BINDING_REQUIRED",
		},
		{
			name: "sink payload required",
			mutate: func(state *State) {
				state.Graph.Bindings = state.Graph.Bindings[:1]
			},
			code: "SINK_PAYLOAD_BINDING_REQUIRED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state := base()
			tt.mutate(&state)
			issues := ValidateStateIssues(state)
			require.NotEmpty(t, issues)
			require.Equal(t, tt.code, issues[0].Code)
		})
	}
}

func TestValidateStateRejectsWhitespaceOnlyTransformSQL(t *testing.T) {
	t.Parallel()

	state := validBuilderState()
	state.PipelineSpec.Steps[1].Transform.SQL = " \n\t "

	issues := ValidateStateIssues(state)
	require.NotEmpty(t, issues)
	require.Equal(t, "TRANSFORM_SQL_REQUIRED", issues[0].Code)
}

func TestValidateStateRejectsCyclicTransformDependencies(t *testing.T) {
	t.Parallel()

	state := validBuilderState()
	state.PipelineSpec.Steps = []pipelinespec.Step{
		testTransformStep("normalize-a", map[string]pipelinespec.StepInput{
			"b": {FromStep: slug.Slug("normalize-b")},
		}),
		testTransformStep("normalize-b", map[string]pipelinespec.StepInput{
			"a": {FromStep: slug.Slug("normalize-a")},
		}),
	}
	state.Graph = Graph{
		Nodes: []GraphNode{{StepSlug: "normalize-a"}, {StepSlug: "normalize-b"}},
		Edges: []Edge{
			{FromStep: "normalize-a", ToStep: "normalize-b"},
			{FromStep: "normalize-b", ToStep: "normalize-a"},
		},
		Bindings: []Binding{
			{FromStep: "normalize-a", FromPort: builderPortRows, ToStep: "normalize-b", ToPort: "a"},
			{FromStep: "normalize-b", FromPort: builderPortRows, ToStep: "normalize-a", ToPort: "b"},
		},
	}

	issues := ValidateStateIssues(state)
	require.NotEmpty(t, issues)
	require.Equal(t, "CYCLIC_STEP_DEPENDENCY", issues[0].Code)
	_, err := ToPipeline(state)
	require.Error(t, err)
}

func TestLayoutGraphPlacesPipelineKindsInStableColumns(t *testing.T) {
	t.Parallel()

	positions := LayoutGraph(&PipelineSpec{
		Steps: []pipelinespec.Step{
			testQueryStep("fetch-users"),
			testTransformStep("normalize-users", map[string]pipelinespec.StepInput{
				"users": {FromStep: slug.Slug("fetch-users")},
			}),
			testSinkStep("persist-users", "normalize-users"),
			{
				Kind: "custom",
				Slug: slug.Slug("custom-source"),
			},
		},
	})

	require.Equal(t, Position{X: 2, Y: 1}, positions["fetch-users"])
	require.Equal(t, Position{X: 2, Y: 4}, positions["custom-source"])
	require.Equal(t, Position{X: 9, Y: 1}, positions["normalize-users"])
	require.Equal(t, Position{X: 16, Y: 1}, positions["persist-users"])
	require.Empty(t, LayoutGraph(nil))
	require.Empty(t, LayoutGraph(&PipelineSpec{}))
}

func TestToDraftPipelineToleratesIncompleteBuilderEdges(t *testing.T) {
	t.Parallel()

	state := validBuilderState()
	state.Graph.Edges = state.Graph.Edges[:1]
	state.Graph.Bindings = state.Graph.Bindings[:1]

	_, err := ToPipeline(state)
	require.Error(t, err)

	draft, err := ToDraftPipeline(state)
	require.NoError(t, err)
	require.Len(t, draft.Steps, 3)
	require.Equal(t, slug.Slug("normalize-users"), draft.Steps[2].Sink.Input.FromStep)
}

func TestToDraftPipelinePreservesDeclaredTransformInputsWhenBindingsArePartial(t *testing.T) {
	t.Parallel()

	state := validBuilderState()
	state.PipelineSpec.Steps[1].Transform.Inputs["users_alias"] = pipelinespec.StepInput{
		FromStep: slug.Slug("fetch-users"),
	}

	draft, err := ToDraftPipeline(state)
	require.NoError(t, err)
	require.Equal(t, map[string]pipelinespec.StepInput{
		"users":       {FromStep: slug.Slug("fetch-users")},
		"users_alias": {FromStep: slug.Slug("fetch-users")},
	}, draft.Steps[1].Transform.Inputs)
}

func TestValidateStateIssuesForFanOutEdgeBoundaries(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		mutate func(*State)
		code   string
	}{
		{
			name: "SinkOutgoingEdge",
			mutate: func(state *State) {
				state.Graph.Edges = append(state.Graph.Edges, Edge{
					FromStep: slug.Slug("persist-users"),
					ToStep:   slug.Slug("archive-users"),
				})
				state.Graph.Nodes = append(state.Graph.Nodes, GraphNode{
					StepSlug: slug.Slug("archive-users"),
				})
				state.PipelineSpec.Steps = append(state.PipelineSpec.Steps, testSinkStep(
					"archive-users",
					"persist-users",
				))
			},
			code: "SINK_OUTGOING_DEPENDENCY_FORBIDDEN",
		},
		{
			name: "OneSinkCannotHaveTwoInputs",
			mutate: func(state *State) {
				state.Graph.Edges = append(state.Graph.Edges, Edge{
					FromStep: slug.Slug("fetch-users"),
					ToStep:   slug.Slug("persist-users"),
				})
			},
			code: "MULTIPLE_INCOMING_DEPENDENCIES",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state := validBuilderState()
			tt.mutate(&state)

			issues := ValidateStateIssues(state)
			require.NotEmpty(t, issues)
			require.Equal(t, tt.code, issues[0].Code)
		})
	}
}

func testQueryStep(stepSlug string) pipelinespec.Step {
	return pipelinespec.Step{
		Kind: "query",
		Slug: slug.Slug(stepSlug),
		Query: &pipelinespec.QueryRef{
			DataSourceSlug: slug.Slug("warehouse"),
			OperationType:  "sql",
			OperationRef:   slug.Slug("list-users"),
			OutputContract: &payload.Contract{Fields: []payload.ContractField{{
				Name:     "id",
				Type:     model.ContractFieldTypeValueInteger,
				Required: true,
			}}},
		},
	}
}

func testInputStep(stepSlug string) pipelinespec.Step {
	return pipelinespec.Step{
		Kind: "input",
		Slug: slug.Slug(stepSlug),
		Input: &pipelinespec.InputSpec{OutputContract: &payload.Contract{
			Name:           stepSlug,
			Representation: model.ContractRepresentationJSON,
		}},
	}
}

func testTransformStep(
	stepSlug string,
	inputs map[string]pipelinespec.StepInput,
) pipelinespec.Step {
	return pipelinespec.Step{
		Kind: "transform",
		Slug: slug.Slug(stepSlug),
		Transform: &pipelinespec.TransformSpec{
			Inputs:     inputs,
			SQL:        "SELECT id FROM users",
			OutputKind: model.TransformOutputKindRows,
			OutputContract: &payload.Contract{Fields: []payload.ContractField{{
				Name:     "id",
				Type:     model.ContractFieldTypeValueInteger,
				Required: true,
			}}},
		},
	}
}

func testSinkStep(stepSlug, fromStep string) pipelinespec.Step {
	step := pipelinespec.Step{
		Kind: "sink",
		Slug: slug.Slug(stepSlug),
		Sink: &pipelinespec.SinkSpec{
			IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
			DataSourceSlug:   slug.Slug("warehouse"),
			OperationType:    "sql",
			OperationRef:     slug.Slug("upsert-users"),
		},
	}
	if fromStep != "" {
		step.Sink.Input = &pipelinespec.StepInput{FromStep: slug.Slug(fromStep)}
	}
	return step
}

func validBuilderState() State {
	return State{
		PipelineSpec: PipelineSpec{
			Metadata: pipelinespec.Metadata{
				Slug: slug.Slug("users-sync"),
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{
				testQueryStep("fetch-users"),
				testTransformStep("normalize-users", map[string]pipelinespec.StepInput{
					"users": {FromStep: slug.Slug("fetch-users")},
				}),
				testSinkStep("persist-users", "normalize-users"),
			},
		},
		Graph: Graph{
			Nodes: []GraphNode{
				{StepSlug: slug.Slug("fetch-users")},
				{StepSlug: slug.Slug("normalize-users")},
				{StepSlug: slug.Slug("persist-users")},
			},
			Edges: []Edge{
				{FromStep: slug.Slug("fetch-users"), ToStep: slug.Slug("normalize-users")},
				{FromStep: slug.Slug("normalize-users"), ToStep: slug.Slug("persist-users")},
			},
			Bindings: []Binding{
				{
					FromStep: slug.Slug("fetch-users"),
					FromPort: "rows",
					ToStep:   slug.Slug("normalize-users"),
					ToPort:   "users",
				},
				{
					FromStep: slug.Slug("normalize-users"),
					FromPort: "rows",
					ToStep:   slug.Slug("persist-users"),
					ToPort:   "payload",
				},
			},
		},
	}
}

func TestODCSReferencesRoundTripAcrossReusablePorts(t *testing.T) {
	t.Parallel()
	ref := &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "rev-one", SchemaObjectID: "order", Representation: model.ContractRepresentationRows}
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders-pipeline", Name: "Orders"}, Steps: []pipelinespec.Step{testQueryStep("fetch-orders"), testTransformStep("normalize-orders", map[string]pipelinespec.StepInput{"orders": {FromStep: "fetch-orders"}}), testSinkStep("persist-orders", "normalize-orders")}}
	pipeline.Steps[0].Query.OutputContract = nil
	pipeline.Steps[0].Query.OutputContractRef = ref
	pipeline.Steps[1].Transform.InputContract = nil
	pipeline.Steps[1].Transform.InputContractRef = ref
	pipeline.Steps[1].Transform.OutputContract = nil
	pipeline.Steps[1].Transform.OutputContractRef = ref
	pipeline.Steps[2].Sink.InputContract = nil
	pipeline.Steps[2].Sink.InputContractRef = ref
	state, err := FromPipeline(pipeline)
	require.NoError(t, err)
	roundTripped, err := ToPipeline(state)
	require.NoError(t, err)
	require.Equal(t, ref, roundTripped.Steps[0].Query.OutputContractRef)
	require.Equal(t, ref, roundTripped.Steps[1].Transform.InputContractRef)
	require.Equal(t, ref, roundTripped.Steps[1].Transform.OutputContractRef)
	require.Equal(t, ref, roundTripped.Steps[2].Sink.InputContractRef)
	require.NotSame(t, ref, roundTripped.Steps[0].Query.OutputContractRef)
	require.Nil(t, roundTripped.Steps[0].Query.OutputContract)
}

func TestCompositionAttachmentsSurviveBuilderRoundTrips(t *testing.T) {
	t.Parallel()
	ref := &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "rev-one", SchemaObjectID: "order", Representation: model.ContractRepresentationRows}
	named := testTransformStep("settlement", map[string]pipelinespec.StepInput{
		"orders": {FromStep: "orders"}, "accounts": {FromStep: "accounts"},
	})
	named.Transform.InputContracts = map[string]*pipelinespec.InputContractAttachment{
		"orders":   {ContractRef: ref},
		"accounts": {Contract: testQueryStep("accounts").Query.OutputContract},
	}
	legacy := testTransformStep("audit", map[string]pipelinespec.StepInput{"accounts": {FromStep: "accounts"}})
	legacy.Transform.InputContract = testQueryStep("accounts").Query.OutputContract
	eventRef := *ref
	eventRef.Representation = model.ContractRepresentationJSON
	eventRef.RootShape = model.ContractRootShapeObject
	eventConsumer := testTransformStep("consume", map[string]pipelinespec.StepInput{"input": {FromStep: "event"}})
	eventConsumer.Transform.InputContract = testQueryStep("event").Query.OutputContract
	eventConsumer.Transform.InputContract.Representation = model.ContractRepresentationJSON
	eventConsumer.Transform.InputContract.RootShape = model.ContractRootShapeObject
	for name, steps := range map[string][]pipelinespec.Step{
		"mixed": {testQueryStep("orders"), testQueryStep("accounts"), named, legacy},
		"event": {{Kind: "input", Slug: "event", Input: &pipelinespec.InputSpec{OutputContractRef: &eventRef}}, eventConsumer},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders-pipeline", Name: "Orders"}, Steps: steps}
			source, err := json.Marshal(pipeline)
			require.NoError(t, err)
			state, err := FromPipelineSpec("orders.pipeline.json", source)
			require.NoError(t, err)
			require.Equal(t, steps, state.PipelineSpec.Steps)
			encoded, err := SerializeStateCanonical(state)
			require.NoError(t, err)
			decoded, err := DeserializeState(encoded)
			require.NoError(t, err)
			strict, err := ToPipeline(decoded)
			require.NoError(t, err)
			require.Equal(t, steps, strict.Steps)
			draft, err := ToDraftPipeline(decoded)
			require.NoError(t, err)
			require.Equal(t, steps, draft.Steps)
			for _, format := range []model.PipelineFormat{model.PipelineFormatJSON, model.PipelineFormatYAML} {
				exported, err := SerializePipelineSpec(decoded, format)
				require.NoError(t, err)
				reimported, err := FromPipelineSpec("orders.pipeline."+format.String(), exported)
				require.NoError(t, err)
				require.Equal(t, steps, reimported.PipelineSpec.Steps)
			}
		})
	}
}

func TestCanonicalizeStepCopiesCompositionAttachments(t *testing.T) {
	t.Parallel()
	ref := &pipelinespec.ODCSContractRef{ContractSlug: "orders", SchemaObjectID: "order"}
	step := testTransformStep("settlement", nil)
	step.Transform.InputContracts = map[string]*pipelinespec.InputContractAttachment{
		"orders": {ContractRef: ref}, "accounts": {Contract: testQueryStep("accounts").Query.OutputContract}, "incomplete": nil,
	}
	cloned := canonicalizeStep(step)
	require.Equal(t, step.Transform.InputContracts, cloned.Transform.InputContracts)
	cloned.Transform.InputContracts["orders"].ContractRef.SchemaObjectID = "changed"
	cloned.Transform.InputContracts["accounts"].Contract.Fields[0].Name = "changed"
	delete(cloned.Transform.InputContracts, "incomplete")
	require.Equal(t, "order", ref.SchemaObjectID)
	require.Equal(t, "id", step.Transform.InputContracts["accounts"].Contract.Fields[0].Name)
	require.Contains(t, step.Transform.InputContracts, "incomplete")
	for _, attachments := range []map[string]*pipelinespec.InputContractAttachment{nil, {}} {
		step.Transform.InputContracts = attachments
		require.Equal(t, attachments, canonicalizeStep(step).Transform.InputContracts)
	}
	event := pipelinespec.Step{Kind: "input", Slug: "event", Input: &pipelinespec.InputSpec{OutputContractRef: ref}}
	clonedEvent := canonicalizeStep(event)
	require.Equal(t, ref, clonedEvent.Input.OutputContractRef)
	clonedEvent.Input.OutputContractRef.SchemaObjectID = "changed"
	require.Equal(t, "order", ref.SchemaObjectID)
}
