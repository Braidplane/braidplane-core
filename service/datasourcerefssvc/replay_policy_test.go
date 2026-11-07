package datasourcerefssvc

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/stretchr/testify/require"
)

func TestResolvePipelineDataSourceRefsChecksConcreteSinkReplayPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		sourceType model.DataSourceType
		operation  string
		policy     pipelinespec.SinkIdempotencyClass
		valid      bool
	}{
		{"postgres rejects request key", model.DataSourceTypePostgres, "sql", pipelinespec.SinkIdempotencyClassIdempotentWithKey, false},
		{"postgres accepts ledger", model.DataSourceTypePostgres, "sql", pipelinespec.SinkIdempotencyClassDeduplicatedByLedger, true},
		{"mysql rejects request key", model.DataSourceTypeMySQL, "sql", pipelinespec.SinkIdempotencyClassIdempotentWithKey, false},
		{"s3 rejects request key", model.DataSourceTypeS3, "s3", pipelinespec.SinkIdempotencyClassIdempotentWithKey, false},
		{"s3 accepts ledger", model.DataSourceTypeS3, "s3", pipelinespec.SinkIdempotencyClassDeduplicatedByLedger, true},
		{"http accepts request key", model.DataSourceTypeHTTP, "http", pipelinespec.SinkIdempotencyClassIdempotentWithKey, true},
		{"hubspot accepts request key", model.DataSourceTypeHubSpot, "hubspot", pipelinespec.SinkIdempotencyClassIdempotentWithKey, true},
		{"bigquery accepts request key", model.DataSourceTypeBigQuery, "bigquery", pipelinespec.SinkIdempotencyClassIdempotentWithKey, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const ref slug.Slug = "destination"
			svc := newTestService(t, testServiceConfig{
				dataSourceRefs: map[string]*model.DataSourceRef{
					ref.String(): newTestDataSourceRef(ref, tc.sourceType,
						model.DataSourceRefCapabilityPipelineSource, model.DataSourceRefCapabilityPipelineSink),
				},
				bindings: map[string]*model.DataSourceBinding{
					"dev/destination": {
						DataSourceRefSlug: ref, EnvironmentSlug: "dev", DataSourceSlug: "concrete-destination",
						DataSourceType: tc.sourceType, DataSourceActive: true,
						RequiredCapabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource, model.DataSourceRefCapabilityPipelineSink},
					},
				},
			})
			pipeline := newPipelineWithLogicalRef(ref)
			pipeline.Steps[0].Query.OperationType = tc.operation
			pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{
				Kind: "sink", Slug: "deliver",
				Sink: &pipelinespec.SinkSpec{
					DataSourceSlug: ref, OperationType: tc.operation, OperationRef: "write-rows",
					Input: &pipelinespec.StepInput{FromStep: "fetch-users"}, IdempotencyClass: tc.policy,
				},
			})
			resolved, err := svc.ResolvePipelineDataSourceRefs(t.Context(), "acme", "operations", "dev", pipeline)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, slug.Slug("concrete-destination"), resolved.Pipeline.Steps[1].Sink.DataSourceSlug)
			} else {
				require.ErrorIs(t, err, ErrDataSourceRefIncompatible)
				require.Nil(t, resolved)
				require.ErrorContains(t, err, "does not support idempotency_class")
				diagnostic, ok := AsResolutionDiagnostic(err)
				require.True(t, ok)
				require.Equal(t, "steps[1].sink.idempotency_class", diagnostic.Field)
				require.Equal(t, slug.Slug("deliver"), *diagnostic.StepSlug)
				require.Equal(t, ref, diagnostic.DataSourceRefSlug)
				require.Equal(t, slug.Slug("dev"), diagnostic.EnvironmentSlug)
			}
			require.Equal(t, ref, pipeline.Steps[1].Sink.DataSourceSlug)
			require.Equal(t, tc.policy, pipeline.Steps[1].Sink.IdempotencyClass)
		})
	}
}
