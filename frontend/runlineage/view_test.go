package runlineage

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestBuildTenSupplierBranchesConvergeOnOneCombineAndSink(t *testing.T) {
	t.Parallel()
	pipeline := pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "supplier-offers", Name: "Supplier offers"}}
	combine := &pipelinespec.TransformSpec{Inputs: make(map[string]pipelinespec.StepInput)}
	branches := make([]string, 10)
	for i := range branches {
		read := slug.Slug(fmt.Sprintf("read-%d", i))
		normalize := slug.Slug(fmt.Sprintf("normalize-%d", i))
		name := fmt.Sprintf("supplier_%d", i)
		pipeline.Steps = append(pipeline.Steps,
			pipelinespec.Step{Kind: "query", Slug: read, Query: &pipelinespec.QueryRef{DataSourceSlug: "supplier-files", OperationType: "http", OperationRef: read}},
			pipelinespec.Step{Kind: "transform", Slug: normalize, Transform: &pipelinespec.TransformSpec{Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: read}}, SQL: "SELECT id FROM input"}},
		)
		combine.Inputs[name] = pipelinespec.StepInput{FromStep: normalize}
		branches[i] = "SELECT id FROM " + name
	}
	combine.SQL = strings.Join(branches, " UNION ALL ")
	pipeline.Steps = append(pipeline.Steps,
		pipelinespec.Step{Kind: "transform", Slug: "combine", Transform: combine},
		pipelinespec.Step{Kind: "sink", Slug: "write-offers", Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "combine"}, DataSourceSlug: "offers-db", OperationType: "sql", OperationRef: "upsert-offers"}},
	)
	spec, err := json.Marshal(pipeline)
	require.NoError(t, err)
	version := decodeJSON[api.PipelineVersion](t, `{"pipeline_slug":"supplier-offers","spec_hash":"recorded-version","spec":`+string(spec)+`}`)
	run := decodeJSON[api.PipelineRunRecord](t, `{"metadata":{"status":"succeeded"},"timeline":{"status":"succeeded"}}`)
	view, err := Build(version, run, "combine", "/runs/synthetic")
	require.NoError(t, err)
	require.True(t, view.Available)
	require.Len(t, view.Nodes, 22)
	require.Len(t, view.Edges, 21)
	nodes := nodesBySlug(view.Nodes)
	require.Len(t, nodes["combine"].Upstream, 10)
	require.Equal(t, []string{"write-offers"}, nodes["combine"].Downstream)
	require.Len(t, nodes["combine"].Planned.Inputs, 10)
	for i := range branches {
		require.Equal(t, fmt.Sprintf("supplier_%d", i), edgeBySlugs(t, view.Edges, fmt.Sprintf("normalize-%d", i), "combine").Label)
	}
	require.Equal(t, []PlannedInput{{Name: "input", FromStepSlug: "combine"}}, nodes["write-offers"].Planned.Inputs)
}

