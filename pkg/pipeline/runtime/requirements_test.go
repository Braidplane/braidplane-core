package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
)

func TestExtractRequirements(t *testing.T) {
	pipeline, err := pipelinespec.Load("requirements.yaml", []byte(`
metadata:
  slug: requirements
  name: Requirements
steps:
  - slug: read
    kind: query
    query:
      data_source_ref: shared
      operation_type: postgres_sql
      operation_ref: fetch
      parameters:
        "a~/b": ["{{runtime.env.batch_size}} {{runtime.env.batch_size}}", "{{runtime.env.run_date | add_days:-1}}"]
        password: synthetic-credential
        "{{runtime.env.map_key}}": ignored
      incremental:
        lookback_window: "{{runtime.env.hours}}h"
        field: "{{runtime.env.unsupported_field}}"
  - slug: read-again
    kind: query
    query:
      data_source_ref: shared
      operation_type: postgres_sql
      operation_ref: fetch
  - slug: read-other
    kind: query
    query:
      data_source_ref: other
      operation_type: postgres_sql
      operation_ref: fetch
  - slug: read-inline
    kind: query
    query:
      data_source_ref: shared
      operation_type: postgres_sql
      operation_inline:
        statement: "SELECT {{ runtime.env.batch_size }}, {{runtime.secrets.token}}, {{runtime.input.value}}"
  - slug: write-inline
    kind: sink
    sink:
      data_source_ref: shared
      operation_type: postgres_sql
      operation_inline:
        statement: "SELECT {{runtime.env.batch_size}}"
      config:
        values:
          limit: "{{runtime.env.batch_size}}"
          run: "{{runtime.run.slug}}"
  - slug: write
    kind: sink
    sink:
      data_source_ref: shared
      operation_type: postgres_sql
      operation_ref: write
      config:
        limit: "{{runtime.env.batch_size}}"
`))
	require.NoError(t, err)
	before, err := json.Marshal(pipeline)
	require.NoError(t, err)

	got := ExtractRequirements(pipeline)
	require.Equal(t, Requirements{
		Pipeline: pipelinespec.Metadata{Slug: "requirements", Name: "Requirements"},
		DataSources: []DataSourceRequirement{
			{Ref: "shared", Capability: model.DataSourceRefCapabilityPipelineSource, Usages: []RequirementUsage{
				{"read", "query", "/steps/0/query/data_source_ref"},
				{"read-again", "query", "/steps/1/query/data_source_ref"},
				{"read-inline", "query", "/steps/3/query/data_source_ref"},
			}},
			{Ref: "other", Capability: model.DataSourceRefCapabilityPipelineSource, Usages: []RequirementUsage{
				{"read-other", "query", "/steps/2/query/data_source_ref"},
			}},
			{Ref: "shared", Capability: model.DataSourceRefCapabilityPipelineSink, Usages: []RequirementUsage{
				{"write-inline", "sink", "/steps/4/sink/data_source_ref"},
				{"write", "sink", "/steps/5/sink/data_source_ref"},
			}},
		},
		EnvironmentVariables: []EnvironmentVariableRequirement{
			{Key: "batch_size", Usages: []RequirementUsage{
				{"read", "query", "/steps/0/query/parameters/a~0~1b/0"},
				{"read-inline", "query", "/steps/3/query/operation_inline/statement"},
				{"write-inline", "sink", "/steps/4/sink/operation_inline/statement"},
				{"write-inline", "sink", "/steps/4/sink/config/values/limit"},
				{"write", "sink", "/steps/5/sink/config/limit"},
			}},
			{Key: "hours", Usages: []RequirementUsage{
				{"read", "query", "/steps/0/query/incremental/lookback_window"},
			}},
			{Key: "run_date", Usages: []RequirementUsage{
				{"read", "query", "/steps/0/query/parameters/a~0~1b/1"},
			}},
		},
		SavedOperations: []SavedOperationRequirement{
			{DataSourceRef: "shared", Ref: "fetch", Usages: []RequirementUsage{
				{"read", "query", "/steps/0/query/operation_ref"},
				{"read-again", "query", "/steps/1/query/operation_ref"},
			}},
			{DataSourceRef: "other", Ref: "fetch", Usages: []RequirementUsage{
				{"read-other", "query", "/steps/2/query/operation_ref"},
			}},
			{DataSourceRef: "shared", Ref: "write", Usages: []RequirementUsage{
				{"write", "sink", "/steps/5/sink/operation_ref"},
			}},
		},
		InlineOperations: []RequirementUsage{
			{"read-inline", "query", "/steps/3/query/operation_inline"},
			{"write-inline", "sink", "/steps/4/sink/operation_inline"},
		},
	}, got)
	after, err := json.Marshal(pipeline)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, Requirements{}, ExtractRequirements(nil))
}
