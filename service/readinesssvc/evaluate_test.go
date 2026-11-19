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
	pipeline.Refs = append(pipeline.Refs, model.DataSourceRef{Slug: "unused-unbound-ref"})
	pipeline.Bindings = append(pipeline.Bindings,
		model.DataSourceBinding{EnvironmentSlug: "prod", DataSourceRefSlug: "settlement-file", DataSourceType: model.DataSourceTypePostgres, DataSourceActive: true},
		model.DataSourceBinding{EnvironmentSlug: "prod", DataSourceRefSlug: "reporting-warehouse", DataSourceSlug: "warehouse", DataSourceName: "Reporting warehouse", DataSourceType: model.DataSourceTypePostgres, DataSourceReadOnly: true},
	)
	envs := []EnvironmentInput{{Slug: "dev", Variables: map[string]Variable{"review_date": {Source: model.RuntimeVariableDefault, Present: true}}}, {Slug: "prod"}}
	before, err := json.Marshal(pipeline.Spec)
	require.NoError(t, err)
	overview := evaluate([]PipelineInput{pipeline}, envs, projectOverview)
	require.Equal(t, "ready", overview.Environments[0].Status)
	require.Equal(t, "blocked", overview.Environments[1].Status)
	for _, d := range overview.Environments[1].Diagnostics {
		require.NotEqual(t, "unused-unbound-ref", d.RequirementKey)
	}
	report := evaluate([]PipelineInput{pipeline}, envs, pipelineDetail)
	after, err := json.Marshal(pipeline.Spec)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, "blocked", report.Status)
	require.Equal(t, "ready", report.Environments[0].Status)
	require.Empty(t, report.Environments[0].Diagnostics)
	dev := report.Environments[0].Requirements
	require.Len(t, dev, 4)
	require.Equal(t, &Variable{Source: model.RuntimeVariableDefault, Present: true}, dev[3].Variable)
	require.Equal(t, "ready", dev[2].Status)
	prod := report.Environments[1]
	require.Equal(t, "blocked", prod.Status)
	require.Len(t, prod.Requirements, 4)
	require.Nil(t, prod.Requirements[1].Target)
	row := prod.Requirements[2]
	require.Equal(t, "data_source_ref", row.Kind)
	require.Equal(t, "reporting-warehouse", row.Key)
	require.Equal(t, sink, row.Capability)
	require.Equal(t, "blocked", row.Status)
	require.Equal(t, []Usage{{StepSlug: "write-comparisons", FieldPath: "/steps/2/sink/data_source_ref"}, {StepSlug: "write-totals", FieldPath: "/steps/3/sink/data_source_ref"}}, row.Usages)
	require.Equal(t, &Target{Slug: "warehouse", Name: "Reporting warehouse", Type: model.DataSourceTypePostgres, ReadOnly: true, SupportsCapability: true}, row.Target)
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

func TestRequirementStatusSeparatesSharedKeyByCapabilityAndKind(t *testing.T) {
	source, sink := model.DataSourceRefCapabilityPipelineSource, model.DataSourceRefCapabilityPipelineSink
	p := PipelineInput{
		Spec: &pipelinespec.Pipeline{Steps: []pipelinespec.Step{
			{Slug: "read", Query: &pipelinespec.QueryRef{DataSourceSlug: "ledger", OperationType: "sql", Parameters: map[string]any{"date": "{{runtime.env.ledger}}"}}},
			{Slug: "write", Sink: &pipelinespec.SinkSpec{DataSourceSlug: "ledger", OperationType: "sql"}},
		}},
		Refs:     []model.DataSourceRef{{Slug: "ledger", RequiredCapabilities: []model.DataSourceRefCapability{source, sink}}},
		Bindings: []model.DataSourceBinding{{EnvironmentSlug: "dev", DataSourceRefSlug: "ledger", DataSourceActive: true, DataSourceReadOnly: true, DataSourceType: model.DataSourceTypePostgres}},
	}
	env := EnvironmentInput{Slug: "dev", Variables: map[string]Variable{"ledger": {Source: model.RuntimeVariableOverride, Present: true, TypeMismatch: true}}}
	result := evaluate([]PipelineInput{p}, []EnvironmentInput{env}, pipelineDetail).Environments[0]
	require.Len(t, result.Requirements, 3)
	require.Equal(t, "ready", result.Requirements[0].Status)
	require.Equal(t, "blocked", result.Requirements[1].Status)
	require.Equal(t, "blocked", result.Requirements[2].Status)
	require.Equal(t, &Variable{Source: model.RuntimeVariableOverride, Present: true, TypeMismatch: true}, result.Requirements[2].Variable)
	require.Len(t, result.Diagnostics, 2)
	require.Equal(t, "read_only_sink", result.Diagnostics[0].Code)
	require.Equal(t, "variable_type_mismatch", result.Diagnostics[1].Code)
	summary := Evaluate([]PipelineInput{p}, []EnvironmentInput{env}).Environments[0]
	require.Nil(t, summary.Requirements)
	require.Equal(t, "missing_variable", summary.Diagnostics[1].Code)
	project := evaluate([]PipelineInput{p}, []EnvironmentInput{env}, projectOverview).Environments[0]
	require.Nil(t, project.Requirements)
	require.Equal(t, "variable_type_mismatch", project.Diagnostics[1].Code)
}