func TestBuildProjectsExactTopologyAndRunFacts(t *testing.T) {
	t.Parallel()

	version := decodeJSON[api.PipelineVersion](t, `{
		"created_at":"2026-03-16T10:00:00Z",
		"pipeline_slug":"users-sync",
		"spec_hash":"abcdef0123456789",
		"short_spec_hash":"abcdef01",
		"spec":{
			"metadata":{"slug":"users-sync","name":"Users sync"},
			"steps":[
				{"kind":"query","slug":"fetch-users","name":"Fetch users","query":{"data_source_ref":"source-db","operation_type":"sql","operation_ref":"list-users"}},
				{"kind":"query","slug":"fetch-teams","name":"Fetch teams","query":{"data_source_ref":"source-db","operation_type":"sql","operation_ref":"list-teams","parameters":{"user_ids":"$steps.fetch-users.rows"}}},
				{"kind":"transform","slug":"join-users","name":"Join users","transform":{"sql":"SELECT 1","output_kind":"rows","inputs":{"users":"fetch-users","teams":"fetch-teams"}}},
				{"kind":"sink","slug":"write-users","name":"Write users","sink":{"input":"join-users","data_source_ref":"warehouse","operation_type":"sql","operation_ref":"upsert-users","idempotency_class":"idempotent_with_key"}}
			]
		}
	}`)
	run := decodeJSON[api.PipelineRunRecord](t, `{
		"metadata":{"pipeline_slug":"users-sync","pipeline_version_spec_hash":"abcdef0123456789","environment_slug":"prod","run_slug":"run-001","status":"failed","duration_ms":900,"step_count":4,"step_slug":"join-users","started_at":"2026-03-16T11:00:00Z","ended_at":"2026-03-16T11:00:01Z","created_at":"2026-03-16T11:00:01Z","incremental":{"mode":"full","checkpoint_status":"","replay_safety_status":"","source_step_slug":"","blocked_reason":"","replay_blocked_reason":"","backfill_series_slug":"","backfill_chunk_index":0,"backfill_chunk_count":0}},
		"timeline":{"run_slug":"run-001","pipeline_slug":"users-sync","environment_slug":"prod","status":"failed","events":[
			{"at":"2026-03-16T11:00:00Z","type":"step_started","status":"running","step":"fetch-users"},
			{"at":"2026-03-16T11:00:00.200Z","type":"step_succeeded","status":"succeeded","step":"fetch-users"},
			{"at":"2026-03-16T11:00:00.250Z","type":"step_started","status":"running","step":"fetch-teams"},
			{"at":"2026-03-16T11:00:00.500Z","type":"step_succeeded","status":"succeeded","step":"fetch-teams"},
			{"at":"2026-03-16T11:00:00.550Z","type":"step_started","status":"running","step":"join-users"},
			{"at":"2026-03-16T11:00:01Z","type":"step_failed","status":"failed","step":"join-users"}
		],"snapshots":[
			{"step":"fetch-users","input":{"organization_slug":"acme","environment_slug":"prod","data_source_slug":"source-db","parameter_keys":[]},"output":{"result_type":"sql","vendor_type":"postgres","duration_ms":200,"details":{"kind":"rows","row_count":12}}}
		],"failure":{"step":"join-users","error":"contract mismatch","at":"2026-03-16T11:00:01Z","kind":"platform","side":"transform"}},
		"incremental_diagnostics":{"replay_safety":{"status":"","blocked_reason":"","blocked_sink":""},"sources":[]}
	}`)

	view, err := Build(version, run, "", "/organizations/acme/projects/default/environments/prod/runs/run-001?panel=run")
	require.NoError(t, err)
	require.True(t, view.Available)
	require.Equal(t, "abcdef01", view.VersionShortHash)
	require.Len(t, view.Nodes, 4)
	require.GreaterOrEqual(t, view.CanvasWidth, 720)
	require.GreaterOrEqual(t, view.CanvasHeight, 260)

	nodes := nodesBySlug(view.Nodes)
	require.Equal(t, StatusSucceeded, nodes["fetch-users"].Status)
	require.Equal(t, int64(200), nodes["fetch-users"].DurationMs)
	require.Equal(t, "source-db", nodes["fetch-users"].Planned.DataSourceRef)
	require.Equal(t, "sql", nodes["fetch-users"].Planned.OperationType)
	require.Equal(t, "list-users", nodes["fetch-users"].Planned.OperationRef)
	require.False(t, nodes["fetch-users"].Planned.UsesInline)
	require.Equal(t, StatusSucceeded, nodes["fetch-teams"].Status)
	require.Equal(t, []string{"user_ids"}, nodes["fetch-teams"].Planned.ParameterKeys)
	require.Equal(t, StatusFailed, nodes["join-users"].Status)
	require.Equal(t, "rows", nodes["join-users"].Planned.OutputKind)
	require.Equal(t, []PlannedInput{
		{Name: "teams", FromStepSlug: "fetch-teams"},
		{Name: "users", FromStepSlug: "fetch-users"},
	}, nodes["join-users"].Planned.Inputs)
	require.NotNil(t, nodes["join-users"].Failure)
	require.True(t, nodes["join-users"].Selected)
	require.Equal(t, StatusNotReached, nodes["write-users"].Status)
	require.Equal(t, "warehouse", nodes["write-users"].Planned.DataSourceRef)
	require.Equal(t, []PlannedInput{{Name: "input", FromStepSlug: "join-users"}}, nodes["write-users"].Planned.Inputs)
	require.Contains(t, nodes["fetch-users"].Href, "lineage_step=fetch-users")
	require.Contains(t, nodes["fetch-users"].Href, "panel=run")
	require.Contains(t, nodes["fetch-users"].Href, "#lineage-step-evidence")

	require.Len(t, view.Edges, 4)
	require.True(t, edgeBySlugs(t, view.Edges, "fetch-users", "fetch-teams").Dashed)
	require.Equal(t, []string{"query_parameter"}, edgeBySlugs(t, view.Edges, "fetch-users", "fetch-teams").Kinds)
	require.Equal(t, "param: user_ids", edgeBySlugs(t, view.Edges, "fetch-users", "fetch-teams").Label)
	require.Equal(t, "users", edgeBySlugs(t, view.Edges, "fetch-users", "join-users").Label)
	require.Equal(t, "teams", edgeBySlugs(t, view.Edges, "fetch-teams", "join-users").Label)
	require.Equal(t, "payload", edgeBySlugs(t, view.Edges, "join-users", "write-users").Label)
	require.Positive(t, edgeBySlugs(t, view.Edges, "fetch-users", "join-users").LabelX)
	require.Positive(t, edgeBySlugs(t, view.Edges, "fetch-users", "join-users").LabelY)
	require.Equal(t, StatusNotReached, edgeBySlugs(t, view.Edges, "fetch-teams", "join-users").Tone)
	require.Contains(t, nodes["fetch-users"].Downstream, "fetch-teams")
	require.Contains(t, nodes["join-users"].Upstream, "fetch-users")
}

