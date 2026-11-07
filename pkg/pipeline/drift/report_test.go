package drift

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestContractImpactReportsDownstreamEdgesAndUnknownConsumers(t *testing.T) {
	t.Parallel()

	before := contractImpactPipeline()
	after := contractImpactPipeline()
	after.Steps[0].Query.OutputContract.Fields[0].Type = model.ContractFieldTypeValueString

	report := ContractImpact(before, after)

	require.NotNil(t, report)
	require.Equal(t, ReportTypeContractImpact, report.Type)
	require.Equal(t, ReportSeverityBreaking, report.Severity)
	require.Len(t, report.Changes, 1)
	require.Equal(t, "type_change", report.Changes[0].Kind)
	require.Equal(t, "step[fetch-users].query.output_contract.id.type", report.Changes[0].Path)
	require.Contains(t, report.Summary, "highest severity breaking")

	require.Contains(t, report.Impacts, Impact{
		Kind:         "edge",
		PipelineSlug: "users-sync",
		StepSlug:     "normalize-users",
		EdgeSlug:     "fetch-users->normalize-users",
		Severity:     ReportSeverityBreaking,
		Message:      "downstream edge fetch-users->normalize-users may be affected",
	})
	require.Contains(t, report.Impacts, Impact{
		Kind:         "unknown_consumer",
		PipelineSlug: "users-sync",
		StepSlug:     "fetch-users",
		Severity:     ReportSeverityWarning,
		Message:      "external app or consumer impact is unknown in the MVP",
	})
}

func TestContractImpactDeduplicatesRepeatedImpacts(t *testing.T) {
	t.Parallel()

	before := contractImpactPipeline()
	after := contractImpactPipeline()
	after.Steps[0].Query.OutputContract.Fields[0].Type = model.ContractFieldTypeValueString
	after.Steps[0].Query.OutputContract.Fields[0].Nullable = true

	report := ContractImpact(before, after)

	require.NotNil(t, report)
	require.Len(t, report.Changes, 2)
	require.Equal(t, 1, countImpacts(report.Impacts, "edge", "fetch-users->normalize-users"))
	require.Equal(t, 1, countImpacts(report.Impacts, "unknown_consumer", ""))
}

func TestContractImpactReportsMetadataChanges(t *testing.T) {
	t.Parallel()

	before := contractImpactPipeline()
	after := contractImpactPipeline()
	after.Metadata.Slug = slug.Slug("users-sync-v2")

	report := ContractImpact(before, after)

	require.NotNil(t, report)
	require.Equal(t, ReportSeverityBreaking, report.Severity)
	require.Contains(t, report.Changes, Change{
		Kind:       "pipeline_slug_change",
		Path:       "metadata.slug",
		Before:     "users-sync",
		After:      "users-sync-v2",
		Severity:   ReportSeverityBreaking,
		ReasonCode: "pipeline_slug_changed",
	})
	require.Contains(t, report.Impacts, Impact{
		Kind:         "pipeline",
		PipelineSlug: "users-sync-v2",
		Severity:     ReportSeverityBreaking,
		Message:      "pipeline metadata changed",
	})

	renamed := contractImpactPipeline()
	renamed.Metadata.Name = "Users Sync v2"

	report = ContractImpact(before, renamed)

	require.NotNil(t, report)
	require.Equal(t, ReportSeveritySafe, report.Severity)
	require.Contains(t, report.Changes, Change{
		Kind:       "pipeline_name_change",
		Path:       "metadata.name",
		Before:     "Users Sync",
		After:      "Users Sync v2",
		Severity:   ReportSeveritySafe,
		ReasonCode: "pipeline_name_changed",
	})
	require.Contains(t, report.Impacts, Impact{
		Kind:         "pipeline",
		PipelineSlug: "users-sync",
		Severity:     ReportSeveritySafe,
		Message:      "pipeline metadata changed",
	})
}

func TestContractImpactReturnsNilWhenSpecsMatch(t *testing.T) {
	t.Parallel()

	pipeline := contractImpactPipeline()

	require.Nil(t, ContractImpact(pipeline, pipeline))
}

func countImpacts(impacts []Impact, kind, edgeSlug string) int {
	count := 0
	for _, impact := range impacts {
		if impact.Kind == kind && impact.EdgeSlug == edgeSlug {
			count++
		}
	}
	return count
}

func contractImpactPipeline() *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: slug.Slug("users-sync"),
			Name: "Users Sync",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: slug.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: slug.Slug("postgres"),
					OperationType:  "sql",
					OperationInline: map[string]any{
						"sql": "select id from users",
					},
					OutputContract: &payload.Contract{
						Name:           "users",
						Representation: model.ContractRepresentationRows,
						Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}},
					},
				},
			},
			{
				Kind: "transform",
				Slug: slug.Slug("normalize-users"),
				Transform: &pipelinespec.TransformSpec{
					SQL:        "select id from input",
					OutputKind: model.TransformOutputKindRows,
					Inputs: map[string]pipelinespec.StepInput{
						"input": {FromStep: slug.Slug("fetch-users")},
					},
				},
			},
		},
	}
}
