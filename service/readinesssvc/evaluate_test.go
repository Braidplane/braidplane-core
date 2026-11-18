package readinesssvc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestSettlementReadiness(t *testing.T) {
	source := model.DataSourceRefCapabilityPipelineSource
	sink := model.DataSourceRefCapabilityPipelineSink
	pipeline := PipelineInput{
		Spec: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "settlement-review"},
			Steps: []pipelinespec.Step{
				{Slug: "read-settlement", Kind: "query", Query: &pipelinespec.QueryRef{DataSourceSlug: "settlement-file", OperationType: "s3"}},
				{Slug: "read-ledger", Kind: "query", Query: &pipelinespec.QueryRef{DataSourceSlug: "payment-ledger", OperationType: "sql", Parameters: map[string]any{"date": "{{runtime.env.review_date}}"}}},
				{Slug: "write-comparisons", Kind: "sink", Sink: &pipelinespec.SinkSpec{DataSourceSlug: "reporting-warehouse", OperationType: "sql"}},
				{Slug: "write-totals", Kind: "sink", Sink: &pipelinespec.SinkSpec{DataSourceSlug: "reporting-warehouse", OperationType: "sql"}},
			},
		},
		Refs: []model.DataSourceRef{
			{Slug: "settlement-file", DataSourceType: model.DataSourceTypeS3, RequiredCapabilities: []model.DataSourceRefCapability{source}},
			{Slug: "payment-ledger", DataSourceType: model.DataSourceTypePostgres, RequiredCapabilities: []model.DataSourceRefCapability{source}},
			{Slug: "reporting-warehouse", DataSourceType: model.DataSourceTypePostgres, RequiredCapabilities: []model.DataSourceRefCapability{sink}},
		},
	}
	for _, ref := range pipeline.Refs {
		pipeline.Bindings = append(pipeline.Bindings, model.DataSourceBinding{EnvironmentSlug: "dev", DataSourceRefSlug: ref.Slug, DataSourceType: ref.DataSourceType, DataSourceActive: true})
	}
	pipeline.Bindings = append(pipeline.Bindings,
		model.DataSourceBinding{EnvironmentSlug: "prod", DataSourceRefSlug: "settlement-file", DataSourceType: model.DataSourceTypePostgres, DataSourceActive: true},
		model.DataSourceBinding{EnvironmentSlug: "prod", DataSourceRefSlug: "reporting-warehouse", DataSourceType: model.DataSourceTypePostgres, DataSourceReadOnly: true},
	)
	envs := []EnvironmentInput{{Slug: "dev", Variables: map[string]bool{"review_date": true}}, {Slug: "prod"}}
	before, err := json.Marshal(pipeline.Spec)
	require.NoError(t, err)
	report := Evaluate([]PipelineInput{pipeline}, envs)
	after, err := json.Marshal(pipeline.Spec)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, "blocked", report.Status)
	require.Equal(t, "ready", report.Environments[0].Status)
	require.Empty(t, report.Environments[0].Diagnostics)
	prod := report.Environments[1]
	require.Equal(t, "blocked", prod.Status)
	var codes []string
	for _, d := range prod.Diagnostics {
		require.Equal(t, slug.Slug("prod"), d.EnvironmentSlug)
		require.Equal(t, slug.Slug("settlement-review"), d.PipelineSlug)
		require.NotEmpty(t, d.NextAction)
		codes = append(codes, d.Code)
	}
	require.Equal(t, []string{"invalid_target_type", "missing_binding", "inactive_target", "read_only_sink", "inactive_target", "read_only_sink", "missing_variable"}, codes)
	require.Equal(t, Diagnostic{Severity: "error", Code: "missing_variable", EnvironmentSlug: "prod", PipelineSlug: "settlement-review", StepSlug: "read-ledger", RequirementKey: "review_date", FieldPath: "/steps/1/query/parameters/date", NextAction: "set_environment_variable"}, prod.Diagnostics[6])

	other := pipeline
	spec := *pipeline.Spec
	spec.Metadata.Slug = "second"
	other.Spec = &spec
	other.Refs = []model.DataSourceRef{{Slug: "reporting-warehouse", DataSourceType: model.DataSourceTypePostgres}}
	report = Evaluate([]PipelineInput{pipeline, other}, envs[:1])
	for _, d := range report.Environments[0].Diagnostics {
		require.Equal(t, slug.Slug("second"), d.PipelineSlug)
	}
	require.Len(t, report.Environments[0].Diagnostics, 4)
	require.Equal(t, "missing_ref", report.Environments[0].Diagnostics[0].Code)
	require.Equal(t, "missing_capability", report.Environments[0].Diagnostics[2].Code)
}

func TestEmptyScope(t *testing.T) {
	require.Equal(t, "warning", Evaluate(nil, nil).Status)
	report := Evaluate(nil, []EnvironmentInput{{Slug: "dev"}})
	require.Equal(t, "informational", report.Status)
	require.Equal(t, "informational", report.Environments[0].Status)
}

func TestIncompatibleLogicalRefAction(t *testing.T) {
	capability := model.DataSourceRefCapabilityPipelineSource
	ref := model.DataSourceRef{DataSourceType: model.DataSourceTypePostgres, RequiredCapabilities: []model.DataSourceRefCapability{capability}}
	binding := model.DataSourceBinding{DataSourceType: model.DataSourceTypePostgres, DataSourceActive: true}
	for _, bound := range []bool{true, false} {
		var actions []string
		checkBinding(ref, binding, bound, capability, "s3", func(_, action string) { actions = append(actions, action) })
		expected := []string{"update_ref_type"}
		if !bound {
			expected = append(expected, "configure_binding")
		}
		require.Equal(t, expected, actions)
	}
}