func TestPlannedEvidenceMarksInlineOperationWithoutProjectingBody(t *testing.T) {
	t.Parallel()

	evidence := plannedEvidence(pipelinespec.Step{
		Kind: "query",
		Query: &pipelinespec.QueryRef{
			DataSourceSlug: "source-db",
			OperationType:  "http",
			OperationInline: map[string]any{
				"authorization": "must-not-be-projected",
			},
		},
	})

	require.True(t, evidence.UsesInline)
	require.Empty(t, evidence.OperationRef)
	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "must-not-be-projected")
}

func TestBuildHonorsRequestedSelection(t *testing.T) {
	t.Parallel()

	version := decodeJSON[api.PipelineVersion](t, `{
		"created_at":"2026-03-16T10:00:00Z",
		"pipeline_slug":"one-step",
		"spec_hash":"abcdef0123456789",
		"short_spec_hash":"abcdef01",
		"spec":{"metadata":{"slug":"one-step","name":"One step"},"steps":[
			{"kind":"query","slug":"fetch-row","name":"Fetch row","query":{"data_source_ref":"source-db","operation_type":"sql","operation_ref":"fetch-row"}}
		]}
	}`)
	run := decodeJSON[api.PipelineRunRecord](t, `{
		"metadata":{"pipeline_slug":"one-step","pipeline_version_spec_hash":"abcdef0123456789","environment_slug":"prod","run_slug":"run-001","status":"queued","duration_ms":0,"step_count":1,"step_slug":"","started_at":"2026-03-16T11:00:00Z","created_at":"2026-03-16T11:00:00Z","incremental":{"mode":"full","checkpoint_status":"","replay_safety_status":"","source_step_slug":"","blocked_reason":"","replay_blocked_reason":"","backfill_series_slug":"","backfill_chunk_index":0,"backfill_chunk_count":0}},
		"timeline":{"run_slug":"run-001","pipeline_slug":"one-step","environment_slug":"prod","status":"queued","events":[],"snapshots":[]},
		"incremental_diagnostics":{"replay_safety":{"status":"","blocked_reason":"","blocked_sink":""},"sources":[]}
	}`)

	view, err := Build(version, run, "fetch-row", "/runs/run-001")
	require.NoError(t, err)
	require.Equal(t, StatusPending, view.Nodes[0].Status)
	require.True(t, view.Nodes[0].Selected)
	require.Contains(t, view.Nodes[0].Href, "spec_hash="+version.SpecHash)
	require.Contains(t, view.Nodes[0].Href, "definition=executed")
	missing, err := Build(version, run, "removed-step", "/runs/run-001")
	require.NoError(t, err)
	require.Equal(t, -1, missing.SelectedIndex)
	require.False(t, missing.Nodes[0].Selected)
}

