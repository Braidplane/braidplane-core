package views

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestDefinitionInspectorPreservesSelectionAndSerializedContent(t *testing.T) {
	pipeline := testPipelineWithSpecContracts(t)
	// The generated step union must retain fields the structured projection omits.
	raw, err := json.Marshal(pipeline.Spec.Steps[0])
	require.NoError(t, err)
	var step map[string]any
	require.NoError(t, json.Unmarshal(raw, &step))
	step["x-inspection"] = map[string]any{"integer": json.Number("9007199254740993"), "html": "<script>bad</script>"}
	raw, err = json.Marshal(step)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &pipeline.Spec.Steps[0]))
	hash := strings.Repeat("a", 64)
	ctx := WithDefinitionInspection(context.Background(), DefinitionInspection{Authority: "Executed version", Hash: hash, Spec: &pipeline.Spec})
	ctx = WithEntityPageURL(ctx, "/orgs/acme/projects/ops/runs/run-one?lineage_step=load-customers&definition_view=serialized")
	view := definitionInspectorView(ctx)
	require.NotNil(t, view.Selected)
	require.Equal(t, "load-customers", view.Selected.Slug)
	require.Contains(t, view.Serialized, "9007199254740993")
	require.Contains(t, view.Serialized, "x-inspection")
	require.NotContains(t, view.Serialized, "normalize-customers")
	var out bytes.Buffer
	require.NoError(t, DefinitionInspector("acme", "ops").Render(ctx, &out))
	require.Contains(t, out.String(), hash)
	require.NotContains(t, out.String(), "<script>bad</script>")
	require.Contains(t, out.String(), "Serialized authored JSON")
	target, err := url.Parse(definitionHref(ctx, "load-customers", "orders", "input", "structured"))
	require.NoError(t, err)
	require.Equal(t, hash, target.Query().Get("spec_hash"))
	require.Equal(t, "orders", target.Query().Get("definition_input"))
	require.Equal(t, "definition", target.Query().Get("section"))
}

func TestDefinitionInspectorMissingInvalidAndOversized(t *testing.T) {
	pipeline := testPipelineWithSpecContracts(t)
	for _, tc := range []struct {
		name            string
		inspection      DefinitionInspection
		query, expected string
	}{
		{"missing", DefinitionInspection{Authority: "Executed version", Hash: "recorded", Message: "The requested definition is missing."}, "lineage_step=old", "The requested definition is missing."},
		{"invalid step", DefinitionInspection{Spec: &pipeline.Spec}, "lineage_step=removed", "Step unavailable in this version."},
		{"invalid input", DefinitionInspection{Spec: &pipeline.Spec}, "lineage_step=normalize-customers&definition_input=missing", "Input unavailable in this step."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithEntityPageURL(WithDefinitionInspection(context.Background(), tc.inspection), "/orgs/acme/projects/ops/runs/run-one?"+tc.query)
			d := definitionInspectorView(ctx)
			require.Contains(t, d.Message+d.SelectionMessage, tc.expected)
			if tc.name == "missing" {
				require.Equal(t, "recorded", d.Hash)
				require.Equal(t, "old", d.Step)
			}
		})
	}
	raw := []byte(`{"kind":"future","slug":"huge","extension":"` + strings.Repeat("x", DefinitionContentLimit) + `"}`)
	var step api.PipelineStep
	require.NoError(t, json.Unmarshal(raw, &step))
	spec := api.PipelineSpec{Steps: []api.PipelineStep{step}}
	d := definitionInspectorView(WithDefinitionInspection(context.Background(), DefinitionInspection{Hash: "huge-hash", Spec: &spec}))
	require.Nil(t, d.Spec)
	require.Empty(t, d.Serialized)
	require.Contains(t, d.Message, "1 MiB")
	require.Equal(t, "huge-hash", d.Hash)
}