func TestBuildDoesNotInferSuccessFromSnapshot(t *testing.T) {
	t.Parallel()

	version := decodeJSON[api.PipelineVersion](t, `{
		"created_at":"2026-03-16T10:00:00Z",
		"pipeline_slug":"one-step",
		"spec_hash":"abcdef0123456789",
		"short_spec_hash":"abcdef01",
		"spec":{"metadata":{"slug":"one-step","name":"One step"},"steps":[
			{"kind":"query","slug":"fetch-row","name":"Fetch row","query":{"data_source_ref":"source-db","operation_type":"sql","operation_ref":"fetch-row"}}
		]}
	}`)
	run := decodeJSON[api.PipelineRunRecord](t, `{
		"metadata":{"pipeline_slug":"one-step","pipeline_version_spec_hash":"abcdef0123456789","environment_slug":"prod","run_slug":"run-001","status":"succeeded","duration_ms":100,"step_count":1,"step_slug":"fetch-row","started_at":"2026-03-16T11:00:00Z","ended_at":"2026-03-16T11:00:00.100Z","created_at":"2026-03-16T11:00:00.100Z","incremental":{"mode":"full","checkpoint_status":"","replay_safety_status":"","source_step_slug":"","blocked_reason":"","replay_blocked_reason":"","backfill_series_slug":"","backfill_chunk_index":0,"backfill_chunk_count":0}},
		"timeline":{"run_slug":"run-001","pipeline_slug":"one-step","environment_slug":"prod","status":"succeeded","events":[],"snapshots":[
			{"step":"fetch-row","input":{"organization_slug":"acme","environment_slug":"prod","data_source_slug":"source-db","parameter_keys":[]},"output":{"result_type":"sql","vendor_type":"postgres","duration_ms":100,"details":{"kind":"rows","row_count":1}}}
		]},
		"incremental_diagnostics":{"replay_safety":{"status":"","blocked_reason":"","blocked_sink":""},"sources":[]}
	}`)

	view, err := Build(version, run, "", "/runs/run-001")
	require.NoError(t, err)
	require.Equal(t, StatusUnknown, view.Nodes[0].Status)
	require.NotNil(t, view.Nodes[0].Snapshot)
	require.Equal(t, int64(100), view.Nodes[0].DurationMs)
}

func TestBuildMarksSnapshotBackedFailedRunStepAsReachedWithUnknownOutcome(t *testing.T) {
	t.Parallel()

	version := decodeJSON[api.PipelineVersion](t, `{
		"created_at":"2026-03-16T10:00:00Z",
		"pipeline_slug":"one-step",
		"spec_hash":"abcdef0123456789",
		"short_spec_hash":"abcdef01",
		"spec":{"metadata":{"slug":"one-step","name":"One step"},"steps":[
			{"kind":"query","slug":"fetch-row","name":"Fetch row","query":{"data_source_ref":"source-db","operation_type":"sql","operation_ref":"fetch-row"}}
		]}
	}`)
	run := decodeJSON[api.PipelineRunRecord](t, `{
		"metadata":{"pipeline_slug":"one-step","pipeline_version_spec_hash":"abcdef0123456789","environment_slug":"prod","run_slug":"run-001","status":"failed","duration_ms":100,"step_count":1,"step_slug":"","started_at":"2026-03-16T11:00:00Z","ended_at":"2026-03-16T11:00:00.100Z","created_at":"2026-03-16T11:00:00.100Z","incremental":{"mode":"full","checkpoint_status":"","replay_safety_status":"","source_step_slug":"","blocked_reason":"","replay_blocked_reason":"","backfill_series_slug":"","backfill_chunk_index":0,"backfill_chunk_count":0}},
		"timeline":{"run_slug":"run-001","pipeline_slug":"one-step","environment_slug":"prod","status":"failed","events":[],"snapshots":[
			{"step":"fetch-row","input":{"organization_slug":"acme","environment_slug":"prod","data_source_slug":"source-db","parameter_keys":[]},"output":{"result_type":"sql","vendor_type":"postgres","duration_ms":100,"details":{"kind":"rows","row_count":1}}}
		]},
		"incremental_diagnostics":{"replay_safety":{"status":"","blocked_reason":"","blocked_sink":""},"sources":[]}
	}`)

	view, err := Build(version, run, "", "/runs/run-001")
	require.NoError(t, err)
	require.Equal(t, StatusUnknown, view.Nodes[0].Status)
	require.NotEqual(t, StatusNotReached, view.Nodes[0].Status)
}

func TestBuildRejectsInvalidHistoricalTopology(t *testing.T) {
	t.Parallel()

	version := decodeJSON[api.PipelineVersion](t, `{
		"created_at":"2026-03-16T10:00:00Z",
		"pipeline_slug":"broken",
		"spec_hash":"abcdef0123456789",
		"short_spec_hash":"abcdef01",
		"spec":{"metadata":{"slug":"broken","name":"Broken"},"steps":[
			{"kind":"sink","slug":"write","sink":{"input":"missing","data_source_ref":"warehouse","operation_type":"sql","operation_ref":"write","idempotency_class":"idempotent_with_key"}}
		]}
	}`)

	_, err := Build(version, api.PipelineRunRecord{}, "", "/runs/run-001")
	require.ErrorContains(t, err, "normalize pipeline version")
}

func nodesBySlug(nodes []Node) map[string]Node {
	result := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		result[node.Slug] = node
	}
	return result
}

func edgeBySlugs(t *testing.T, edges []Edge, from, to string) Edge {
	t.Helper()
	for _, edge := range edges {
		if edge.FromSlug == from && edge.ToSlug == to {
			return edge
		}
	}
	t.Fatalf("edge %s -> %s not found", from, to)
	return Edge{}
}

func decodeJSON[T any](t *testing.T, raw string) T {
	t.Helper()
	var value T
	require.NoError(t, json.Unmarshal([]byte(raw), &value))
	return value
}

func TestBuildDoesNotInventCompletionForStartedSteps(t *testing.T) {
	version := decodeJSON[api.PipelineVersion](t, `{"spec_hash":"executed-v1","spec":{"metadata":{"slug":"one"},"steps":[{"kind":"query","slug":"read","query":{"data_source_ref":"source","operation_type":"sql","operation_ref":"read"}}]}}`)
	for _, tc := range []struct {
		status   api.PipelineRunMetadataStatus
		expected string
	}{
		{"running", StatusRunning}, {"succeeded", StatusUnknown}, {"failed", StatusUnknown}, {"canceled", StatusCanceled},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			run := decodeJSON[api.PipelineRunRecord](t, `{"timeline":{"events":[{"type":"step_started","step":"read","at":"2026-10-19T12:00:00Z"}]}}`)
			run.Metadata.Status = tc.status
			view, err := Build(version, run, "read", "/orgs/acme/projects/ops/runs/one")
			require.NoError(t, err)
			require.Equal(t, tc.expected, view.Nodes[0].Status)
			require.NotNil(t, view.Nodes[0].StartedAt)
			require.Nil(t, view.Nodes[0].EndedAt)
			require.Contains(t, view.Nodes[0].Href, "section=steps")
		})
	}
}

func TestWholeDefinitionDoesNotSelectADifferentGraphStep(t *testing.T) {
	version := decodeJSON[api.PipelineVersion](t, `{"spec_hash":"executed-v1","spec":{"metadata":{"slug":"one"},"steps":[{"kind":"query","slug":"read","query":{"data_source_ref":"source","operation_type":"sql","operation_ref":"read"}}]}}`)
	view, err := Build(version, api.PipelineRunRecord{}, "", "/orgs/acme/projects/ops/runs/one?definition_scope=whole")
	require.NoError(t, err)
	require.Equal(t, -1, view.SelectedIndex)
	require.False(t, view.Nodes[0].Selected)
}

func TestFailureBoundaryRequiresRecordedInputIdentity(t *testing.T) {
	version := decodeJSON[api.PipelineVersion](t, `{"spec_hash":"historical","spec":{"metadata":{"slug":"orders"},"steps":[{"kind":"query","slug":"orders","query":{"data_source_ref":"http","operation_type":"http","operation_ref":"orders"}},{"kind":"query","slug":"customers","query":{"data_source_ref":"db","operation_type":"sql","operation_ref":"customers"}},{"kind":"transform","slug":"enrich","transform":{"inputs":{"orders":"orders","customers":"customers"},"sql":"SELECT * FROM orders"}},{"kind":"sink","slug":"export","sink":{"input":"enrich","data_source_ref":"s3","operation_type":"s3","operation_ref":"export"}}]}}`)
	for _, tc := range []struct{ name, failure, checks, from string }{
		{"ambiguous transform", `{"step":"enrich","side":"transform","reason_code":"transform_contract_mismatch"}`, `[]`, ""},
		{"named input", `{"step":"enrich","side":"transform","reason_code":"transform_contract_mismatch"}`, `[{"step":"enrich","status":"failed","phase":"normalized_input","input":"orders","target":"orders"}]`, "orders"},
		{"source output", `{"step":"orders","side":"source","reason_code":"source_result_shape_mismatch"}`, `[]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := decodeJSON[api.PipelineRunRecord](t, `{"metadata":{"status":"failed"},"timeline":{"failure":`+tc.failure+`,"structural_checks":`+tc.checks+`}}`)
			view, err := Build(version, run, "", "/runs/historical?environment_slug=dev")
			require.NoError(t, err)
			nodes := nodesBySlug(view.Nodes)
			require.True(t, nodes["export"].Blocked)
			require.False(t, nodes["customers"].Blocked)
			for _, edge := range view.Edges {
				require.Equal(t, tc.from != "" && edge.FromSlug == tc.from && edge.ToSlug == "enrich", edge.Tone == StatusFailed)
			}
			if tc.name == "source output" {
				require.Equal(t, "output", nodes["orders"].Boundary)
			}
			if tc.name == "ambiguous transform" {
				require.Empty(t, nodes["enrich"].Boundary)
			}
		})
	}
}