func TestDefinitionContractRoundTripAllowlist(t *testing.T) {
	proposalReturn := DefinitionReturnHref("/orgs/acme/projects/ops/pipeline-creations/creation-one?definition_view=serialized&token=private", "acme", "ops")
	require.Contains(t, proposalReturn, "/pipeline-creations/creation-one?")
	require.Contains(t, proposalReturn, "definition_view=serialized")
	require.NotContains(t, proposalReturn, "private")
	raw := "/orgs/acme/projects/ops/runs/run-one?environment_slug=dev&definition=executed&spec_hash=" + strings.Repeat("b", 64) + "&lineage_step=join&definition_input=orders&definition_port=input&definition_view=serialized&token=private"
	safe := DefinitionReturnHref(raw, "acme", "ops")
	require.NotEmpty(t, safe)
	require.NotContains(t, safe, "private")
	ctx := WithEntityPageURL(context.Background(), safe)
	slot := PipelineSpecContractSlot{Label: "Input requirement: orders", Ref: &api.PipelineODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectId: "order-object"}}
	target, err := url.Parse(definitionContractHref(ctx, "acme", "ops", "join", slot))
	require.NoError(t, err)
	require.Equal(t, "v1", target.Query().Get("revision"))
	require.Equal(t, "order-object", target.Query().Get("object"))
	data := ODCSCatalogView{Org: "acme", Project: "ops", Contract: "orders", Revision: "v1", Object: "order-object", DefinitionReturn: target.Query().Get("definition_return")}
	next, err := url.Parse(ODCSCatalogSectionHref(data, "document"))
	require.NoError(t, err)
	back, err := url.Parse(next.Query().Get("definition_return"))
	require.NoError(t, err)
	require.Equal(t, "orders", back.Query().Get("definition_input"))
	require.Equal(t, "input", back.Query().Get("definition_port"))
	require.Equal(t, "join", back.Query().Get("lineage_step"))
	require.Equal(t, strings.Repeat("b", 64), back.Query().Get("spec_hash"))
	for _, invalid := range []string{"https://evil.test" + raw, "//evil.test/path", "/orgs/other/projects/ops/runs/run-one", "/orgs/acme/projects/ops/pipelines/run-one/delete", "/orgs/acme/projects/ops/settings", `/orgs/acme/projects/ops/runs/..`, "/orgs/acme/projects/ops/runs/run-one%0aevil"} {
		require.Empty(t, DefinitionReturnHref(invalid, "acme", "ops"), invalid)
	}
}

func TestDefinitionInspectorKeepsNamedMixedBoundariesAndFanOut(t *testing.T) {
	var spec api.PipelineSpec
	require.NoError(t, json.Unmarshal([]byte(`{"metadata":{"slug":"joined"},"steps":[
 {"kind":"query","slug":"fetch","query":{"data_source_ref":"source","operation_type":"sql","operation_ref":"read","parameters":{"after":"$steps.event.id"},"output_contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row"}}},
 {"kind":"transform","slug":"join","transform":{"inputs":{"orders":"fetch","other":"fetch"},"sql":"SELECT * FROM orders","output_kind":"rows","input_contracts":{"orders":{"contract_ref":{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row"}},"other":{"contract":{"name":"local-shape","representation":"rows","fields":[]}}}}},
 {"kind":"sink","slug":"store-one","sink":{"input":"join","data_source_ref":"sink","operation_type":"sql","operation_ref":"write","idempotency_class":"non_idempotent"}},
 {"kind":"sink","slug":"store-two","sink":{"input":"join","data_source_ref":"sink","operation_type":"sql","operation_ref":"write","idempotency_class":"non_idempotent"}}
 ]}`), &spec))
	ctx := WithEntityPageURL(WithDefinitionInspection(context.Background(), DefinitionInspection{Spec: &spec, Hash: strings.Repeat("c", 64)}), "/orgs/acme/projects/ops/pipelines/joined?lineage_step=join&definition_input=orders&definition_port=input")
	d := definitionInspectorView(ctx)
	require.Empty(t, d.SelectionMessage)
	require.Len(t, d.Steps, 4)
	require.Len(t, d.Selected.Inputs, 2)
	require.Equal(t, "store-two", d.Steps[3].Slug)
	var out bytes.Buffer
	require.NoError(t, DefinitionInspector("acme", "ops").Render(ctx, &out))
	require.Contains(t, out.String(), "local-shape")
	require.Contains(t, out.String(), "revision=v1")
	require.Contains(t, out.String(), "object=row")
	require.Contains(t, out.String(), "definition_input=other")
	require.Contains(t, out.String(), "Producer agreement")
	require.Contains(t, d.Steps[0].Parameters, "$steps.event.id")
}
